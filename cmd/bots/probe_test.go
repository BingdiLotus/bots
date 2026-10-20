package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeReadyChecksOnlyHTTPStatus(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		wantOK bool
	}{
		{name: "ready", status: http.StatusOK, wantOK: true},
		{name: "not ready", status: http.StatusServiceUnavailable, wantOK: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(`{"error":"dependency detail must not be logged"}`))
			}))
			defer server.Close()
			err := probeReady(server.URL)
			if (err == nil) != test.wantOK {
				t.Fatalf("probe error = %v, want OK=%v", err, test.wantOK)
			}
		})
	}
}

func TestProbeReadyRejectsUnreachableEndpoint(t *testing.T) {
	if err := probeReady("http://127.0.0.1:1/readyz"); err == nil {
		t.Fatal("unreachable readiness endpoint reported success")
	}
}
