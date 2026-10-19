package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/bingdilotus/bots/internal/domain"
	"github.com/jackc/pgx/v5"
)

const inboxColumns = `id,tenant_id,bot_id,scope_id,namespace,source_id,command_id,session_key,input,target,revision,runtime_session_id,run_id,state,source_state,error,created_at`

func scanInbox(row pgx.Row) (domain.Inbox, error) {
	var i domain.Inbox
	var target []byte
	err := row.Scan(&i.ID, &i.TenantID, &i.BotID, &i.ScopeID, &i.Namespace, &i.SourceID, &i.CommandID, &i.SessionKey, &i.Input, &target, &i.Revision, &i.RuntimeSessionID, &i.RunID, &i.State, &i.SourceState, &i.Error, &i.CreatedAt)
	if err != nil {
		return i, normalized(err)
	}
	err = json.Unmarshal(target, &i.Target)
	return i, err
}
func (s *Store) AcceptInbox(ctx context.Context, i domain.Inbox) (domain.Inbox, bool, error) {
	if i.TenantID == "" || i.BotID == "" || i.Namespace == "" || i.SourceID == "" || i.Input == "" || i.Target.ChannelID == "" || i.Namespace == "tool" || i.Namespace == "fire" {
		return i, false, domain.ErrInvalid
	}
	scope := domain.KnowledgeScope(i.TenantID, i.BotID, i.Target.InstallationID, i.Target.ChannelID)
	if i.ScopeID != "" && i.ScopeID != scope {
		return i, false, domain.ErrConflict
	}
	i.ScopeID = scope
	i.ID = domain.StableID("inbox_", i.TenantID, i.BotID, i.Namespace, i.SourceID)
	i.CommandID = domain.StableID("command_", i.ID)
	i.SessionKey = domain.SessionKey(i.TenantID, i.BotID, i.Target)
	hash := domain.Hash(map[string]any{"tenant": i.TenantID, "bot": i.BotID, "scope": i.ScopeID, "namespace": i.Namespace, "source": i.SourceID, "input": i.Input, "target": i.Target})
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return i, false, err
	}
	defer tx.Rollback(ctx)
	b, err := lockBot(ctx, tx, i.TenantID, i.BotID)
	if err != nil {
		return i, false, err
	}
	var previous string
	err = tx.QueryRow(ctx, `SELECT request_hash FROM bots.inbox WHERE id=$1`, i.ID).Scan(&previous)
	if err == nil {
		if previous != hash {
			return i, false, domain.ErrConflict
		}
		existing, err := scanInbox(tx.QueryRow(ctx, `SELECT `+inboxColumns+` FROM bots.inbox WHERE id=$1`, i.ID))
		return existing, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return i, false, err
	}
	if !b.Enabled || !b.AccountEnabled {
		return i, false, domain.ErrDisabled
	}
	if i.Target.Provider != b.Config.ChannelKind || i.Target.InstallationID != b.Config.InstallationID || b.Config.ChannelID != "" && i.Target.ChannelID != b.Config.ChannelID {
		return i, false, domain.ErrInvalid
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bots.inbox(id,tenant_id,bot_id,scope_id,namespace,source_id,request_hash,command_id,session_key,input,target,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'pending')`, i.ID, i.TenantID, i.BotID, i.ScopeID, i.Namespace, i.SourceID, hash, i.CommandID, i.SessionKey, i.Input, raw(i.Target)); err != nil {
		return i, false, err
	}
	if err := audit(ctx, tx, domain.Scope{TenantID: i.TenantID, BotID: i.BotID, ScopeID: i.ScopeID}, "inbox_accepted", i.CommandID, 0, "channel", map[string]string{"source_id": i.SourceID, "input_hash": domain.Hash(i.Input)}); err != nil {
		return i, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return i, false, err
	}
	stored, err := scanInbox(s.Pool.QueryRow(ctx, `SELECT `+inboxColumns+` FROM bots.inbox WHERE id=$1`, i.ID))
	return stored, true, err
}
func (s *Store) ClaimInbox(ctx context.Context, tenantFilter string) (domain.Inbox, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.Inbox{}, err
	}
	defer tx.Rollback(ctx)
	var tenant, bot string
	err = tx.QueryRow(ctx, `SELECT b.tenant_id,b.bot_id FROM bots.bots b WHERE b.tenant_id=$1 AND b.enabled AND b.account_enabled AND b.runtime_agent_id<>'' AND EXISTS(SELECT 1 FROM bots.inbox i WHERE i.tenant_id=b.tenant_id AND i.bot_id=b.bot_id AND i.state='pending') AND NOT EXISTS(SELECT 1 FROM bots.inbox a WHERE a.tenant_id=b.tenant_id AND a.bot_id=b.bot_id AND a.state IN ('processing','running')) ORDER BY b.created_at FOR UPDATE OF b SKIP LOCKED LIMIT 1`, tenantFilter).Scan(&tenant, &bot)
	if err != nil {
		return domain.Inbox{}, normalized(err)
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bots.inbox WHERE tenant_id=$1 AND bot_id=$2 AND state IN ('processing','running'))`, tenant, bot).Scan(&active); err != nil {
		return domain.Inbox{}, err
	}
	if active {
		return domain.Inbox{}, domain.ErrNotFound
	}
	i, err := scanInbox(tx.QueryRow(ctx, `SELECT `+inboxColumns+` FROM bots.inbox WHERE tenant_id=$1 AND bot_id=$2 AND state='pending' ORDER BY created_at,id FOR UPDATE LIMIT 1`, tenant, bot))
	if err != nil {
		return i, err
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.inbox SET state='processing',error='',updated_at=now() WHERE id=$1`, i.ID); err != nil {
		return i, err
	}
	if err := tx.Commit(ctx); err != nil {
		return i, err
	}
	i.State = "processing"
	return i, nil
}
func (s *Store) ReleaseInbox(ctx context.Context, id, reason string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE bots.inbox SET state=CASE WHEN run_id='' THEN 'pending' ELSE 'running' END,error=$2,updated_at=now() WHERE id=$1 AND state IN ('processing','running')`, id, reason)
	return err
}

const sessionColumns = `session_key,tenant_id,bot_id,scope_id,revision,runtime_agent_id,runtime_session_id,target`

func scanRuntimeSession(row pgx.Row) (domain.RuntimeSession, error) {
	var r domain.RuntimeSession
	var target []byte
	err := row.Scan(&r.Key, &r.TenantID, &r.BotID, &r.ScopeID, &r.Revision, &r.RuntimeAgentID, &r.RuntimeSessionID, &target)
	if err != nil {
		return r, normalized(err)
	}
	err = json.Unmarshal(target, &r.Target)
	return r, err
}
func (s *Store) EnsureRuntimeSession(ctx context.Context, inboxID string) (domain.RuntimeSession, error) {
	i, err := scanInbox(s.Pool.QueryRow(ctx, `SELECT `+inboxColumns+` FROM bots.inbox WHERE id=$1`, inboxID))
	if err != nil {
		return domain.RuntimeSession{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.RuntimeSession{}, err
	}
	defer tx.Rollback(ctx)
	b, err := lockBot(ctx, tx, i.TenantID, i.BotID)
	if err != nil {
		return domain.RuntimeSession{}, err
	}
	if b.RuntimeAgentID == "" {
		return domain.RuntimeSession{}, domain.ErrBusy
	}
	_, err = tx.Exec(ctx, `INSERT INTO bots.runtime_sessions(session_key,tenant_id,bot_id,scope_id,revision,runtime_agent_id,provider,installation_id,channel_id,thread_id,target) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, i.SessionKey, i.TenantID, i.BotID, i.ScopeID, b.Revision, b.RuntimeAgentID, i.Target.Provider, i.Target.InstallationID, i.Target.ChannelID, i.Target.ThreadID, raw(i.Target))
	if err != nil {
		return domain.RuntimeSession{}, err
	}
	r, err := scanRuntimeSession(tx.QueryRow(ctx, `SELECT `+sessionColumns+` FROM bots.runtime_sessions WHERE session_key=$1`, i.SessionKey))
	if err != nil {
		return r, err
	}
	if r.TenantID != i.TenantID || r.BotID != i.BotID || r.ScopeID != i.ScopeID {
		return r, domain.ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.inbox SET revision=$2,runtime_session_id=$3 WHERE id=$1`, i.ID, r.Revision, r.RuntimeSessionID); err != nil {
		return r, err
	}
	request := r
	request.RuntimeSessionID = ""
	if err := putCommand(ctx, tx, domain.Command{ID: r.Key, TenantID: r.TenantID, BotID: r.BotID, Kind: "session", RefID: r.Key, Revision: r.Revision, Payload: raw(request)}); err != nil {
		return r, err
	}
	if err := tx.Commit(ctx); err != nil {
		return r, err
	}
	return r, nil
}
func (s *Store) SaveRuntimeSession(ctx context.Context, key, id string) error {
	if id == "" {
		return domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var existing string
	if err := tx.QueryRow(ctx, `SELECT runtime_session_id FROM bots.runtime_sessions WHERE session_key=$1 FOR UPDATE`, key).Scan(&existing); err != nil {
		return normalized(err)
	}
	if existing != "" && existing != id {
		return domain.ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.runtime_sessions SET runtime_session_id=$2 WHERE session_key=$1`, key, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.inbox SET runtime_session_id=$2 WHERE session_key=$1 AND runtime_session_id=''`, key, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.command_outbox SET state='accepted',handle=$2 WHERE id=$1 AND kind='session'`, key, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) PrepareRun(ctx context.Context, inboxID string) (domain.Command, error) {
	i, err := scanInbox(s.Pool.QueryRow(ctx, `SELECT `+inboxColumns+` FROM bots.inbox WHERE id=$1`, inboxID))
	if err != nil {
		return domain.Command{}, err
	}
	if i.RuntimeSessionID == "" || i.Revision <= 0 {
		return domain.Command{}, domain.ErrBusy
	}
	c := domain.Command{ID: i.CommandID, TenantID: i.TenantID, BotID: i.BotID, Kind: "run", RefID: i.ID, Revision: i.Revision, Payload: raw(map[string]any{"session_id": i.RuntimeSessionID, "input": "请求接纳时间(UTC): " + i.CreatedAt.UTC().Format(time.RFC3339Nano) + "\n用户消息: " + i.Input, "origin_command_id": i.CommandID})}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return c, err
	}
	defer tx.Rollback(ctx)
	if err := putCommand(ctx, tx, c); err != nil {
		return c, err
	}
	if err := tx.Commit(ctx); err != nil {
		return c, err
	}
	return scanCommand(s.Pool.QueryRow(ctx, `SELECT `+commandColumns+` FROM bots.command_outbox WHERE id=$1`, c.ID))
}
func (s *Store) SaveRunHandle(ctx context.Context, inboxID, runID string) error {
	if runID == "" {
		return domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	i, err := scanInbox(tx.QueryRow(ctx, `SELECT `+inboxColumns+` FROM bots.inbox WHERE id=$1 FOR UPDATE`, inboxID))
	if err != nil {
		return err
	}
	if i.RunID != "" && i.RunID != runID {
		return domain.ErrConflict
	}
	state := "running"
	if i.State == "complete" {
		state = i.State
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.inbox SET run_id=$2,state=$3,updated_at=now() WHERE id=$1`, inboxID, runID, state); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.command_outbox SET state='accepted',handle=$2 WHERE id=$1`, i.CommandID, runID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) ListRunningInbox(ctx context.Context, tenant string) ([]domain.Inbox, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+inboxColumns+` FROM bots.inbox WHERE state='running' AND tenant_id=$1 ORDER BY created_at LIMIT 100`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Inbox{}
	for rows.Next() {
		i, err := scanInbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Store) ListProcessingInbox(ctx context.Context, tenant string) ([]domain.Inbox, error) {
	if tenant == "" {
		return nil, domain.ErrInvalid
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+inboxColumns+` FROM bots.inbox WHERE tenant_id=$1 AND state='processing' ORDER BY created_at LIMIT 100`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Inbox{}
	for rows.Next() {
		i, err := scanInbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
func (s *Store) FinishRun(ctx context.Context, inboxID string, result domain.RunResult) error {
	if result.Status != "completed" && result.Status != "failed" && result.Status != "canceled" {
		return domain.ErrInvalid
	}
	initial, err := scanInbox(s.Pool.QueryRow(ctx, `SELECT `+inboxColumns+` FROM bots.inbox WHERE id=$1`, inboxID))
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := lockBot(ctx, tx, initial.TenantID, initial.BotID); err != nil {
		return err
	}
	i, err := scanInbox(tx.QueryRow(ctx, `SELECT `+inboxColumns+` FROM bots.inbox WHERE id=$1 FOR UPDATE`, inboxID))
	if err != nil {
		return err
	}
	if i.RunID == "" {
		return domain.ErrConflict
	}
	if i.State == "complete" {
		var saved []byte
		if err := tx.QueryRow(ctx, `SELECT result FROM bots.inbox WHERE id=$1`, i.ID).Scan(&saved); err != nil {
			return err
		}
		if domain.Hash(json.RawMessage(saved)) != domain.Hash(result) {
			return domain.ErrConflict
		}
		return nil
	}
	text := result.Final
	if text == "" {
		text = "请求未能完成（" + result.Status + "），请查看运行记录。"
	}
	d := domain.Delivery{ID: domain.StableID("delivery_final_", i.RunID), OperationID: domain.StableID("final_", i.RunID), TenantID: i.TenantID, BotID: i.BotID, ScopeID: i.ScopeID, CommandID: i.CommandID, Target: i.Target, Text: text}
	if err := insertDelivery(ctx, tx, d); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.inbox SET state='complete',result=$2,updated_at=now() WHERE id=$1`, i.ID, raw(result)); err != nil {
		return err
	}
	if err := audit(ctx, tx, domain.Scope{TenantID: i.TenantID, BotID: i.BotID, ScopeID: i.ScopeID}, "run_final", i.RunID, i.Revision, "runtime", map[string]any{"command_id": i.CommandID, "status": result.Status, "delivery_id": d.ID, "usage": result.Usage}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const commandColumns = `id,tenant_id,bot_id,kind,ref_id,revision,request_hash,payload,handle,state,error,attempts,next_attempt_at`

func scanCommand(row pgx.Row) (domain.Command, error) {
	var c domain.Command
	err := row.Scan(&c.ID, &c.TenantID, &c.BotID, &c.Kind, &c.RefID, &c.Revision, &c.RequestHash, &c.Payload, &c.Handle, &c.State, &c.Error, &c.Attempts, &c.NextAttemptAt)
	return c, normalized(err)
}
func (s *Store) ListPendingCommands(ctx context.Context, tenant, kind string, n int) ([]domain.Command, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+commandColumns+` FROM bots.command_outbox WHERE tenant_id=$1 AND state='pending' AND next_attempt_at<=now() AND ($2='' OR kind=$2) ORDER BY created_at LIMIT $3`, tenant, kind, limit(n))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Command{}
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) SaveCommandReceipt(ctx context.Context, id, handle string) error {
	if handle == "" {
		return domain.ErrInvalid
	}
	initial, err := scanCommand(s.Pool.QueryRow(ctx, `SELECT `+commandColumns+` FROM bots.command_outbox WHERE id=$1`, id))
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := lockBot(ctx, tx, initial.TenantID, initial.BotID); err != nil {
		return err
	}
	if initial.Kind == "timer" {
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id FROM bots.tasks WHERE id=$1 FOR UPDATE`, initial.RefID).Scan(&locked); err != nil {
			return normalized(err)
		}
	}
	c, err := scanCommand(tx.QueryRow(ctx, `SELECT `+commandColumns+` FROM bots.command_outbox WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return err
	}
	if c.Handle != "" && c.Handle != handle {
		return domain.ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE bots.command_outbox SET state='accepted',handle=$2,error='' WHERE id=$1`, id, handle); err != nil {
		return err
	}
	if c.Kind == "timer" {
		if _, err := tx.Exec(ctx, `UPDATE bots.tasks SET timer_handle=$3,status=CASE WHEN status='pending_schedule' THEN 'scheduled' ELSE status END WHERE id=$1 AND revision=$2`, c.RefID, c.Revision, handle); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s *Store) FailCommand(ctx context.Context, id, reason string, next time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE bots.command_outbox SET attempts=attempts+1,error=$2,next_attempt_at=$3 WHERE id=$1 AND state='pending'`, id, reason, next)
	return err
}

func (s *Store) BlockCommand(ctx context.Context, id, reason string) error {
	initial, err := scanCommand(s.Pool.QueryRow(ctx, `SELECT `+commandColumns+` FROM bots.command_outbox WHERE id=$1`, id))
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := lockBot(ctx, tx, initial.TenantID, initial.BotID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE bots.command_outbox SET state='blocked',error=$2 WHERE id=$1 AND state='pending'`, id, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if err := audit(ctx, tx, domain.Scope{TenantID: initial.TenantID, BotID: initial.BotID}, "command_blocked", id, initial.Revision, "dispatcher", map[string]string{"error": reason, "request_hash": initial.RequestHash}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func timerCommand(t domain.Task) domain.Command {
	return domain.Command{ID: domain.StableID("timer_command_", t.ID, strconv.Itoa(t.Revision)), TenantID: t.TenantID, BotID: t.BotID, Kind: "timer", RefID: t.ID, Revision: t.Revision, Payload: raw(map[string]any{"tenant_id": t.TenantID, "subject_ref": t.BotID, "scope_id": t.ScopeID, "timer_id": t.TimerID, "revision": t.Revision, "due_at": t.DueAt.UTC(), "opaque_action_ref": t.ID})}
}

func (s *Store) HasConversation(ctx context.Context, tenant, bot, key string) (bool, error) {
	if tenant == "" || bot == "" || key == "" {
		return false, domain.ErrInvalid
	}
	var found bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bots.runtime_sessions WHERE tenant_id=$1 AND bot_id=$2 AND session_key=$3) OR EXISTS(SELECT 1 FROM bots.inbox WHERE tenant_id=$1 AND bot_id=$2 AND session_key=$3 AND namespace NOT IN ('tool','fire'))`, tenant, bot, key).Scan(&found)
	return found, err
}
