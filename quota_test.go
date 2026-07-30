package main

import (
	"encoding/json"
	"math"
	"testing"
)

// anthropicSample mirrors the real /api/oauth/usage response shape, where
// utilization is a percentage rather than a 0-1 fraction.
const anthropicSample = `{
  "five_hour": {"utilization": 3.0, "resets_at": "2026-07-30T07:09:59Z"},
  "seven_day": {"utilization": 68.0, "resets_at": "2026-07-30T18:00:00Z"},
  "seven_day_opus": {"utilization": 12.5, "resets_at": "2026-07-31T00:00:00Z"}
}`

func TestClaudePayloadUsesPercentSemantics(t *testing.T) {
	var payload claudeUsagePayload
	if err := json.Unmarshal([]byte(anthropicSample), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got := remainingFromPercent(payload.FiveHour.Utilization); math.Abs(got-0.97) > 1e-9 {
		t.Fatalf("five hour remaining = %v, want 0.97", got)
	}
	// 68% used must report 32% remaining, not a clamped zero.
	if got := remainingFromPercent(payload.SevenDay.Utilization); math.Abs(got-0.32) > 1e-9 {
		t.Fatalf("seven day remaining = %v, want 0.32", got)
	}
	if payload.SevenDayOpus == nil {
		t.Fatal("expected opus window to be parsed")
	}
	if got := remainingFromPercent(payload.SevenDayOpus.Utilization); math.Abs(got-0.875) > 1e-9 {
		t.Fatalf("opus remaining = %v, want 0.875", got)
	}
}

func TestClaudeOptionalWindowAbsent(t *testing.T) {
	var payload claudeUsagePayload
	if err := json.Unmarshal([]byte(`{"five_hour":{"utilization":0},"seven_day":{"utilization":0},"seven_day_opus":null}`), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.SevenDayOpus != nil {
		t.Fatal("expected nil opus window when upstream reports null")
	}
}

func TestClampFraction(t *testing.T) {
	cases := map[float64]float64{
		-0.5:       0,
		1.5:        1,
		0.42:       0.42,
		math.NaN(): 0,
	}
	for in, want := range cases {
		if got := clampFraction(in); got != want {
			t.Fatalf("clampFraction(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestNormalizeProviderAndSupport(t *testing.T) {
	if normalizeProvider("Claude-Web") != "claude" {
		t.Fatal("expected claude-web to normalize to claude")
	}
	if normalizeProvider("ChatGPT") != "codex" {
		t.Fatal("expected chatgpt to normalize to codex")
	}
	if providerSupported("antigravity") {
		t.Fatal("antigravity must be reported as unsupported")
	}
	if !providerSupported("anthropic") {
		t.Fatal("anthropic must be supported")
	}
}
