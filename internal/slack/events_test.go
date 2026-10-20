package slack

import (
	"encoding/json"
	"testing"
)

func testBinding() Binding {
	return Binding{TenantID: "org", BotID: "bot", AccountID: "account", TeamID: "T123", AppID: "A123", ChannelID: "C123", BotUserID: "U_BOT"}
}
func eventEnvelope(t *testing.T, event string) Envelope {
	t.Helper()
	raw := []byte(`{"type":"event_callback","team_id":"T123","api_app_id":"A123","event_id":"Ev123","event":` + event + `}`)
	e, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestParseChallengeAndNormalizeHumanInputs(t *testing.T) {
	e, err := ParseEnvelope([]byte(`{"type":"url_verification","challenge":"proof","token":"deprecated-not-exposed"}`))
	if err != nil || e.Challenge != "proof" {
		t.Fatalf("challenge: %+v %v", e, err)
	}
	if event, ok, err := Normalize(testBinding(), e); err != nil || ok || event.Kind != "" {
		t.Fatal("challenge was normalized as model input")
	}
	mention := eventEnvelope(t, `{"type":"app_mention","user":"U_HUMAN","channel":"C123","text":"<@U_BOT> remember this","ts":"1700000000.000100","event_ts":"1700000000.000200"}`)
	event, ok, err := Normalize(testBinding(), mention)
	if err != nil || !ok || event.Kind != EventMention || event.ThreadTS != "1700000000.000100" || event.Target.ChannelID != "C123" || event.Target.ThreadTS != event.ThreadTS || event.TenantID != "org" || event.AccountID != "account" {
		t.Fatalf("mention: %+v %v %v", event, ok, err)
	}
	reply := eventEnvelope(t, `{"type":"message","user":"U_HUMAN","channel":"C123","text":"follow-up","ts":"1700000001.000100","thread_ts":"1700000000.000100"}`)
	event, ok, err = Normalize(testBinding(), reply)
	if err != nil || !ok || event.Kind != EventThreadReply || event.MessageTS == event.ThreadTS {
		t.Fatalf("root must decide subscription for thread reply: %+v %v", event, err)
	}
	for _, raw := range []string{
		`{"type":"message","user":"U_HUMAN","channel":"C123","text":"ambient","ts":"1700000000.000100"}`,
		`{"type":"message","user":"U_BOT","channel":"C123","thread_ts":"1700000000.000100","ts":"1700000001.000100"}`,
		`{"type":"app_mention","user":"OTHER_BOT","bot_id":"B123","channel":"C123","ts":"1700000001.000100"}`,
		`{"type":"message","subtype":"bot_message","channel":"C123","thread_ts":"1700000000.000100","ts":"1700000001.000100"}`,
		`{"type":"app_mention","user":"U_HUMAN","channel":"C_OTHER","ts":"1700000000.000100"}`,
		`{"type":"message","subtype":"message_replied","user":"U_HUMAN","channel":"C123","thread_ts":"1700000000.000100","ts":"1700000001.000100"}`,
	} {
		if _, ok, err := Normalize(testBinding(), eventEnvelope(t, raw)); err != nil || ok {
			t.Fatalf("ignored event %s: ok=%v err=%v", raw, ok, err)
		}
	}
	foreign := mention
	foreign.TeamID = "T_OTHER"
	if _, _, err := Normalize(testBinding(), foreign); err == nil {
		t.Fatal("foreign workspace accepted")
	}
	foreign = mention
	foreign.AppID = "A_OTHER"
	if _, _, err := Normalize(testBinding(), foreign); err == nil {
		t.Fatal("foreign application accepted")
	}
}

func TestSourceChangesUseOriginalMessageIdentity(t *testing.T) {
	changed := eventEnvelope(t, `{"type":"message","subtype":"message_changed","hidden":true,"channel":"C123","ts":"1700000099.000100","event_ts":"1700000099.000200","message":{"user":"U_HUMAN","text":"edited","ts":"1700000001.000100","thread_ts":"1700000000.000100","edited":{"ts":"1700000099.000100"}}}`)
	event, ok, err := Normalize(testBinding(), changed)
	if err != nil || !ok || event.Kind != EventSourceChanged || event.MessageTS != "1700000001.000100" || event.SourceRevision != "1700000099.000100" || event.ThreadTS != "1700000000.000100" {
		t.Fatalf("changed: %+v %v", event, err)
	}
	deleted := eventEnvelope(t, `{"type":"message","subtype":"message_deleted","hidden":true,"channel":"C123","ts":"1700000099.000100","event_ts":"1700000099.000200","deleted_ts":"1700000001.000100"}`)
	event, ok, err = Normalize(testBinding(), deleted)
	if err != nil || !ok || event.Kind != EventSourceDeleted || event.MessageTS != "1700000001.000100" || event.Text != "" || event.ThreadTS != "" {
		t.Fatalf("deletion without previous_message: %+v %v", event, err)
	}
	var body map[string]any
	_ = json.Unmarshal(changed.Event, &body)
	body["message"] = map[string]any{"user": "U_BOT", "ts": "1700000001.000100"}
	changed.Event, _ = json.Marshal(body)
	if _, ok, err := Normalize(testBinding(), changed); err != nil || ok {
		t.Fatalf("self edit should not start a loop: %v %v", ok, err)
	}
}

func TestPeerBotMentionRequiresExplicitTestAllowlist(t *testing.T) {
	binding := testBinding()
	binding.AllowedBotIDs = map[string]bool{"B_PEER": true, "B_SELF": true}
	peer := eventEnvelope(t, `{"type":"app_mention","user":"U_PEER","bot_id":"B_PEER","channel":"C123","text":"<@U_BOT> coordinate","ts":"1700000002.000100","event_ts":"1700000002.000200"}`)
	event, ok, err := Normalize(binding, peer)
	if err != nil || !ok || event.Kind != EventMention || event.UserID != "U_PEER" || !event.PeerBot {
		t.Fatalf("allowed peer mention: %+v %v %v", event, ok, err)
	}
	foreignPeer := eventEnvelope(t, `{"type":"app_mention","user":"U_OTHER","bot_id":"B_OTHER","channel":"C123","text":"<@U_BOT> nope","ts":"1700000003.000100"}`)
	if _, ok, err := Normalize(binding, foreignPeer); err != nil || ok {
		t.Fatalf("unknown peer bot accepted: %v %v", ok, err)
	}
	self := eventEnvelope(t, `{"type":"app_mention","user":"U_BOT","bot_id":"B_SELF","channel":"C123","text":"<@U_BOT> self","ts":"1700000004.000100"}`)
	if _, ok, err := Normalize(binding, self); err != nil || ok {
		t.Fatalf("self bot mention accepted: %v %v", ok, err)
	}
	thread := eventEnvelope(t, `{"type":"message","user":"U_PEER","bot_id":"B_PEER","channel":"C123","text":"implicit","ts":"1700000005.000100","thread_ts":"1700000002.000100"}`)
	if _, ok, err := Normalize(binding, thread); err != nil || ok {
		t.Fatalf("peer thread reply bypassed explicit mention: %v %v", ok, err)
	}
}
