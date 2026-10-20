package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("BOTS_DATABASE_URL", "postgres://test:test@localhost/bots_test")
	t.Setenv("BOTS_HTTP_ADDR", "")
	t.Setenv("BOTS_SHUTDOWN_TIMEOUT", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:8090" || cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("BOTS_DATABASE_URL", "postgres://test:test@localhost/bots_test")
	t.Setenv("BOTS_HTTP_ADDR", ":9090")
	t.Setenv("BOTS_SHUTDOWN_TIMEOUT", "2s")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":9090" || cfg.ShutdownTimeout != 2*time.Second {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name, addr, timeout, wantError string
	}{
		{"missing port", "localhost", "10s", "BOTS_HTTP_ADDR"},
		{"invalid port", ":http", "10s", "BOTS_HTTP_ADDR"},
		{"port out of range", ":65536", "10s", "BOTS_HTTP_ADDR"},
		{"negative port", ":-1", "10s", "BOTS_HTTP_ADDR"},
		{"invalid duration", ":8090", "soon", "BOTS_SHUTDOWN_TIMEOUT"},
		{"zero duration", ":8090", "0s", "BOTS_SHUTDOWN_TIMEOUT"},
		{"negative duration", ":8090", "-1s", "BOTS_SHUTDOWN_TIMEOUT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BOTS_HTTP_ADDR", tt.addr)
			t.Setenv("BOTS_SHUTDOWN_TIMEOUT", tt.timeout)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("got error %v, want %s", err, tt.wantError)
			}
		})
	}
}
