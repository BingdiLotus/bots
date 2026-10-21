package chronotope

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, endpoint string, change ...func(*Config)) *Client {
	t.Helper()
	cfg := Config{BaseURL: endpoint, TokenRef: "env:CHRONOTOPE_TOKEN", ResolveToken: func(_ context.Context, ref string) (string, error) {
		if ref != "env:CHRONOTOPE_TOKEN" {
			t.Fatalf("wrong secret reference: %q", ref)
		}
		return "service-token", nil
	}}
	for _, option := range change {
		option(&cfg)
	}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

func requireAPIError(t *testing.T, err error, kind ErrorKind, status int) *APIError {
	t.Helper()
	var failure *APIError
	if !errors.As(err, &failure) || failure.Kind != kind || failure.StatusCode != status {
		t.Fatalf("want %s HTTP %d; got %#v", kind, status, err)
	}
	return failure
}

func TestClientExercisesActualApplicationWire(t *testing.T) {
	ctx := context.Background()
	input := CreateAgentRequest{Name: "bot", Config: AgentConfig{Model: "model-1", Instructions: "Use the native tools.", Tools: []string{}, Version: 1, ApplicationProfile: true, ApplicationBridgeRef: "bots", ApplicationTools: NativeTools(), MaxOutputTokens: 256, MaxSteps: 8, MaxDurationMS: 60000}}
	timerInput := TimerRequest{TenantID: "tenant & 1", SubjectRef: "bot?1", ScopeID: "scope+1", TimerID: "task/with?#id", Revision: 2,
		DueAt: time.Date(2026, 10, 20, 12, 3, 4, 123456789, time.FixedZone("CST", 8*3600)), OpaqueActionRef: "task-1"}
	timerResponse := Timer{TimerRequest: timerInput, TimerHandle: "timer-stable", FireID: "fire-stable", State: "pending"}
	timerResponse.DueAt = timerInput.DueAt.UTC().Truncate(time.Microsecond)
	var seen []string
	var authCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.EscapedPath())
		if r.Header.Get("Authorization") != fmt.Sprintf("Bearer rotated-%d", len(seen)) {
			t.Errorf("missing rotated service token: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Error("missing Accept header")
		}
		if r.Method == http.MethodPost && r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing JSON Content-Type")
		}
		switch {
		case r.URL.Path == "/prefix/orgs/org1/agents":
			if r.Header.Get("Idempotency-Key") != "create-agent:stable" {
				t.Errorf("original key lost: %q", r.Header.Get("Idempotency-Key"))
			}
			var got CreateAgentRequest
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Error(err)
			}
			if !reflect.DeepEqual(got, input) {
				t.Errorf("agent request changed: %+v", got)
			}
			writeJSON(t, w, 201, Agent{ID: "a1", Name: got.Name, Config: got.Config, SpecDigest: "digest-1"})
		case r.URL.Path == "/prefix/agents/a1/sessions":
			if r.Header.Get("Idempotency-Key") != "create-session:stable" {
				t.Error("session key lost")
			}
			var got CreateSessionRequest
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Error(err)
			}
			if got.SubjectRef != "bot1" || got.ScopeID != "scope1" {
				t.Errorf("wrong binding: %+v", got)
			}
			writeJSON(t, w, 201, Session{ID: "s1", AgentID: "a1", Status: "ready", SubjectRef: got.SubjectRef, ScopeID: got.ScopeID})
		case r.URL.Path == "/prefix/sessions/s1/runs":
			if r.Header.Get("Idempotency-Key") != "command:original" {
				t.Error("run key changed")
			}
			var got SubmitRunRequest
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Error(err)
			}
			if got.Input != "remember this" || got.Topic != "thread" || got.OriginCommandID != "inbox-1" || got.Trigger["source"] != "slack" {
				t.Errorf("full command changed: %+v", got)
			}
			writeJSON(t, w, 202, RunHandle{RunID: "r1", Status: RunQueued})
		case r.URL.Path == "/prefix/runs/r1":
			if r.Header.Get("Idempotency-Key") != "" {
				t.Error("read must not invent a mutation key")
			}
			final := "actual result"
			writeJSON(t, w, 200, Run{RunID: "r1", SessionID: "s1", Status: RunCompleted, Final: &final, Usage: &RunUsage{TokensIn: 7, TokensOut: 3, TokensTotal: 10},
				Bound: RunBinding{TenantID: "org1", SubjectRef: "bot1", ScopeID: "scope1", OriginCommandID: "inbox-1", AgentConfigVersion: 1}})
		case r.URL.Path == "/prefix/timers":
			if r.Header.Get("Idempotency-Key") != "" {
				t.Error("timer uses tenant/id/revision, not an invented header")
			}
			var got TimerRequest
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Error(err)
			}
			if !reflect.DeepEqual(got, timerInput) { // Time zones may have different location names after JSON decoding.
				if !got.DueAt.Equal(timerInput.DueAt) || got.TimerID != timerInput.TimerID || got.TenantID != timerInput.TenantID || got.OpaqueActionRef != timerInput.OpaqueActionRef || got.Revision != 2 {
					t.Errorf("timer payload changed: %+v", got)
				}
			}
			writeJSON(t, w, 202, timerResponse)
		case r.URL.EscapedPath() == "/prefix/timers/"+url.PathEscape(timerInput.TimerID):
			query := r.URL.Query()
			if query.Get("tenant_id") != timerInput.TenantID || query.Get("subject_ref") != timerInput.SubjectRef || query.Get("scope_id") != timerInput.ScopeID || query.Get("revision") != "2" {
				t.Errorf("query lost binding: %s", r.URL.RawQuery)
			}
			writeJSON(t, w, 200, timerResponse)
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := testClient(t, server.URL+"/prefix/", func(c *Config) {
		c.ResolveToken = func(_ context.Context, ref string) (string, error) {
			if ref != "env:CHRONOTOPE_TOKEN" {
				t.Errorf("wrong credential ref %q", ref)
			}
			authCalls++
			return fmt.Sprintf("rotated-%d", authCalls), nil
		}
	})
	agent, err := client.CreateAgent(ctx, "org1", "create-agent:stable", input)
	if err != nil || agent.ID != "a1" {
		t.Fatalf("agent: %+v %v", agent, err)
	}
	session, err := client.CreateSession(ctx, "a1", "create-session:stable", CreateSessionRequest{SubjectRef: "bot1", ScopeID: "scope1"})
	if err != nil || session.ID != "s1" {
		t.Fatalf("session: %+v %v", session, err)
	}
	handle, err := client.SubmitRun(ctx, "s1", "command:original", SubmitRunRequest{Input: "remember this", Topic: "thread", OriginCommandID: "inbox-1", Trigger: map[string]any{"source": "slack"}})
	if err != nil || handle.RunID != "r1" || handle.Status != RunQueued {
		t.Fatalf("admission: %+v %v", handle, err)
	}
	run, err := client.GetRun(ctx, "r1")
	if err != nil || run.Final == nil || *run.Final != "actual result" || !run.Status.Terminal() || run.Bound.OriginCommandID != "inbox-1" {
		t.Fatalf("run result: %+v %v", run, err)
	}
	timer, err := client.CreateTimer(ctx, timerInput)
	if err != nil || timer.TimerHandle != "timer-stable" {
		t.Fatalf("timer: %+v %v", timer, err)
	}
	queried, err := client.GetTimer(ctx, TimerQuery{TenantID: timerInput.TenantID, SubjectRef: timerInput.SubjectRef, ScopeID: timerInput.ScopeID, TimerID: timerInput.TimerID, Revision: 2})
	if err != nil || queried.FireID != "fire-stable" {
		t.Fatalf("timer query: %+v %v", queried, err)
	}
	if len(seen) != 6 || authCalls != 6 {
		t.Fatalf("six explicit calls expected: %v; auth=%d", seen, authCalls)
	}
	if timerInput.DueAt.Nanosecond() != 123456789 {
		t.Fatal("client mutated caller's timer input")
	}
}

func TestRunRetryReturnsActualIdempotentHandle(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Idempotency-Key") != "same" {
			t.Error("unstable retry key")
		}
		if calls == 1 {
			writeJSON(t, w, 202, RunHandle{RunID: "run-stable", Status: RunQueued})
			return
		}
		writeJSON(t, w, 200, RunHandle{RunID: "run-stable", Status: RunRunning, Idempotent: true})
	}))
	defer server.Close()
	client := testClient(t, server.URL)
	first, err := client.SubmitRun(context.Background(), "s", "same", SubmitRunRequest{Input: "x"})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := client.SubmitRun(context.Background(), "s", "same", SubmitRunRequest{Input: "x"})
	if err != nil || retry.RunID != first.RunID || !retry.Idempotent || retry.Status != RunRunning || calls != 2 {
		t.Fatalf("retry must return actual 200 canonical handle: %+v %v calls=%d", retry, err, calls)
	}
}

func TestStructuredErrorsRemainDistinctAndAreNotRetried(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		kind   ErrorKind
		retry  string
	}{
		{"busy", 409, `{"code":409,"error_code":"session_busy","error":"busy","active_run_id":"r-active"}`, ErrorSessionBusy, ""},
		{"idempotency", 409, `{"code":409,"error_code":"idempotency_conflict","error":"changed request"}`, ErrorIdempotencyConflict, ""},
		{"phase conflict", 409, `{"code":409,"error":"paused session"}`, ErrorConflict, ""},
		{"unauthorized", 401, `{"code":401,"error":"missing key"}`, ErrorAuth, ""},
		{"forbidden", 403, `{"code":403,"error":"other tenant"}`, ErrorAuth, ""},
		{"bad payload", 422, `{"code":422,"error":"invalid input"}`, ErrorBadPayload, ""},
		{"bad request", 400, `{"code":400,"error":"invalid JSON"}`, ErrorBadPayload, ""},
		{"not found", 404, `{"code":404,"error":"not found"}`, ErrorNotFound, ""},
		{"rate limit", 429, `{"code":429,"error":"limited"}`, ErrorRateLimited, "7"},
		{"proxy unavailable", 503, `upstream unavailable`, ErrorUnavailable, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", test.retry)
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			_, err := testClient(t, server.URL).SubmitRun(context.Background(), "s", "original-key", SubmitRunRequest{Input: "x"})
			failure := requireAPIError(t, err, test.kind, test.status)
			if calls.Load() != 1 {
				t.Fatal("client retried a mutation")
			}
			if test.name == "busy" && (failure.ActiveRunID != "r-active" || failure.ErrorCode != "session_busy" || failure.Code != 409) {
				t.Fatalf("lost busy facts: %+v", failure)
			}
			if test.retry != "" && failure.RetryAfter != 7*time.Second {
				t.Fatalf("lost retry delay: %v", failure.RetryAfter)
			}
			if strings.Contains(err.Error(), "service-token") {
				t.Fatal("error leaked credential")
			}
		})
	}
}

func TestTimeoutAndCancellationPropagate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	client := testClient(t, server.URL, func(c *Config) { c.Timeout = 20 * time.Millisecond })
	_, err := client.GetRun(context.Background(), "r")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout must remain inspectable: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.SubmitRun(ctx, "s", "stable", SubmitRunRequest{Input: "x"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWritesDisableTransportReplayAndReturnOriginalFailure(t *testing.T) {
	var calls int
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.GetBody != nil {
			t.Error("POST with Idempotency-Key must not be replayable by Transport")
		}
		if req.Header.Get("Idempotency-Key") != "persisted-key" {
			t.Error("key changed")
		}
		return nil, io.ErrUnexpectedEOF
	})}
	client := testClient(t, "http://fixed-runtime", func(c *Config) { c.HTTPClient = httpClient })
	_, err := client.SubmitRun(context.Background(), "s", "persisted-key", SubmitRunRequest{Input: "x"})
	if calls != 1 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("unknown transport failure must return without replay: calls=%d err=%v", calls, err)
	}
}

func TestRedirectDoesNotForwardServiceCredentials(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		t.Errorf("redirect followed with auth %q", r.Header.Get("Authorization"))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	provided := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		t.Error("caller redirect policy must not be used for service requests")
		return nil
	}}
	_, err := testClient(t, source.URL, func(c *Config) { c.HTTPClient = provided }).SubmitRun(context.Background(), "s", "key", SubmitRunRequest{Input: "x"})
	requireAPIError(t, err, ErrorBadResponse, http.StatusTemporaryRedirect)
	if forwarded.Load() != 0 || provided.CheckRedirect == nil {
		t.Fatal("client must clone the provided HTTP client without following redirects")
	}
}

func TestResponseBodiesAreBoundedAndCanonicalResultIsRequired(t *testing.T) {
	for _, test := range []struct {
		name, body string
		limit      int64
	}{
		{"oversized", strings.Repeat("x", 256), 64},
		{"invalid JSON", `{broken`, 1024},
		{"null", `null`, 1024},
		{"missing result", `{"run_id":"r","session_id":"s","status":"completed","final":null,"usage":null}`, 1024},
		{"wrong identity", `{"run_id":"different","session_id":"s","status":"queued","final":null,"usage":null}`, 1024},
		{"trailing JSON", `{"run_id":"r","session_id":"s","status":"queued"} {}`, 1024},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, test.body) }))
			defer server.Close()
			_, err := testClient(t, server.URL, func(c *Config) { c.MaxResponseBytes = test.limit }).GetRun(context.Background(), "r")
			requireAPIError(t, err, ErrorBadResponse, 200)
		})
	}
}

func TestRequestValidationOccursBeforeSending(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := testClient(t, server.URL, func(c *Config) { c.MaxRequestBytes = 128 })
	for _, key := range []string{"", " padded ", "bad\nheader", strings.Repeat("x", 257)} {
		_, err := client.SubmitRun(context.Background(), "s", key, SubmitRunRequest{Input: "x"})
		requireAPIError(t, err, ErrorBadPayload, 0)
	}
	_, err := client.SubmitRun(context.Background(), "s", "valid", SubmitRunRequest{Input: strings.Repeat("x", 256)})
	requireAPIError(t, err, ErrorBadPayload, 0)
	_, err = client.SubmitRun(context.Background(), "s", "valid", SubmitRunRequest{Trigger: map[string]any{"invalid": make(chan bool)}})
	requireAPIError(t, err, ErrorBadPayload, 0)
	_, err = client.GetRun(context.Background(), "")
	requireAPIError(t, err, ErrorBadPayload, 0)
	if calls.Load() != 0 {
		t.Fatal("invalid local request crossed network boundary")
	}
}

func TestOversizedErrorsPreserveHTTPFacts(t *testing.T) {
	for _, test := range []struct {
		status int
		kind   ErrorKind
	}{{403, ErrorAuth}, {429, ErrorRateLimited}, {503, ErrorUnavailable}} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, strings.Repeat("x", 1000))
			}))
			defer server.Close()
			_, err := testClient(t, server.URL, func(c *Config) { c.MaxResponseBytes = 32 }).GetRun(context.Background(), "r")
			failure := requireAPIError(t, err, test.kind, test.status)
			if failure.RetryAfter != 3*time.Second {
				t.Fatalf("bounded body must not discard Retry-After: %+v", failure)
			}
		})
	}
}

func TestTimerConflictAndWrongScope(t *testing.T) {
	in := TimerRequest{TenantID: "tenant", SubjectRef: "bot", ScopeID: "scope", TimerID: "task", Revision: 1, DueAt: time.Now().Add(time.Hour).Truncate(time.Microsecond), OpaqueActionRef: "task"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeJSON(t, w, 409, map[string]any{"code": 409, "error": "timer key has a different request"})
			return
		}
		bad := in
		bad.ScopeID = "other"
		writeJSON(t, w, 200, Timer{TimerRequest: bad, TimerHandle: "h", FireID: "f", State: "pending"})
	}))
	defer server.Close()
	client := testClient(t, server.URL)
	_, err := client.CreateTimer(context.Background(), in)
	requireAPIError(t, err, ErrorIdempotencyConflict, 409)
	_, err = client.GetTimer(context.Background(), TimerQuery{TenantID: in.TenantID, SubjectRef: in.SubjectRef, ScopeID: in.ScopeID, TimerID: in.TimerID, Revision: 1})
	requireAPIError(t, err, ErrorBadResponse, 200)
}

func TestTokenResolverIsContextBoundAndErrorsStayWrapped(t *testing.T) {
	sentinel := errors.New("credential unavailable")
	client := testClient(t, "http://fixed-runtime", func(c *Config) {
		c.ResolveToken = func(context.Context, string) (string, error) { return "", sentinel }
	})
	_, err := client.GetRun(context.Background(), "r")
	if !errors.Is(err, sentinel) {
		t.Fatalf("resolver error lost: %v", err)
	}
	client = testClient(t, "http://fixed-runtime", func(c *Config) {
		c.Timeout = 10 * time.Millisecond
		c.ResolveToken = func(ctx context.Context, _ string) (string, error) { <-ctx.Done(); return "", ctx.Err() }
	})
	_, err = client.GetRun(context.Background(), "r")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("resolver received no request deadline: %v", err)
	}
}

func TestClientConfigurationAndRetryAfter(t *testing.T) {
	for _, base := range []string{"", "ftp://runtime", "http://user:pass@runtime", "http://runtime?url=evil", "http://runtime#fragment"} {
		if _, err := NewClient(Config{BaseURL: base, TokenRef: "ref", ResolveToken: func(context.Context, string) (string, error) { return "token", nil }}); err == nil {
			t.Fatalf("unsafe base URL accepted: %q", base)
		}
	}
	if _, err := NewClient(Config{BaseURL: "http://runtime"}); err == nil {
		t.Fatal("missing resolver/ref accepted")
	}
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if retryAfter(now.Add(9*time.Second).Format(http.TimeFormat), now) != 9*time.Second || retryAfter("-1", now) != 0 || retryAfter("invalid", now) != 0 {
		t.Fatal("Retry-After date/numeric handling changed")
	}
}
