package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestReadyzRequiresConfiguredDependencyChecks(t *testing.T) {
	_, _, mux := newTestApp(t)
	response := appRequest(mux, http.MethodGet, "/readyz", "", "", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured readiness status = %d, want 503", response.Code)
	}
}

func TestReadyzHidesDependencyDetailsAndReportsSuccess(t *testing.T) {
	a, _, mux := newTestApp(t)
	a.Ready = func(context.Context) error { return errors.New("database credential-bearing detail") }
	response := appRequest(mux, http.MethodGet, "/readyz", "", "", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed readiness status = %d, want 503", response.Code)
	}
	if body := response.Body.String(); strings.Contains(body, "credential-bearing") {
		t.Fatalf("readiness leaked dependency detail: %s", body)
	}
	a.Ready = func(context.Context) error { return nil }
	response = appRequest(mux, http.MethodGet, "/readyz", "", "", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"ready"`) {
		t.Fatalf("ready response = %d %s", response.Code, response.Body.String())
	}
}
