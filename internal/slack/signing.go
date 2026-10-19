// Package slack adapts Slack wire protocols. It owns no Bot knowledge, tasks,
// inbox, delivery ledger or channel participation policy.
package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"
)

var ErrInvalidSignature = errors.New("invalid Slack request signature")
var ErrStaleRequest = errors.New("Slack request timestamp outside five-minute window")

// VerifyRequest authenticates the untouched request bytes, before JSON parsing.
// https://docs.slack.dev/authentication/verifying-requests-from-slack/
func VerifyRequest(secret string, headers http.Header, rawBody []byte, now time.Time) error {
	if secret == "" {
		return ErrInvalidSignature
	}
	timestamp := headers.Get("X-Slack-Request-Timestamp")
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || seconds <= 0 {
		return ErrInvalidSignature
	}
	// Avoid duration subtraction overflow for attacker-controlled epoch values.
	current := now.Unix()
	if seconds < current-300 || seconds > current+300 {
		return ErrStaleRequest
	}
	sentAt := time.Unix(seconds, 0)
	if now.Before(sentAt.Add(-5*time.Minute)) || now.After(sentAt.Add(5*time.Minute)) {
		return ErrStaleRequest
	}
	signature := headers.Get("X-Slack-Signature")
	if len(signature) != 67 || signature[:3] != "v0=" {
		return ErrInvalidSignature
	}
	provided, err := hex.DecodeString(signature[3:])
	if err != nil {
		return ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + timestamp + ":"))
	_, _ = mac.Write(rawBody)
	if !hmac.Equal(mac.Sum(nil), provided) {
		return ErrInvalidSignature
	}
	return nil
}
