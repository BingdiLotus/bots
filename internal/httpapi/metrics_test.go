package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bingdilotus/bots/internal/domain"
)

func TestMetricsExposesReadinessAndLedgerCounts(t *testing.T) {
	_, s, mux := newTestApp(t)
	s.metrics = domain.Metrics{
		Inbox:           map[string]int{"complete": 2, "running": 1},
		Tasks:           map[string]int{"scheduled": 1, "unknown": 1},
		Deliveries:      map[string]int{"confirmed": 3, "unknown": 1, "rejected": 1},
		KnowledgeActive: 2,
	}
	response := appRequest(mux, http.MethodGet, "/metrics", "", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("metrics status = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, want := range []string{
		"bots_up 1",
		"bots_ready 1",
		`bots_inbox_total{state="complete"} 2`,
		`bots_tasks_total{status="unknown"} 1`,
		`bots_deliveries_total{state="rejected"} 1`,
		"bots_knowledge_active 2",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
	if s.metricsTenant != "tenant" {
		t.Fatalf("metrics tenant = %q", s.metricsTenant)
	}
}

func TestMetricsReportsNotReadyWithoutLeakingError(t *testing.T) {
	a, s, mux := newTestApp(t)
	a.Ready = func(_ context.Context) error { return errors.New("postgres password should not leak") }
	s.metrics = domain.Metrics{Inbox: map[string]int{}}
	response := appRequest(mux, http.MethodGet, "/metrics", "", "", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "bots_ready 0") || strings.Contains(response.Body.String(), "password") {
		t.Fatalf("metrics readiness = %d %s", response.Code, response.Body.String())
	}
}
