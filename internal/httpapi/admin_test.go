package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bingdilotus/bots/internal/domain"
)

func TestAdminQueriesCarryTenantBotAndOptionalScope(t *testing.T) {
	_, s, mux := newTestApp(t)
	for _, kind := range []string{"knowledge", "tasks", "deliveries", "audit"} {
		w := appRequest(mux, "GET", "/api/tenants/tenant/bots/bot/"+kind+"?scope_id=channel-scope&limit=7", "", "admin-only", "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"`+kind+`":[]`) {
			t.Fatalf("list %s: %d %s", kind, w.Code, w.Body.String())
		}
		if got := s.scopes[len(s.scopes)-1]; got.TenantID != "tenant" || got.BotID != "bot" || got.ScopeID != "channel-scope" {
			t.Fatalf("query escaped authority: %+v", got)
		}
		if s.limits[len(s.limits)-1] != 7 {
			t.Fatal("query limit was dropped")
		}
	}
	w := appRequest(mux, "GET", "/api/tenants/tenant/bots/bot/knowledge", "", "admin-only", "")
	if w.Code != 200 || s.scopes[len(s.scopes)-1].ScopeID != "" {
		t.Fatal("authenticated admin should see the Bot's full scope when scope_id is empty")
	}
	w = appRequest(mux, "GET", "/api/tenants/tenant/bots/bot/tasks/task-id?scope_id=scope", "", "admin-only", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"task-id"`) {
		t.Fatalf("get task: %d %s", w.Code, w.Body.String())
	}
	before := len(s.queries)
	for _, value := range []string{"0", "101", "invalid"} {
		w := appRequest(mux, "GET", "/api/tenants/tenant/bots/bot/tasks?limit="+value, "", "admin-only", "")
		if w.Code != 400 || len(s.queries) != before {
			t.Fatal("invalid query limit reached store")
		}
	}
	w = appRequest(mux, "GET", "/api/tenants/tenant/bots/bot", "", "admin-only", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "xoxb-do-not-expose") {
		t.Fatalf("admin config exposed secret value: %s", w.Body.String())
	}
}

func TestAdminMutationsUseCASAndExplicitEnableFields(t *testing.T) {
	a, s, mux := newTestApp(t)
	var updateCalls int
	a.UpdateConfig = func(_ context.Context, tenant, bot string, expected int, cfg domain.BotConfig) (domain.Bot, error) {
		updateCalls++
		if tenant != "tenant" || bot != "bot" || expected != 1 || cfg.Instructions != "updated SOP" {
			t.Fatalf("update lost trusted route/CAS: %s %s %d %+v", tenant, bot, expected, cfg)
		}
		result := s.bot
		result.Revision = 2
		result.Config = cfg
		return result, nil
	}
	cfg := s.bot.Config
	cfg.Instructions = "updated SOP"
	body, _ := json.Marshal(map[string]any{"expected_revision": 1, "config": cfg})
	w := appRequest(mux, "PUT", "/api/tenants/tenant/bots/bot/config", string(body), "admin-only", "")
	if w.Code != 200 || updateCalls != 1 || !strings.Contains(w.Body.String(), `"revision":2`) {
		t.Fatalf("config update: %d %s", w.Code, w.Body.String())
	}
	a.UpdateConfig = func(context.Context, string, string, int, domain.BotConfig) (domain.Bot, error) {
		return domain.Bot{}, domain.ErrConflict
	}
	w = appRequest(mux, "PUT", "/api/tenants/tenant/bots/bot/config", string(body), "admin-only", "")
	if w.Code != 409 {
		t.Fatalf("CAS conflict must remain visible: %d", w.Code)
	}
	for _, path := range []string{"enabled", "account-enabled"} {
		w := appRequest(mux, "PUT", "/api/tenants/tenant/bots/bot/"+path, `{"enabled":false}`, "admin-only", "")
		if w.Code != 200 {
			t.Fatalf("toggle %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if s.bot.Enabled || s.bot.AccountEnabled || len(s.mutations) != 2 || s.mutations[0] != "bot" || s.mutations[1] != "account" {
		t.Fatal("bot and account enable controls were mixed")
	}
	w = appRequest(mux, "POST", "/api/tenants/tenant/bots/bot/knowledge/k1/deactivate?scope_id=scope", `{"expected_revision":3}`, "admin-only", "")
	if w.Code != 200 || s.mutationID != "k1" || s.expectedRevision != 3 || s.scopes[len(s.scopes)-1].ScopeID != "scope" {
		t.Fatalf("knowledge CAS/scope lost: %d %s", w.Code, w.Body.String())
	}
	count := len(s.mutations)
	w = appRequest(mux, "POST", "/api/tenants/tenant/bots/bot/knowledge/k1/deactivate", `{"expected_revision":0}`, "admin-only", "")
	if w.Code != 400 || len(s.mutations) != count {
		t.Fatal("invalid knowledge CAS caused mutation")
	}
}
