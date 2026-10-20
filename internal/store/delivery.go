package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/bingdilotus/bots/internal/domain"
	"github.com/jackc/pgx/v5"
)

const deliveryColumns = `id,operation_id,tenant_id,bot_id,scope_id,task_id,task_revision,command_id,target,text,payload_hash,state,provider_message_id,error,attempts,next_attempt_at,created_at`

func scanDelivery(row pgx.Row) (domain.Delivery, error) {
	var d domain.Delivery
	var target []byte
	err := row.Scan(&d.ID, &d.OperationID, &d.TenantID, &d.BotID, &d.ScopeID, &d.TaskID, &d.TaskRevision, &d.CommandID, &target, &d.Text, &d.PayloadHash, &d.State, &d.ProviderMessageID, &d.Error, &d.Attempts, &d.NextAttemptAt, &d.CreatedAt)
	if err != nil {
		return d, normalized(err)
	}
	err = json.Unmarshal(target, &d.Target)
	return d, err
}
func deliveryHash(d domain.Delivery) string {
	return domain.Hash(map[string]any{"target": d.Target, "text": d.Text, "task_id": d.TaskID, "task_revision": d.TaskRevision, "command_id": d.CommandID})
}
func insertDelivery(ctx context.Context, tx pgx.Tx, d domain.Delivery) error {
	d.PayloadHash = deliveryHash(d)
	_, err := tx.Exec(ctx, `INSERT INTO bots.deliveries(id,operation_id,tenant_id,bot_id,scope_id,task_id,task_revision,command_id,target,text,payload_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(operation_id) DO NOTHING`, d.ID, d.OperationID, d.TenantID, d.BotID, d.ScopeID, d.TaskID, d.TaskRevision, d.CommandID, raw(d.Target), d.Text, d.PayloadHash)
	if err != nil {
		return err
	}
	var hash string
	if err := tx.QueryRow(ctx, `SELECT payload_hash FROM bots.deliveries WHERE operation_id=$1`, d.OperationID).Scan(&hash); err != nil {
		return err
	}
	if hash != d.PayloadHash {
		return domain.ErrConflict
	}
	return nil
}
func (s *Store) AcceptFire(ctx context.Context, fire domain.TimerFire) (bool, error) {
	if fire.TenantID == "" || fire.TimerID == "" || fire.FireID == "" || fire.OpaqueActionRef == "" || fire.Revision < 1 {
		return false, domain.ErrInvalid
	}
	initial, err := scanTask(s.Pool.QueryRow(ctx, `SELECT `+taskColumns+` FROM bots.tasks WHERE tenant_id=$1 AND timer_id=$2`, fire.TenantID, fire.TimerID))
	if err != nil {
		return false, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err := lockBot(ctx, tx, initial.TenantID, initial.BotID); err != nil {
		return false, err
	}
	task, err := scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM bots.tasks WHERE id=$1 FOR UPDATE`, initial.ID))
	if err != nil {
		return false, err
	}
	if fire.OpaqueActionRef != task.ID {
		return false, domain.ErrConflict
	}
	hash := domain.Hash(fire)
	var existing string
	err = tx.QueryRow(ctx, `SELECT request_hash FROM bots.inbox WHERE tenant_id=$1 AND bot_id=$2 AND namespace='fire' AND source_id=$3`, task.TenantID, task.BotID, fire.FireID).Scan(&existing)
	if err == nil {
		if existing != hash {
			return false, domain.ErrConflict
		}
		return false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	state := "ignored"
	if task.Revision == fire.Revision && (task.Status == "pending_schedule" || task.Status == "scheduled" || task.Status == "due") {
		state = "complete"
		d := domain.Delivery{ID: domain.StableID("delivery_reminder_", task.ID, string(raw(task.Revision))), OperationID: domain.StableID("reminder_", task.ID, string(raw(task.Revision))), TenantID: task.TenantID, BotID: task.BotID, ScopeID: task.ScopeID, TaskID: task.ID, TaskRevision: task.Revision, CommandID: task.OriginCommandID, Target: task.Target, Text: task.Text}
		if err := insertDelivery(ctx, tx, d); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `UPDATE bots.tasks SET status='due',fire_id=$2,delivery_id=$3 WHERE id=$1`, task.ID, fire.FireID, d.ID); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bots.inbox(id,tenant_id,bot_id,scope_id,namespace,source_id,request_hash,state) VALUES($1,$2,$3,$4,'fire',$5,$6,$7)`, domain.StableID("fire_", task.TenantID, task.BotID, fire.FireID), task.TenantID, task.BotID, task.ScopeID, fire.FireID, hash, state); err != nil {
		return false, err
	}
	if err := audit(ctx, tx, domain.Scope{TenantID: task.TenantID, BotID: task.BotID, ScopeID: task.ScopeID}, "timer_fire_"+state, fire.FireID, fire.Revision, "timer", map[string]string{"task_id": task.ID, "timer_id": task.TimerID}); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
func (s *Store) ClaimDelivery(ctx context.Context, tenantFilter string) (domain.Delivery, error) {
	if tenantFilter == "" {
		return domain.Delivery{}, domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.Delivery{}, err
	}
	defer tx.Rollback(ctx)
	b, err := scanBot(tx.QueryRow(ctx, `SELECT `+botColumns+` FROM bots.bots b WHERE tenant_id=$1 AND enabled AND account_enabled AND EXISTS(SELECT 1 FROM bots.deliveries d WHERE d.tenant_id=b.tenant_id AND d.bot_id=b.bot_id AND d.state IN ('pending','retryable') AND d.next_attempt_at<=now()) ORDER BY created_at FOR UPDATE OF b SKIP LOCKED LIMIT 1`, tenantFilter))
	if err != nil {
		return domain.Delivery{}, err
	}
	d, err := scanDelivery(tx.QueryRow(ctx, `SELECT `+deliveryColumns+` FROM bots.deliveries WHERE tenant_id=$1 AND bot_id=$2 AND state IN ('pending','retryable') AND next_attempt_at<=now() ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, b.TenantID, b.ID))
	if err != nil {
		return d, err
	}
	valid := d.Target.Provider == b.Config.ChannelKind && d.Target.InstallationID == b.Config.InstallationID && d.Target.ChannelID != "" && (b.Config.ChannelID == "" || d.Target.ChannelID == b.Config.ChannelID)
	var sourceState string
	err = tx.QueryRow(ctx, `SELECT source_state FROM bots.inbox WHERE tenant_id=$1 AND bot_id=$2 AND command_id=$3`, d.TenantID, d.BotID, d.CommandID).Scan(&sourceState)
	if err != nil {
		return d, normalized(err)
	}
	if sourceState == "deleted" || sourceState == "permission" {
		valid = false
	}
	var task domain.Task
	if d.TaskID != "" {
		task, err = scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM bots.tasks WHERE id=$1 FOR UPDATE`, d.TaskID))
		if err != nil {
			return d, err
		}
		if task.TenantID != d.TenantID || task.BotID != d.BotID || task.ScopeID != d.ScopeID || task.Revision != d.TaskRevision || task.Status != "due" {
			valid = false
		}
	}
	if !valid {
		if _, err := tx.Exec(ctx, `UPDATE bots.deliveries SET state='obsolete',error='source, task revision or account target is no longer valid',updated_at=now() WHERE id=$1`, d.ID); err != nil {
			return d, err
		}
		if err := tx.Commit(ctx); err != nil {
			return d, err
		}
		return domain.Delivery{}, domain.ErrNotFound
	}
	if d.TaskID != "" && time.Since(task.DueAt) > time.Minute && !strings.HasPrefix(d.Text, "（迟到提醒）") {
		d.Text = "（迟到提醒）\n" + d.Text
		d.PayloadHash = deliveryHash(d)
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.deliveries SET state='dispatching',attempts=attempts+1,text=$2,payload_hash=$3,error='',updated_at=now() WHERE id=$1`, d.ID, d.Text, d.PayloadHash); err != nil {
		return d, err
	}
	if d.TaskID != "" {
		if _, err := tx.Exec(ctx, `UPDATE bots.tasks SET status='dispatching' WHERE id=$1 AND revision=$2`, d.TaskID, d.TaskRevision); err != nil {
			return d, err
		}
	}
	if err := audit(ctx, tx, domain.Scope{TenantID: d.TenantID, BotID: d.BotID, ScopeID: d.ScopeID}, "delivery_claimed", d.ID, d.TaskRevision, "sender", map[string]string{"payload_hash": d.PayloadHash, "operation_id": d.OperationID}); err != nil {
		return d, err
	}
	if err := tx.Commit(ctx); err != nil {
		return d, err
	}
	d.State = "dispatching"
	d.Attempts++
	return d, nil
}

func (s *Store) lockDelivery(ctx context.Context, tx pgx.Tx, id string) (domain.Delivery, error) {
	initial, err := scanDelivery(tx.QueryRow(ctx, `SELECT `+deliveryColumns+` FROM bots.deliveries WHERE id=$1`, id))
	if err != nil {
		return initial, err
	}
	if _, err := lockBot(ctx, tx, initial.TenantID, initial.BotID); err != nil {
		return initial, err
	}
	if initial.TaskID != "" {
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id FROM bots.tasks WHERE id=$1 FOR UPDATE`, initial.TaskID).Scan(&locked); err != nil {
			return initial, normalized(err)
		}
	}
	return scanDelivery(tx.QueryRow(ctx, `SELECT `+deliveryColumns+` FROM bots.deliveries WHERE id=$1 FOR UPDATE`, id))
}
func (s *Store) CompleteDelivery(ctx context.Context, id, hash, providerMessageID string) error {
	if providerMessageID == "" {
		return domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	d, err := s.lockDelivery(ctx, tx, id)
	if err != nil {
		return err
	}
	if d.PayloadHash != hash {
		return domain.ErrConflict
	}
	if d.State == "confirmed" {
		if d.ProviderMessageID != providerMessageID {
			return domain.ErrConflict
		}
		return nil
	}
	if d.State != "dispatching" && d.State != "unknown" {
		return domain.ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.deliveries SET state='confirmed',provider_message_id=$2,error='',updated_at=now() WHERE id=$1`, d.ID, providerMessageID); err != nil {
		return err
	}
	if d.TaskID != "" {
		if _, err := tx.Exec(ctx, `UPDATE bots.tasks SET status='completed' WHERE id=$1 AND revision=$2 AND delivery_id=$3 AND status IN ('dispatching','unknown')`, d.TaskID, d.TaskRevision, d.ID); err != nil {
			return err
		}
	}
	if err := audit(ctx, tx, domain.Scope{TenantID: d.TenantID, BotID: d.BotID, ScopeID: d.ScopeID}, "delivery_confirmed", d.ID, d.TaskRevision, "sender", map[string]string{"provider_message_id": providerMessageID, "payload_hash": hash}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ResolveDelivery records an administrator's manual verification of an
// ambiguous provider result. Only an unknown delivery may be resolved; this
// never calls the provider and never retries a send. A confirmed result must
// carry the actual provider message ID; a rejection must carry a reason.
func (s *Store) ResolveDelivery(ctx context.Context, scope domain.Scope, id, resolution, providerMessageID, note string) (domain.Delivery, error) {
	if !scopeValid(scope) || id == "" || resolution != "confirmed" && resolution != "rejected" || len(note) > 512 ||
		len(providerMessageID) > 128 || strings.TrimSpace(providerMessageID) != providerMessageID ||
		strings.ContainsAny(providerMessageID, " \t\r\n") {
		return domain.Delivery{}, domain.ErrInvalid
	}
	if resolution == "confirmed" && providerMessageID == "" || resolution == "rejected" && providerMessageID != "" {
		return domain.Delivery{}, domain.ErrInvalid
	}
	if resolution == "rejected" && strings.TrimSpace(note) == "" {
		return domain.Delivery{}, domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.Delivery{}, err
	}
	defer tx.Rollback(ctx)
	d, err := scanDelivery(tx.QueryRow(ctx, `SELECT `+deliveryColumns+` FROM bots.deliveries WHERE id=$1 AND tenant_id=$2 AND bot_id=$3 AND ($4='' OR scope_id=$4) FOR UPDATE`, id, scope.TenantID, scope.BotID, scope.ScopeID))
	if err != nil {
		return domain.Delivery{}, err
	}
	if d.State != "unknown" {
		return domain.Delivery{}, domain.ErrConflict
	}
	taskState := "completed"
	if resolution == "rejected" {
		taskState = "canceled"
	}
	errorText := ""
	if resolution == "rejected" {
		errorText = note
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.deliveries SET state=$2,provider_message_id=$3,error=$4,updated_at=now() WHERE id=$1`, d.ID, resolution, providerMessageID, errorText); err != nil {
		return domain.Delivery{}, err
	}
	if d.TaskID != "" {
		if _, err := tx.Exec(ctx, `UPDATE bots.tasks SET status=$4 WHERE id=$1 AND revision=$2 AND delivery_id=$3 AND status IN ('dispatching','unknown')`, d.TaskID, d.TaskRevision, d.ID, taskState); err != nil {
			return domain.Delivery{}, err
		}
	}
	if err := audit(ctx, tx, domain.Scope{TenantID: d.TenantID, BotID: d.BotID, ScopeID: d.ScopeID}, "delivery_resolved", d.ID, d.TaskRevision, "admin", map[string]string{
		"previous_state": d.State, "state": resolution, "provider_message_id": providerMessageID, "note": note,
	}); err != nil {
		return domain.Delivery{}, err
	}
	updated, err := scanDelivery(tx.QueryRow(ctx, `SELECT `+deliveryColumns+` FROM bots.deliveries WHERE id=$1`, d.ID))
	if err != nil {
		return domain.Delivery{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Delivery{}, err
	}
	return updated, nil
}

func (s *Store) FailDelivery(ctx context.Context, id, hash, state, reason string, retryAt time.Time) error {
	if state != "unknown" && state != "retryable" && state != "rejected" {
		return domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	d, err := s.lockDelivery(ctx, tx, id)
	if err != nil {
		return err
	}
	if d.PayloadHash != hash {
		return domain.ErrConflict
	}
	if d.State == state {
		return nil
	}
	if d.State != "dispatching" {
		return domain.ErrBusy
	}
	if state == "retryable" && d.Attempts >= 5 {
		state = "rejected"
	}
	if retryAt.IsZero() {
		retryAt = time.Now().Add(time.Second)
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.deliveries SET state=$2,error=$3,next_attempt_at=$4,updated_at=now() WHERE id=$1`, id, state, reason, retryAt); err != nil {
		return err
	}
	taskState := "blocked"
	if state == "unknown" {
		taskState = "unknown"
	}
	if state == "retryable" {
		taskState = "due"
	}
	if d.TaskID != "" {
		if _, err := tx.Exec(ctx, `UPDATE bots.tasks SET status=$4 WHERE id=$1 AND revision=$2 AND delivery_id=$3 AND status='dispatching'`, d.TaskID, d.TaskRevision, d.ID, taskState); err != nil {
			return err
		}
	}
	if err := audit(ctx, tx, domain.Scope{TenantID: d.TenantID, BotID: d.BotID, ScopeID: d.ScopeID}, "delivery_"+state, id, d.TaskRevision, "sender", map[string]string{"error": reason, "payload_hash": hash}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
