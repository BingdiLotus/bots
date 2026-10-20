package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bingdilotus/bots/internal/chronotope"
	"github.com/bingdilotus/bots/internal/domain"
	"github.com/bingdilotus/bots/internal/service"
	"github.com/bingdilotus/bots/internal/slack"
	"github.com/bingdilotus/bots/internal/store"
)

type runtimeCall struct{ kind, key, path, hash string }
type runtimeReply struct {
	hash string
	body any
}

// runtimeFixture speaks the real public wire contract, including immutable
// agent config and canonical run bound/result. It deliberately loses selected
// responses after recording acceptance; the production HTTP client must expose
// that uncertainty to the durable Bots outbox.
type runtimeFixture struct {
	mu          sync.Mutex
	t           *testing.T
	tenant      string
	cache       map[string]runtimeReply
	agents      map[string]chronotope.Agent
	sessions    map[string]chronotope.Session
	runs        map[string]chronotope.Run
	timers      map[string]chronotope.Timer
	calls       []runtimeCall
	drop        map[string]bool
	holdResults bool
	onRun       func(context.Context, string, chronotope.SubmitRunRequest, chronotope.Session) error
	onGetRun    func(*chronotope.Run)
}

func newRuntimeFixture(t *testing.T, tenant string) *runtimeFixture {
	return &runtimeFixture{t: t, tenant: tenant, cache: map[string]runtimeReply{}, agents: map[string]chronotope.Agent{}, sessions: map[string]chronotope.Session{}, runs: map[string]chronotope.Run{}, timers: map[string]chronotope.Timer{}, drop: map[string]bool{}}
}

func jsonReply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (f *runtimeFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer runtime-test-token" {
		f.t.Error("runtime credential was not resolved into request header")
		jsonReply(w, 401, map[string]any{"error": "not authenticated"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == "GET" && len(parts) == 2 && parts[0] == "runs" {
		run, ok := f.runs[parts[1]]
		if !ok {
			jsonReply(w, 404, map[string]any{"error": "unknown run"})
			return
		}
		if f.holdResults {
			run.Status = chronotope.RunRunning
			run.Final = nil
			run.Usage = nil
		}
		if f.onGetRun != nil {
			f.onGetRun(&run)
		}
		jsonReply(w, 200, run)
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Error(err)
		jsonReply(w, 400, map[string]any{"error": "body"})
		return
	}
	kind, key := "", r.Header.Get("Idempotency-Key")
	switch {
	case len(parts) == 3 && parts[0] == "orgs" && parts[2] == "agents":
		kind = "agent"
	case len(parts) == 3 && parts[0] == "agents" && parts[2] == "sessions":
		kind = "session"
	case len(parts) == 3 && parts[0] == "sessions" && parts[2] == "runs":
		kind = "run"
	case len(parts) == 1 && parts[0] == "timers":
		kind = "timer"
	default:
		jsonReply(w, 404, map[string]any{"error": "unknown runtime route"})
		return
	}
	if r.Method != "POST" || kind != "timer" && key == "" {
		f.t.Error("missing stable POST identity")
		jsonReply(w, 400, map[string]any{"error": "key"})
		return
	}
	if kind == "agent" && parts[1] != f.tenant {
		f.t.Error("cross-tenant runtime command")
		jsonReply(w, 403, map[string]any{"error": "tenant"})
		return
	}
	if kind == "timer" {
		var in chronotope.TimerRequest
		if json.Unmarshal(data, &in) != nil {
			jsonReply(w, 400, map[string]any{"error": "timer"})
			return
		}
		key = in.TenantID + "/" + in.TimerID + "/" + strconv.FormatInt(in.Revision, 10)
	}
	requestHash := domain.Hash(json.RawMessage(data))
	f.calls = append(f.calls, runtimeCall{kind: kind, key: key, path: r.URL.Path, hash: requestHash})
	cacheKey := r.URL.Path + "|" + key
	if old, ok := f.cache[cacheKey]; ok {
		if old.hash != requestHash {
			f.t.Error("retry mutated its stable request")
			jsonReply(w, 409, map[string]any{"error_code": "idempotency_conflict", "error": "payload changed"})
			return
		}
		switch body := old.body.(type) {
		case chronotope.Agent:
			body.Idempotent = true
			jsonReply(w, 200, body)
		case chronotope.Session:
			body.Idempotent = true
			jsonReply(w, 200, body)
		case chronotope.RunHandle:
			body.Idempotent = true
			if run, ok := f.runs[body.RunID]; ok {
				body.Status = run.Status
			}
			jsonReply(w, 200, body)
		default:
			jsonReply(w, 202, body)
		}
		return
	}
	status := http.StatusCreated
	var response any
	switch kind {
	case "agent":
		var in chronotope.CreateAgentRequest
		if err := json.Unmarshal(data, &in); err != nil {
			f.t.Error(err)
			return
		}
		out := chronotope.Agent{ID: domain.StableID("runtime_agent_", key), Name: in.Name, Config: in.Config, SpecDigest: domain.Hash(in.Config)}
		f.agents[out.ID] = out
		response = out
	case "session":
		var in chronotope.CreateSessionRequest
		if err := json.Unmarshal(data, &in); err != nil {
			f.t.Error(err)
			return
		}
		if _, ok := f.agents[parts[1]]; !ok {
			f.t.Error("session referenced unregistered agent")
		}
		out := chronotope.Session{ID: domain.StableID("runtime_session_", key), AgentID: parts[1], Status: "ready", SubjectRef: in.SubjectRef, ScopeID: in.ScopeID}
		f.sessions[out.ID] = out
		response = out
	case "run":
		var in chronotope.SubmitRunRequest
		if err := json.Unmarshal(data, &in); err != nil {
			f.t.Error(err)
			return
		}
		session, ok := f.sessions[parts[1]]
		if !ok {
			f.t.Error("run referenced unknown session")
		}
		agent := f.agents[session.AgentID]
		id := domain.StableID("runtime_run_", key)
		if f.onRun != nil {
			if err := f.onRun(r.Context(), id, in, session); err != nil {
				f.t.Error(err)
				jsonReply(w, 500, map[string]any{"error": "tool callback failed"})
				return
			}
		}
		final := "verified final for " + in.OriginCommandID
		f.runs[id] = chronotope.Run{RunID: id, SessionID: session.ID, Status: chronotope.RunCompleted, Final: &final, Usage: &chronotope.RunUsage{TokensIn: 3, TokensOut: 5, TokensTotal: 8}, Bound: chronotope.RunBinding{
			TenantID: f.tenant, SubjectRef: session.SubjectRef, ScopeID: session.ScopeID, OriginCommandID: in.OriginCommandID,
			AgentConfigVersion: agent.Config.Version, AgentConfig: agent.Config, SpecDigest: agent.SpecDigest, Model: agent.Config.Model, ProtocolVersion: "1.0", DeadlineAt: time.Now().Add(time.Minute)}}
		response = chronotope.RunHandle{RunID: id, Status: chronotope.RunQueued}
		status = http.StatusAccepted
	case "timer":
		var in chronotope.TimerRequest
		if err := json.Unmarshal(data, &in); err != nil {
			f.t.Error(err)
			return
		}
		if in.TenantID != f.tenant {
			f.t.Error("timer escaped tenant")
		}
		in.DueAt = in.DueAt.UTC().Truncate(time.Microsecond)
		now := time.Now().UTC()
		out := chronotope.Timer{TimerRequest: in, TimerHandle: domain.StableID("timer_handle_", key), FireID: domain.StableID("fire_", key), State: "scheduled", TargetRef: "timer-target", TargetDigest: "configured-target-digest", RequestDigest: domain.Hash(in), CreatedAt: now, SubmittedAt: &now}
		f.timers[key] = out
		response = out
		status = http.StatusAccepted
	}
	f.cache[cacheKey] = runtimeReply{hash: requestHash, body: response}
	if f.drop[kind] {
		delete(f.drop, kind)
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			f.t.Error(err)
			return
		}
		_ = connection.Close()
		return
	}
	jsonReply(w, status, response)
}

func (f *runtimeFixture) counts() (agents, sessions, runs, timers int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.agents), len(f.sessions), len(f.runs), len(f.timers)
}
func (f *runtimeFixture) attempts(kind string) []runtimeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []runtimeCall
	for _, c := range f.calls {
		if c.kind == kind {
			out = append(out, c)
		}
	}
	return out
}

type sentMessage struct{ Channel, Thread, Text, ClientID string }
type slackFixture struct {
	mu     sync.Mutex
	t      *testing.T
	modes  []string
	calls  []sentMessage
	onSend func(context.Context) error
}

func (f *slackFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.Path != "/chat.postMessage" || r.Header.Get("Authorization") != "Bearer slack-test-token" {
		f.t.Error("unexpected Slack request/auth")
	}
	var in struct {
		Channel  string `json:"channel"`
		Thread   string `json:"thread_ts"`
		Text     string `json:"text"`
		ClientID string `json:"client_msg_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		f.t.Error(err)
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, sentMessage{in.Channel, in.Thread, in.Text, in.ClientID})
	count := len(f.calls)
	mode := "ok"
	if len(f.modes) > 0 {
		mode, f.modes = f.modes[0], f.modes[1:]
	}
	hook := f.onSend
	f.mu.Unlock()
	if hook != nil {
		if err := hook(r.Context()); err != nil {
			f.t.Error(err)
			jsonReply(w, 500, map[string]any{"ok": false})
			return
		}
	}
	switch mode {
	case "lost":
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			f.t.Error(err)
			return
		}
		_ = connection.Close()
	case "429":
		w.Header().Set("Retry-After", "3600")
		jsonReply(w, 429, map[string]any{"ok": false, "error": "ratelimited"})
	default:
		jsonReply(w, 200, map[string]any{"ok": true, "channel": in.Channel, "ts": fmt.Sprintf("1700000000.%06d", count)})
	}
}
func (f *slackFixture) messages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage{}, f.calls...)
}

type engineFixture struct {
	store   *store.Store
	engine  *service.Engine
	runtime *runtimeFixture
	slack   *slackFixture
	tenant  string
	bot     domain.Bot
}

func setupEngine(t *testing.T) *engineFixture {
	t.Helper()
	dsn := os.Getenv("BOTS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BOTS_TEST_DATABASE_URL required for service integration tests")
	}
	st, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(t.Context()); err != nil {
		st.Close()
		t.Fatal(err)
	}
	tenant, err := domain.NewID("service_test_")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tx, err := st.Pool.Begin(ctx)
		if err == nil {
			defer tx.Rollback(ctx)
			for _, table := range []string{"audit_events", "deliveries", "command_outbox", "tasks", "knowledge", "inbox", "runtime_sessions", "bots"} {
				if _, err := tx.Exec(ctx, `DELETE FROM bots.`+table+` WHERE tenant_id=$1`, tenant); err != nil {
					t.Error(err)
				}
			}
			if err := tx.Commit(ctx); err != nil {
				t.Error(err)
			}
		} else {
			t.Error(err)
		}
		st.Close()
	})
	t.Setenv("BOTS_SERVICE_TEST_SLACK_TOKEN", "slack-test-token")
	bot, err := st.ImportBot(t.Context(), domain.Bot{TenantID: tenant, ID: "bot", Name: "integration bot", Enabled: true, AccountEnabled: true, Config: domain.BotConfig{
		Model: "test-model", Instructions: "native tools with strict receipts", Timezone: "Asia/Shanghai", ChannelKind: "slack", InstallationID: "installation", WorkspaceID: "T123", BotUserID: "U_BOT", ChannelID: "C123", TokenEnv: "BOTS_SERVICE_TEST_SLACK_TOKEN", SigningSecretEnv: "BOTS_SERVICE_TEST_SIGNING", BridgeRef: "bridge", TimerTargetRef: "timer-target", MaxPendingTasks: 5}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newRuntimeFixture(t, tenant)
	runtimeServer := httptest.NewServer(runtime)
	t.Cleanup(runtimeServer.Close)
	runtimeClient, err := chronotope.NewClient(chronotope.Config{BaseURL: runtimeServer.URL, TokenRef: "runtime-reference", ResolveToken: func(_ context.Context, ref string) (string, error) {
		if ref != "runtime-reference" {
			return "", fmt.Errorf("unexpected token ref")
		}
		return "runtime-test-token", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	sink := &slackFixture{t: t}
	slackServer := httptest.NewServer(sink)
	t.Cleanup(slackServer.Close)
	slackClient, err := slack.NewClient(slackServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &engineFixture{store: st, engine: &service.Engine{Store: st, Runtime: runtimeClient, Slack: slackClient, TenantID: tenant, Interval: time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, runtime: runtime, slack: sink, tenant: tenant, bot: bot}
}

func (f *engineFixture) accept(t *testing.T, source, thread string) domain.Inbox {
	t.Helper()
	in, created, err := f.store.AcceptInbox(t.Context(), domain.Inbox{TenantID: f.tenant, BotID: f.bot.ID, Namespace: "slack", SourceID: source, Input: "remember meeting and remind", Target: domain.Target{Provider: "slack", InstallationID: f.bot.Config.InstallationID, ChannelID: f.bot.Config.ChannelID, ThreadID: thread, MessageID: source}})
	if err != nil || !created {
		t.Fatalf("inbox admission: %+v created=%v err=%v", in, created, err)
	}
	return in
}
func (f *engineFixture) scope() domain.Scope {
	return domain.Scope{TenantID: f.tenant, BotID: f.bot.ID}
}
func (f *engineFixture) step(t *testing.T) {
	t.Helper()
	if err := f.engine.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func (f *engineFixture) makeCommandsDue(t *testing.T) {
	t.Helper()
	if _, err := f.store.Pool.Exec(t.Context(), `UPDATE bots.command_outbox SET next_attempt_at=now() WHERE tenant_id=$1 AND state='pending'`, f.tenant); err != nil {
		t.Fatal(err)
	}
}
func (f *engineFixture) drive(t *testing.T, until func() bool) {
	t.Helper()
	for range 24 {
		f.step(t)
		if until() {
			return
		}
		f.makeCommandsDue(t)
	}
	t.Fatal("dispatcher failed to reach expected observable state")
}
func (f *engineFixture) deliveries(t *testing.T) []domain.Delivery {
	t.Helper()
	rows, err := f.store.ListDeliveries(t.Context(), f.scope(), 100)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestEngineCollectsRunFromAgentWithLegacyToolDefinitions(t *testing.T) {
	f := setupEngine(t)
	f.runtime.mu.Lock()
	f.runtime.onGetRun = func(run *chronotope.Run) {
		run.Bound.AgentConfig.ApplicationTools = []chronotope.ApplicationTool{{
			ModelAlias: "legacy_recall", ToolRef: "legacy_recall", Description: "tool set from an earlier binary",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}}
	}
	f.runtime.mu.Unlock()
	f.accept(t, "event-legacy-tools", "1700000000.000200")
	f.drive(t, func() bool { return len(f.slack.messages()) == 1 })
	if rows := f.deliveries(t); len(rows) != 1 || rows[0].State != "confirmed" {
		t.Fatalf("legacy tool metadata blocked canonical result collection: %+v", rows)
	}
}

func TestEngineRejectsRunWithDifferentAgentPolicy(t *testing.T) {
	f := setupEngine(t)
	f.runtime.mu.Lock()
	f.runtime.onGetRun = func(run *chronotope.Run) {
		run.Bound.AgentConfig.Instructions = "unapproved policy"
	}
	f.runtime.mu.Unlock()
	f.accept(t, "event-tampered-policy", "1700000000.000300")
	for range 12 {
		f.step(t)
		f.makeCommandsDue(t)
	}
	if len(f.slack.messages()) != 0 || len(f.deliveries(t)) != 0 {
		t.Fatal("run with a different agent policy produced a final reply or delivery")
	}
}

func TestEngineDurableHTTPChainAndFrozenSession(t *testing.T) {
	f := setupEngine(t)
	f.runtime.mu.Lock()
	f.runtime.drop = map[string]bool{"agent": true, "session": true, "run": true, "timer": true}
	f.runtime.mu.Unlock()
	in := f.accept(t, "event-one", "1700000000.000100")
	var earlyCallback atomic.Bool
	f.runtime.mu.Lock()
	f.runtime.onRun = func(ctx context.Context, id string, request chronotope.SubmitRunRequest, session chronotope.Session) error {
		if request.OriginCommandID != in.CommandID {
			return nil
		}
		var saved string
		if err := f.store.Pool.QueryRow(ctx, `SELECT run_id FROM bots.inbox WHERE tenant_id=$1 AND command_id=$2`, f.tenant, in.CommandID).Scan(&saved); err != nil {
			return err
		}
		if saved != "" {
			return fmt.Errorf("tool callback no longer precedes SubmitRun response")
		}
		earlyCallback.Store(true)
		for _, tool := range []struct {
			name string
			args any
		}{{"remember", map[string]any{"content": "native integration knowledge", "kind": "knowledge"}}, {"create_reminder", map[string]any{"due_at": time.Now().Add(-time.Second).UTC(), "text": "deterministic reminder"}}} {
			arguments, _ := json.Marshal(tool.args)
			result, err := f.store.ExecuteTool(ctx, domain.ToolCall{Context: domain.ToolContext{TenantID: f.tenant, SubjectRef: f.bot.ID, ScopeID: session.ScopeID, OriginCommandID: in.CommandID, RunID: id, OperationID: domain.StableID("op_", id, tool.name), ToolCallID: tool.name}, ToolRef: tool.name, Arguments: arguments})
			if err != nil {
				return err
			}
			var value map[string]any
			if json.Unmarshal(result, &value) != nil || value["error"] != nil {
				return fmt.Errorf("native tool %s failed: %s", tool.name, result)
			}
		}
		return nil
	}
	f.runtime.mu.Unlock()
	f.drive(t, func() bool { _, _, _, timers := f.runtime.counts(); return timers == 1 })
	var commandState, commandHandle string
	var commandAttempts int
	if err := f.store.Pool.QueryRow(t.Context(), `SELECT state,handle,attempts FROM bots.command_outbox WHERE tenant_id=$1 AND kind='timer'`, f.tenant).Scan(&commandState, &commandHandle, &commandAttempts); err != nil {
		t.Fatal(err)
	}
	if commandState != "pending" || commandHandle != "" || commandAttempts != 1 {
		t.Fatalf("lost timer ACK did not retain durable submission responsibility: %s %s %d", commandState, commandHandle, commandAttempts)
	}
	// Recreate the dispatcher and run startup recovery. Repair must come from
	// PostgreSQL, while the HTTP runtime keeps its already accepted timer.
	restarted := *f.engine
	f.engine = &restarted
	if err := f.store.Recover(t.Context(), f.tenant); err != nil {
		t.Fatal(err)
	}
	f.makeCommandsDue(t)
	f.drive(t, func() bool {
		rows, err := f.store.ListTasks(t.Context(), f.scope(), 10)
		if err != nil {
			t.Fatal(err)
		}
		return len(rows) == 1 && rows[0].Status == "scheduled" && len(f.slack.messages()) == 1
	})
	if !earlyCallback.Load() {
		t.Fatal("first tool did not race ahead of run response")
	}
	for _, kind := range []string{"agent", "session", "run", "timer"} {
		calls := f.runtime.attempts(kind)
		if len(calls) != 2 || calls[0].key != calls[1].key || calls[0].hash != calls[1].hash {
			t.Fatalf("%s response loss changed request identity: %+v", kind, calls)
		}
	}
	if a, s, r, tr := f.runtime.counts(); a != 1 || s != 1 || r != 1 || tr != 1 {
		t.Fatalf("duplicate logical runtime resources: %d/%d/%d/%d", a, s, r, tr)
	}
	for range 3 {
		f.step(t)
	}
	if len(f.slack.messages()) != 1 {
		t.Fatal("completed run final reply repeated")
	}
	rows := f.deliveries(t)
	if len(rows) != 1 || rows[0].State != "confirmed" || rows[0].ProviderMessageID == "" {
		t.Fatalf("missing durable final receipt: %+v", rows)
	}
	knowledge, err := f.store.ListKnowledge(t.Context(), f.scope(), 10)
	if err != nil || len(knowledge) != 1 {
		t.Fatalf("native knowledge lost: %+v %v", knowledge, err)
	}
	tasks, err := f.store.ListTasks(t.Context(), f.scope(), 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("task: %+v %v", tasks, err)
	}
	task := tasks[0]
	// due_at is already past, yet Step must not invent a second due scanner.
	if len(f.slack.messages()) != 1 || task.Status != "scheduled" {
		t.Fatal("reminder sent before durable timer fire")
	}
	fire := domain.TimerFire{TenantID: f.tenant, TimerID: task.TimerID, Revision: task.Revision, FireID: "fire-one", OpaqueActionRef: task.ID}
	if created, err := f.store.AcceptFire(t.Context(), fire); err != nil || !created {
		t.Fatalf("fire: %v %v", created, err)
	}
	if created, err := f.store.AcceptFire(t.Context(), fire); err != nil || created {
		t.Fatalf("fire replay: %v %v", created, err)
	}
	f.step(t)
	f.step(t)
	if len(f.slack.messages()) != 2 {
		t.Fatalf("reminder sends=%d", len(f.slack.messages()))
	}
	for _, message := range f.slack.messages() {
		if message.Channel != in.Target.ChannelID || message.Thread != in.Target.ThreadID {
			t.Fatalf("final/reminder escaped the source reply target: %+v", message)
		}
	}
	if _, _, runs, _ := f.runtime.counts(); runs != 1 {
		t.Fatal("timer fire created a model run")
	}
	task, err = f.store.GetTask(t.Context(), f.scope(), task.ID)
	if err != nil || task.Status != "completed" {
		t.Fatalf("reminder contract did not complete: %+v %v", task, err)
	}
	oldBot, err := f.store.GetBot(t.Context(), f.tenant, f.bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated := oldBot.Config
	updated.Instructions = "updated SOP for new threads"
	newBot, err := f.engine.UpdateConfig(t.Context(), f.tenant, f.bot.ID, oldBot.Revision, updated)
	if err != nil {
		t.Fatal(err)
	}
	if newBot.Revision != 2 || newBot.RuntimeAgentID == oldBot.RuntimeAgentID {
		t.Fatal("new configuration did not bind a new immutable agent")
	}
	f.accept(t, "event-followup", "1700000000.000100")
	f.drive(t, func() bool { return len(f.slack.messages()) == 3 })
	if _, sessions, _, _ := f.runtime.counts(); sessions != 1 {
		t.Fatal("old thread lost its original technical session")
	}
	f.accept(t, "event-new-thread", "1700000010.000100")
	f.drive(t, func() bool { return len(f.slack.messages()) == 4 })
	if a, s, r, tr := f.runtime.counts(); a != 2 || s != 2 || r != 3 || tr != 1 {
		t.Fatalf("unexpected post-publication bindings: %d/%d/%d/%d", a, s, r, tr)
	}
	f.runtime.mu.Lock()
	oldBindings, newBindings := 0, 0
	for _, session := range f.runtime.sessions {
		if session.AgentID == oldBot.RuntimeAgentID {
			oldBindings++
		}
		if session.AgentID == newBot.RuntimeAgentID {
			newBindings++
		}
	}
	f.runtime.mu.Unlock()
	if oldBindings != 1 || newBindings != 1 {
		t.Fatalf("session revisions not frozen: old=%d new=%d", oldBindings, newBindings)
	}
	messages := f.slack.messages()
	if messages[2].Thread != "1700000000.000100" || messages[3].Thread != "1700000010.000100" {
		t.Fatalf("thread routing changed across publication: %+v", messages)
	}
	audit, err := f.store.ListAudit(t.Context(), f.scope(), 200)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(audit)
	if strings.Contains(string(data), "runtime-test-token") || strings.Contains(string(data), "slack-test-token") {
		t.Fatal("provider credential leaked into durable audit")
	}
}
