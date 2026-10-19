package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/bingdilotus/bots/internal/config"
	"github.com/bingdilotus/bots/internal/domain"
)

func (a *App) listBots(w http.ResponseWriter, r *http.Request) {
	if !a.admin(w, r) {
		return
	}
	bots, err := a.Store.ListBots(r.Context(), a.TenantID)
	if err != nil {
		replyStoreError(w, err)
		return
	}
	if bots == nil {
		bots = []domain.Bot{}
	}
	replyJSON(w, 200, map[string]any{"bots": bots})
}

func (a *App) botScope(w http.ResponseWriter, r *http.Request) (domain.Scope, bool) {
	if !a.admin(w, r) {
		return domain.Scope{}, false
	}
	scope := domain.Scope{TenantID: a.TenantID, BotID: r.PathValue("bot"), ScopeID: r.URL.Query().Get("scope_id")}
	if scope.BotID == "" || strings.TrimSpace(scope.ScopeID) != scope.ScopeID {
		replyError(w, 400, "invalid_request", "bot and scope identifiers are invalid")
		return scope, false
	}
	bot, err := a.Store.GetBot(r.Context(), scope.TenantID, scope.BotID)
	if err != nil {
		replyStoreError(w, err)
		return scope, false
	}
	if bot.TenantID != scope.TenantID || bot.ID != scope.BotID {
		replyError(w, 403, "forbidden", "bot is not authorized")
		return scope, false
	}
	return scope, true
}

func (a *App) getBot(w http.ResponseWriter, r *http.Request) {
	if !a.admin(w, r) {
		return
	}
	bot, err := a.Store.GetBot(r.Context(), a.TenantID, r.PathValue("bot"))
	if err != nil {
		replyStoreError(w, err)
		return
	}
	if bot.TenantID != a.TenantID || bot.ID != r.PathValue("bot") {
		replyError(w, 403, "forbidden", "bot is not authorized")
		return
	}
	replyJSON(w, 200, bot)
}

func (a *App) updateConfig(w http.ResponseWriter, r *http.Request) {
	scope, ok := a.botScope(w, r)
	if !ok {
		return
	}
	var input struct {
		ExpectedRevision int               `json:"expected_revision"`
		Config           *domain.BotConfig `json:"config"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ExpectedRevision < 1 || input.Config == nil {
		replyError(w, 400, "invalid_request", "expected_revision and config are required")
		return
	}
	normalized, err := config.NormalizeBot(*input.Config)
	if err != nil {
		replyError(w, 422, "invalid_request", "bot configuration is invalid")
		return
	}
	if a.UpdateConfig == nil {
		replyError(w, 503, "unavailable", "configuration updates are not configured")
		return
	}
	bot, err := a.UpdateConfig(r.Context(), scope.TenantID, scope.BotID, input.ExpectedRevision, normalized)
	if err != nil {
		replyStoreError(w, err)
		return
	}
	if bot.TenantID != scope.TenantID || bot.ID != scope.BotID {
		replyError(w, 500, "internal_error", "configuration result could not be read")
		return
	}
	replyJSON(w, 200, bot)
}

func (a *App) setEnabled(w http.ResponseWriter, r *http.Request) {
	scope, ok := a.botScope(w, r)
	if !ok {
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		replyError(w, 400, "invalid_request", "enabled must be a boolean")
		return
	}
	var err error
	if strings.HasSuffix(r.URL.Path, "/account-enabled") {
		err = a.Store.SetAccountEnabled(r.Context(), scope.TenantID, scope.BotID, *input.Enabled)
	} else {
		err = a.Store.SetBotEnabled(r.Context(), scope.TenantID, scope.BotID, *input.Enabled)
	}
	if err != nil {
		replyStoreError(w, err)
		return
	}
	replyJSON(w, 200, map[string]any{"enabled": *input.Enabled})
}

func listLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	value := r.URL.Query().Get("limit")
	if value == "" {
		return 50, true
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 100 {
		replyError(w, 400, "invalid_request", "limit must be between 1 and 100")
		return 0, false
	}
	return n, true
}

func (a *App) listRecords(w http.ResponseWriter, r *http.Request) {
	scope, ok := a.botScope(w, r)
	if !ok {
		return
	}
	n, ok := listLimit(w, r)
	if !ok {
		return
	}
	kind := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	var result any
	var err error
	switch kind {
	case "knowledge":
		var rows []domain.Knowledge
		rows, err = a.Store.ListKnowledge(r.Context(), scope, n)
		if rows == nil {
			rows = []domain.Knowledge{}
		}
		result = rows
	case "tasks":
		var rows []domain.Task
		rows, err = a.Store.ListTasks(r.Context(), scope, n)
		if rows == nil {
			rows = []domain.Task{}
		}
		result = rows
	case "deliveries":
		var rows []domain.Delivery
		rows, err = a.Store.ListDeliveries(r.Context(), scope, n)
		if rows == nil {
			rows = []domain.Delivery{}
		}
		result = rows
	case "audit":
		var rows []domain.AuditEvent
		rows, err = a.Store.ListAudit(r.Context(), scope, n)
		if rows == nil {
			rows = []domain.AuditEvent{}
		}
		result = rows
	}
	if err != nil {
		replyStoreError(w, err)
		return
	}
	replyJSON(w, 200, map[string]any{kind: result})
}

func (a *App) getTask(w http.ResponseWriter, r *http.Request) {
	scope, ok := a.botScope(w, r)
	if !ok {
		return
	}
	task, err := a.Store.GetTask(r.Context(), scope, r.PathValue("task"))
	if err != nil {
		replyStoreError(w, err)
		return
	}
	if task.TenantID != scope.TenantID || task.BotID != scope.BotID || scope.ScopeID != "" && task.ScopeID != scope.ScopeID {
		replyError(w, 404, "not_found", "task was not found in scope")
		return
	}
	replyJSON(w, 200, task)
}

func (a *App) deactivateKnowledge(w http.ResponseWriter, r *http.Request) {
	scope, ok := a.botScope(w, r)
	if !ok {
		return
	}
	var input struct {
		ExpectedRevision int `json:"expected_revision"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ExpectedRevision < 1 {
		replyError(w, 400, "invalid_request", "expected_revision must be positive")
		return
	}
	if err := a.Store.DeactivateKnowledge(r.Context(), scope, r.PathValue("knowledge"), input.ExpectedRevision); err != nil {
		replyStoreError(w, err)
		return
	}
	replyJSON(w, 200, map[string]bool{"active": false})
}
