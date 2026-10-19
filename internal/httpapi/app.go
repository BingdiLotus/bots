package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bingdilotus/bots/internal/config"
	"github.com/bingdilotus/bots/internal/domain"
)

// Store is the HTTP layer's persistence boundary. Mutations and their receipts
// remain atomic inside the concrete store; handlers acknowledge only its result.
type Store interface {
	GetBot(context.Context, string, string) (domain.Bot, error)
	ListBots(context.Context, string) ([]domain.Bot, error)
	SetBotEnabled(context.Context, string, string, bool) error
	SetAccountEnabled(context.Context, string, string, bool) error
	ExecuteTool(context.Context, domain.ToolCall) (json.RawMessage, error)
	AcceptFire(context.Context, domain.TimerFire) (bool, error)
	AcceptInbox(context.Context, domain.Inbox) (domain.Inbox, bool, error)
	HasConversation(context.Context, string, string, string) (bool, error)
	InvalidateMessage(context.Context, string, string, string, string, string, string) error
	ListKnowledge(context.Context, domain.Scope, int) ([]domain.Knowledge, error)
	DeactivateKnowledge(context.Context, domain.Scope, string, int) error
	GetTask(context.Context, domain.Scope, string) (domain.Task, error)
	ListTasks(context.Context, domain.Scope, int) ([]domain.Task, error)
	ListDeliveries(context.Context, domain.Scope, int) ([]domain.Delivery, error)
	ListAudit(context.Context, domain.Scope, int) ([]domain.AuditEvent, error)
}

type App struct {
	Store            Store
	TenantID         string
	AdminTokenEnv    string
	InternalTokenEnv string
	UpdateConfig     func(context.Context, string, string, int, domain.BotConfig) (domain.Bot, error)
	Now              func() time.Time
}

func (a *App) Register(mux *http.ServeMux) {
	a.registerConsole(mux)
	mux.HandleFunc("POST /internal/tools", a.executeTool)
	mux.HandleFunc("POST /internal/timer-fires", a.acceptTimerFire)
	mux.HandleFunc("GET /api/tenants/{tenant}/bots", a.listBots)
	mux.HandleFunc("GET /api/tenants/{tenant}/bots/{bot}", a.getBot)
	mux.HandleFunc("PUT /api/tenants/{tenant}/bots/{bot}/config", a.updateConfig)
	mux.HandleFunc("PUT /api/tenants/{tenant}/bots/{bot}/enabled", a.setEnabled)
	mux.HandleFunc("PUT /api/tenants/{tenant}/bots/{bot}/account-enabled", a.setEnabled)
	mux.HandleFunc("GET /api/tenants/{tenant}/bots/{bot}/knowledge", a.listRecords)
	mux.HandleFunc("POST /api/tenants/{tenant}/bots/{bot}/knowledge/{knowledge}/deactivate", a.deactivateKnowledge)
	mux.HandleFunc("GET /api/tenants/{tenant}/bots/{bot}/tasks", a.listRecords)
	mux.HandleFunc("GET /api/tenants/{tenant}/bots/{bot}/tasks/{task}", a.getTask)
	mux.HandleFunc("GET /api/tenants/{tenant}/bots/{bot}/deliveries", a.listRecords)
	mux.HandleFunc("GET /api/tenants/{tenant}/bots/{bot}/audit", a.listRecords)
	mux.HandleFunc("POST /channels/slack/{tenant}/{bot}/events", a.slackEvents)
}

func (a *App) authenticate(w http.ResponseWriter, r *http.Request, reference string) bool {
	expected, err := config.Secret(reference)
	if err != nil {
		replyError(w, http.StatusServiceUnavailable, "authentication_unavailable", "authentication is not configured")
		return false
	}
	scheme, supplied, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	expectedHash, suppliedHash := sha256.Sum256([]byte(expected)), sha256.Sum256([]byte(supplied))
	matched := subtle.ConstantTimeCompare(expectedHash[:], suppliedHash[:])
	if !ok || scheme != "Bearer" || supplied == "" || strings.TrimSpace(supplied) != supplied || matched != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		replyError(w, http.StatusUnauthorized, "unauthorized", "valid service credentials are required")
		return false
	}
	return true
}

func (a *App) tenant(w http.ResponseWriter, tenant string) bool {
	if a.TenantID == "" || a.Store == nil {
		replyError(w, http.StatusServiceUnavailable, "unavailable", "application is not configured")
		return false
	}
	if tenant == "" || tenant != a.TenantID {
		replyError(w, http.StatusForbidden, "forbidden", "tenant is not authorized")
		return false
	}
	return true
}

func (a *App) admin(w http.ResponseWriter, r *http.Request) bool {
	return a.authenticate(w, r, a.AdminTokenEnv) && a.tenant(w, r.PathValue("tenant"))
}

func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	var raw json.RawMessage
	err := decoder.Decode(&raw)
	if err == nil {
		err = decoder.Decode(new(any))
		trimmed := bytes.TrimSpace(raw)
		if errors.Is(err, io.EOF) && len(trimmed) > 0 && trimmed[0] == '{' {
			strict := json.NewDecoder(bytes.NewReader(raw))
			strict.DisallowUnknownFields()
			err = strict.Decode(out)
			if err == nil {
				return true
			}
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		replyError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds 1 MiB")
	} else {
		replyError(w, http.StatusBadRequest, "invalid_request", "one JSON object with declared fields is required")
	}
	return false
}

func (a *App) executeTool(w http.ResponseWriter, r *http.Request) {
	if !a.authenticate(w, r, a.InternalTokenEnv) {
		return
	}
	var call domain.ToolCall
	if !decodeJSON(w, r, &call) || !a.tenant(w, call.Context.TenantID) {
		return
	}
	c := call.Context
	if c.SubjectRef == "" || c.ScopeID == "" || c.OriginCommandID == "" || c.RunID == "" || c.OperationID == "" || c.ToolCallID == "" || call.ToolRef == "" || len(call.Arguments) == 0 || r.Header.Get("Idempotency-Key") != c.OperationID {
		replyError(w, http.StatusBadRequest, "invalid_request", "bound tool context and matching Idempotency-Key are required")
		return
	}
	// The store verifies origin/run/scope against the durable source association,
	// and returns an existing operation receipt before live enable-state checks.
	result, err := a.Store.ExecuteTool(r.Context(), call)
	if err != nil {
		replyStoreError(w, err)
		return
	}
	if !json.Valid(result) {
		replyError(w, http.StatusInternalServerError, "internal_error", "tool result could not be read")
		return
	}
	jsonHeaders(w)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

func (a *App) acceptTimerFire(w http.ResponseWriter, r *http.Request) {
	if !a.authenticate(w, r, a.InternalTokenEnv) {
		return
	}
	var fire domain.TimerFire
	if !decodeJSON(w, r, &fire) || !a.tenant(w, fire.TenantID) {
		return
	}
	if fire.TimerID == "" || fire.Revision < 1 || fire.FireID == "" || fire.OpaqueActionRef == "" || r.Header.Get("Idempotency-Key") != fire.FireID {
		replyError(w, http.StatusBadRequest, "invalid_request", "timer identity and matching Idempotency-Key are required")
		return
	}
	// The five-field fire has no subject/scope. AcceptFire resolves and checks
	// those from the existing task/timer binding before committing its receipt.
	created, err := a.Store.AcceptFire(r.Context(), fire)
	if err != nil {
		replyStoreError(w, err)
		return
	}
	replyJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "idempotent": !created})
}

func jsonHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
}

func replyJSON(w http.ResponseWriter, status int, body any) {
	jsonHeaders(w)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func replyError(w http.ResponseWriter, status int, code, message string) {
	replyJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func replyStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		replyError(w, 422, "invalid_request", "request is invalid")
	case errors.Is(err, domain.ErrNotFound):
		replyError(w, 404, "not_found", "resource was not found in scope")
	case errors.Is(err, domain.ErrConflict):
		replyError(w, 409, "conflict", "request or revision conflicts with stored state")
	case errors.Is(err, domain.ErrDisabled):
		replyError(w, 409, "disabled", "bot or account is disabled")
	case errors.Is(err, domain.ErrBusy):
		replyError(w, 409, "in_flight_or_unknown", "operation is in flight or has an unknown result")
	case errors.Is(err, domain.ErrLimit):
		replyError(w, 409, "pending_task_limit", "pending reminder limit has been reached")
	default:
		replyError(w, 500, "internal_error", "request could not be completed")
	}
}
