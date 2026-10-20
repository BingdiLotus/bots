package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/bingdilotus/bots/internal/chronotope"
	"github.com/bingdilotus/bots/internal/domain"
	"github.com/bingdilotus/bots/internal/service"
)

func freshEngine(e *service.Engine) *service.Engine {
	// Do not copy the first engine's private lease pointer: exclusion must be
	// established by PostgreSQL between independent dispatcher instances.
	return &service.Engine{Store: e.Store, Runtime: e.Runtime, Slack: e.Slack, TenantID: e.TenantID, Interval: e.Interval, Logger: e.Logger}
}

func initializeEngine(t *testing.T, e *service.Engine) func(context.Context) error {
	t.Helper()
	closeLease, err := e.Initialize(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = closeLease(ctx)
	})
	return closeLease
}

func TestEngineInitializeExcludesRecoveryUntilLeaseReleased(t *testing.T) {
	f := preparePausedReply(t)
	second := freshEngine(f.engine)
	if err := second.Run(t.Context()); err == nil {
		t.Fatal("Run exposed an uninitialized dispatcher")
	}
	closeFirst := initializeEngine(t, f.engine)
	if err := f.store.SetBotEnabled(t.Context(), f.tenant, f.bot.ID, true); err != nil {
		t.Fatal(err)
	}
	claimed, err := f.store.ClaimDelivery(t.Context(), f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if closeSecond, err := second.Initialize(t.Context()); !errors.Is(err, domain.ErrBusy) {
		if closeSecond != nil {
			_ = closeSecond(t.Context())
		}
		t.Fatalf("second Initialize must fail with ErrBusy, got %v", err)
	}
	rows := f.deliveries(t)
	if len(rows) != 1 || rows[0].ID != claimed.ID || rows[0].State != "dispatching" {
		t.Fatalf("rejected second owner ran recovery over a live claim: %+v", rows)
	}
	if err := closeFirst(t.Context()); err != nil {
		t.Fatal(err)
	}
	initializeEngine(t, second)
	rows = f.deliveries(t)
	if len(rows) != 1 || rows[0].State != "unknown" {
		t.Fatalf("successor did not recover abandoned claim: %+v", rows)
	}
	if err := f.engine.Step(t.Context()); err == nil {
		t.Fatal("released owner accepted another dispatcher step")
	}
	if err := second.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.slack.messages()) != 0 {
		t.Fatal("lease handoff blindly replayed an unresolved send")
	}
}

// Label only this fixture's pool so a backend can be terminated without exposing
// private store connections or guessing which production session owns a lease.
func labelLeaseTestPool(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("BOTS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BOTS_TEST_DATABASE_URL required")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Skip("lease-loss fixture requires a URL DSN to set a unique application_name")
	}
	name, err := domain.NewID("bots_service_lease_")
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("application_name", name)
	u.RawQuery = query.Encode()
	t.Setenv("BOTS_TEST_DATABASE_URL", u.String())
	return name
}

func leaseBackendPID(t *testing.T, f *engineFixture, name string) int {
	t.Helper()
	rows, err := f.store.Pool.Query(t.Context(), `SELECT DISTINCT a.pid FROM pg_stat_activity a
JOIN pg_locks l ON l.pid=a.pid WHERE a.datname=current_database() AND a.application_name=$1
AND l.locktype='advisory' AND l.granted AND a.pid<>pg_backend_pid()`, name)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for rows.Next() {
		var pid int
		if err := rows.Scan(&pid); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		ids = append(ids, pid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if name == "" || len(ids) != 1 {
		t.Skipf("cannot identify exactly one labeled lease backend (matches=%d); no connection was terminated", len(ids))
	}
	return ids[0]
}

func terminateLeaseBackend(ctx context.Context, f *engineFixture, pid int) error {
	var terminated bool
	// PostgreSQL 16 waits for this exact backend to exit, avoiding a race between
	// sending its termination signal and the dispatcher's next Ping checkpoint.
	if err := f.store.Pool.QueryRow(ctx, `SELECT pg_terminate_backend($1,5000)`, pid).Scan(&terminated); err != nil {
		return err
	}
	if !terminated {
		return fmt.Errorf("labeled lease backend did not terminate")
	}
	return nil
}

func TestEngineLostBackendStopsNewRunAndSendClaims(t *testing.T) {
	name := labelLeaseTestPool(t)
	f := preparePausedReply(t)
	if err := f.store.SetBotEnabled(t.Context(), f.tenant, f.bot.ID, true); err != nil {
		t.Fatal(err)
	}
	pending := f.accept(t, "pending-after-lease-loss", "1700000010.000100")
	initializeEngine(t, f.engine)
	beforeAgents, beforeSessions, beforeRuns, beforeTimers := f.runtime.counts()
	pid := leaseBackendPID(t, f, name)
	if err := terminateLeaseBackend(t.Context(), f, pid); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.Step(t.Context()); err == nil {
		t.Fatal("lost lease allowed a dispatcher step")
	}
	if agents, sessions, runs, timers := f.runtime.counts(); agents != beforeAgents || sessions != beforeSessions || runs != beforeRuns || timers != beforeTimers {
		t.Fatal("lost owner submitted new runtime work")
	}
	var state string
	if err := f.store.Pool.QueryRow(t.Context(), `SELECT state FROM bots.inbox WHERE tenant_id=$1 AND id=$2`, f.tenant, pending.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "pending" {
		t.Fatalf("lost owner claimed new input: %s", state)
	}
	rows := f.deliveries(t)
	if len(rows) != 1 || rows[0].State != "pending" || rows[0].Attempts != 0 || len(f.slack.messages()) != 0 {
		t.Fatalf("lost owner claimed/sent pending reply: %+v", rows)
	}
}

func TestEngineLeaseLossDuringSendStillCommitsReceipt(t *testing.T) {
	name := labelLeaseTestPool(t)
	f := setupEngine(t)
	in := f.accept(t, "in-flight-send", "1700000000.000100")
	f.runtime.mu.Lock()
	f.runtime.holdResults = true
	f.runtime.onRun = func(ctx context.Context, runID string, request chronotope.SubmitRunRequest, session chronotope.Session) error {
		arguments, _ := json.Marshal(map[string]any{"due_at": time.Now().Add(-time.Second).UTC(), "text": "second pending reminder"})
		result, err := f.store.ExecuteTool(ctx, domain.ToolCall{Context: domain.ToolContext{TenantID: f.tenant, SubjectRef: f.bot.ID, ScopeID: session.ScopeID, OriginCommandID: request.OriginCommandID, RunID: runID, OperationID: "create-before-final", ToolCallID: "create"}, ToolRef: "create_reminder", Arguments: arguments})
		if err != nil {
			return err
		}
		var value map[string]any
		if json.Unmarshal(result, &value) != nil || value["error"] != nil {
			return fmt.Errorf("create tool failed: %s", result)
		}
		return nil
	}
	f.runtime.mu.Unlock()
	f.drive(t, func() bool { _, _, runs, _ := f.runtime.counts(); return runs == 1 })
	if err := f.store.SetBotEnabled(t.Context(), f.tenant, f.bot.ID, false); err != nil {
		t.Fatal(err)
	}
	f.runtime.mu.Lock()
	f.runtime.holdResults = false
	f.runtime.mu.Unlock()
	f.step(t)
	tasks, err := f.store.ListTasks(t.Context(), f.scope(), 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("native task: %+v %v", tasks, err)
	}
	task := tasks[0]
	if _, err := f.store.AcceptFire(t.Context(), domain.TimerFire{TenantID: f.tenant, TimerID: task.TimerID, Revision: task.Revision, FireID: "pending-second-send", OpaqueActionRef: task.ID}); err != nil {
		t.Fatal(err)
	}
	if len(f.deliveries(t)) != 2 {
		t.Fatal("test requires two pending outbound responsibilities")
	}
	initializeEngine(t, f.engine)
	pid := leaseBackendPID(t, f, name)
	f.slack.mu.Lock()
	f.slack.onSend = func(ctx context.Context) error { return terminateLeaseBackend(ctx, f, pid) }
	f.slack.mu.Unlock()
	if err := f.store.SetBotEnabled(t.Context(), f.tenant, f.bot.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.Step(t.Context()); err == nil {
		t.Fatal("dispatcher continued after the in-flight send lost its lease")
	}
	rows := f.deliveries(t)
	confirmed, pending := 0, 0
	for _, row := range rows {
		switch row.State {
		case "confirmed":
			if row.ProviderMessageID == "" || row.Attempts != 1 {
				t.Fatalf("receipt not committed: %+v", row)
			}
			confirmed++
		case "pending":
			if row.Attempts != 0 {
				t.Fatal("next send acquired a claim after lease loss")
			}
			pending++
		default:
			t.Fatalf("unexpected send disposition: %+v", row)
		}
	}
	if confirmed != 1 || pending != 1 || len(f.slack.messages()) != 1 {
		t.Fatalf("in-flight receipt or fail-stop violated: confirmed=%d pending=%d requests=%d", confirmed, pending, len(f.slack.messages()))
	}
	if f.slack.messages()[0].Thread != in.Target.ThreadID {
		t.Fatal("in-flight send changed reply target")
	}
}
