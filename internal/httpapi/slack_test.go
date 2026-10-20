package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

func TestSlackSocketAndWebhookShareAdmissionSemantics(t *testing.T) {
	var stores []*appStore
	for _, transport := range []string{domain.SlackWebhook, domain.SlackSocket} {
		t.Run(transport, func(t *testing.T) {
			a, s, mux := newTestApp(t)
			s.bot.Config.EventTransport = transport
			if transport == domain.SlackSocket {
				s.bot.Config.SigningSecretEnv = ""
				s.bot.Config.AppTokenEnv = "HTTP_TEST_SLACK_APP_TOKEN"
			}
			accept := func(eventID, event string) {
				t.Helper()
				body := strings.Replace(slackBody(event), `"event_id":"Ev1"`, `"event_id":"`+eventID+`"`, 1)
				if transport == domain.SlackSocket {
					if err := a.AcceptSlackSocketEvent(context.Background(), "bot", json.RawMessage(body)); err != nil {
						t.Fatal(err)
					}
				} else if w := signedSlackRequest(mux, body, "slack-signing", a.Now()); w.Code != 200 {
					t.Fatalf("webhook event: %d %s", w.Code, w.Body.String())
				}
			}
			accept("mention", `{"type":"app_mention","user":"HUMAN","channel":"C1","text":"<@U_BOT> remember this","ts":"1800000000.000001"}`)
			reply := `{"type":"message","user":"HUMAN","channel":"C1","text":"follow up","ts":"1800000001.000001","thread_ts":"1800000000.000001"}`
			accept("unsubscribed", reply)
			if len(s.inboxes) != 1 {
				t.Fatal("unsubscribed reply became model input")
			}
			s.subscribed = true
			accept("reply", reply)
			accept("duplicate-mention", strings.Replace(reply, `"type":"message"`, `"type":"app_mention"`, 1))
			if len(s.inboxes) != 3 || s.inboxes[1].SourceID != s.inboxes[2].SourceID || s.inboxes[1].Target != s.inboxes[2].Target {
				t.Fatalf("mention and message deliveries diverged before atomic deduplication: %+v", s.inboxes)
			}
			// Revocations still reach durable storage while the Bot is paused.
			s.bot.Enabled = false
			s.bot.AccountEnabled = false
			accept("edit", `{"type":"message","subtype":"message_changed","hidden":true,"channel":"C1","message":{"user":"HUMAN","text":"edited","ts":"1800000000.000001"}}`)
			accept("delete", `{"type":"message","subtype":"message_deleted","hidden":true,"channel":"C1","deleted_ts":"1800000000.000001"}`)
			if len(s.inboxes) != 3 || len(s.invalidations) != 2 {
				t.Fatalf("source revocation bypassed invalidation: inbox=%+v invalidations=%v", s.inboxes, s.invalidations)
			}
			stores = append(stores, s)
		})
	}
	if len(stores) != 2 {
		t.Fatal("both ingress paths must complete")
	}
	webhook, socket := stores[0], stores[1]
	if !reflect.DeepEqual(socket.inboxes, webhook.inboxes) || !reflect.DeepEqual(socket.conversationKeys, webhook.conversationKeys) || !reflect.DeepEqual(socket.invalidations, webhook.invalidations) {
		t.Fatalf("Socket ingress changed canonical sources, targets, subscriptions or revocations: webhook=%+v socket=%+v", webhook, socket)
	}
}

type slackIngressStore struct {
	Store
	bot    *domain.Bot
	failAt string
	err    error
}

func (s slackIngressStore) GetBot(ctx context.Context, tenant, bot string) (domain.Bot, error) {
	if s.failAt == "lookup" {
		return domain.Bot{}, s.err
	}
	if s.bot != nil {
		return *s.bot, nil
	}
	return s.Store.GetBot(ctx, tenant, bot)
}

func (s slackIngressStore) AcceptInbox(ctx context.Context, in domain.Inbox) (domain.Inbox, bool, error) {
	if s.failAt == "inbox" {
		return domain.Inbox{}, false, s.err
	}
	return s.Store.AcceptInbox(ctx, in)
}

func (s slackIngressStore) HasConversation(ctx context.Context, tenant, bot, key string) (bool, error) {
	if s.failAt == "subscription" {
		return false, s.err
	}
	return s.Store.HasConversation(ctx, tenant, bot, key)
}

func (s slackIngressStore) InvalidateMessage(ctx context.Context, tenant, bot, installation, channel, message, kind string) error {
	if s.failAt == "invalidation" {
		return s.err
	}
	return s.Store.InvalidateMessage(ctx, tenant, bot, installation, channel, message, kind)
}

func TestSlackSocketReturnsPersistenceErrorsBeforeAcknowledgment(t *testing.T) {
	for _, entry := range []struct{ stage, event string }{
		{"lookup", `{"type":"app_mention","user":"HUMAN","channel":"C1","text":"x","ts":"1800000000.000001"}`},
		{"inbox", `{"type":"app_mention","user":"HUMAN","channel":"C1","text":"x","ts":"1800000000.000001"}`},
		{"subscription", `{"type":"message","user":"HUMAN","channel":"C1","text":"reply","ts":"1800000001.000001","thread_ts":"1800000000.000001"}`},
		{"invalidation", `{"type":"message","subtype":"message_deleted","hidden":true,"channel":"C1","deleted_ts":"1800000000.000001"}`},
	} {
		t.Run(entry.stage, func(t *testing.T) {
			a, s, _ := newTestApp(t)
			s.bot.Config.EventTransport = domain.SlackSocket
			s.subscribed = true
			failure := errors.New("durable storage unavailable")
			a.Store = slackIngressStore{Store: s, failAt: entry.stage, err: failure}
			body := json.RawMessage(slackBody(entry.event))
			if err := a.AcceptSlackSocketEvent(context.Background(), "bot", body); !errors.Is(err, failure) {
				t.Fatalf("failed durable admission must prevent transport ACK: %v", err)
			}
			if len(s.inboxes) != 0 || len(s.invalidations) != 0 {
				t.Fatal("failed operation changed durable input or source state")
			}
			a.Store = s
			if err := a.AcceptSlackSocketEvent(context.Background(), "bot", body); err != nil {
				t.Fatalf("redelivery after storage recovery failed: %v", err)
			}
			if len(s.inboxes)+len(s.invalidations) != 1 {
				t.Fatal("recovered delivery did not reach durable storage")
			}
		})
	}
}

func TestSlackSocketIgnoresForeignEventsAndBotLoops(t *testing.T) {
	a, s, _ := newTestApp(t)
	s.bot.Config.EventTransport = domain.SlackSocket
	for _, body := range []string{
		`{"type":"event_callback","team_id":"OTHER","event_id":"Ev1","event":{"type":"app_mention","user":"HUMAN","channel":"C1","text":"x","ts":"1800000000.000001"}}`,
		slackBody(`{"type":"app_mention","user":"HUMAN","channel":"OTHER","text":"x","ts":"1800000000.000001"}`),
		slackBody(`{"type":"app_mention","user":"U_BOT","channel":"C1","text":"x","ts":"1800000000.000001"}`),
		slackBody(`{"type":"app_mention","user":"OTHER_BOT","bot_id":"B1","channel":"C1","text":"x","ts":"1800000000.000001"}`),
		slackBody(`{"type":"message","user":"HUMAN","channel":"C1","text":"ambient","ts":"1800000000.000001"}`),
	} {
		if err := a.AcceptSlackSocketEvent(context.Background(), "bot", json.RawMessage(body)); err != nil {
			t.Fatalf("deterministically ignored event should permit ACK: %v", err)
		}
	}
	if len(s.inboxes) != 0 || len(s.invalidations) != 0 || len(s.conversationKeys) != 0 {
		t.Fatal("unrelated Slack event reached a durable operation")
	}
}

func TestSlackSocketRejectsInvalidEnvelopeAndIngressBinding(t *testing.T) {
	valid := slackBody(`{"type":"app_mention","user":"HUMAN","channel":"C1","text":"x","ts":"1800000000.000001"}`)
	for _, entry := range []struct {
		name   string
		body   string
		mutate func(*App, *appStore)
	}{
		{name: "malformed", body: valid + ` {}`},
		{name: "null", body: `null`},
		{name: "array", body: `[]`},
		{name: "incomplete", body: `{"type":"event_callback","team_id":"T1"}`},
		{name: "oversized", body: strings.Repeat("x", 1<<20+1)},
		{name: "webhook transport", mutate: func(_ *App, s *appStore) { s.bot.Config.EventTransport = domain.SlackWebhook }},
		{name: "foreign channel kind", mutate: func(_ *App, s *appStore) { s.bot.Config.ChannelKind = "other" }},
		{name: "foreign tenant", mutate: func(a *App, s *appStore) {
			bot := s.bot
			bot.TenantID = "other"
			a.Store = slackIngressStore{Store: s, bot: &bot}
		}},
		{name: "foreign bot", mutate: func(a *App, s *appStore) {
			bot := s.bot
			bot.ID = "other"
			a.Store = slackIngressStore{Store: s, bot: &bot}
		}},
	} {
		t.Run(entry.name, func(t *testing.T) {
			a, s, _ := newTestApp(t)
			s.bot.Config.EventTransport = domain.SlackSocket
			if entry.mutate != nil {
				entry.mutate(a, s)
			}
			body := entry.body
			if body == "" {
				body = valid
			}
			if err := a.AcceptSlackSocketEvent(context.Background(), "bot", json.RawMessage(body)); err == nil {
				t.Fatal("invalid Socket ingress permitted transport ACK")
			}
			if len(s.inboxes) != 0 || len(s.invalidations) != 0 || len(s.conversationKeys) != 0 {
				t.Fatal("invalid Socket ingress reached a durable operation")
			}
		})
	}
}

func TestSlackWebhookRejectsSocketOnlyInstallation(t *testing.T) {
	a, s, mux := newTestApp(t)
	s.bot.Config.EventTransport = domain.SlackSocket
	for _, body := range []string{
		`{"type":"url_verification","challenge":"proof"}`,
		slackBody(`{"type":"app_mention","user":"HUMAN","channel":"C1","text":"x","ts":"1800000000.000001"}`),
	} {
		w := signedSlackRequest(mux, body, "slack-signing", a.Now())
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "proof") {
			t.Fatalf("Socket installation exposed a webhook receiver: %d %s", w.Code, w.Body.String())
		}
	}
	if len(s.inboxes) != 0 || len(s.invalidations) != 0 {
		t.Fatal("webhook request mutated a Socket-only installation")
	}
}

func TestSlackSocketTestPeerMentionAllowlistIsExplicitOnly(t *testing.T) {
	a, s, _ := newTestApp(t)
	s.bot.Config.EventTransport = domain.SlackSocket
	a.TestAllowedBotIDs = map[string]bool{"BPEER": true, "BSELF": true}
	peer := slackBody(`{"type":"app_mention","user":"U_PEER","bot_id":"BPEER","channel":"C1","text":"<@U_BOT> hello","ts":"1800000010.000001"}`)
	if err := a.AcceptSlackSocketEvent(context.Background(), "bot", json.RawMessage(peer)); err != nil {
		t.Fatal(err)
	}
	if len(s.inboxes) != 1 {
		t.Fatalf("allowed peer mention inboxes = %d", len(s.inboxes))
	}
	thread := slackBody(`{"type":"message","user":"U_PEER","bot_id":"BPEER","channel":"C1","text":"implicit","ts":"1800000011.000001","thread_ts":"1800000010.000001"}`)
	if err := a.AcceptSlackSocketEvent(context.Background(), "bot", json.RawMessage(thread)); err != nil {
		t.Fatal(err)
	}
	self := slackBody(`{"type":"app_mention","user":"U_BOT","bot_id":"BSELF","channel":"C1","text":"<@U_BOT> self","ts":"1800000012.000001"}`)
	if err := a.AcceptSlackSocketEvent(context.Background(), "bot", json.RawMessage(self)); err != nil {
		t.Fatal(err)
	}
	if len(s.inboxes) != 1 {
		t.Fatalf("peer thread/self message was admitted: inboxes=%d", len(s.inboxes))
	}
}
