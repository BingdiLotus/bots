package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

var (
	ErrSocketConfiguration  = errors.New("Slack Socket Mode is not configured")
	ErrSocketAuthentication = errors.New("Slack Socket Mode authentication was rejected")
	ErrSocketTransport      = errors.New("Slack Socket Mode transport stopped")
)

// SocketClient consumes authenticated Slack envelopes using the SDK's managed
// connection and reconnect loop. It never posts messages or invokes a model.
type SocketClient struct {
	transport      socketTransport
	firstConnected chan struct{}
	connectedOnce  sync.Once
}

type socketTransport interface {
	RunContext(context.Context) error
	AckCtx(context.Context, string, any) error
	events() <-chan socketmode.Event
}

type sdkSocketTransport struct{ *socketmode.Client }

func (t sdkSocketTransport) events() <-chan socketmode.Event { return t.Events }

// The SDK can include credential-bearing WebSocket URLs and raw payloads in
// diagnostic messages. Keep its logger disabled even on non-debug code paths.
type socketDiscardLogger struct{}

func (socketDiscardLogger) Output(int, string) error { return nil }

// NewSocketClient only needs the app token with connections:write. Bot tokens
// belong to outbound Web API calls and are deliberately not held by this client.
// The caller resolves its configured secret reference before construction.
func NewSocketClient(appToken string) (*SocketClient, error) {
	if appToken == "" || strings.ContainsFunc(appToken, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return nil, ErrSocketConfiguration
	}
	logger := socketDiscardLogger{}
	api := slackapi.New("",
		slackapi.OptionAppLevelToken(appToken),
		slackapi.OptionDebug(false),
		slackapi.OptionLog(logger),
		slackapi.OptionHTTPClient(&http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}),
	)
	client := socketmode.New(api, socketmode.OptionDebug(false), socketmode.OptionLog(logger))
	return &SocketClient{transport: sdkSocketTransport{client}, firstConnected: make(chan struct{})}, nil
}

// FirstConnected closes after the SDK first establishes a connection. It records
// historical success only; it does not describe current health or readiness.
func (c *SocketClient) FirstConnected() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.firstConnected
}

// Run passes the original event_callback JSON envelope to accept. A nil return
// means durable acceptance or a deliberate ignore, and only then is its Socket
// Mode envelope acknowledged. Errors leave it unacknowledged for Slack to retry;
// accept should record its own controlled error without logging payloads/secrets.
// The callback must honor its two-second ctx and must not wait for model execution
// or delivery. Expired acceptance is left unacknowledged even if it returns nil.
// Run must not be called concurrently on the same client.
func (c *SocketClient) Run(ctx context.Context, accept func(context.Context, json.RawMessage) error) error {
	if c == nil || c.transport == nil || accept == nil {
		return ErrSocketConfiguration
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		result <- c.transport.RunContext(runCtx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-result:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if socketAuthenticationError(err) {
				return ErrSocketAuthentication
			}
			// SDK errors can contain URLs or provider details. Do not wrap them.
			return ErrSocketTransport
		case event, ok := <-c.transport.events():
			if !ok {
				return ErrSocketTransport
			}
			if event.Type == socketmode.EventTypeConnected {
				if c.firstConnected != nil {
					c.connectedOnce.Do(func() { close(c.firstConnected) })
				}
				continue
			}
			if event.Type == socketmode.EventTypeInvalidAuth {
				return ErrSocketAuthentication
			}
			if event.Type == socketmode.EventTypeConnectionError {
				if failure, ok := event.Data.(*slackapi.ConnectionErrorEvent); ok && socketAuthenticationError(failure.ErrorObj) {
					return ErrSocketAuthentication
				}
				// Transient connection failures and backoff belong to the SDK.
				continue
			}
			request := socketEventRequest(event)
			if request == nil || request.EnvelopeID == "" {
				continue // hello and SDK lifecycle events do not require ACKs.
			}
			if request.Type == socketmode.RequestTypeEventsAPI {
				var envelope struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(request.Payload, &envelope) != nil {
					continue
				}
				if envelope.Type == "event_callback" {
					acceptCtx, cancelAccept := context.WithTimeout(runCtx, 2*time.Second)
					err := accept(acceptCtx, request.Payload)
					expired := acceptCtx.Err() != nil
					cancelAccept()
					if err != nil || expired {
						continue
					}
				}
			}
			// Interactive, slash-command and unknown requests are deliberately
			// ignored. Never give their payloads to the business callback.
			if runCtx.Err() == nil {
				// AckCtx enqueues a bare ACK; failed writes are reported by the SDK
				// and Slack may redeliver. Durable admission handles duplicates.
				_ = c.transport.AckCtx(runCtx, request.EnvelopeID, nil)
			}
		}
	}
}

// The SDK returns unsupported event types as ErrorBadMessage rather than a
// Request event. Recover only its original JSON envelope so unknown requests can
// be ignored/ACKed and event_callback still uses our existing normalization.
func socketEventRequest(event socketmode.Event) *socketmode.Request {
	if event.Request != nil {
		return event.Request
	}
	if event.Type != socketmode.EventTypeErrorBadMessage {
		return nil
	}
	bad, ok := event.Data.(*socketmode.ErrorBadMessage)
	if !ok {
		return nil
	}
	var request socketmode.Request
	if json.Unmarshal(bad.Message, &request) != nil || request.Type == "" {
		return nil
	}
	return &request
}

func socketAuthenticationError(err error) bool {
	if err == nil {
		return false
	}
	var status slackapi.StatusCodeError
	if errors.As(err, &status) && (status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden || status.Code == http.StatusNotFound) {
		return true
	}
	// Check fixed codes only: arbitrary provider/transport errors may include
	// secret URLs. A wrapped API response still exposes its structured code.
	var response slackapi.SlackErrorResponse
	code := err.Error()
	if errors.As(err, &response) {
		code = response.Err
	}
	switch code {
	case "invalid_auth", "not_authed", "account_inactive", "token_revoked", "token_expired", "missing_scope", "not_allowed_token_type",
		"access_denied", "no_permission", "app_not_installed", "invalid_app_id", "not_enabled", "app_not_enabled",
		"team_access_not_granted", "org_login_required", "enterprise_is_restricted":
		return true
	default:
		return false
	}
}
