package service

import (
	"context"
	"errors"
	"time"

	"github.com/bingdilotus/bots/internal/config"
	"github.com/bingdilotus/bots/internal/domain"
	"github.com/bingdilotus/bots/internal/slack"
)

func (e *Engine) sendDeliveries(ctx context.Context) error {
	for range 16 {
		if err := e.checkpoint(ctx); err != nil {
			return err
		}
		delivery, err := e.Store.ClaimDelivery(ctx, e.TenantID)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		bot, err := e.Store.GetBot(ctx, delivery.TenantID, delivery.BotID)
		if err != nil {
			return e.markSend(ctx, delivery, "retryable", "configuration_unavailable_before_send", time.Now().Add(time.Minute))
		}
		token, err := config.Secret(bot.Config.TokenEnv)
		if err != nil {
			if err := e.markSend(ctx, delivery, "retryable", "credential_unavailable_before_send", time.Now().Add(time.Minute)); err != nil {
				return err
			}
			continue
		}
		var result slack.SendResult
		if err := e.checkpoint(ctx); err != nil {
			// No provider call has started. A lost-lease claimant relinquishes
			// only with proof it did not cross the external boundary.
			persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = e.markSend(persistCtx, delivery, "retryable", "dispatcher_stopped_before_send", time.Now().Add(time.Second))
			cancel()
			return err
		}
		if delivery.Target.Provider == "slack" && delivery.Target.InstallationID == bot.Config.InstallationID {
			result = e.Slack.ChatPostMessage(ctx, token, slack.ReplyTarget{ChannelID: delivery.Target.ChannelID, ThreadTS: delivery.Target.ThreadID}, delivery.Text, delivery.OperationID)
		} else {
			result = slack.SendResult{State: slack.Rejected, ErrorCode: "unsupported_or_changed_account"}
		}
		// An attempted send needs bookkeeping even if shutdown canceled ctx.
		// A confirmed receipt may be written again locally; the provider call is
		// never repeated to repair a database error.
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if result.State == slack.Confirmed && result.Receipt != nil {
			for attempt := range 3 {
				err = e.Store.CompleteDelivery(persistCtx, delivery.ID, delivery.PayloadHash, result.Receipt.TS)
				if err == nil || errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrBusy) || persistCtx.Err() != nil {
					break
				}
				if attempt < 2 {
					timer := time.NewTimer(100 * time.Millisecond)
					select {
					case <-persistCtx.Done():
					case <-timer.C:
					}
					timer.Stop()
				}
			}
			if err != nil {
				// If the commit actually succeeded, FailDelivery cannot demote a
				// confirmed result. Otherwise the unresolved claim stays unknown.
				_ = e.markSend(persistCtx, delivery, "unknown", "receipt_not_committed", time.Time{})
			}
		} else {
			state := string(result.State)
			if state != "retryable" && state != "rejected" {
				state = "unknown"
			}
			next := time.Time{}
			if state == "retryable" {
				if delivery.Attempts >= 5 {
					state = "rejected"
				} else {
					delay := result.RetryAfter
					if delay <= 0 {
						delay = retryDelay(delivery.Attempts, nil)
					}
					next = time.Now().Add(delay)
				}
			}
			err = e.markSend(persistCtx, delivery, state, result.ErrorCode, next)
		}
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) markSend(ctx context.Context, d domain.Delivery, state, reason string, next time.Time) error {
	return e.Store.FailDelivery(ctx, d.ID, d.PayloadHash, state, reason, next)
}
