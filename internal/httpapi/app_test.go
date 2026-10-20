package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bingdilotus/bots/internal/domain"
	"github.com/bingdilotus/bots/internal/store"
)

var _ Store = (*store.Store)(nil)

type appStore struct {
	bot              domain.Bot
	err              error
	tools            []domain.ToolCall
	toolResult       json.RawMessage
	fires            []domain.TimerFire
	fireCreated      bool
	fireHook         func()
	inboxes          []domain.Inbox
	subscribed       bool
	conversationKeys []string
	invalidations    [][]string
	scopes           []domain.Scope
	limits           []int
	queries          []string
	mutations        []string
	resolved         []string
	mutationID       string
	expectedRevision int
	getBotCalls      int
}

func (s *appStore) GetBot(_ context.Context, tenant, bot string) (domain.Bot, error) {
	s.getBotCalls++
	if s.err != nil {
		return domain.Bot{}, s.err
	}
	if tenant != s.bot.TenantID || bot != s.bot.ID {
		return domain.Bot{}, domain.ErrNotFound
	}
	return s.bot, nil
}
func (s *appStore) ListBots(context.Context, string) ([]domain.Bot, error) {
	return []domain.Bot{s.bot}, s.err
}
func (s *appStore) SetBotEnabled(_ context.Context, tenant, bot string, enabled bool) error {
	s.mutations = append(s.mutations, "bot")
	s.bot.Enabled = enabled
	return s.err
}
func (s *appStore) SetAccountEnabled(_ context.Context, tenant, bot string, enabled bool) error {
	s.mutations = append(s.mutations, "account")
	s.bot.AccountEnabled = enabled
	return s.err
}
func (s *appStore) ExecuteTool(_ context.Context, call domain.ToolCall) (json.RawMessage, error) {
	s.tools = append(s.tools, call)
	return s.toolResult, s.err
}
func (s *appStore) AcceptFire(_ context.Context, fire domain.TimerFire) (bool, error) {
	s.fires = append(s.fires, fire)
	if s.fireHook != nil {
		s.fireHook()
	}
	return s.fireCreated, s.err
}
func (s *appStore) AcceptInbox(_ context.Context, in domain.Inbox) (domain.Inbox, bool, error) {
	s.inboxes = append(s.inboxes, in)
	in.ID = "inbox-persisted"
	return in, true, s.err
}
func (s *appStore) HasConversation(_ context.Context, tenant, bot, key string) (bool, error) {
	s.conversationKeys = append(s.conversationKeys, key)
	return s.subscribed, s.err
}
func (s *appStore) InvalidateMessage(_ context.Context, tenant, bot, installation, channel, message, kind string) error {
	s.invalidations = append(s.invalidations, []string{tenant, bot, installation, channel, message, kind})
	return s.err
}
func (s *appStore) recordQuery(kind string, scope domain.Scope, n int) {
	s.queries = append(s.queries, kind)
	s.scopes = append(s.scopes, scope)
	s.limits = append(s.limits, n)
}
func (s *appStore) ListKnowledge(_ context.Context, scope domain.Scope, n int) ([]domain.Knowledge, error) {
	s.recordQuery("knowledge", scope, n)
	return nil, s.err
}
func (s *appStore) DeactivateKnowledge(_ context.Context, scope domain.Scope, id string, expected int) error {
	s.scopes = append(s.scopes, scope)
	s.mutations = append(s.mutations, "knowledge")
	s.mutationID = id
	s.expectedRevision = expected
	return s.err
}
func (s *appStore) GetTask(_ context.Context, scope domain.Scope, id string) (domain.Task, error) {
	s.scopes = append(s.scopes, scope)
	return domain.Task{ID: id, TenantID: scope.TenantID, BotID: scope.BotID, ScopeID: scope.ScopeID}, s.err
}
func (s *appStore) ListTasks(_ context.Context, scope domain.Scope, n int) ([]domain.Task, error) {
	s.recordQuery("tasks", scope, n)
	return nil, s.err
}
func (s *appStore) ListDeliveries(_ context.Context, scope domain.Scope, n int) ([]domain.Delivery, error) {
	s.recordQuery("deliveries", scope, n)
	return nil, s.err
}
func (s *appStore) ListAudit(_ context.Context, scope domain.Scope, n int) ([]domain.AuditEvent, error) {
	s.recordQuery("audit", scope, n)
	return nil, s.err
}
func (s *appStore) ResolveDelivery(_ context.Context, scope domain.Scope, id, resolution, providerMessageID, note string) (domain.Delivery, error) {
	s.resolved = append(s.resolved, id+"|"+resolution+"|"+providerMessageID+"|"+note)
	return domain.Delivery{ID: id, TenantID: scope.TenantID, BotID: scope.BotID, ScopeID: scope.ScopeID, State: resolution, ProviderMessageID: providerMessageID}, s.err
}

func newTestApp(t *testing.T) (*App, *appStore, *http.ServeMux) {
	t.Helper()
	t.Setenv("HTTP_TEST_ADMIN", "admin-only")
	t.Setenv("HTTP_TEST_INTERNAL", "internal-only")
	t.Setenv("HTTP_TEST_SLACK_SIGNING", "slack-signing")
	t.Setenv("HTTP_TEST_SLACK_TOKEN", "xoxb-do-not-expose")
	s := &appStore{bot: domain.Bot{TenantID: "tenant", ID: "bot", Name: "bot", Enabled: true, AccountEnabled: true, Revision: 1,
		Config: domain.BotConfig{Model: "model", Instructions: "follow SOP", Timezone: "Asia/Shanghai", ChannelKind: "slack", InstallationID: "install", WorkspaceID: "T1", BotUserID: "U_BOT", ChannelID: "C1", TokenEnv: "HTTP_TEST_SLACK_TOKEN", SigningSecretEnv: "HTTP_TEST_SLACK_SIGNING", BridgeRef: "bots", TimerTargetRef: "bots-fire", MaxOutputTokens: 4096, MaxSteps: 16, MaxDurationMS: 300000, MaxPendingTasks: 20}},
		toolResult: json.RawMessage(`{"knowledge_id":"k1","revision":1}`), fireCreated: true}
	a := &App{Store: s, TenantID: "tenant", AdminTokenEnv: "HTTP_TEST_ADMIN", InternalTokenEnv: "HTTP_TEST_INTERNAL", Now: func() time.Time { return time.Unix(1800000000, 0) }}
	mux := New()
	a.Register(mux)
	return a, s, mux
}

func appRequest(mux http.Handler, method, path, body, token, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func toolBody() string {
	return `{"context":{"tenant_id":"tenant","subject_ref":"bot","scope_id":"scope","origin_command_id":"origin","run_id":"run","operation_id":"operation","tool_call_id":"call"},"tool_ref":"remember","arguments":{"content":"knowledge","kind":"knowledge"}}`
}
func fireBody() string {
	return `{"tenant_id":"tenant","timer_id":"timer","revision":2,"fire_id":"fire","opaque_action_ref":"task"}`
}

func TestBusinessRoutesFailClosedAndResolveCredentialsDynamically(t *testing.T) {
	a, s, mux := newTestApp(t)
	for _, token := range []string{"", "wrong", "internal-only"} {
		w := appRequest(mux, "GET", "/api/tenants/tenant/bots", "", token, "")
		if w.Code != 401 {
			t.Fatalf("admin credential %q: %d %s", token, w.Code, w.Body.String())
		}
	}
	w := appRequest(mux, "GET", "/api/tenants/other/bots/bot", "", "admin-only", "")
	if w.Code != 403 || s.getBotCalls != 0 {
		t.Fatalf("cross-tenant request reached store: %d %s", w.Code, w.Body.String())
	}
	w = appRequest(mux, "POST", "/internal/tools", toolBody(), "admin-only", "operation")
	if w.Code != 401 || len(s.tools) != 0 {
		t.Fatal("admin credential must not authorize internal tools")
	}
	t.Setenv("HTTP_TEST_ADMIN", "rotated-admin")
	if w = appRequest(mux, "GET", "/api/tenants/tenant/bots", "", "admin-only", ""); w.Code != 401 {
		t.Fatal("stale admin token accepted")
	}
	if w = appRequest(mux, "GET", "/api/tenants/tenant/bots", "", "rotated-admin", ""); w.Code != 200 {
		t.Fatalf("dynamic token was not resolved: %d", w.Code)
	}
	a.InternalTokenEnv = "HTTP_TEST_UNCONFIGURED"
	w = appRequest(mux, "POST", "/internal/tools", toolBody(), "internal-only", "operation")
	if w.Code != 503 || strings.Contains(w.Body.String(), "HTTP_TEST_UNCONFIGURED") {
		t.Fatalf("missing secret must fail without leaking its reference: %s", w.Body.String())
	}
	if w = appRequest(mux, "GET", "/healthz", "", "", ""); w.Code != 200 {
		t.Fatal("missing channel/internal configuration must not block liveness")
	}
}

func TestInternalToolsValidateBoundContextAndPreserveStoredResult(t *testing.T) {
	_, s, mux := newTestApp(t)
	for _, test := range []struct {
		body, key string
		status    int
	}{
		{strings.Replace(toolBody(), `"tenant_id":"tenant"`, `"tenant_id":"other"`, 1), "operation", 403},
		{strings.Replace(toolBody(), `"scope_id":"scope"`, `"scope_id":""`, 1), "operation", 400},
		{toolBody(), "different", 400},
		{strings.Replace(toolBody(), `"run_id":"run"`, `"run_id":"run","url":"http://evil"`, 1), "operation", 400},
		{toolBody() + ` {}`, "operation", 400},
		{`null`, "operation", 400},
	} {
		w := appRequest(mux, "POST", "/internal/tools", test.body, "internal-only", test.key)
		if w.Code != test.status || len(s.tools) != 0 {
			t.Fatalf("invalid authority/payload reached persistence: %d %s", w.Code, w.Body.String())
		}
	}
	for i := 0; i < 2; i++ {
		w := appRequest(mux, "POST", "/internal/tools", toolBody(), "internal-only", "operation")
		if w.Code != 200 || w.Body.String() != string(s.toolResult) {
			t.Fatalf("stored receipt must be returned unchanged: %d %s", w.Code, w.Body.String())
		}
	}
	if len(s.tools) != 2 || s.tools[1].Context.OperationID != "operation" || s.tools[1].Context.ToolCallID != "call" || s.tools[1].Context.OriginCommandID != "origin" {
		t.Fatal("trusted context was not forwarded to atomic store operation")
	}
	s.err = domain.ErrDisabled
	w := appRequest(mux, "POST", "/internal/tools", toolBody(), "internal-only", "operation")
	if w.Code != 409 || !json.Valid(w.Body.Bytes()) || !strings.Contains(w.Body.String(), `"disabled"`) {
		t.Fatalf("known business failure must be model-visible JSON, not unknown 500: %s", w.Body.String())
	}
	s.err = errors.New("postgres password=do-not-leak SECRET connection failed")
	w = appRequest(mux, "POST", "/internal/tools", toolBody(), "internal-only", "operation")
	if w.Code != 500 || strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), "postgres") {
		t.Fatalf("storage failure leaked details: %s", w.Body.String())
	}
}

type commitAwareRecorder struct {
	*httptest.ResponseRecorder
	committed *bool
	t         *testing.T
}

func (w commitAwareRecorder) WriteHeader(status int) {
	if status >= 200 && status < 300 && !*w.committed {
		w.t.Error("ACK emitted before durable store result")
	}
	w.ResponseRecorder.WriteHeader(status)
}

func TestTimerFireUsesFiveFieldsAndAcknowledgesAfterPersistence(t *testing.T) {
	_, s, mux := newTestApp(t)
	for _, test := range []struct {
		body, key string
		status    int
	}{
		{strings.Replace(fireBody(), `"tenant_id":"tenant"`, `"tenant_id":"other"`, 1), "fire", 403},
		{strings.Replace(fireBody(), `"revision":2`, `"revision":0`, 1), "fire", 400},
		{strings.Replace(fireBody(), `"timer_id":"timer"`, `"timer_id":"timer","subject_ref":"forged"`, 1), "fire", 400},
		{fireBody(), "other-fire", 400},
	} {
		w := appRequest(mux, "POST", "/internal/timer-fires", test.body, "internal-only", test.key)
		if w.Code != test.status || len(s.fires) != 0 {
			t.Fatalf("invalid fire reached store: %d %s", w.Code, w.Body.String())
		}
	}
	committed := false
	s.fireHook = func() { committed = true }
	r := httptest.NewRequest("POST", "/internal/timer-fires", strings.NewReader(fireBody()))
	r.Header.Set("Authorization", "Bearer internal-only")
	r.Header.Set("Idempotency-Key", "fire")
	w := commitAwareRecorder{httptest.NewRecorder(), &committed, t}
	mux.ServeHTTP(w, r)
	if w.Code != 202 || len(s.fires) != 1 || s.fires[0].OpaqueActionRef != "task" {
		t.Fatalf("fire not persisted: %d %s", w.Code, w.Body.String())
	}
	s.fireCreated = false
	duplicate := appRequest(mux, "POST", "/internal/timer-fires", fireBody(), "internal-only", "fire")
	if duplicate.Code != 202 || !strings.Contains(duplicate.Body.String(), `"idempotent":true`) {
		t.Fatalf("duplicate durable fire must ACK: %d %s", duplicate.Code, duplicate.Body.String())
	}
	s.err = errors.New("SQL secret")
	failed := appRequest(mux, "POST", "/internal/timer-fires", fireBody(), "internal-only", "fire")
	if failed.Code != 500 || strings.Contains(failed.Body.String(), "secret") {
		t.Fatal("failed persistence must not ACK or expose DB errors")
	}
}

func TestBusinessBodiesAreBoundedAndStrict(t *testing.T) {
	_, s, mux := newTestApp(t)
	oversized := strings.Replace(toolBody(), `"knowledge"`, `"`+strings.Repeat("x", 1<<20)+`"`, 1)
	w := appRequest(mux, "POST", "/internal/tools", oversized, "internal-only", "operation")
	if w.Code != 413 || len(s.tools) != 0 {
		t.Fatalf("oversized input executed: %d", w.Code)
	}
	for _, body := range []string{`{}`, `null`, `[]`, `{"enabled":false,"tenant_id":"other"}`, `{"enabled":false} {}`} {
		w := appRequest(mux, "PUT", "/api/tenants/tenant/bots/bot/enabled", body, "admin-only", "")
		if w.Code != 400 || len(s.mutations) != 0 {
			t.Fatalf("invalid toggle caused write: %s -> %d", body, w.Code)
		}
	}
}
