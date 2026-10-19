package chronotope

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type TokenResolver func(context.Context, string) (string, error)

type Config struct {
	BaseURL          string
	TokenRef         string
	ResolveToken     TokenResolver
	Timeout          time.Duration
	HTTPClient       *http.Client
	MaxRequestBytes  int64
	MaxResponseBytes int64
}

type Client struct {
	baseURL          string
	tokenRef         string
	resolveToken     TokenResolver
	timeout          time.Duration
	http             *http.Client
	maxRequestBytes  int64
	maxResponseBytes int64
}

// NewClient fixes the service address and credential reference at construction.
// Token resolution happens per request so credential rotation needs no client
// replacement. The resolver must honor its context. Redirects and application
// retries are disabled; caller-owned outboxes decide retry and recovery policy.
func NewClient(cfg Config) (*Client, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Opaque != "" {
		return nil, fmt.Errorf("chronotope: invalid configured base URL")
	}
	if strings.TrimSpace(cfg.TokenRef) == "" || cfg.ResolveToken == nil {
		return nil, fmt.Errorf("chronotope: token reference and resolver are required")
	}
	if cfg.Timeout < 0 || cfg.MaxRequestBytes < 0 || cfg.MaxResponseBytes < 0 {
		return nil, fmt.Errorf("chronotope: timeout and body limits cannot be negative")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.MaxRequestBytes == 0 {
		cfg.MaxRequestBytes = 1 << 20
	}
	if cfg.MaxResponseBytes == 0 {
		cfg.MaxResponseBytes = 1 << 20
	}
	client := http.Client{}
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(base.String(), "/"), tokenRef: cfg.TokenRef,
		resolveToken: cfg.ResolveToken, timeout: cfg.Timeout, http: &client,
		maxRequestBytes: cfg.MaxRequestBytes, maxResponseBytes: cfg.MaxResponseBytes}, nil
}

func (c *Client) CreateAgent(ctx context.Context, orgID, key string, in CreateAgentRequest) (Agent, error) {
	var out Agent
	if err := validateID(orgID); err != nil {
		return out, err
	}
	status, err := c.do(ctx, http.MethodPost, "/orgs/"+segment(orgID)+"/agents", key, in, &out, http.StatusCreated, http.StatusOK)
	if err == nil && (out.ID == "" || out.Name != in.Name || out.SpecDigest == "" || out.Config.Model != in.Config.Model || out.Config.Version <= 0 || out.Idempotent != (status == http.StatusOK)) {
		err = badResponse(status, "agent response is missing immutable identity/configuration")
	}
	return out, err
}

func (c *Client) CreateSession(ctx context.Context, agentID, key string, in CreateSessionRequest) (Session, error) {
	var out Session
	if err := validateID(agentID); err != nil {
		return out, err
	}
	status, err := c.do(ctx, http.MethodPost, "/agents/"+segment(agentID)+"/sessions", key, in, &out, http.StatusCreated, http.StatusOK)
	if err == nil && (out.ID == "" || out.AgentID != agentID || out.Status == "" || out.SubjectRef != in.SubjectRef || out.ScopeID != in.ScopeID || out.Idempotent != (status == http.StatusOK)) {
		err = badResponse(status, "session response does not match the requested binding")
	}
	return out, err
}

func (c *Client) SubmitRun(ctx context.Context, sessionID, key string, in SubmitRunRequest) (RunHandle, error) {
	var out RunHandle
	if err := validateID(sessionID); err != nil {
		return out, err
	}
	status, err := c.do(ctx, http.MethodPost, "/sessions/"+segment(sessionID)+"/runs", key, in, &out, http.StatusAccepted, http.StatusOK)
	if err == nil && (out.RunID == "" || !out.Status.valid() || out.Idempotent != (status == http.StatusOK) || status == http.StatusAccepted && out.Status != RunQueued) {
		err = badResponse(status, "run admission did not return a valid stable handle")
	}
	return out, err
}

func (c *Client) GetRun(ctx context.Context, runID string) (Run, error) {
	var out Run
	if err := validateID(runID); err != nil {
		return out, err
	}
	status, err := c.do(ctx, http.MethodGet, "/runs/"+segment(runID), "", nil, &out, http.StatusOK)
	if err == nil && (out.RunID != runID || out.SessionID == "" || !out.Status.valid() || out.Status.Terminal() && (out.Final == nil || out.Usage == nil || out.Status != RunCompleted && out.Error == nil)) {
		err = badResponse(status, "run response is missing canonical identity/status/result")
	}
	return out, err
}

func (c *Client) CreateTimer(ctx context.Context, in TimerRequest) (Timer, error) {
	var out Timer
	query := TimerQuery{TenantID: in.TenantID, SubjectRef: in.SubjectRef, ScopeID: in.ScopeID, TimerID: in.TimerID, Revision: in.Revision}
	if err := query.validate(); err != nil {
		return out, err
	}
	if in.DueAt.IsZero() || in.DueAt.Year() < 1 || in.DueAt.Year() > 9999 || !identifier(in.OpaqueActionRef, 2048) {
		return out, badPayload("timer requires an absolute due_at and opaque_action_ref")
	}
	status, err := c.do(ctx, http.MethodPost, "/timers", "", in, &out, http.StatusAccepted)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
		// The timer endpoint currently has no machine error_code; its 409 is
		// defined solely as reuse of tenant/timer_id/revision with new input.
		apiErr.Kind = ErrorIdempotencyConflict
	}
	if err == nil && (!out.matches(query) || out.OpaqueActionRef != in.OpaqueActionRef || !out.DueAt.Equal(in.DueAt.UTC().Truncate(time.Microsecond))) {
		err = badResponse(status, "timer response does not match the requested identity/payload")
	}
	return out, err
}

func (c *Client) GetTimer(ctx context.Context, query TimerQuery) (Timer, error) {
	var out Timer
	if err := query.validate(); err != nil {
		return out, err
	}
	values := url.Values{"tenant_id": {query.TenantID}, "subject_ref": {query.SubjectRef}, "scope_id": {query.ScopeID}, "revision": {strconv.FormatInt(query.Revision, 10)}}
	status, err := c.do(ctx, http.MethodGet, "/timers/"+segment(query.TimerID)+"?"+values.Encode(), "", nil, &out, http.StatusOK)
	if err == nil && !out.matches(query) {
		err = badResponse(status, "timer query returned a different identity or scope")
	}
	return out, err
}

func (q TimerQuery) validate() error {
	for _, value := range []string{q.TenantID, q.SubjectRef, q.ScopeID, q.TimerID} {
		if !identifier(value, 256) {
			return badPayload("timer tenant/subject/scope/id must be nonempty identifiers of at most 256 bytes")
		}
	}
	if q.Revision <= 0 {
		return badPayload("timer revision must be positive")
	}
	return nil
}

func (t Timer) matches(q TimerQuery) bool {
	if t.TenantID != q.TenantID || t.SubjectRef != q.SubjectRef || t.ScopeID != q.ScopeID || t.TimerID != q.TimerID || t.Revision != q.Revision || t.TimerHandle == "" || t.FireID == "" {
		return false
	}
	switch t.State {
	case "pending", "scheduled", "firing", "retrying", "blocked", "acknowledged":
		return true
	}
	return false
}

func identifier(value string, max int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= max && !strings.ContainsFunc(value, unicode.IsControl)
}

func validateID(value string) error {
	if !identifier(value, 4096) {
		return badPayload("resource identifier is empty or invalid")
	}
	return nil
}

func segment(value string) string {
	if value == "." || value == ".." {
		return strings.Repeat("%2E", len(value))
	}
	return url.PathEscape(value)
}

func (c *Client) do(ctx context.Context, method, path, key string, body, out any, allowed ...int) (int, error) {
	if method == http.MethodPost && path != "/timers" && !identifier(key, 256) {
		return 0, badPayload("a stable Idempotency-Key of 1–256 bytes is required")
	}
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return 0, badPayload("encode request: " + err.Error())
		}
		if int64(len(raw)) > c.maxRequestBytes {
			return 0, badPayload("request body exceeds configured byte limit")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	token, err := c.resolveToken(ctx, c.tokenRef)
	if err != nil {
		return 0, fmt.Errorf("chronotope: resolve service credential: %w", err)
	}
	if strings.TrimSpace(token) == "" || strings.ContainsFunc(token, unicode.IsSpace) || strings.ContainsFunc(token, unicode.IsControl) {
		return 0, &APIError{Kind: ErrorAuth, Message: "resolved service credential is empty or invalid"}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return 0, badPayload("build request: " + err.Error())
	}
	// Go's Transport may otherwise replay a POST carrying Idempotency-Key on a
	// reused connection. Writes return their actual error to the caller/outbox.
	if method == http.MethodPost {
		req.GetBody = nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("chronotope %s request: %w", method, err)
	}
	defer response.Body.Close()
	accepted := false
	for _, status := range allowed {
		accepted = accepted || response.StatusCode == status
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, c.maxResponseBytes+1))
	if err != nil {
		return response.StatusCode, fmt.Errorf("chronotope: read response: %w", err)
	}
	if int64(len(data)) > c.maxResponseBytes {
		kind := ErrorBadResponse
		if !accepted {
			kind = classifyStatus(response.StatusCode, "")
		}
		return response.StatusCode, &APIError{StatusCode: response.StatusCode, Kind: kind,
			Message: "response body exceeds configured byte limit", RetryAfter: retryAfter(response.Header.Get("Retry-After"), time.Now())}
	}
	if !accepted {
		var failure struct {
			Code        int    `json:"code"`
			ErrorCode   string `json:"error_code"`
			Error       string `json:"error"`
			ActiveRunID string `json:"active_run_id"`
		}
		_ = json.Unmarshal(data, &failure)
		if failure.Error == "" {
			failure.Error = http.StatusText(response.StatusCode)
		}
		return response.StatusCode, &APIError{StatusCode: response.StatusCode, Code: failure.Code, ErrorCode: failure.ErrorCode,
			Kind: classifyStatus(response.StatusCode, failure.ErrorCode), Message: failure.Error, ActiveRunID: failure.ActiveRunID,
			RetryAfter: retryAfter(response.Header.Get("Retry-After"), time.Now())}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return response.StatusCode, badResponse(response.StatusCode, "invalid JSON response: "+err.Error())
	}
	return response.StatusCode, nil
}
