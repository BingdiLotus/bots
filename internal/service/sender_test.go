package service_test

import (
	"testing"
	"time"
)

func preparePausedReply(t *testing.T) *engineFixture {
	t.Helper()
	f := setupEngine(t)
	f.runtime.mu.Lock()
	f.runtime.holdResults = true
	f.runtime.mu.Unlock()
	f.accept(t, "event-final", "1700000000.000100")
	f.drive(t, func() bool { _, _, runs, _ := f.runtime.counts(); return runs == 1 })
	if err := f.store.SetBotEnabled(t.Context(), f.tenant, f.bot.ID, false); err != nil {
		t.Fatal(err)
	}
	f.runtime.mu.Lock()
	f.runtime.holdResults = false
	f.runtime.mu.Unlock()
	f.step(t)
	rows := f.deliveries(t)
	if len(rows) != 1 || rows[0].State != "pending" || rows[0].Attempts != 0 || len(f.slack.messages()) != 0 {
		t.Fatalf("paused Bot acquired a send claim or lost its in-flight result: %+v", rows)
	}
	return f
}

func TestSenderUnknownResultIsNotReplayedAfterRecovery(t *testing.T) {
	f := preparePausedReply(t)
	f.slack.mu.Lock()
	f.slack.modes = []string{"lost", "ok"}
	f.slack.mu.Unlock()
	if err := f.store.SetBotEnabled(t.Context(), f.tenant, f.bot.ID, true); err != nil {
		t.Fatal(err)
	}
	f.step(t)
	rows := f.deliveries(t)
	if len(rows) != 1 || rows[0].State != "unknown" || rows[0].ProviderMessageID != "" || rows[0].Attempts != 1 {
		t.Fatalf("lost response became a false rejection/receipt: %+v", rows)
	}
	if len(f.slack.messages()) != 1 {
		t.Fatal("Slack client internally retried ambiguous send")
	}
	if err := f.store.Recover(t.Context(), f.tenant); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		f.step(t)
	}
	if len(f.slack.messages()) != 1 {
		t.Fatal("dispatcher or recovery blindly resent an unknown effect")
	}
	if _, _, runs, _ := f.runtime.counts(); runs != 1 {
		t.Fatal("sender uncertainty created another model run")
	}
}

func TestSenderRateLimitDefersAndPausePreventsClaim(t *testing.T) {
	f := preparePausedReply(t)
	f.slack.mu.Lock()
	f.slack.modes = []string{"429", "ok"}
	f.slack.mu.Unlock()
	if err := f.store.SetBotEnabled(t.Context(), f.tenant, f.bot.ID, true); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	f.step(t)
	rows := f.deliveries(t)
	if len(rows) != 1 || rows[0].State != "retryable" || rows[0].Attempts != 1 || rows[0].NextAttemptAt.Before(before.Add(3595*time.Second)) {
		t.Fatalf("Retry-After was not persisted: %+v", rows)
	}
	for range 3 {
		f.step(t)
	}
	if len(f.slack.messages()) != 1 {
		t.Fatal("429 was retried before Retry-After")
	}
	if err := f.store.SetBotEnabled(t.Context(), f.tenant, f.bot.ID, false); err != nil {
		t.Fatal(err)
	}
	// Advance only this test tenant's queue clock rather than sleeping an hour.
	if _, err := f.store.Pool.Exec(t.Context(), `UPDATE bots.deliveries SET next_attempt_at=now() WHERE tenant_id=$1`, f.tenant); err != nil {
		t.Fatal(err)
	}
	f.step(t)
	rows = f.deliveries(t)
	if len(f.slack.messages()) != 1 || rows[0].Attempts != 1 || rows[0].State != "retryable" {
		t.Fatal("paused retry acquired another claim")
	}
	if err := f.store.SetBotEnabled(t.Context(), f.tenant, f.bot.ID, true); err != nil {
		t.Fatal(err)
	}
	f.step(t)
	f.step(t)
	rows = f.deliveries(t)
	messages := f.slack.messages()
	if len(messages) != 2 || rows[0].State != "confirmed" || rows[0].Attempts != 2 || rows[0].ProviderMessageID == "" {
		t.Fatalf("retry confirmation missing: messages=%+v ledger=%+v", messages, rows)
	}
	if messages[0].ClientID == "" || messages[0] != messages[1] {
		t.Fatal("retry changed operation identity, payload or trusted target")
	}
}
