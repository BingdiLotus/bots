package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

type fakeSocketTransport struct {
	incoming chan socketmode.Event
	acks     chan string
	started  chan struct{}
	stopped  chan struct{}
	runError error
}

func newFakeSocketTransport() *fakeSocketTransport {
	return &fakeSocketTransport{
		incoming: make(chan socketmode.Event, 16),
		acks:     make(chan string, 16),
		started:  make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

func (f *fakeSocketTransport) RunContext(ctx context.Context) error {
	close(f.started)
	defer close(f.stopped)
	if f.runError != nil {
		return f.runError
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeSocketTransport) AckCtx(ctx context.Context, id string, payload any) error {
	if payload != nil {
		return errors.New("unexpected response payload")
	}
	select {
	case f.acks <- id:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeSocketTransport) events() <-chan socketmode.Event { return f.incoming }

func socketTestReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("Socket Mode operation did not finish")
		var zero T
		return zero
	}
}

func socketTestEvent(id string, body json.RawMessage) socketmode.Event {
	return socketmode.Event{Type: socketmode.EventTypeEventsAPI, Request: &socketmode.Request{
		Type: socketmode.RequestTypeEventsAPI, EnvelopeID: id, Payload: body,
	}}
}

func TestSocketRunAcknowledgesOnlyAfterAcceptanceAndPreservesPayload(t *testing.T) {
	transport := newFakeSocketTransport()
	client := &SocketClient{transport: transport}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := json.RawMessage(`{ "type":"event_callback", "event_id":"Ev1", "team_id":"T1", "event":{"type":"app_mention", "text":"你好\n<@U1>"}, "future": [1,  2] }`)
	transport.incoming <- socketTestEvent("envelope-1", body)
	entered, release := make(chan json.RawMessage, 1), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- client.Run(ctx, func(callCtx context.Context, raw json.RawMessage) error {
			entered <- raw
			select {
			case <-release:
				return nil
			case <-callCtx.Done():
				return callCtx.Err()
			}
		})
	}()
	if raw := socketTestReceive(t, entered); !bytes.Equal(raw, body) {
		t.Fatalf("callback did not receive original payload: %q", raw)
	}
	select {
	case id := <-transport.acks:
		t.Fatalf("ACK %s preceded durable acceptance", id)
	default:
	}
	close(release)
	if id := socketTestReceive(t, transport.acks); id != "envelope-1" {
		t.Fatalf("wrong ACK: %s", id)
	}
	cancel()
	if err := socketTestReceive(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
}

func TestSocketRunLeavesAcceptanceErrorsUnacknowledgedAndContinues(t *testing.T) {
	transport := newFakeSocketTransport()
	client := &SocketClient{transport: transport}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport.incoming <- socketmode.Event{Type: socketmode.EventTypeConnectionError, Data: &slackapi.ConnectionErrorEvent{
		ErrorObj: errors.New("temporary connection failure at wss://private.example?ticket=secret"),
	}}
	transport.incoming <- socketTestEvent("retry-me", json.RawMessage(`{"type":"event_callback","event_id":"first"}`))
	transport.incoming <- socketTestEvent("accepted", json.RawMessage(`{"type":"event_callback","event_id":"second"}`))
	var calls atomic.Int32
	result := make(chan error, 1)
	go func() {
		result <- client.Run(ctx, func(context.Context, json.RawMessage) error {
			if calls.Add(1) == 1 {
				return errors.New("temporary persistence failure with private details")
			}
			return nil
		})
	}()
	if id := socketTestReceive(t, transport.acks); id != "accepted" {
		t.Fatalf("failed callback received ACK: %s", id)
	}
	cancel()
	_ = socketTestReceive(t, result)
	if calls.Load() != 2 {
		t.Fatalf("consumer stopped after transient acceptance error: %d calls", calls.Load())
	}
	select {
	case id := <-transport.acks:
		t.Fatalf("unexpected extra ACK: %s", id)
	default:
	}
}

func TestSocketRunLeavesExpiredAcceptanceUnacknowledgedAndContinues(t *testing.T) {
	transport := newFakeSocketTransport()
	client := &SocketClient{transport: transport}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport.incoming <- socketTestEvent("expired", json.RawMessage(`{"type":"event_callback","event_id":"first"}`))
	transport.incoming <- socketTestEvent("accepted", json.RawMessage(`{"type":"event_callback","event_id":"second"}`))
	var calls atomic.Int32
	expired := make(chan error, 1)
	result := make(chan error, 1)
	go func() {
		result <- client.Run(ctx, func(callCtx context.Context, _ json.RawMessage) error {
			if calls.Add(1) == 1 {
				<-callCtx.Done()
				expired <- callCtx.Err()
				// A late nil return must not turn expired admission into an ACK.
				return nil
			}
			return nil
		})
	}()
	select {
	case err := <-expired:
		if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			t.Fatalf("expected callback deadline with live parent, got child=%v parent=%v", err, ctx.Err())
		}
	case <-time.After(4 * time.Second):
		t.Fatal("acceptance did not receive its two-second deadline")
	}
	if id := socketTestReceive(t, transport.acks); id != "accepted" {
		t.Fatalf("expired callback received ACK: %s", id)
	}
	cancel()
	_ = socketTestReceive(t, result)
	if calls.Load() != 2 {
		t.Fatalf("consumer stopped after callback deadline: %d calls", calls.Load())
	}
	select {
	case id := <-transport.acks:
		t.Fatalf("unexpected extra ACK: %s", id)
	default:
	}
}

func TestSocketFirstConnectedClosesOnceWithoutBusinessCallback(t *testing.T) {
	transport := newFakeSocketTransport()
	client, err := NewSocketClient("fixture-app-token")
	if err != nil {
		t.Fatal(err)
	}
	client.transport = transport
	connected := client.FirstConnected()
	if connected == nil {
		t.Fatal("constructor did not initialize first-connection channel")
	}
	select {
	case <-connected:
		t.Fatal("first connection reported before SDK connected event")
	default:
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	result := make(chan error, 1)
	go func() {
		result <- client.Run(ctx, func(context.Context, json.RawMessage) error {
			calls.Add(1)
			return nil
		})
	}()
	transport.incoming <- socketmode.Event{Type: socketmode.EventTypeConnected}
	socketTestReceive(t, connected)
	// Reconnection must neither close the channel twice nor reset its history.
	transport.incoming <- socketmode.Event{Type: socketmode.EventTypeConnectionError, Data: &slackapi.ConnectionErrorEvent{ErrorObj: errors.New("temporary outage")}}
	transport.incoming <- socketmode.Event{Type: socketmode.EventTypeConnected}
	transport.incoming <- socketmode.Event{Type: socketmode.EventTypeInteractive, Request: &socketmode.Request{Type: "interactive", EnvelopeID: "barrier"}}
	if id := socketTestReceive(t, transport.acks); id != "barrier" {
		t.Fatalf("wrong barrier ACK: %s", id)
	}
	if client.FirstConnected() != connected {
		t.Fatal("first-connection channel changed after reconnect")
	}
	cancel()
	_ = socketTestReceive(t, result)
	if calls.Load() != 0 {
		t.Fatalf("lifecycle events produced %d business callbacks", calls.Load())
	}
}

func TestSocketRunIgnoresNonEventRequestsAndAcknowledgesTheirEnvelopes(t *testing.T) {
	transport := newFakeSocketTransport()
	client := &SocketClient{transport: transport}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport.incoming <- socketmode.Event{Type: socketmode.EventTypeHello, Request: &socketmode.Request{Type: "hello"}}
	for _, kind := range []string{"interactive", "slash_commands", "future_request"} {
		transport.incoming <- socketmode.Event{Type: socketmode.EventType(kind), Request: &socketmode.Request{
			Type: kind, EnvelopeID: kind, Payload: json.RawMessage(`{"type":"event_callback","text":"never admit this"}`),
		}}
	}
	transport.incoming <- socketTestEvent("rate-limit", json.RawMessage(`{"type":"app_rate_limited","team_id":"T1"}`))
	transport.incoming <- socketTestEvent("verification", json.RawMessage(`{"type":"url_verification","challenge":"ignored"}`))
	// The SDK emits unknown request types via its bad-message event, retaining
	// the original WebSocket bytes. They still need an ACK, without business work.
	transport.incoming <- socketmode.Event{Type: socketmode.EventTypeErrorBadMessage, Data: &socketmode.ErrorBadMessage{
		Cause: errors.New("unsupported type"), Message: json.RawMessage(`{"type":"future_request","envelope_id":"future-raw","payload":[1,2]}`),
	}}
	var calls atomic.Int32
	result := make(chan error, 1)
	go func() {
		result <- client.Run(ctx, func(context.Context, json.RawMessage) error {
			calls.Add(1)
			return nil
		})
	}()
	want := []string{"interactive", "slash_commands", "future_request", "rate-limit", "verification", "future-raw"}
	for _, id := range want {
		if got := socketTestReceive(t, transport.acks); got != id {
			t.Fatalf("ACK = %q, want %q", got, id)
		}
	}
	cancel()
	_ = socketTestReceive(t, result)
	if calls.Load() != 0 {
		t.Fatalf("non-event requests produced %d business callbacks", calls.Load())
	}
}

func TestSocketRunUsesOriginalEnvelopeWhenSDKCannotParseInnerEvent(t *testing.T) {
	transport := newFakeSocketTransport()
	client := &SocketClient{transport: transport}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := json.RawMessage(`{"type":"event_callback", "team_id":"T1", "event_id":"unknown-event", "event":{"type":"future_inner_event","channel":"channel"}}`)
	wire := append([]byte(`{"type":"events_api","envelope_id":"unknown-inner","payload":`), body...)
	wire = append(wire, '}')
	transport.incoming <- socketmode.Event{Type: socketmode.EventTypeErrorBadMessage, Data: &socketmode.ErrorBadMessage{
		Cause: errors.New("inner Event does not exist"), Message: wire,
	}}
	var modeled atomic.Bool
	result := make(chan error, 1)
	go func() {
		result <- client.Run(ctx, func(_ context.Context, raw json.RawMessage) error {
			if !bytes.Equal(raw, body) {
				return errors.New("payload bytes changed")
			}
			envelope, err := ParseEnvelope(raw)
			if err != nil {
				return err
			}
			_, accepted, err := Normalize(Binding{TenantID: "tenant", BotID: "bot", AccountID: "app", TeamID: "T1", ChannelID: "channel", BotUserID: "bot-user"}, envelope)
			modeled.Store(accepted)
			return err
		})
	}()
	if id := socketTestReceive(t, transport.acks); id != "unknown-inner" {
		t.Fatalf("wrong ACK: %s", id)
	}
	cancel()
	_ = socketTestReceive(t, result)
	if modeled.Load() {
		t.Fatal("unknown inner event became model input")
	}
}

func TestSocketRunCancellationStopsManagedTransport(t *testing.T) {
	transport := newFakeSocketTransport()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- (&SocketClient{transport: transport}).Run(ctx, func(context.Context, json.RawMessage) error { return nil })
	}()
	socketTestReceive(t, transport.started)
	cancel()
	if err := socketTestReceive(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
	select {
	case <-transport.stopped:
	default:
		t.Fatal("Run returned before stopping its transport")
	}
}

func TestSocketRunCancellationDuringAcceptanceDoesNotAcknowledge(t *testing.T) {
	transport := newFakeSocketTransport()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport.incoming <- socketTestEvent("unfinished", json.RawMessage(`{"type":"event_callback"}`))
	entered, result := make(chan struct{}), make(chan error, 1)
	go func() {
		result <- (&SocketClient{transport: transport}).Run(ctx, func(callCtx context.Context, _ json.RawMessage) error {
			close(entered)
			<-callCtx.Done()
			return callCtx.Err()
		})
	}()
	socketTestReceive(t, entered)
	cancel()
	if err := socketTestReceive(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
	select {
	case id := <-transport.acks:
		t.Fatalf("canceled acceptance received ACK: %s", id)
	default:
	}
}

func TestSocketRunStopsOnAuthenticationFailureAndSanitizesErrors(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		event *socketmode.Event
		want  error
	}{
		{name: "invalid auth", err: slackapi.SlackErrorResponse{Err: "invalid_auth"}, want: ErrSocketAuthentication},
		{name: "revoked app token", err: errors.New("token_revoked"), want: ErrSocketAuthentication},
		{name: "HTTP rejection", err: slackapi.StatusCodeError{Code: http.StatusNotFound, Status: "private response"}, want: ErrSocketAuthentication},
		{name: "SDK auth event", event: &socketmode.Event{Type: socketmode.EventTypeInvalidAuth}, want: ErrSocketAuthentication},
		{name: "missing scope in retry event", event: &socketmode.Event{Type: socketmode.EventTypeConnectionError, Data: &slackapi.ConnectionErrorEvent{
			ErrorObj: fmt.Errorf("private outer data: %w", slackapi.SlackErrorResponse{Err: "missing_scope"}),
		}}, want: ErrSocketAuthentication},
		{name: "wrong token type", event: &socketmode.Event{Type: socketmode.EventTypeConnectionError, Data: &slackapi.ConnectionErrorEvent{
			ErrorObj: errors.New("not_allowed_token_type"),
		}}, want: ErrSocketAuthentication},
		{name: "transport URL", err: errors.New("dial wss://private.example/socket?ticket=credential: failed"), want: ErrSocketTransport},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := newFakeSocketTransport()
			transport.runError = test.err
			if test.event != nil {
				transport.incoming <- *test.event
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := (&SocketClient{transport: transport}).Run(ctx, func(context.Context, json.RawMessage) error { return nil })
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "credential") {
				t.Fatalf("unsafe or wrong error: %v, want %v", err, test.want)
			}
			select {
			case <-transport.stopped:
			default:
				t.Fatal("authentication failure did not stop SDK reconnect loop")
			}
		})
	}
}

func TestNewSocketClientRejectsInvalidConfigurationWithoutSecrets(t *testing.T) {
	for _, token := range []string{"", "secret token", "secret\ntoken", "secret\x00token"} {
		if _, err := NewSocketClient(token); !errors.Is(err, ErrSocketConfiguration) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid credentials returned unsafe or wrong error: %v", err)
		}
	}
	if err := (*SocketClient)(nil).Run(context.Background(), func(context.Context, json.RawMessage) error { return nil }); !errors.Is(err, ErrSocketConfiguration) {
		t.Fatalf("nil client returned %v", err)
	}
}
