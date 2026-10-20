package slack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type SendState string

const (
	Confirmed SendState = "confirmed"
	Rejected  SendState = "rejected"
	Retryable SendState = "retryable"
	Unknown   SendState = "unknown"
)

type Receipt struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

type SendResult struct {
	State      SendState
	Receipt    *Receipt
	ErrorCode  string
	RetryAfter time.Duration
	HTTPStatus int
}

type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient uses the Slack API unless a deployment/test base is supplied. The
// destination is constructor configuration, never a model or message argument.
func NewClient(baseURL string, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		baseURL = "https://slack.com/api"
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid configured Slack API base URL")
	}
	client := http.Client{Timeout: 10 * time.Second}
	if httpClient != nil {
		client = *httpClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &client}, nil
}

// ClientMessageID is a stable UUID-shaped correlation ID. Slack's handling of
// client_msg_id is not an exactly-once contract and does not replace the ledger.
func ClientMessageID(operationID string) string {
	d := sha256.Sum256([]byte("bingdi-bots/slack/chat.postMessage/v1\x00" + operationID))
	d[6] = (d[6] & 0x0f) | 0x50
	d[8] = (d[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", d[0:4], d[4:6], d[6:8], d[8:10], d[10:16])
}

// ChatPostMessage performs exactly one transport attempt. The caller owns the
// trusted target, credential resolution, claim, receipt transaction and retry
// policy. Unknown outcomes must never be retried here under a new identity.
func (c *Client) ChatPostMessage(ctx context.Context, token string, target ReplyTarget, text, operationID string) SendResult {
	if c == nil || c.http == nil {
		return SendResult{State: Rejected, ErrorCode: "client_not_configured"}
	}
	if !target.validate() || text == "" || operationID == "" {
		return SendResult{State: Rejected, ErrorCode: "invalid_send_input"}
	}
	if token == "" || strings.ContainsFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return SendResult{State: Rejected, ErrorCode: "invalid_token"}
	}
	body := struct {
		Channel         string `json:"channel"`
		ThreadTS        string `json:"thread_ts,omitempty"`
		Text            string `json:"text"`
		ClientMessageID string `json:"client_msg_id"`
		ReplyBroadcast  bool   `json:"reply_broadcast"`
		UnfurlLinks     bool   `json:"unfurl_links"`
		UnfurlMedia     bool   `json:"unfurl_media"`
	}{Channel: target.ChannelID, ThreadTS: target.ThreadTS, Text: text, ClientMessageID: ClientMessageID(operationID)}
	payload, _ := json.Marshal(body)
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.baseURL+"/chat.postMessage", bytes.NewReader(payload))
	if err != nil {
		return SendResult{State: Rejected, ErrorCode: "invalid_send_request"}
	}
	// No Idempotency-Key header and no GetBody replay function: disable hidden
	// net/http POST replay even when the underlying connection was reused.
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	response, err := c.http.Do(req)
	if err != nil {
		return SendResult{State: Unknown, ErrorCode: "transport_error"}
	}
	defer response.Body.Close()
	result := SendResult{State: Unknown, HTTPStatus: response.StatusCode}
	if response.StatusCode == http.StatusTooManyRequests {
		result.State, result.ErrorCode, result.RetryAfter = Retryable, "rate_limited", retryAfter(response.Header.Get("Retry-After"))
		return result
	}
	if response.StatusCode >= 500 {
		result.ErrorCode = "provider_5xx"
		return result
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		result.ErrorCode = "http_redirect"
		return result
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		result.ErrorCode = "response_read_error"
		return result
	}
	var reply struct {
		OK      *bool  `json:"ok"`
		Error   string `json:"error"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if err := json.Unmarshal(data, &reply); err != nil || reply.OK == nil {
		result.ErrorCode = "invalid_response"
		return result
	}
	if *reply.OK {
		if reply.Channel != "" && reply.TS != "" {
			result.Receipt = &Receipt{Channel: reply.Channel, TS: reply.TS}
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 && reply.Channel == target.ChannelID && validTimestamp(reply.TS) {
			result.State = Confirmed
			return result
		}
		result.ErrorCode = "receipt_mismatch"
		return result
	}
	if reply.Error == "ratelimited" || reply.Error == "rate_limited" {
		result.State, result.ErrorCode, result.RetryAfter = Retryable, reply.Error, retryAfter(response.Header.Get("Retry-After"))
		return result
	}
	if rejectedError(reply.Error) {
		result.State, result.ErrorCode = Rejected, reply.Error
		return result
	}
	// Unrecognized/internal errors do not prove the message was not accepted.
	result.ErrorCode = "unconfirmed_provider_error"
	switch reply.Error {
	case "internal_error", "fatal_error", "request_timeout", "service_unavailable", "duplicate_message":
		result.ErrorCode = reply.Error
	}
	return result
}

func retryAfter(value string) time.Duration {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds < 0 || seconds > int64((1<<63-1)/time.Second) {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func rejectedError(code string) bool {
	switch code {
	case "not_authed", "invalid_auth", "account_inactive", "token_revoked", "missing_scope", "no_permission", "not_allowed_token_type",
		"channel_not_found", "not_in_channel", "is_archived", "restricted_action", "restricted_action_read_only_channel", "restricted_action_thread_only_channel",
		"ekm_access_denied", "access_denied", "team_access_not_granted", "org_login_required", "enterprise_is_restricted",
		"no_text", "invalid_arguments", "invalid_arg_name", "invalid_array_arg", "invalid_blocks", "msg_too_long", "too_many_attachments",
		"invalid_metadata", "invalid_metadata_schema", "invalid_thread_ts", "thread_not_found", "cannot_reply_to_message", "method_not_supported_for_channel_type":
		return true
	default:
		return false
	}
}
