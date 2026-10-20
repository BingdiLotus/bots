package service_test

import (
	"testing"
	"time"
)

func TestEngineReadyRequiresLeaseAndRecentSuccessfulStep(t *testing.T) {
	f := setupEngine(t)
	if err := f.engine.Ready(t.Context(), time.Second); err == nil {
		t.Fatal("uninitialized dispatcher reported ready")
	}
	initializeEngine(t, f.engine)
	if err := f.engine.Ready(t.Context(), time.Second); err == nil {
		t.Fatal("dispatcher reported ready before its first completed step")
	}
	if err := f.engine.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.Ready(t.Context(), time.Second); err != nil {
		t.Fatalf("successful step left dispatcher unready: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := f.engine.Ready(t.Context(), 10*time.Millisecond); err == nil {
		t.Fatal("stale dispatcher step still reported ready")
	}
}
