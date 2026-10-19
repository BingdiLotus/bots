package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func signed(secret string, body []byte, seconds int64) http.Header {
	timestamp := strconv.FormatInt(seconds, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + timestamp + ":"))
	_, _ = mac.Write(body)
	h := http.Header{}
	h.Set("X-Slack-Request-Timestamp", timestamp)
	h.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
	return h
}

func TestVerifyRequestRawBodyAndReplayWindow(t *testing.T) {
	now := time.Unix(1792386000, 0)
	body := []byte(`{"type":"url_verification", "challenge":"test"}`)
	for _, offset := range []int64{-300, 0, 300} {
		if err := VerifyRequest("fixture-secret", signed("fixture-secret", body, now.Unix()+offset), body, now); err != nil {
			t.Fatalf("boundary %d: %v", offset, err)
		}
	}
	for _, offset := range []int64{-301, 301} {
		if err := VerifyRequest("fixture-secret", signed("fixture-secret", body, now.Unix()+offset), body, now); !errors.Is(err, ErrStaleRequest) {
			t.Fatalf("stale %d: %v", offset, err)
		}
	}
	if err := VerifyRequest("fixture-secret", signed("fixture-secret", body, now.Unix()-300), body, now.Add(time.Nanosecond)); !errors.Is(err, ErrStaleRequest) {
		t.Fatalf("older than exactly five minutes accepted: %v", err)
	}
	h := signed("fixture-secret", body, now.Unix())
	if err := VerifyRequest("fixture-secret", h, append(body, ' '), now); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("modified raw bytes accepted: %v", err)
	}
	if err := VerifyRequest("different", h, body, now); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("wrong secret accepted: %v", err)
	}
	for _, value := range []string{"", "v1=bad", "v0=zz", "v0=0000000000000000000000000000000000000000000000000000000000000000"} {
		bad := h.Clone()
		bad.Set("X-Slack-Signature", value)
		if err := VerifyRequest("fixture-secret", bad, body, now); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("bad signature %q: %v", value, err)
		}
	}
	for _, value := range []string{"", "not-an-integer", "9223372036854775808", "-1"} {
		bad := h.Clone()
		bad.Set("X-Slack-Request-Timestamp", value)
		if err := VerifyRequest("fixture-secret", bad, body, now); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("bad timestamp %q: %v", value, err)
		}
	}
	if err := VerifyRequest("", h, body, now); !errors.Is(err, ErrInvalidSignature) {
		t.Fatal("empty signing secret accepted")
	}
}
