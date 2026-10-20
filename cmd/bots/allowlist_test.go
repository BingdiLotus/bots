package main

import "testing"

func TestParsePeerRoundBudget(t *testing.T) {
	for raw, want := range map[string]int{"": 0, "0": 0, "1": 1, " 2 ": 2, "-1": 0, "x": 0} {
		if got := parsePeerRoundBudget(raw); got != want {
			t.Fatalf("parsePeerRoundBudget(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestParseBotIDAllowlist(t *testing.T) {
	got := parseBotIDAllowlist(" B1, B2 ,, B1 ")
	if len(got) != 2 || !got["B1"] || !got["B2"] {
		t.Fatalf("allowlist = %+v", got)
	}
	if len(parseBotIDAllowlist("")) != 0 {
		t.Fatal("empty allowlist was not empty")
	}
}
