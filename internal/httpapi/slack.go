package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/bingdilotus/bots/internal/config"
	"github.com/bingdilotus/bots/internal/domain"
	"github.com/bingdilotus/bots/internal/slack"
)

var errSlackBinding = errors.New("Slack installation binding mismatch")

func (a *App) slackEvents(w http.ResponseWriter, r *http.Request) {
	if !a.tenant(w, r.PathValue("tenant")) {
		return
	}
	bot, err := a.Store.GetBot(r.Context(), a.TenantID, r.PathValue("bot"))
	if err != nil {
		replyStoreError(w, err)
		return
	}
	if bot.TenantID != a.TenantID || bot.ID != r.PathValue("bot") || bot.Config.ChannelKind != "slack" || bot.Config.SlackTransport() != domain.SlackWebhook {
		replyError(w, 404, "not_found", "channel account was not found")
		return
	}
	secret, err := config.Secret(bot.Config.SigningSecretEnv)
	if err != nil {
		replyError(w, 503, "authentication_unavailable", "channel authentication is not configured")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			replyError(w, 413, "payload_too_large", "request body exceeds 1 MiB")
		} else {
			replyError(w, 400, "invalid_request", "request body could not be read")
		}
		return
	}
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	if err := slack.VerifyRequest(secret, r.Header, body, now); err != nil {
		replyError(w, 401, "invalid_signature", "Slack signature or timestamp is invalid")
		return
	}
	envelope, err := slack.ParseEnvelope(body)
	if err != nil {
		replyError(w, 400, "invalid_request", "Slack envelope is invalid")
		return
	}
	if envelope.Type == "url_verification" {
		replyJSON(w, 200, map[string]string{"challenge": envelope.Challenge})
		return
	}
	result, err := a.admitSlackEnvelope(r.Context(), bot, envelope)
	if errors.Is(err, errSlackBinding) {
		replyError(w, 403, "forbidden", "Slack event does not match the configured installation")
		return
	}
	if err != nil {
		replyStoreError(w, err)
		return
	}
	replyJSON(w, 200, result)
}

// AcceptSlackSocketEvent is called only by the authenticated, in-process Slack
// Socket Mode receiver. It uses the same source binding and durable admission
// as the signed webhook. A nil result permits the receiver to ACK the envelope.
func (a *App) AcceptSlackSocketEvent(ctx context.Context, botID string, body json.RawMessage) error {
	if a.Store == nil || a.TenantID == "" || botID == "" || len(body) > 1<<20 {
		return domain.ErrInvalid
	}
	bot, err := a.Store.GetBot(ctx, a.TenantID, botID)
	if err != nil {
		return err
	}
	if bot.TenantID != a.TenantID || bot.ID != botID || bot.Config.ChannelKind != "slack" || bot.Config.SlackTransport() != domain.SlackSocket {
		return domain.ErrInvalid
	}
	envelope, err := slack.ParseEnvelope(body)
	if err != nil {
		return domain.ErrInvalid
	}
	if envelope.Type != "event_callback" {
		return nil
	}
	_, err = a.admitSlackEnvelope(ctx, bot, envelope)
	if errors.Is(err, errSlackBinding) {
		return nil // An event outside the installation is a deterministic ignore.
	}
	return err
}

func (a *App) admitSlackEnvelope(ctx context.Context, bot domain.Bot, envelope slack.Envelope) (map[string]any, error) {
	event, ok, err := slack.Normalize(slack.Binding{TenantID: bot.TenantID, BotID: bot.ID, AccountID: bot.Config.InstallationID, TeamID: bot.Config.WorkspaceID, ChannelID: bot.Config.ChannelID, BotUserID: bot.Config.BotUserID, AllowedBotIDs: a.TestAllowedBotIDs}, envelope)
	if err != nil {
		return nil, errSlackBinding
	}
	if !ok {
		return map[string]any{"ok": true, "ignored": true}, nil
	}
	if !a.peerBudgetAllows(event) {
		return map[string]any{"ok": true, "ignored": true, "reason": "peer_budget"}, nil
	}
	if event.Kind == slack.EventSourceChanged || event.Kind == slack.EventSourceDeleted {
		kind := "edited"
		if event.Kind == slack.EventSourceDeleted {
			kind = "deleted"
		}
		if err := a.Store.InvalidateMessage(ctx, bot.TenantID, bot.ID, bot.Config.InstallationID, event.ChannelID, event.MessageTS, kind); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "invalidated": true}, nil
	}
	target := domain.Target{Provider: "slack", InstallationID: bot.Config.InstallationID, ChannelID: event.ChannelID, ThreadID: event.Target.ThreadTS, MessageID: event.MessageTS}
	if event.Kind == slack.EventThreadReply {
		subscribed, err := a.Store.HasConversation(ctx, bot.TenantID, bot.ID, domain.SessionKey(bot.TenantID, bot.ID, target))
		if err != nil {
			return nil, err
		}
		if !subscribed {
			return map[string]any{"ok": true, "ignored": true}, nil
		}
	}
	// app_mention and message subscriptions can deliver the same human message
	// under different event IDs. The message is the logical source; event_id is
	// only a Slack delivery identity and must not create a second command.
	sourceID := domain.StableID("slack_message_", bot.Config.InstallationID, event.ChannelID, event.MessageTS)
	input := domain.Inbox{TenantID: bot.TenantID, BotID: bot.ID, ScopeID: domain.KnowledgeScope(bot.TenantID, bot.ID, bot.Config.InstallationID, event.ChannelID), Namespace: "slack", SourceID: sourceID, Input: event.Text, Target: target}
	stored, created, err := a.Store.AcceptInbox(ctx, input)
	if created {
		a.recordPeerRound(event)
	}
	if errors.Is(err, domain.ErrDisabled) {
		return map[string]any{"ok": true, "ignored": true, "reason": "disabled"}, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "accepted": true, "inbox_id": stored.ID, "idempotent": !created}, nil
}
