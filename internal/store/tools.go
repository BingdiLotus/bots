package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/bingdilotus/bots/internal/domain"
	"github.com/jackc/pgx/v5"
)

const taskColumns = `id,tenant_id,bot_id,scope_id,origin_command_id,reminder_slot,text,due_at,status,revision,timer_id,timer_handle,fire_id,delivery_id,target,created_at`

func scanTask(row pgx.Row) (domain.Task, error) {
	var t domain.Task
	var target []byte
	err := row.Scan(&t.ID, &t.TenantID, &t.BotID, &t.ScopeID, &t.OriginCommandID, &t.ReminderSlot, &t.Text, &t.DueAt, &t.Status, &t.Revision, &t.TimerID, &t.TimerHandle, &t.FireID, &t.DeliveryID, &target, &t.CreatedAt)
	if err != nil {
		return t, normalized(err)
	}
	err = json.Unmarshal(target, &t.Target)
	return t, err
}

func parseArguments(input json.RawMessage, target any) error {
	d := json.NewDecoder(bytes.NewReader(input))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return domain.ErrInvalid
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return domain.ErrInvalid
	}
	return nil
}
func toolError(err error) json.RawMessage {
	code := "invalid_request"
	switch {
	case errors.Is(err, domain.ErrConflict):
		code = "conflict"
	case errors.Is(err, domain.ErrNotFound):
		code = "not_found"
	case errors.Is(err, domain.ErrBusy):
		code = "in_flight_or_unknown"
	case errors.Is(err, domain.ErrLimit):
		code = "pending_task_limit"
	case errors.Is(err, domain.ErrDisabled):
		code = "disabled"
	}
	return raw(map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
}
func businessError(err error) bool {
	return errors.Is(err, domain.ErrInvalid) || errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrBusy) || errors.Is(err, domain.ErrLimit) || errors.Is(err, domain.ErrDisabled)
}

// ExecuteTool commits the operation receipt, native mutation, outbox and audit
// together. A retry returns its original result before checking live enabled state.
func (s *Store) ExecuteTool(ctx context.Context, call domain.ToolCall) (json.RawMessage, error) {
	c := call.Context
	if c.TenantID == "" || c.SubjectRef == "" || c.ScopeID == "" || c.OriginCommandID == "" || c.RunID == "" || c.OperationID == "" || c.ToolCallID == "" || !json.Valid(call.Arguments) {
		return nil, domain.ErrInvalid
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(call.Arguments, &object) != nil || object == nil {
		return nil, domain.ErrInvalid
	}
	hash := domain.Hash(call)
	scope := domain.Scope{TenantID: c.TenantID, BotID: c.SubjectRef, ScopeID: c.ScopeID}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	bot, err := lockBot(ctx, tx, c.TenantID, c.SubjectRef)
	if err != nil {
		return nil, err
	}
	var savedHash, savedScope string
	var result []byte
	err = tx.QueryRow(ctx, `SELECT request_hash,scope_id,result FROM bots.inbox WHERE tenant_id=$1 AND bot_id=$2 AND namespace='tool' AND source_id=$3`, c.TenantID, c.SubjectRef, c.OperationID).Scan(&savedHash, &savedScope, &result)
	if err == nil {
		if savedHash != hash || savedScope != c.ScopeID {
			return nil, domain.ErrConflict
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if !bot.Enabled || !bot.AccountEnabled {
		return nil, domain.ErrDisabled
	}
	origin, err := scanInbox(tx.QueryRow(ctx, `SELECT `+inboxColumns+` FROM bots.inbox WHERE tenant_id=$1 AND bot_id=$2 AND scope_id=$3 AND command_id=$4 FOR UPDATE`, c.TenantID, c.SubjectRef, c.ScopeID, c.OriginCommandID))
	if err != nil {
		return nil, err
	}
	if origin.SourceState == "deleted" || origin.SourceState == "permission" || origin.RuntimeSessionID == "" || origin.RunID != "" && origin.RunID != c.RunID || origin.State != "processing" && origin.State != "running" {
		return nil, domain.ErrConflict
	}
	if origin.RunID == "" {
		if _, err := tx.Exec(ctx, `UPDATE bots.inbox SET run_id=$2,state='running' WHERE id=$1`, origin.ID, c.RunID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT tool_mutation`); err != nil {
		return nil, err
	}
	var value any
	switch call.ToolRef {
	case "remember":
		if origin.SourceState == "edited" {
			err = domain.ErrConflict
			break
		}
		var args struct {
			Content string `json:"content"`
			Kind    string `json:"kind"`
		}
		err = parseArguments(call.Arguments, &args)
		if err != nil {
			break
		}
		if args.Kind == "" {
			args.Kind = "knowledge"
		}
		if strings.TrimSpace(args.Content) == "" || len(args.Content) > 16384 || args.Kind != "knowledge" && args.Kind != "experience" {
			err = domain.ErrInvalid
			break
		}
		id, idErr := domain.NewID("knowledge_")
		if idErr != nil {
			return nil, idErr
		}
		_, err = tx.Exec(ctx, `INSERT INTO bots.knowledge(id,tenant_id,bot_id,scope_id,content,kind,source_id,origin_command_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, c.TenantID, c.SubjectRef, c.ScopeID, args.Content, args.Kind, origin.SourceID, c.OriginCommandID)
		if err == nil {
			err = audit(ctx, tx, scope, "knowledge_created", id, 1, "tool", map[string]string{"operation_id": c.OperationID, "command_id": c.OriginCommandID, "content_hash": domain.Hash(args.Content)})
		}
		value = map[string]any{"knowledge_id": id, "revision": 1}
	case "recall":
		var args struct {
			Query string `json:"query"`
		}
		err = parseArguments(call.Arguments, &args)
		if err != nil {
			break
		}
		var items []domain.Knowledge
		items, err = queryKnowledge(ctx, tx, scope, args.Query, 20)
		value = map[string]any{"items": items}
	case "create_reminder":
		var args struct {
			DueAt time.Time `json:"due_at"`
			Text  string    `json:"text"`
		}
		err = parseArguments(call.Arguments, &args)
		if err != nil {
			break
		}
		args.DueAt = args.DueAt.UTC().Truncate(time.Microsecond)
		if args.DueAt.IsZero() || strings.TrimSpace(args.Text) == "" || len(args.Text) > 8192 {
			err = domain.ErrInvalid
			break
		}
		var task domain.Task
		task, err = scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM bots.tasks WHERE tenant_id=$1 AND bot_id=$2 AND origin_command_id=$3 AND reminder_slot=0 FOR UPDATE`, c.TenantID, c.SubjectRef, c.OriginCommandID))
		if err == nil {
			if task.Text != args.Text || !task.DueAt.Equal(args.DueAt) {
				err = domain.ErrConflict
			}
			value = taskResult(task)
			break
		}
		if !errors.Is(err, domain.ErrNotFound) {
			break
		}
		var count int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM bots.tasks WHERE tenant_id=$1 AND bot_id=$2 AND status NOT IN ('completed','canceled')`, c.TenantID, c.SubjectRef).Scan(&count)
		if err != nil {
			break
		}
		if count >= bot.Config.MaxPendingTasks {
			err = domain.ErrLimit
			break
		}
		id := domain.StableID("task_", c.TenantID, c.SubjectRef, c.OriginCommandID, "0")
		timer := domain.StableID("timer_", id)
		task, err = scanTask(tx.QueryRow(ctx, `INSERT INTO bots.tasks(id,tenant_id,bot_id,scope_id,origin_command_id,text,due_at,status,timer_id,target) VALUES($1,$2,$3,$4,$5,$6,$7,'pending_schedule',$8,$9) RETURNING `+taskColumns, id, c.TenantID, c.SubjectRef, c.ScopeID, c.OriginCommandID, args.Text, args.DueAt, timer, raw(origin.Target)))
		if err == nil {
			err = putCommand(ctx, tx, timerCommand(task))
		}
		if err == nil {
			err = audit(ctx, tx, scope, "task_created", task.ID, task.Revision, "tool", map[string]string{"operation_id": c.OperationID, "command_id": c.OriginCommandID, "timer_id": task.TimerID})
		}
		value = taskResult(task)
	case "update_reminder", "cancel_reminder":
		var args struct {
			TaskID           string    `json:"task_id"`
			ExpectedRevision int       `json:"expected_revision"`
			DueAt            time.Time `json:"due_at,omitempty"`
			Text             string    `json:"text,omitempty"`
		}
		err = parseArguments(call.Arguments, &args)
		if err != nil {
			break
		}
		if args.ExpectedRevision < 1 {
			err = domain.ErrInvalid
			break
		}
		var task domain.Task
		task, err = scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM bots.tasks WHERE id=$1 AND tenant_id=$2 AND bot_id=$3 AND scope_id=$4 FOR UPDATE`, args.TaskID, c.TenantID, c.SubjectRef, c.ScopeID))
		if err != nil {
			break
		}
		if task.Revision != args.ExpectedRevision {
			err = domain.ErrConflict
			break
		}
		if task.Status == "dispatching" || task.Status == "unknown" {
			err = domain.ErrBusy
			break
		}
		if task.Status == "completed" || task.Status == "canceled" {
			err = domain.ErrConflict
			break
		}
		var inFlight bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bots.deliveries WHERE task_id=$1 AND task_revision=$2 AND state IN ('dispatching','unknown'))`, task.ID, task.Revision).Scan(&inFlight)
		if err != nil {
			break
		}
		if inFlight {
			err = domain.ErrBusy
			break
		}
		task.Revision++
		task.TimerHandle = ""
		task.FireID = ""
		task.DeliveryID = ""
		task.Status = "canceled"
		if call.ToolRef == "update_reminder" {
			if args.DueAt.IsZero() || strings.TrimSpace(args.Text) == "" || len(args.Text) > 8192 {
				err = domain.ErrInvalid
				break
			}
			task.DueAt = args.DueAt.UTC().Truncate(time.Microsecond)
			task.Text = args.Text
			task.Status = "pending_schedule"
		}
		_, err = tx.Exec(ctx, `UPDATE bots.tasks SET revision=$2,text=$3,due_at=$4,status=$5,timer_handle='',fire_id='',delivery_id='' WHERE id=$1`, task.ID, task.Revision, task.Text, task.DueAt, task.Status)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE bots.deliveries SET state='obsolete',error='task revision changed' WHERE task_id=$1 AND state IN ('pending','retryable')`, task.ID)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE bots.command_outbox SET state='obsolete' WHERE kind='timer' AND ref_id=$1 AND state='pending'`, task.ID)
		}
		if err == nil && task.Status == "pending_schedule" {
			err = putCommand(ctx, tx, timerCommand(task))
		}
		if err == nil {
			err = audit(ctx, tx, scope, call.ToolRef, task.ID, task.Revision, "tool", map[string]string{"operation_id": c.OperationID})
		}
		value = taskResult(task)
	default:
		err = domain.ErrInvalid
	}
	if err != nil {
		if !businessError(err) {
			return nil, err
		}
		if _, saveErr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT tool_mutation`); saveErr != nil {
			return nil, saveErr
		}
		result = toolError(err)
	} else {
		result = raw(value)
	}
	if call.ToolRef != "recall" {
		_, err = tx.Exec(ctx, `INSERT INTO bots.inbox(id,tenant_id,bot_id,scope_id,namespace,source_id,request_hash,state,result,run_id,session_key,revision,runtime_session_id,input) VALUES($1,$2,$3,$4,'tool',$5,$6,'complete',$7,$8,$9,$10,$11,$12)`, domain.StableID("operation_", c.TenantID, c.SubjectRef, c.OperationID), c.TenantID, c.SubjectRef, c.ScopeID, c.OperationID, hash, result, c.RunID, origin.SessionKey, origin.Revision, origin.RuntimeSessionID, call.ToolRef)
		if err != nil {
			return nil, err
		}
		if err := audit(ctx, tx, scope, "tool_operation", c.OperationID, origin.Revision, "tool", map[string]string{"tool_ref": call.ToolRef, "origin_command_id": c.OriginCommandID, "run_id": c.RunID, "tool_call_id": c.ToolCallID, "request_hash": hash, "result_hash": domain.Hash(json.RawMessage(result))}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func taskResult(task domain.Task) map[string]any {
	return map[string]any{"task_id": task.ID, "revision": task.Revision, "status": task.Status, "due_at": task.DueAt, "text": task.Text}
}
