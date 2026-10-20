package store

import (
	"errors"
	"testing"
	"time"

	"github.com/bingdilotus/bots/internal/domain"
)

func unknownDelivery(t *testing.T, f fixture) domain.Delivery {
	t.Helper()
	task := f.createTask(t)
	f.fire(t, task)
	delivery, err := f.s.ClaimDelivery(t.Context(), f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.FailDelivery(t.Context(), delivery.ID, delivery.PayloadHash, "unknown", "fixture result unconfirmed", time.Time{}); err != nil {
		t.Fatal(err)
	}
	return delivery
}

func TestResolveUnknownDeliveryUpdatesTaskAndAudit(t *testing.T) {
	for _, test := range []struct {
		name       string
		resolution string
		provider   string
		note       string
		wantTask   string
	}{
		{name: "confirmed", resolution: "confirmed", provider: "1792479946.186979", note: "message present in Slack thread", wantTask: "completed"},
		{name: "rejected", resolution: "rejected", note: "no message in Slack thread", wantTask: "canceled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setupPG(t)
			delivery := unknownDelivery(t, f)
			resolved, err := f.s.ResolveDelivery(t.Context(), f.scope, delivery.ID, test.resolution, test.provider, test.note)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.State != test.resolution || resolved.ProviderMessageID != test.provider {
				t.Fatalf("delivery was not resolved: %+v", resolved)
			}
			task, err := f.s.GetTask(t.Context(), f.scope, delivery.TaskID)
			if err != nil || task.Status != test.wantTask {
				t.Fatalf("task after resolution: %+v %v", task, err)
			}
			var audits int
			if err := f.s.Pool.QueryRow(t.Context(), `SELECT count(*) FROM bots.audit_events WHERE tenant_id=$1 AND kind='delivery_resolved' AND ref_id=$2`, f.tenant, delivery.ID).Scan(&audits); err != nil || audits != 1 {
				t.Fatalf("resolution audit: %d %v", audits, err)
			}
			if _, err := f.s.ResolveDelivery(t.Context(), f.scope, delivery.ID, test.resolution, test.provider, test.note); !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("second resolution = %v, want conflict", err)
			}
			if _, err := f.s.ResolveDelivery(t.Context(), domain.Scope{TenantID: f.tenant, BotID: "other"}, delivery.ID, test.resolution, test.provider, test.note); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("cross-bot resolution = %v, want not found", err)
			}
		})
	}
}

func TestResolveDeliveryValidatesManualReceipt(t *testing.T) {
	f := setupPG(t)
	for _, test := range []struct {
		name       string
		resolution string
		provider   string
		note       string
	}{
		{name: "unsupported state", resolution: "pending"},
		{name: "confirmed without provider id", resolution: "confirmed", note: "checked"},
		{name: "rejected with provider id", resolution: "rejected", provider: "123.456", note: "checked"},
		{name: "rejected without reason", resolution: "rejected"},
		{name: "provider id with whitespace", resolution: "confirmed", provider: "123 456"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := f.s.ResolveDelivery(t.Context(), f.scope, "missing", test.resolution, test.provider, test.note); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("invalid resolution accepted: %v", err)
			}
		})
	}
}
