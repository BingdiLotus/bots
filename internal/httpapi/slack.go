package httpapi

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/bingdilotus/bots/internal/config"
	"github.com/bingdilotus/bots/internal/domain"
	"github.com/bingdilotus/bots/internal/slack"
)

func (a *App) slackEvents(w http.ResponseWriter, r *http.Request) {
	if !a.tenant(w, r.PathValue("tenant")) {
		return
	}
	bot, err := a.Store.GetBot(r.Context(), a.TenantID, r.PathValue("bot"))
	if err != nil {
		replyStoreError(w, err)
		return
	}
	if bot.TenantID != a.TenantID || bot.ID != r.PathValue("bot") || bot.Config.ChannelKind != "slack" {
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
	event, ok, err := slack.Normalize(slack.Binding{TenantID: bot.TenantID, BotID: bot.ID, AccountID: bot.Config.InstallationID, TeamID: bot.Config.WorkspaceID, ChannelID: bot.Config.ChannelID, BotUserID: bot.Config.BotUserID}, envelope)
	if err != nil {
		replyError(w, 403, "forbidden", "Slack event does not match the configured installation")
		return
	}
	if !ok {
		replyJSON(w, 200, map[string]bool{"ok": true, "ignored": true})
		return
	}
	if event.Kind == slack.EventSourceChanged || event.Kind == slack.EventSourceDeleted {
		kind := "edited"
		if event.Kind == slack.EventSourceDeleted {
			kind = "deleted"
		}
		if err := a.Store.InvalidateMessage(r.Context(), bot.TenantID, bot.ID, bot.Config.InstallationID, event.ChannelID, event.MessageTS, kind); err != nil {
			replyStoreError(w, err)
			return
		}
		replyJSON(w, 200, map[string]bool{"ok": true, "invalidated": true})
		return
	}
	target := domain.Target{Provider: "slack", InstallationID: bot.Config.InstallationID, ChannelID: event.ChannelID, ThreadID: event.Target.ThreadTS, MessageID: event.MessageTS}
	if event.Kind == slack.EventThreadReply {
		subscribed, err := a.Store.HasConversation(r.Context(), bot.TenantID, bot.ID, domain.SessionKey(bot.TenantID, bot.ID, target))
		if err != nil {
			replyStoreError(w, err)
			return
		}
		if !subscribed {
			replyJSON(w, 200, map[string]bool{"ok": true, "ignored": true})
			return
		}
	}
	// app_mention and message subscriptions can deliver the same human message
	// under different event IDs. The message is the logical source; event_id is
	// only a Slack delivery identity and must not create a second command.
	sourceID := domain.StableID("slack_message_", bot.Config.InstallationID, event.ChannelID, event.MessageTS)
	input := domain.Inbox{TenantID: bot.TenantID, BotID: bot.ID, ScopeID: domain.KnowledgeScope(bot.TenantID, bot.ID, bot.Config.InstallationID, event.ChannelID), Namespace: "slack", SourceID: sourceID, Input: event.Text, Target: target}
	stored, created, err := a.Store.AcceptInbox(r.Context(), input)
	if errors.Is(err, domain.ErrDisabled) {
		replyJSON(w, 200, map[string]any{"ok": true, "ignored": true, "reason": "disabled"})
		return
	}
	if err != nil {
		replyStoreError(w, err)
		return
	}
	replyJSON(w, 200, map[string]any{"ok": true, "accepted": true, "inbox_id": stored.ID, "idempotent": !created})
}
