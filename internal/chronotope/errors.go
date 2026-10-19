package chronotope

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type ErrorKind string

const (
	ErrorSessionBusy         ErrorKind = "session_busy"
	ErrorIdempotencyConflict ErrorKind = "idempotency_conflict"
	ErrorAuth                ErrorKind = "auth"
	ErrorBadPayload          ErrorKind = "bad_payload"
	ErrorRateLimited         ErrorKind = "rate_limited"
	ErrorNotFound            ErrorKind = "not_found"
	ErrorConflict            ErrorKind = "conflict"
	ErrorUnavailable         ErrorKind = "unavailable"
	ErrorBadResponse         ErrorKind = "bad_response"
)

// APIError preserves the runtime's HTTP and structured error facts. It does not
// decide whether an outbox operation should be retried or change its identity.
type APIError struct {
	StatusCode  int
	Code        int
	ErrorCode   string
	Kind        ErrorKind
	Message     string
	ActiveRunID string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string {
	if e.StatusCode == 0 {
		return fmt.Sprintf("chronotope %s: %s", e.Kind, e.Message)
	}
	return fmt.Sprintf("chronotope HTTP %d (%s): %s", e.StatusCode, e.Kind, e.Message)
}

func classifyStatus(status int, code string) ErrorKind {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrorAuth
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return ErrorBadPayload
	case http.StatusNotFound:
		return ErrorNotFound
	case http.StatusConflict:
		switch code {
		case "session_busy":
			return ErrorSessionBusy
		case "idempotency_conflict":
			return ErrorIdempotencyConflict
		default:
			return ErrorConflict
		}
	case http.StatusTooManyRequests:
		return ErrorRateLimited
	}
	if status >= 500 {
		return ErrorUnavailable
	}
	return ErrorBadResponse
}

func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		const maxSeconds = int64((1<<63 - 1) / time.Second)
		if seconds > maxSeconds {
			return time.Duration(1<<63 - 1)
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

func badPayload(message string) error {
	return &APIError{Kind: ErrorBadPayload, Message: message}
}

func badResponse(status int, message string) error {
	return &APIError{StatusCode: status, Kind: ErrorBadResponse, Message: message}
}
