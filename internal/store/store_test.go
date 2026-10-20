package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bingdilotus/bots/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	s      *Store
	tenant string
	bot    domain.Bot
	inbox  domain.Inbox
	scope  domain.Scope
}

func setupPG(t *testing.T) fixture {
	t.Helper()
	dsn := os.Getenv("BOTS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set BOTS_TEST_DATABASE_URL to an isolated bots_test database")
	}
	s, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Bootstrap(t.Context()); err != nil {
		s.Close()
		t.Fatal(err)
	}
	tenant, err := domain.NewID("test_")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tx, err := s.Pool.Begin(ctx)
		if err == nil {
			defer tx.Rollback(ctx)
			for _, table := range []string{"audit_events", "deliveries", "command_outbox", "tasks", "knowledge", "inbox", "runtime_sessions", "bots"} {
				if _, err := tx.Exec(ctx, `DELETE FROM bots.`+table+` WHERE tenant_id=$1`, tenant); err != nil {
					t.Log(err)
				}
			}
			if err := tx.Commit(ctx); err != nil {
				t.Log(err)
			}
		}
		s.Close()
	})
	b, err := s.ImportBot(t.Context(), domain.Bot{TenantID: tenant, ID: "bot", Name: "test", Enabled: true, AccountEnabled: true, Config: domain.BotConfig{Model: "fixture", Instructions: "native tools", Timezone: "Asia/Shanghai", ChannelKind: "slack", InstallationID: "install", ChannelID: "channel", BotUserID: "identity", TokenEnv: "TEST_TOKEN", SigningSecretEnv: "TEST_SIGNING", BridgeRef: "bridge", TimerTargetRef: "fire", MaxPendingTasks: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRuntimeAgent(t.Context(), tenant, "bot", 1, "agent-one"); err != nil {
		t.Fatal(err)
	}
	f := fixture{s: s, tenant: tenant, bot: b}
	f.inbox = f.startInput(t, "event-one", "thread", "run-one")
	f.scope = domain.Scope{TenantID: tenant, BotID: "bot", ScopeID: f.inbox.ScopeID}
	return f
}
func (f fixture) startInput(t *testing.T, event, thread, run string) domain.Inbox {
	t.Helper()
	ctx := t.Context()
	i, created, err := f.s.AcceptInbox(ctx, domain.Inbox{TenantID: f.tenant, BotID: "bot", Namespace: "slack", SourceID: event, Input: "remember meeting and remind", Target: domain.Target{Provider: "slack", InstallationID: "install", ChannelID: "channel", ThreadID: thread, MessageID: event}})
	if err != nil || !created {
		t.Fatalf("accept: %+v %v", i, err)
	}
	claimed, err := f.s.ClaimInbox(ctx, f.tenant)
	if err != nil || claimed.ID != i.ID {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	r, err := f.s.EnsureRuntimeSession(ctx, i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.RuntimeSessionID == "" {
		if err := f.s.SaveRuntimeSession(ctx, r.Key, domain.StableID("session-", r.Key)); err != nil {
			t.Fatal(err)
		}
	}
	c, err := f.s.PrepareRun(ctx, i.ID)
	if err != nil {
		t.Fatal(err)
	}
	var command struct {
		Input string `json:"input"`
	}
	if json.Unmarshal(c.Payload, &command) != nil || !strings.Contains(command.Input, "请求接纳时间(UTC): ") {
		t.Fatalf("missing fixed request time: %s", c.Payload)
	}
	if err := f.s.SaveRunHandle(ctx, i.ID, run); err != nil {
		t.Fatal(err)
	}
	i.RunID = run
	return i
}
func (f fixture) call(t *testing.T, op, tool string, args any) json.RawMessage {
	t.Helper()
	result, err := f.s.ExecuteTool(t.Context(), domain.ToolCall{Context: domain.ToolContext{TenantID: f.tenant, SubjectRef: "bot", ScopeID: f.inbox.ScopeID, OriginCommandID: f.inbox.CommandID, RunID: f.inbox.RunID, OperationID: op, ToolCallID: op}, ToolRef: tool, Arguments: raw(args)})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func taskID(t *testing.T, result json.RawMessage) string {
	t.Helper()
	var value struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(result, &value); err != nil || value.TaskID == "" {
		t.Fatalf("expected task result: %s %v", result, err)
	}
	return value.TaskID
}
func (f fixture) createTask(t *testing.T) domain.Task {
	t.Helper()
	id := taskID(t, f.call(t, "create", "create_reminder", map[string]any{"due_at": time.Now().Add(time.Minute).UTC(), "text": "meeting"}))
	task, err := f.s.GetTask(t.Context(), f.scope, id)
	if err != nil {
		t.Fatal(err)
	}
	return task
}
func (f fixture) fire(t *testing.T, task domain.Task) domain.TimerFire {
	t.Helper()
	fire := domain.TimerFire{TenantID: f.tenant, TimerID: task.TimerID, Revision: task.Revision, FireID: domain.StableID("fire", task.ID, string(raw(task.Revision))), OpaqueActionRef: task.ID}
	if _, err := f.s.AcceptFire(t.Context(), fire); err != nil {
		t.Fatal(err)
	}
	return fire
}

// Schema repair tests own a database because their DDL must not interfere with
// service package tests using the shared bots_test schema.
func setupBootstrapPG(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("BOTS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set BOTS_TEST_DATABASE_URL to an isolated bots_test database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	var adminDatabase string
	if err := admin.Pool.QueryRow(t.Context(), `SELECT current_database()`).Scan(&adminDatabase); err != nil || adminDatabase != cfg.ConnConfig.Database {
		t.Fatalf("schema test admin connected to %q instead of %q: %v", adminDatabase, cfg.ConnConfig.Database, err)
	}
	name, err := domain.NewID("bots_bootstrap_test_")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Pool.Exec(t.Context(), `CREATE DATABASE `+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool == nil {
			t.Logf("leaving unverified temporary database %q", name)
			return
		}
		defer pool.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var connectedDatabase string
		if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&connectedDatabase); err != nil || connectedDatabase != name {
			t.Errorf("refusing cleanup for unverified database %q: connected=%q, err=%v", name, connectedDatabase, err)
			return
		}
		pool.Close()
		if _, err := admin.Pool.Exec(ctx, `DROP DATABASE `+pgx.Identifier{name}.Sanitize()+` WITH (FORCE)`); err != nil {
			t.Error(err)
		}
	})
	cfg.ConnConfig.Database = name
	pool, err = pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{Pool: pool}
	var connectedDatabase string
	if err := s.Pool.QueryRow(t.Context(), `SELECT current_database()`).Scan(&connectedDatabase); err != nil {
		t.Fatal(err)
	}
	if connectedDatabase != name {
		t.Fatalf("schema test connected to %q instead of its own database %q", connectedDatabase, name)
	}
	return s
}

func TestBootstrapDoesNotLockInitializedTables(t *testing.T) {
	s := setupBootstrapPG(t)
	ctx := t.Context()
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO bots.bots(tenant_id,bot_id,name,enabled,account_enabled,config_hash,config) VALUES('bootstrap','bot','test',true,true,'hash','{}')`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := audit(ctx, tx, domain.Scope{TenantID: "bootstrap", BotID: "bot"}, "test", "test", 1, "test", nil); err != nil {
		t.Fatal(err)
	}

	otherPool, err := pgxpool.NewWithConfig(ctx, s.Pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	other := &Store{Pool: otherPool}
	defer other.Close()
	var otherDatabase string
	if err := other.Pool.QueryRow(ctx, `SELECT current_database()`).Scan(&otherDatabase); err != nil || otherDatabase != s.Pool.Config().ConnConfig.Database {
		t.Fatalf("bootstrap peer connected to unexpected database %q: %v", otherDatabase, err)
	}
	bootstrapCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	// The audit write remains uncommitted. Repeating CREATE INDEX takes a
	// ShareLock here and cannot finish; once it holds command_outbox's ShareLock,
	// the following command insert completes the production deadlock cycle.
	if err := other.Bootstrap(bootstrapCtx); err != nil {
		t.Fatalf("bootstrap blocked by an active audit transaction: %v", err)
	}
	if err := putCommand(ctx, tx, domain.Command{ID: "bootstrap-command", TenantID: "bootstrap", BotID: "bot", Kind: "agent", RefID: "bot", Revision: 1, Payload: raw(map[string]string{})}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapRepairsMissingRelations(t *testing.T) {
	s := setupBootstrapPG(t)
	ctx := t.Context()
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	for _, drop := range []string{`DROP INDEX bots.inbox_pending`, `DROP TABLE bots.knowledge`} {
		if _, err := s.Pool.Exec(ctx, drop); err != nil {
			t.Fatal(err)
		}
		if err := s.Bootstrap(ctx); err != nil {
			t.Fatalf("repair after %s: %v", drop, err)
		}
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ready, err := schemaReady(ctx, tx)
		tx.Rollback(ctx)
		if err != nil || !ready {
			t.Fatalf("schema incomplete after %s: ready=%v, err=%v", drop, ready, err)
		}
	}
}

func TestOperationReceiptAndOriginSlotAreTransactional(t *testing.T) {
	f := setupPG(t)
	ctx := t.Context()
	first := f.call(t, "remember", "remember", map[string]string{"content": "meeting is at three", "kind": "knowledge"})
	var knowledge struct {
		ID string `json:"knowledge_id"`
	}
	if json.Unmarshal(first, &knowledge) != nil || knowledge.ID == "" {
		t.Fatalf("missing opaque id: %s", first)
	}
	again := f.call(t, "remember", "remember", map[string]string{"content": "meeting is at three", "kind": "knowledge"})
	if domain.Hash(first) != domain.Hash(again) {
		t.Fatal("response-loss retry changed result")
	}
	items, err := f.s.ListKnowledge(ctx, f.scope, 20)
	if err != nil || len(items) != 1 || items[0].ID != knowledge.ID {
		t.Fatalf("knowledge duplicated/lost: %+v %v", items, err)
	}
	_, err = f.s.ExecuteTool(ctx, domain.ToolCall{Context: domain.ToolContext{TenantID: f.tenant, SubjectRef: "bot", ScopeID: f.scope.ScopeID, OriginCommandID: f.inbox.CommandID, RunID: f.inbox.RunID, OperationID: "remember", ToolCallID: "remember"}, ToolRef: "remember", Arguments: raw(map[string]string{"content": "changed", "kind": "knowledge"})})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("same op different payload accepted: %v", err)
	}
	due := time.Now().Add(time.Minute).UTC()
	args := map[string]any{"due_at": due, "text": "meeting"}
	id := taskID(t, f.call(t, "create-one", "create_reminder", args))
	same := taskID(t, f.call(t, "create-two", "create_reminder", args))
	if id != same {
		t.Fatal("origin slot duplicated")
	}
	tasks, _ := f.s.ListTasks(ctx, f.scope, 20)
	commands, _ := f.s.ListPendingCommands(ctx, f.tenant, "timer", 20)
	if len(tasks) != 1 || len(commands) != 1 {
		t.Fatalf("task/outbox count: %d %d", len(tasks), len(commands))
	}
	if result := f.call(t, "create-three", "create_reminder", map[string]any{"due_at": due, "text": "changed"}); !strings.Contains(string(result), "conflict") {
		t.Fatalf("changed intent silently replaced slot: %s", result)
	}
	updated := f.call(t, "update", "update_reminder", map[string]any{"task_id": id, "expected_revision": 1, "due_at": due.Add(time.Minute), "text": "updated"})
	retry := f.call(t, "update", "update_reminder", map[string]any{"task_id": id, "expected_revision": 1, "due_at": due.Add(time.Minute), "text": "updated"})
	if domain.Hash(updated) != domain.Hash(retry) {
		t.Fatalf("successful update became CAS conflict: %s / %s", updated, retry)
	}
	if err := f.s.SetBotEnabled(ctx, f.tenant, "bot", false); err != nil {
		t.Fatal(err)
	}
	retry = f.call(t, "remember", "remember", map[string]string{"content": "meeting is at three", "kind": "knowledge"})
	if domain.Hash(retry) != domain.Hash(first) {
		t.Fatal("pause prevented original receipt replay")
	}
}

func TestFireAcceptancePauseOldRevisionAndReceipt(t *testing.T) {
	f := setupPG(t)
	ctx := t.Context()
	task := f.createTask(t)
	if err := f.s.SetBotEnabled(ctx, f.tenant, "bot", false); err != nil {
		t.Fatal(err)
	}
	fire := f.fire(t, task)
	if created, err := f.s.AcceptFire(ctx, fire); err != nil || created {
		t.Fatalf("fire retry not deduped: %v %v", created, err)
	}
	if _, err := f.s.ClaimDelivery(ctx, f.tenant); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("paused sender claimed: %v", err)
	}
	commands, _ := f.s.ListPendingCommands(ctx, f.tenant, "timer", 20)
	if err := f.s.SaveCommandReceipt(ctx, commands[0].ID, "timer-handle"); err != nil {
		t.Fatal(err)
	}
	current, _ := f.s.GetTask(ctx, f.scope, task.ID)
	if current.Status != "due" {
		t.Fatalf("late acceptance overwrote fire: %+v", current)
	}
	if err := f.s.SetBotEnabled(ctx, f.tenant, "bot", true); err != nil {
		t.Fatal(err)
	}
	delivery, err := f.s.ClaimDelivery(ctx, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if result := f.call(t, "cancel-claimed", "cancel_reminder", map[string]any{"task_id": task.ID, "expected_revision": 1}); !strings.Contains(string(result), "in_flight_or_unknown") {
		t.Fatalf("claimed cancellation accepted: %s", result)
	}
	if err := f.s.CompleteDelivery(ctx, delivery.ID, delivery.PayloadHash, "provider-ts"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.CompleteDelivery(ctx, delivery.ID, delivery.PayloadHash, "provider-ts"); err != nil {
		t.Fatal(err)
	}
	current, _ = f.s.GetTask(ctx, f.scope, task.ID)
	if current.Status != "completed" {
		t.Fatal("receipt and task completion diverged")
	}
	deliveries, _ := f.s.ListDeliveries(ctx, f.scope, 20)
	if len(deliveries) != 1 || deliveries[0].ProviderMessageID != "provider-ts" {
		t.Fatalf("receipt lost: %+v", deliveries)
	}
	var confirmed int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM bots.audit_events WHERE tenant_id=$1 AND kind='delivery_confirmed'`, f.tenant).Scan(&confirmed); err != nil || confirmed != 1 {
		t.Fatalf("receipt audit duplicated: %d %v", confirmed, err)
	}
}

func TestUpdateCancelFenceOldTimersAndUnknown(t *testing.T) {
	f := setupPG(t)
	ctx := t.Context()
	task := f.createTask(t)
	f.call(t, "update", "update_reminder", map[string]any{"task_id": task.ID, "expected_revision": 1, "due_at": task.DueAt.Add(time.Minute), "text": "new time"})
	f.fire(t, task)
	deliveries, _ := f.s.ListDeliveries(ctx, f.scope, 20)
	if len(deliveries) != 0 {
		t.Fatal("old timer produced a delivery")
	}
	current, _ := f.s.GetTask(ctx, f.scope, task.ID)
	f.fire(t, current)
	d, err := f.s.ClaimDelivery(ctx, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.Recover(ctx, f.tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ClaimDelivery(ctx, f.tenant); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown resent: %v", err)
	}
	if result := f.call(t, "update-unknown", "update_reminder", map[string]any{"task_id": task.ID, "expected_revision": current.Revision, "due_at": current.DueAt.Add(time.Minute), "text": "replacement"}); !strings.Contains(string(result), "in_flight_or_unknown") {
		t.Fatalf("unknown replaced: %s", result)
	}
	// A late receipt is still evidence, but must not overwrite a newer task state.
	if _, err := f.s.Pool.Exec(ctx, `UPDATE bots.tasks SET revision=revision+1,status='canceled' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.CompleteDelivery(ctx, d.ID, d.PayloadHash, "late-provider-ts"); err != nil {
		t.Fatal(err)
	}
	current, _ = f.s.GetTask(ctx, f.scope, task.ID)
	if current.Status != "canceled" || current.Revision != 3 {
		t.Fatalf("late receipt overwrote newer revision: %+v", current)
	}
}

func TestInboxSerializationRecoveryAndFrozenSession(t *testing.T) {
	f := setupPG(t)
	ctx := t.Context()
	duplicate, created, err := f.s.AcceptInbox(ctx, domain.Inbox{TenantID: f.tenant, BotID: "bot", Namespace: "slack", SourceID: "event-one", Input: "remember meeting and remind", Target: f.inbox.Target})
	if err != nil || created || duplicate.ID != f.inbox.ID {
		t.Fatalf("source dedup: %+v %v %v", duplicate, created, err)
	}
	if _, err := f.s.ClaimInbox(ctx, f.tenant); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("more than one run per bot admitted")
	}
	result := domain.RunResult{Status: "completed", Final: "done", Error: json.RawMessage(`null`), Usage: json.RawMessage(`{"tokens_total":10}`)}
	if err := f.s.FinishRun(ctx, f.inbox.ID, result); err != nil {
		t.Fatal(err)
	}
	if err := f.s.FinishRun(ctx, f.inbox.ID, result); err != nil {
		t.Fatal(err)
	}
	newConfig := f.bot.Config
	newConfig.Instructions = "new SOP"
	newConfig = defaults(newConfig)
	if _, err := f.s.UpdateConfig(ctx, f.tenant, "bot", 1, newConfig, "agent-two"); err != nil {
		t.Fatal(err)
	}
	i := f.startInput(t, "event-two", "thread", "run-two")
	session, err := f.s.EnsureRuntimeSession(ctx, i.ID)
	if err != nil || session.Revision != 1 || session.RuntimeAgentID != "agent-one" {
		t.Fatalf("old thread changed configuration: %+v %v", session, err)
	}
	if found, err := f.s.HasConversation(ctx, f.tenant, "bot", session.Key); err != nil || !found {
		t.Fatalf("conversation lost: %v %v", found, err)
	}
	if found, err := f.s.HasConversation(ctx, "another-tenant", "bot", session.Key); err != nil || found {
		t.Fatalf("conversation leaked across tenant: %v %v", found, err)
	}
	if err := f.s.FinishRun(ctx, i.ID, result); err != nil {
		t.Fatal(err)
	}
	third, _, err := f.s.AcceptInbox(ctx, domain.Inbox{TenantID: f.tenant, BotID: "bot", Namespace: "slack", SourceID: "event-three", Input: "new thread", Target: domain.Target{Provider: "slack", InstallationID: "install", ChannelID: "channel", ThreadID: "new-thread", MessageID: "event-three"}})
	if err != nil {
		t.Fatal(err)
	}
	var claimed int
	var wg sync.WaitGroup
	var mu sync.Mutex
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.s.ClaimInbox(ctx, f.tenant); err == nil {
				mu.Lock()
				claimed++
				mu.Unlock()
			} else if !errors.Is(err, domain.ErrNotFound) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if claimed != 1 {
		t.Fatalf("concurrent bot claims = %d", claimed)
	}
	if err := f.s.Recover(ctx, f.tenant); err != nil {
		t.Fatal(err)
	}
	replayed, err := f.s.ClaimInbox(ctx, f.tenant)
	if err != nil || replayed.ID != third.ID {
		t.Fatalf("pending command not recovered: %+v %v", replayed, err)
	}
	newSession, err := f.s.EnsureRuntimeSession(ctx, third.ID)
	if err != nil || newSession.Revision != 2 {
		t.Fatalf("new thread did not use active revision: %+v %v", newSession, err)
	}
}

func TestScopeIsolationAndDirectSourceInvalidation(t *testing.T) {
	f := setupPG(t)
	ctx := t.Context()
	f.call(t, "learn", "remember", map[string]string{"content": "private source fact", "kind": "experience"})
	task := f.createTask(t)
	for _, scope := range []domain.Scope{{TenantID: "other", BotID: "bot", ScopeID: f.scope.ScopeID}, {TenantID: f.tenant, BotID: "other", ScopeID: f.scope.ScopeID}, {TenantID: f.tenant, BotID: "bot", ScopeID: "other"}} {
		if rows, err := f.s.ListKnowledge(ctx, scope, 20); err != nil || len(rows) != 0 {
			t.Fatalf("knowledge scope leak: %+v %v", rows, err)
		}
		if _, err := f.s.GetTask(ctx, scope, task.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("task scope leak: %v", err)
		}
	}
	if err := f.s.InvalidateMessage(ctx, f.tenant, "bot", "install", "channel", "event-one", "edited"); err != nil {
		t.Fatal(err)
	}
	knowledge, _ := f.s.ListKnowledge(ctx, f.scope, 20)
	current, _ := f.s.GetTask(ctx, f.scope, task.ID)
	if len(knowledge) != 0 || current.Revision != 1 {
		t.Fatalf("edit semantics wrong: %+v %+v", knowledge, current)
	}
	if err := f.s.InvalidateMessage(ctx, f.tenant, "bot", "install", "channel", "event-one", "deleted"); err != nil {
		t.Fatal(err)
	}
	current, _ = f.s.GetTask(ctx, f.scope, task.ID)
	if current.Status != "canceled" || current.Revision != 2 {
		t.Fatalf("deleted source did not fence pending reminder: %+v", current)
	}
	f.fire(t, task)
	if rows, _ := f.s.ListDeliveries(ctx, f.scope, 20); len(rows) != 0 {
		t.Fatal("deleted source generated delivery")
	}
}

func TestTenantLeaseAndPermanentCommandBlock(t *testing.T) {
	f := setupPG(t)
	ctx := t.Context()
	lease, err := f.s.AcquireTenant(ctx, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.AcquireTenant(ctx, f.tenant); !errors.Is(err, domain.ErrBusy) {
		t.Fatalf("second engine acquired live tenant: %v", err)
	}
	if err := lease.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := lease.Check(ctx); !errors.Is(err, domain.ErrBusy) {
		t.Fatalf("closed lease still accepted: %v", err)
	}
	next, err := f.s.AcquireTenant(ctx, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(ctx)
	task := f.createTask(t)
	command := timerCommand(task)
	if err := f.s.BlockCommand(ctx, command.ID, "idempotency_conflict"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.BlockCommand(ctx, command.ID, "idempotency_conflict"); err != nil {
		t.Fatal(err)
	}
	pending, err := f.s.ListPendingCommands(ctx, f.tenant, "timer", 20)
	if err != nil || len(pending) != 0 {
		t.Fatalf("blocked command requeued: %+v %v", pending, err)
	}
	var count int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM bots.audit_events WHERE tenant_id=$1 AND kind='command_blocked'`, f.tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("command block audit duplicated: %d %v", count, err)
	}
}

func TestNativeMutationRollsBackIfOutboxConflicts(t *testing.T) {
	f := setupPG(t)
	ctx := t.Context()
	id := domain.StableID("task_", f.tenant, "bot", f.inbox.CommandID, "0")
	commandID := domain.StableID("timer_command_", id, "1")
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO bots.command_outbox(id,tenant_id,bot_id,kind,ref_id,revision,request_hash,payload) VALUES($1,$2,'bot','timer',$3,1,'different','{}')`, commandID, f.tenant, id); err != nil {
		t.Fatal(err)
	}
	result := f.call(t, "conflicting-create", "create_reminder", map[string]any{"due_at": time.Now().Add(time.Minute).UTC(), "text": "meeting"})
	if !strings.Contains(string(result), "conflict") {
		t.Fatalf("outbox conflict was hidden: %s", result)
	}
	tasks, err := f.s.ListTasks(ctx, f.scope, 20)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("mutation committed without its outbox: %+v %v", tasks, err)
	}
	var count int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM bots.audit_events WHERE tenant_id=$1 AND kind='task_created'`, f.tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed mutation left success audit: %d %v", count, err)
	}
}

func TestFirstTrustedToolCanBindRunBeforeSubmissionResponse(t *testing.T) {
	f := setupPG(t)
	ctx := t.Context()
	if err := f.s.FinishRun(ctx, f.inbox.ID, domain.RunResult{Status: "completed", Final: "done"}); err != nil {
		t.Fatal(err)
	}
	i, _, err := f.s.AcceptInbox(ctx, domain.Inbox{TenantID: f.tenant, BotID: "bot", Namespace: "slack", SourceID: "fast-event", Input: "fast tool", Target: domain.Target{Provider: "slack", InstallationID: "install", ChannelID: "channel", ThreadID: "fast-thread", MessageID: "fast-event"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ClaimInbox(ctx, f.tenant); err != nil {
		t.Fatal(err)
	}
	session, err := f.s.EnsureRuntimeSession(ctx, i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.SaveRuntimeSession(ctx, session.Key, "fast-session"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PrepareRun(ctx, i.ID); err != nil {
		t.Fatal(err)
	}
	f.inbox = i
	f.inbox.RunID = "early-run"
	f.call(t, "early-op", "remember", map[string]string{"content": "early result", "kind": "knowledge"})
	if err := f.s.SaveRunHandle(ctx, i.ID, "early-run"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SaveRunHandle(ctx, i.ID, "wrong-run"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("trusted run binding changed: %v", err)
	}
	running, err := f.s.ListRunningInbox(ctx, f.tenant)
	if err != nil || len(running) != 1 || running[0].RunID != "early-run" {
		t.Fatalf("early run was lost: %+v %v", running, err)
	}
}

func TestConfigurationCannotSwitchDeploymentIdentity(t *testing.T) {
	f := setupPG(t)
	ctx := t.Context()
	bot, err := f.s.GetBot(ctx, f.tenant, "bot")
	if err != nil {
		t.Fatal(err)
	}
	changed := bot.Config
	changed.TokenEnv = "ANOTHER_ACCOUNT_TOKEN"
	if _, err := f.s.UpdateConfig(ctx, f.tenant, "bot", bot.Revision, changed, "other-agent"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("deployment identity switched: %v", err)
	}
	unchanged, err := f.s.GetBot(ctx, f.tenant, "bot")
	if err != nil || unchanged.Revision != bot.Revision || unchanged.RuntimeAgentID != bot.RuntimeAgentID || !unchanged.Config.SameDeployment(bot.Config) {
		t.Fatalf("rejected configuration mutated state: %+v %v", unchanged, err)
	}
	changed = bot.Config
	changed.Instructions = "improved SOP"
	changed.Timezone = "UTC"
	if _, err := f.s.UpdateConfig(ctx, f.tenant, "bot", bot.Revision, changed, "improved-agent"); err != nil {
		t.Fatal(err)
	}
	old, agent, err := f.s.ConfigRevision(ctx, f.tenant, "bot", 1)
	if err != nil || old.Instructions != bot.Config.Instructions || agent != bot.RuntimeAgentID {
		t.Fatalf("old immutable snapshot changed: %+v %s %v", old, agent, err)
	}
}

func TestPendingQuotaIncludesUnknownAndBlockedWithoutChargingSlotRetries(t *testing.T) {
	for _, status := range []string{"unknown", "blocked"} {
		t.Run(status, func(t *testing.T) {
			f := setupPG(t)
			ctx := t.Context()
			cfg := f.bot.Config
			cfg.MaxPendingTasks = 1
			if _, err := f.s.UpdateConfig(ctx, f.tenant, "bot", 1, cfg, "quota-agent"); err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"due_at": time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond), "text": "one logical reminder"}
			original := f.call(t, "quota-create", "create_reminder", args)
			id := taskID(t, original)
			task, err := f.s.GetTask(ctx, f.scope, id)
			if err != nil {
				t.Fatal(err)
			}
			f.fire(t, task)
			delivery, err := f.s.ClaimDelivery(ctx, f.tenant)
			if err != nil {
				t.Fatal(err)
			}
			outcome := "unknown"
			if status == "blocked" {
				outcome = "rejected"
			}
			if err := f.s.FailDelivery(ctx, delivery.ID, delivery.PayloadHash, outcome, "fixture outcome", time.Time{}); err != nil {
				t.Fatal(err)
			}
			current, err := f.s.GetTask(ctx, f.scope, id)
			if err != nil || current.Status != status {
				t.Fatalf("task did not retain %s responsibility: %+v %v", status, current, err)
			}
			// Both RPC replay and a new call for the same origin slot remain one
			// task, even though its unresolved responsibility consumes all quota.
			if replay := f.call(t, "quota-create", "create_reminder", args); domain.Hash(replay) != domain.Hash(original) {
				t.Fatalf("original creation receipt changed: %s / %s", original, replay)
			}
			if replayID := taskID(t, f.call(t, "same-slot", "create_reminder", args)); replayID != id {
				t.Fatal("same origin slot charged a second task")
			}
			if err := f.s.FinishRun(ctx, f.inbox.ID, domain.RunResult{Status: "completed", Final: "done"}); err != nil {
				t.Fatal(err)
			}
			f.inbox = f.startInput(t, "second-origin", "second-thread", "second-run")
			refused := f.call(t, "over-quota", "create_reminder", map[string]any{"due_at": task.DueAt.Add(time.Minute), "text": "second logical reminder"})
			if !strings.Contains(string(refused), "pending_task_limit") {
				t.Fatalf("%s responsibility released its quota: %s", status, refused)
			}
			tasks, err := f.s.ListTasks(ctx, f.scope, 20)
			if err != nil || len(tasks) != 1 || tasks[0].ID != id || tasks[0].Status != status {
				t.Fatalf("quota refusal mutated existing responsibility: %+v %v", tasks, err)
			}
		})
	}
}

func TestSuccessfulCancelResponseLossReplaysAndFencesOldFire(t *testing.T) {
	f := setupPG(t)
	ctx := t.Context()
	task := f.createTask(t)
	args := map[string]any{"task_id": task.ID, "expected_revision": task.Revision}
	original := f.call(t, "cancel-once", "cancel_reminder", args)
	var canceled struct {
		Revision int    `json:"revision"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(original, &canceled); err != nil || canceled.Revision != 2 || canceled.Status != "canceled" {
		t.Fatalf("cancel did not commit: %s %v", original, err)
	}
	if retry := f.call(t, "cancel-once", "cancel_reminder", args); domain.Hash(retry) != domain.Hash(original) {
		t.Fatalf("response-loss retry became a revision conflict: %s / %s", original, retry)
	}
	f.fire(t, task)
	if _, err := f.s.ClaimDelivery(ctx, f.tenant); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("old fire obtained send claim after cancellation: %v", err)
	}
	deliveries, err := f.s.ListDeliveries(ctx, f.scope, 20)
	if err != nil || len(deliveries) != 0 {
		t.Fatalf("old revision produced delivery responsibility: %+v %v", deliveries, err)
	}
	current, err := f.s.GetTask(ctx, f.scope, task.ID)
	if err != nil || current.Status != "canceled" || current.Revision != 2 {
		t.Fatalf("old fire changed canceled task: %+v %v", current, err)
	}
	var count int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM bots.audit_events WHERE tenant_id=$1 AND kind='cancel_reminder'`, f.tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("cancel replay duplicated mutation audit: %d %v", count, err)
	}
}

func TestDisabledAccountBlocksNewWorkButReplaysOriginalOperation(t *testing.T) {
	f := setupPG(t)
	ctx := t.Context()
	args := map[string]string{"content": "known result before account disabled", "kind": "knowledge"}
	original := f.call(t, "learn-before-disable", "remember", args)
	task := f.createTask(t)
	f.fire(t, task)
	if err := f.s.SetAccountEnabled(ctx, f.tenant, "bot", false); err != nil {
		t.Fatal(err)
	}
	if replay := f.call(t, "learn-before-disable", "remember", args); domain.Hash(replay) != domain.Hash(original) {
		t.Fatalf("account disable erased known operation receipt: %s / %s", original, replay)
	}
	_, err := f.s.ExecuteTool(ctx, domain.ToolCall{Context: domain.ToolContext{TenantID: f.tenant, SubjectRef: "bot", ScopeID: f.scope.ScopeID, OriginCommandID: f.inbox.CommandID, RunID: f.inbox.RunID, OperationID: "new-write", ToolCallID: "new-write"}, ToolRef: "remember", Arguments: raw(map[string]string{"content": "must not be written", "kind": "knowledge"})})
	if !errors.Is(err, domain.ErrDisabled) {
		t.Fatalf("disabled account accepted new native tool: %v", err)
	}
	if _, _, err := f.s.AcceptInbox(ctx, domain.Inbox{TenantID: f.tenant, BotID: "bot", Namespace: "slack", SourceID: "disabled-event", Input: "new request", Target: f.inbox.Target}); !errors.Is(err, domain.ErrDisabled) {
		t.Fatalf("disabled account accepted new inbox: %v", err)
	}
	if _, err := f.s.ClaimDelivery(ctx, f.tenant); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("disabled account obtained send claim: %v", err)
	}
	knowledge, err := f.s.ListKnowledge(ctx, f.scope, 20)
	if err != nil || len(knowledge) != 1 || knowledge[0].Content != args["content"] {
		t.Fatalf("disabled operation mutated knowledge: %+v %v", knowledge, err)
	}
	deliveries, err := f.s.ListDeliveries(ctx, f.scope, 20)
	if err != nil || len(deliveries) != 1 || deliveries[0].State != "pending" || deliveries[0].Attempts != 0 {
		t.Fatalf("disabled sender changed pending responsibility: %+v %v", deliveries, err)
	}
	if err := f.s.SetAccountEnabled(ctx, f.tenant, "bot", true); err != nil {
		t.Fatal(err)
	}
	resumed, err := f.s.ClaimDelivery(ctx, f.tenant)
	if err != nil || resumed.ID != deliveries[0].ID || resumed.TaskID != task.ID {
		t.Fatalf("re-enable did not resume the original delivery: %+v %v", resumed, err)
	}
}
