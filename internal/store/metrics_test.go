package store

import "testing"

func TestMetricsCountsUnknownDeliveryAndTask(t *testing.T) {
	f := setupPG(t)
	delivery := unknownDelivery(t, f)
	snapshot, err := f.s.Metrics(t.Context(), f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Deliveries["unknown"] != 1 || snapshot.Tasks["unknown"] != 1 || len(snapshot.Inbox) == 0 {
		t.Fatalf("metrics snapshot = %+v (delivery %s)", snapshot, delivery.ID)
	}
	empty, err := f.s.Metrics(t.Context(), "other-tenant")
	if err != nil || len(empty.Deliveries) != 0 || len(empty.Tasks) != 0 {
		t.Fatalf("cross-tenant metrics = %+v %v", empty, err)
	}
	if _, err := f.s.Metrics(t.Context(), ""); err == nil {
		t.Fatal("empty tenant accepted")
	}
}
