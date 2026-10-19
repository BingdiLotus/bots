package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bingdilotus/bots/internal/domain"
)

func signedSlackRequest(mux http.Handler, body, secret string, at time.Time) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/channels/slack/tenant/bot/events", strings.NewReader(body))
	timestamp := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + timestamp + ":" + body))
	r.Header.Set("X-Slack-Request-Timestamp", timestamp)
	r.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func slackBody(event string) string {
	return `{"type":"event_callback","team_id":"T1","event_id":"Ev1","event":` + event + `}`
}

func TestSlackChallengeRequiresRawSignatureAndFreshTimestamp(t *testing.T) {
	a, s, mux := newTestApp(t)
	body := `{"type":"url_verification","challenge":"proof","token":"legacy-verification-token"}`
	for _, entry := range []struct {
		secret string
		at     time.Time
		want   int
	}{
		{"wrong", a.Now(), 401},
		{"slack-signing", a.Now().Add(-6 * time.Minute), 401},
		{"slack-signing", a.Now(), 200},
	} {
		w := signedSlackRequest(mux, body, entry.secret, entry.at)
		if w.Code != entry.want {
			t.Fatalf("challenge authentication: %d %s", w.Code, w.Body.String())
		}
		if entry.want == 200 && !strings.Contains(w.Body.String(), `"challenge":"proof"`) {
			t.Fatal("authenticated Slack challenge missing")
		}
		if entry.want != 200 && strings.Contains(w.Body.String(), "proof") {
			t.Fatal("unauthenticated challenge was echoed")
		}
	}
	if len(s.inboxes) != 0 || len(s.invalidations) != 0 {
		t.Fatal("challenge generated model input or mutation")
	}
	t.Setenv("HTTP_TEST_SLACK_SIGNING", "rotated-slack-signing")
	if w := signedSlackRequest(mux, body, "slack-signing", a.Now()); w.Code != 401 {
		t.Fatal("old signing secret accepted after rotation")
	}
	if w := signedSlackRequest(mux, body, "rotated-slack-signing", a.Now()); w.Code != 200 {
		t.Fatalf("dynamic signing secret not used: %d", w.Code)
	}
}

func TestSlackMentionAndSubscribedReplyBecomeDurableInboxInputs(t *testing.T) {
	a, s, mux := newTestApp(t)
	mention := slackBody(`{"type":"app_mention","user":"HUMAN","channel":"C1","text":"<@U_BOT> remember this","ts":"1800000000.000001","event_ts":"1800000000.000002"}`)
	w := signedSlackRequest(mux, mention, "slack-signing", a.Now())
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"accepted":true`) || len(s.inboxes) != 1 {
		t.Fatalf("mention admission: %d %s", w.Code, w.Body.String())
	}
	in := s.inboxes[0]
	if in.TenantID != "tenant" || in.BotID != "bot" || in.Namespace != "slack" || in.SourceID != domain.StableID("slack_message_", "install", "C1", "1800000000.000001") || in.Input != "<@U_BOT> remember this" || in.ScopeID != domain.KnowledgeScope("tenant", "bot", "install", "C1") || in.Target.Provider != "slack" || in.Target.InstallationID != "install" || in.Target.ChannelID != "C1" || in.Target.ThreadID != "1800000000.000001" || in.Target.MessageID != "1800000000.000001" {
		t.Fatalf("source lost authoritative route: %+v", in)
	}
	reply := slackBody(`{"type":"message","user":"HUMAN","channel":"C1","text":"follow up","ts":"1800000001.000001","thread_ts":"1800000000.000001"}`)
	w = signedSlackRequest(mux, reply, "slack-signing", a.Now())
	if w.Code != 200 || len(s.inboxes) != 1 || !strings.Contains(w.Body.String(), `"ignored":true`) {
		t.Fatal("ambient thread reply admitted before durable subscription")
	}
	if len(s.conversationKeys) != 1 || s.conversationKeys[0] != domain.SessionKey("tenant", "bot", in.Target) {
		t.Fatalf("wrong conversation identity: %v", s.conversationKeys)
	}
	s.subscribed = true
	w = signedSlackRequest(mux, reply, "slack-signing", a.Now())
	if w.Code != 200 || len(s.inboxes) != 2 || s.inboxes[1].Target.ThreadID != in.Target.ThreadID || s.inboxes[1].Target.MessageID == in.Target.MessageID {
		t.Fatalf("subscribed reply failed: %d %+v", w.Code, s.inboxes)
	}
}

func TestSlackMentionAndMessageDeliveriesShareCanonicalSource(t *testing.T) {
	a, s, mux := newTestApp(t)
	s.subscribed = true
	for _, kind := range []string{"app_mention", "message"} {
		body := `{"type":"event_callback","team_id":"T1","event_id":"delivery_` + kind + `","event":{"type":"` + kind + `","user":"HUMAN","channel":"C1","text":"<@U_BOT> one command","ts":"1800000001.000001","thread_ts":"1800000000.000001"}}`
		w := signedSlackRequest(mux, body, "slack-signing", a.Now())
		if w.Code != 200 {
			t.Fatalf("%s event: %d %s", kind, w.Code, w.Body.String())
		}
	}
	if len(s.inboxes) != 2 {
		t.Fatalf("both delivery candidates should reach the atomic deduper: %+v", s.inboxes)
	}
	mention, message := s.inboxes[0], s.inboxes[1]
	want := domain.StableID("slack_message_", "install", "C1", "1800000001.000001")
	if mention.SourceID != want || message.SourceID != want || mention.Namespace != "slack" || message.Namespace != "slack" || mention.Input != message.Input || mention.Target != message.Target {
		t.Fatalf("two Slack delivery IDs must have the same logical command payload: %+v %+v", mention, message)
	}
}

func TestSlackFiltersForeignInstallationsChannelsAndBotLoops(t *testing.T) {
	a, s, mux := newTestApp(t)
	for _, entry := range []struct {
		body   string
		status int
	}{
		{`{"type":"event_callback","team_id":"OTHER","event_id":"Ev1","event":{"type":"app_mention","user":"HUMAN","channel":"C1","text":"x","ts":"1800000000.000001"}}`, 403},
		{slackBody(`{"type":"app_mention","user":"HUMAN","channel":"OTHER","text":"x","ts":"1800000000.000001"}`), 200},
		{slackBody(`{"type":"app_mention","user":"U_BOT","channel":"C1","text":"x","ts":"1800000000.000001"}`), 200},
		{slackBody(`{"type":"app_mention","user":"OTHER_BOT","bot_id":"B1","channel":"C1","text":"x","ts":"1800000000.000001"}`), 200},
		{slackBody(`{"type":"message","user":"HUMAN","channel":"C1","text":"ambient","ts":"1800000000.000001"}`), 200},
	} {
		w := signedSlackRequest(mux, entry.body, "slack-signing", a.Now())
		if w.Code != entry.status || len(s.inboxes) != 0 {
			t.Fatalf("unexpected admission: %d %s", w.Code, w.Body.String())
		}
	}
	if len(s.conversationKeys) != 0 {
		t.Fatal("unrelated messages should not query subscribed threads")
	}
}

func TestSlackEditsAndDeletionsInvalidateSourceInsteadOfCreatingRuns(t *testing.T) {
	a, s, mux := newTestApp(t)
	// Source revocation must still be observed while the Bot is paused.
	s.bot.Enabled = false
	for _, entry := range []struct{ event, kind string }{
		{`{"type":"message","subtype":"message_changed","hidden":true,"channel":"C1","ts":"1800000100.000001","message":{"user":"HUMAN","text":"edited","ts":"1800000000.000001","edited":{"ts":"1800000100.000001"}}}`, "edited"},
		{`{"type":"message","subtype":"message_deleted","hidden":true,"channel":"C1","deleted_ts":"1800000000.000001"}`, "deleted"},
	} {
		w := signedSlackRequest(mux, slackBody(entry.event), "slack-signing", a.Now())
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"invalidated":true`) || len(s.inboxes) != 0 {
			t.Fatalf("source event created a new command: %d %s", w.Code, w.Body.String())
		}
		got := s.invalidations[len(s.invalidations)-1]
		if !reflect.DeepEqual(got, []string{"tenant", "bot", "install", "C1", "1800000000.000001", entry.kind}) {
			t.Fatalf("wrong source identity invalidated: %v", got)
		}
	}
}

func TestSlackRequiresOneBoundedSignedEnvelope(t *testing.T) {
	a, s, mux := newTestApp(t)
	valid := `{"type":"url_verification","challenge":"proof"}`
	for _, body := range []string{valid + ` {}`, `null`, `[]`} {
		w := signedSlackRequest(mux, body, "slack-signing", a.Now())
		if w.Code != 400 {
			t.Fatalf("invalid signed envelope accepted: %s -> %d", body, w.Code)
		}
	}
	oversized, _ := json.Marshal(map[string]string{"type": "url_verification", "challenge": strings.Repeat("x", 1<<20)})
	w := signedSlackRequest(mux, string(oversized), "slack-signing", a.Now())
	if w.Code != 413 || len(s.inboxes) != 0 {
		t.Fatal("oversized Slack payload was processed")
	}
}
