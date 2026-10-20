package slack

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Binding is installation/account routing supplied by the trusted caller. The
// channel is a configured integration route, not a member/business whitelist.
type Binding struct {
	TenantID  string
	BotID     string
	AccountID string
	TeamID    string
	AppID     string
	ChannelID string
	BotUserID string
	// AllowedBotIDs is a test-lab allowlist. A peer bot is admitted only for an
	// explicit app_mention; implicit thread replies remain filtered to avoid loops.
	AllowedBotIDs map[string]bool
}

type Envelope struct {
	Type      string          `json:"type"`
	Challenge string          `json:"challenge,omitempty"`
	TeamID    string          `json:"team_id,omitempty"`
	AppID     string          `json:"api_app_id,omitempty"`
	EventID   string          `json:"event_id,omitempty"`
	Event     json.RawMessage `json:"event,omitempty"`
}

// ParseEnvelope does not authenticate. VerifyRequest must precede challenge or
// event handling; legacy verification tokens are intentionally not exposed.
func ParseEnvelope(raw []byte) (Envelope, error) {
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Envelope{}, fmt.Errorf("invalid Slack envelope: %w", err)
	}
	if envelope.Type == "url_verification" {
		if envelope.Challenge == "" {
			return Envelope{}, errors.New("missing Slack challenge")
		}
		return envelope, nil
	}
	if envelope.Type == "event_callback" && (envelope.TeamID == "" || envelope.EventID == "" || len(envelope.Event) == 0 || string(envelope.Event) == "null") {
		return Envelope{}, errors.New("incomplete Slack event envelope")
	}
	if envelope.Type == "" {
		return Envelope{}, errors.New("missing Slack envelope type")
	}
	return envelope, nil
}

type EventKind string

const (
	EventMention       EventKind = "mention"
	EventThreadReply   EventKind = "thread_reply"
	EventSourceChanged EventKind = "source_changed"
	EventSourceDeleted EventKind = "source_deleted"
)

type ReplyTarget struct {
	ChannelID string `json:"channel_id"`
	ThreadTS  string `json:"thread_ts,omitempty"`
}

type Event struct {
	Kind           EventKind
	EventID        string
	TenantID       string
	BotID          string
	AccountID      string
	TeamID         string
	ChannelID      string
	UserID         string
	MessageTS      string
	ThreadTS       string
	EventTS        string
	SourceRevision string
	Text           string
	Target         ReplyTarget
	// PeerBot is true only when this is an explicit app_mention admitted by the
	// test-lab peer allowlist. It is never set for human messages or self loops.
	PeerBot bool
}

type wireMessage struct {
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype"`
	User       string          `json:"user"`
	BotID      string          `json:"bot_id"`
	BotProfile json.RawMessage `json:"bot_profile"`
	Channel    string          `json:"channel"`
	Text       string          `json:"text"`
	TS         string          `json:"ts"`
	ThreadTS   string          `json:"thread_ts"`
	EventTS    string          `json:"event_ts"`
	DeletedTS  string          `json:"deleted_ts"`
	Hidden     bool            `json:"hidden"`
	Edited     *struct {
		TS string `json:"ts"`
	} `json:"edited"`
	Message  *wireMessage `json:"message"`
	Previous *wireMessage `json:"previous_message"`
}

func isBot(message *wireMessage, binding Binding) bool {
	return message != nil && (message.BotID != "" || message.Subtype == "bot_message" || len(message.BotProfile) > 0 && string(message.BotProfile) != "null" || binding.BotUserID != "" && message.User == binding.BotUserID)
}

func peerMentionAllowed(message *wireMessage, binding Binding) bool {
	return message != nil && message.Type == "app_mention" && message.BotID != "" &&
		message.User != binding.BotUserID && binding.AllowedBotIDs[message.BotID]
}

// Normalize returns thread replies as candidates. The caller must check its
// durable subscribed-thread mapping before admitting one as a command. Source
// changes/deletions are separate invalidation events, never fresh model input.
func Normalize(binding Binding, envelope Envelope) (Event, bool, error) {
	if envelope.Type != "event_callback" {
		return Event{}, false, nil
	}
	if binding.TenantID == "" || binding.BotID == "" || binding.AccountID == "" || binding.TeamID == "" || binding.ChannelID == "" || binding.BotUserID == "" {
		return Event{}, false, errors.New("incomplete trusted Slack binding")
	}
	if envelope.TeamID != binding.TeamID || binding.AppID != "" && envelope.AppID != binding.AppID {
		return Event{}, false, errors.New("Slack installation binding mismatch")
	}
	var message wireMessage
	if err := json.Unmarshal(envelope.Event, &message); err != nil {
		return Event{}, false, errors.New("invalid Slack event")
	}
	if message.Channel != binding.ChannelID {
		return Event{}, false, nil
	}
	if isBot(&message, binding) && !peerMentionAllowed(&message, binding) {
		return Event{}, false, nil
	}
	event := Event{EventID: envelope.EventID, TenantID: binding.TenantID, BotID: binding.BotID, AccountID: binding.AccountID,
		TeamID: binding.TeamID, ChannelID: message.Channel, UserID: message.User, MessageTS: message.TS,
		ThreadTS: message.ThreadTS, EventTS: message.EventTS, Text: message.Text}
	switch {
	case message.Type == "app_mention":
		if message.Hidden || message.User == "" || message.TS == "" {
			return Event{}, false, nil
		}
		event.Kind = EventMention
	case message.Type == "message" && message.Subtype == "message_changed":
		updated := message.Message
		if updated == nil || updated.TS == "" {
			return Event{}, false, errors.New("changed Slack message missing original timestamp")
		}
		if isBot(updated, binding) || isBot(message.Previous, binding) {
			return Event{}, false, nil
		}
		event.Kind = EventSourceChanged
		event.MessageTS, event.Text, event.UserID, event.ThreadTS = updated.TS, updated.Text, updated.User, updated.ThreadTS
		if updated.Edited != nil {
			event.SourceRevision = updated.Edited.TS
		}
		if event.ThreadTS == "" && message.Previous != nil {
			event.ThreadTS = message.Previous.ThreadTS
		}
	case message.Type == "message" && message.Subtype == "message_deleted":
		if message.DeletedTS == "" {
			return Event{}, false, errors.New("deleted Slack message missing deleted_ts")
		}
		if isBot(message.Previous, binding) {
			return Event{}, false, nil
		}
		event.Kind, event.MessageTS, event.Text, event.UserID, event.ThreadTS = EventSourceDeleted, message.DeletedTS, "", "", ""
		if message.Previous != nil {
			event.UserID, event.ThreadTS = message.Previous.User, message.Previous.ThreadTS
		}
	case message.Type == "message" && (message.Subtype == "" || message.Subtype == "thread_broadcast"):
		if message.Hidden || message.ThreadTS == "" || message.TS == "" || message.User == "" {
			return Event{}, false, nil
		}
		event.Kind = EventThreadReply
	default:
		return Event{}, false, nil
	}
	event.PeerBot = peerMentionAllowed(&message, binding)
	if event.SourceRevision == "" {
		event.SourceRevision = message.EventTS
	}
	if event.ThreadTS == "" && event.Kind != EventSourceDeleted {
		event.ThreadTS = event.MessageTS
	}
	event.Target = ReplyTarget{ChannelID: event.ChannelID, ThreadTS: event.ThreadTS}
	return event, true, nil
}

func (t ReplyTarget) validate() bool {
	return t.ChannelID != "" && !strings.ContainsAny(t.ChannelID, " \t\r\n") && (t.ThreadTS == "" || validTimestamp(t.ThreadTS))
}

func validTimestamp(ts string) bool {
	left, right, ok := strings.Cut(ts, ".")
	if !ok || left == "" || right == "" {
		return false
	}
	for _, part := range []string{left, right} {
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}
