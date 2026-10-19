package slack

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestChatPostMessageWireAndOutcomeClassification(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body, retry string
		state       SendState
		code        string
		after       time.Duration
	}{
		{"accepted", 200, `{"ok":true,"channel":"C123","ts":"1700000000.000101"}`, "", Confirmed, "", 0},
		{"not in channel", 200, `{"ok":false,"error":"not_in_channel"}`, "", Rejected, "not_in_channel", 0},
		{"missing scope", 200, `{"ok":false,"error":"missing_scope"}`, "", Rejected, "missing_scope", 0},
		{"invalid auth", 401, `{"ok":false,"error":"invalid_auth"}`, "", Rejected, "invalid_auth", 0},
		{"rate limit", 429, `not required to parse this body`, "17", Retryable, "rate_limited", 17 * time.Second},
		{"rate error", 200, `{"ok":false,"error":"ratelimited"}`, "22", Retryable, "ratelimited", 22 * time.Second},
		{"server error", 503, `{"ok":false,"error":"not_in_channel"}`, "", Unknown, "provider_5xx", 0},
		{"ambiguous internal error", 200, `{"ok":false,"error":"internal_error"}`, "", Unknown, "internal_error", 0},
		{"duplicate without receipt", 200, `{"ok":false,"error":"duplicate_message"}`, "", Unknown, "duplicate_message", 0},
		{"missing ok", 200, `{"channel":"C123","ts":"1700000000.000101"}`, "", Unknown, "invalid_response", 0},
		{"invalid json", 200, `{"ok":true`, "", Unknown, "invalid_response", 0},
		{"missing timestamp", 200, `{"ok":true,"channel":"C123"}`, "", Unknown, "receipt_mismatch", 0},
		{"different channel", 200, `{"ok":true,"channel":"OTHER","ts":"1700000000.000101"}`, "", Unknown, "receipt_mismatch", 0},
		{"invalid timestamp", 200, `{"ok":true,"channel":"C123","ts":"not-a-ts"}`, "", Unknown, "receipt_mismatch", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/api/chat.postMessage" {
					t.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("missing Bearer header")
				}
				if r.Header.Get("Idempotency-Key") != "" {
					t.Error("transport idempotency header must not enable POST replay")
				}
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if strings.Contains(string(data), "fixture-token") {
					t.Error("token leaked into message payload")
				}
				var body map[string]any
				if err := json.Unmarshal(data, &body); err != nil {
					t.Error(err)
				}
				if body["channel"] != "C123" || body["thread_ts"] != "1700000000.000100" || body["text"] != "reminder" || body["client_msg_id"] != ClientMessageID("op") || body["reply_broadcast"] != false {
					t.Errorf("wrong trusted target or payload: %+v", body)
				}
				if test.retry != "" {
					w.Header().Set("Retry-After", test.retry)
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			client, err := NewClient(server.URL+"/api", nil)
			if err != nil {
				t.Fatal(err)
			}
			got := client.ChatPostMessage(context.Background(), "fixture-token", ReplyTarget{"C123", "1700000000.000100"}, "reminder", "op")
			if got.State != test.state || got.ErrorCode != test.code || got.RetryAfter != test.after || got.HTTPStatus != test.status {
				t.Fatalf("got %+v", got)
			}
			if calls.Load() != 1 {
				t.Fatalf("client retried %d times", calls.Load())
			}
			if test.state == Confirmed && (got.Receipt == nil || got.Receipt.Channel != "C123" || got.Receipt.TS != "1700000000.000101") {
				t.Fatalf("missing actual receipt: %+v", got)
			}
		})
	}
}

func TestChatPostMessageDoesNotRetryLostResponseOnReusedConnection(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	var warmPeer, postPeer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/warm" {
			mu.Lock()
			warmPeer = r.RemoteAddr
			mu.Unlock()
			_, _ = io.WriteString(w, "warm")
			return
		}
		calls.Add(1)
		mu.Lock()
		postPeer = r.RemoteAddr
		mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	}))
	defer server.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport}
	warm, err := httpClient.Get(server.URL + "/warm")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, warm.Body)
	_ = warm.Body.Close()
	client, err := NewClient(server.URL, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	result := client.ChatPostMessage(context.Background(), "fixture-token", ReplyTarget{ChannelID: "C123", ThreadTS: "1700000000.000100"}, "text", "operation")
	if result.State != Unknown || calls.Load() != 1 {
		t.Fatalf("lost response was retried or misreported: %+v requests=%d", result, calls.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if warmPeer == "" || warmPeer != postPeer {
		t.Fatalf("test did not exercise a reused TCP connection: %s %s", warmPeer, postPeer)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failedBody struct{}

func (failedBody) Read([]byte) (int, error) { return 0, errors.New("private-sensitive-body-error") }
func (failedBody) Close() error             { return nil }

func TestChatPostMessageReadErrorsAndRedirectAreUnknown(t *testing.T) {
	for _, transport := range []roundTripFunc{
		func(*http.Request) (*http.Response, error) {
			return nil, errors.New("private-sensitive-transport-error")
		},
		func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: failedBody{}}, nil
		},
	} {
		client, err := NewClient("http://fixture.invalid/api", &http.Client{Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		result := client.ChatPostMessage(context.Background(), "private-token", ReplyTarget{ChannelID: "C123"}, "text", "operation")
		if result.State != Unknown || strings.Contains(result.ErrorCode, "private") {
			t.Fatalf("unsafe unknown handling: %+v", result)
		}
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	client, err := NewClient(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := client.ChatPostMessage(context.Background(), "private-token", ReplyTarget{ChannelID: "C123"}, "text", "operation")
	if result.State != Unknown || result.ErrorCode != "http_redirect" || redirected.Load() != 0 {
		t.Fatalf("redirect escaped trusted target: %+v %d", result, redirected.Load())
	}
}

func TestClientInputAndStableMessageIdentity(t *testing.T) {
	client, err := NewClient("", nil)
	if err != nil || client.baseURL != "https://slack.com/api" {
		t.Fatalf("default Slack API: %v", err)
	}
	for _, url := range []string{"https://user:secret@slack.com/api", "ftp://slack.com/api", "https://slack.com/api?token=secret"} {
		if _, err := NewClient(url, nil); err == nil {
			t.Fatalf("invalid API config accepted: %s", url)
		}
	}
	for _, target := range []ReplyTarget{{}, {ChannelID: "C123", ThreadTS: "1e6"}, {ChannelID: "C123\n"}} {
		if result := client.ChatPostMessage(context.Background(), "fixture-token", target, "text", "op"); result.State != Rejected {
			t.Fatalf("invalid target dispatched: %+v", result)
		}
	}
	if result := client.ChatPostMessage(context.Background(), "", ReplyTarget{ChannelID: "C123"}, "text", "op"); result.State != Rejected {
		t.Fatal("empty token dispatched")
	}
	if ClientMessageID("op") == ClientMessageID("other") || ClientMessageID("op") != ClientMessageID("op") {
		t.Fatal("unstable operation correlation ID")
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(ClientMessageID("op")) {
		t.Fatal("invalid UUID-shaped client_msg_id")
	}
	if retryAfter("-1") != 0 || retryAfter("9223372036854775807") != 0 || retryAfter("not-seconds") != 0 {
		t.Fatal("invalid Retry-After accepted")
	}
}
