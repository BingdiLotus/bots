package main

import "testing"

func TestParseBotIDAllowlist(t *testing.T) {
	got := parseBotIDAllowlist(" B1, B2 ,, B1 ")
	if len(got) != 2 || !got["B1"] || !got["B2"] {
		t.Fatalf("allowlist = %+v", got)
	}
	if len(parseBotIDAllowlist("")) != 0 {
		t.Fatal("empty allowlist was not empty")
	}
}
