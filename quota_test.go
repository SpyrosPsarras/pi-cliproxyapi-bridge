package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// anthropicSample mirrors the real /api/oauth/usage response shape, where
// utilization is a percentage rather than a 0-1 fraction.
// The shape Anthropic returns today: the flat model-scoped fields are null and
// the real windows, including model-scoped ones like Fable, live in the
// structured array.
const anthropicLimitsSample = `{
  "five_hour": {"utilization": 27.0, "resets_at": "2026-08-09T10:30:00Z"},
  "seven_day": {"utilization": 18.0, "resets_at": "2026-08-13T18:00:00Z"},
  "seven_day_opus": null,
  "seven_day_sonnet": null,
  "limits": [
    {"kind": "session", "percent": 27, "resets_at": "2026-08-09T10:30:00Z", "scope": null},
    {"kind": "weekly_all", "percent": 18, "resets_at": "2026-08-13T18:00:00Z", "scope": null},
    {"kind": "weekly_scoped", "percent": 9, "resets_at": "2026-08-13T17:59:59Z",
     "scope": {"model": {"id": null, "display_name": "Fable"}}}
  ]
}`

// The older shape, before the structured array existed.
const anthropicFlatSample = `{
  "five_hour": {"utilization": 3.0, "resets_at": "2026-07-29T21:00:00Z"},
  "seven_day": {"utilization": 68.0, "resets_at": "2026-07-30T18:00:00Z"},
  "seven_day_opus": {"utilization": 12.5, "resets_at": "2026-07-31T00:00:00Z"}
}`

func claudeGroups(t *testing.T, body string) map[string]quotaGroup {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out := map[string]quotaGroup{}
	for _, g := range applyQuotaRules(payload, defaultQuotaRules()["claude"]) {
		out[g.ID] = g
	}
	return out
}

func TestClaudeReadsModelScopedLimits(t *testing.T) {
	groups := claudeGroups(t, anthropicLimitsSample)
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3: %v", len(groups), groups)
	}
	fable, ok := groups["seven-day-fable"]
	if !ok {
		t.Fatalf("missing Fable window, got %v", groups)
	}
	if math.Abs(fable.RemainingFraction-0.91) > 1e-9 {
		t.Fatalf("fable remaining = %v, want 0.91", fable.RemainingFraction)
	}
	if fable.Label != "7d Fable" {
		t.Fatalf("fable label = %q", fable.Label)
	}
	// Pi renders the models entry, so it must mirror the group.
	if len(fable.Models) != 1 || fable.Models[0].RemainingFraction != fable.RemainingFraction {
		t.Fatalf("fable models entry not mirrored: %+v", fable.Models)
	}
	// 27% used is 73% left, not a clamped zero.
	if math.Abs(groups["five-hour"].RemainingFraction-0.73) > 1e-9 {
		t.Fatalf("session remaining = %v, want 0.73", groups["five-hour"].RemainingFraction)
	}
}

// The array wins over the flat fields, which are still present but null for
// scoped windows; reading them first would drop Fable.
func TestClaudePrefersStructuredArray(t *testing.T) {
	groups := claudeGroups(t, anthropicLimitsSample)
	if _, ok := groups["seven-day-fable"]; !ok {
		t.Fatal("scoped window lost to the flat fallback")
	}
	if math.Abs(groups["seven-day"].RemainingFraction-0.82) > 1e-9 {
		t.Fatalf("weekly remaining = %v, want 0.82", groups["seven-day"].RemainingFraction)
	}
}

// Payloads that predate the array must keep working.
func TestClaudeFallsBackToFlatWindows(t *testing.T) {
	groups := claudeGroups(t, anthropicFlatSample)
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3: %v", len(groups), groups)
	}
	// 68% used must report 32% remaining.
	if math.Abs(groups["seven-day"].RemainingFraction-0.32) > 1e-9 {
		t.Fatalf("weekly remaining = %v", groups["seven-day"].RemainingFraction)
	}
	if math.Abs(groups["seven-day-opus"].RemainingFraction-0.875) > 1e-9 {
		t.Fatalf("opus remaining = %v", groups["seven-day-opus"].RemainingFraction)
	}
}

// A window the account does not have carries a null percent; reporting it as
// full quota would be worse than omitting it.
func TestClaudeSkipsNullPercent(t *testing.T) {
	groups := claudeGroups(t, `{"limits":[
		{"kind":"session","percent":10,"resets_at":"x"},
		{"kind":"weekly_scoped","percent":null,"scope":{"model":{"display_name":"Opus"}}}
	],"five_hour":null,"seven_day":null}`)
	if len(groups) != 1 {
		t.Fatalf("want only the session window, got %v", groups)
	}
	if _, ok := groups["five-hour"]; !ok {
		t.Fatalf("session window missing: %v", groups)
	}
}

// The point of the rules: a window Anthropic adds later is picked up from its
// own description, with no code change.
func TestClaudeUnknownScopedWindowAppears(t *testing.T) {
	groups := claudeGroups(t, `{"limits":[
		{"kind":"weekly_scoped","percent":40,"resets_at":"2026-09-01T00:00:00Z",
		 "scope":{"model":{"display_name":"Some New Model"}}}
	]}`)
	g, ok := groups["seven-day-some-new-model"]
	if !ok {
		t.Fatalf("unknown scoped window dropped: %v", groups)
	}
	if g.Label != "7d Some New Model" {
		t.Fatalf("label = %q", g.Label)
	}
}

// A deployment can describe a payload this build has never seen.
func TestQuotaRulesAreConfigurable(t *testing.T) {
	var rules []quotaRule
	if err := json.Unmarshal([]byte(`[
		{"array":"buckets","id":"seven-day-{name}","label":"7d {name}",
		 "percent":"left","percent_means":"remaining","reset":"until",
		 "name_from":"model","name_fallback":"Scoped"}
	]`), &rules); err != nil {
		t.Fatalf("unmarshal rules: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(`{"buckets":[
		{"model":"Fable","left":25,"until":1788000000}
	]}`), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	groups := applyQuotaRules(payload, rules)
	if len(groups) != 1 {
		t.Fatalf("groups = %+v", groups)
	}
	// percent_means: remaining is taken as-is rather than inverted.
	if math.Abs(groups[0].RemainingFraction-0.25) > 1e-9 {
		t.Fatalf("remaining = %v, want 0.25", groups[0].RemainingFraction)
	}
	if groups[0].ID != "seven-day-fable" {
		t.Fatalf("id = %q", groups[0].ID)
	}
	// Unix seconds are accepted for resets as well as RFC 3339 text.
	if groups[0].ResetTime == "" {
		t.Fatal("reset time not parsed from unix seconds")
	}
}

func TestQuotaRulePathLookup(t *testing.T) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(`{"a":{"b":[{"c":7}]}}`), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, ok := lookupFloat(payload, "a.b.0.c"); !ok || got != 7 {
		t.Fatalf("lookupFloat = %v, %v", got, ok)
	}
	if _, ok := lookupFloat(payload, "a.b.9.c"); ok {
		t.Fatal("out-of-range index should not resolve")
	}
	if _, ok := lookupFloat(payload, "a.missing"); ok {
		t.Fatal("missing path should not resolve")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Fable":      "fable",
		"Claude 5.5": "claude-55",
		"  Sonnet  ": "sonnet",
		"!!!":        "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
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
	if normalizeProvider("Copilot") != "github-copilot" {
		t.Fatal("expected copilot to normalize to github-copilot")
	}
	// Parity with the sidecar: these providers report real quota.
	for _, provider := range []string{"anthropic", "codex", "antigravity", "github-copilot"} {
		if !providerSupported(provider) {
			t.Fatalf("%s must be supported", provider)
		}
	}
	if providerSupported("unknown") {
		t.Fatal("unknown providers must be reported as unsupported")
	}
}

// Antigravity reports per-model quota; a group must surface the limit the
// user hits first and keep the model breakdown the sidecar exposed.
func TestGroupAntigravityModels(t *testing.T) {
	groups := groupAntigravityModels([]quotaModel{
		{ID: "gemini-3.6-flash-high", RemainingFraction: 0.8, ResetTime: "2026-08-09T12:05:17Z"},
		{ID: "gemini-3.6-flash-medium", RemainingFraction: 0.4, ResetTime: "2026-08-08T12:05:17Z"},
		{ID: "gemini-3.6-pro", RemainingFraction: 0.9, ResetTime: "2026-08-10T12:05:17Z"},
	})

	if len(groups) != 2 {
		t.Fatalf("expected flash and pro groups, got %d", len(groups))
	}

	var flash *quotaGroup
	for i := range groups {
		if groups[i].Label == "Flash Models" {
			flash = &groups[i]
		}
	}
	if flash == nil {
		t.Fatal("missing flash group")
	}
	if flash.RemainingFraction != 0.4 {
		t.Fatalf("group fraction = %v, want the lowest model fraction 0.4", flash.RemainingFraction)
	}
	if flash.ResetTime != "2026-08-08T12:05:17Z" {
		t.Fatalf("group reset = %q, want the earliest reset", flash.ResetTime)
	}
	if len(flash.Models) != 2 {
		t.Fatalf("expected the model breakdown to be preserved, got %d", len(flash.Models))
	}
}

// An account without a secondary window must not be reported as having full
// quota there: the sidecar omits the window entirely.
func TestCodexOmitsAbsentWindow(t *testing.T) {
	var payload codexUsagePayload
	if err := json.Unmarshal([]byte(`{"rate_limit":{
		"primary_window":{"used_percent":76,"reset_at":1785000000,"limit_window_seconds":18000},
		"secondary_window":{"used_percent":null}}}`), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.RateLimit.Secondary.UsedPercent != nil {
		t.Fatal("absent window should decode to a nil percentage")
	}
	if payload.RateLimit.Primary.UsedPercent == nil || *payload.RateLimit.Primary.UsedPercent != 76 {
		t.Fatal("primary window percentage was not decoded")
	}
}

// Window labels come from the reported duration, since the same slot means
// different things per account.
func TestCodexWindowLabel(t *testing.T) {
	hours5 := int64(18000)
	week := int64(7 * 24 * 60 * 60)
	cases := []struct {
		seconds *int64
		want    string
	}{
		{nil, "Primary Window"},
		{&hours5, "5h Window"},
		{&week, "7d Window"},
	}
	for _, c := range cases {
		if got := codexWindowLabel(c.seconds, "Primary"); got != c.want {
			t.Fatalf("codexWindowLabel = %q, want %q", got, c.want)
		}
	}
}

// Codex reports resets as a unix timestamp, not a formatted string.
func TestUnixToRFC3339(t *testing.T) {
	ts := int64(1785000000)
	if got := unixToRFC3339(&ts); got != "2026-07-25T17:20:00Z" {
		t.Fatalf("unixToRFC3339 = %q", got)
	}
	if got := unixToRFC3339(nil); got != "" {
		t.Fatalf("nil timestamp should render empty, got %q", got)
	}
}

// Pi renders the models array, so single-window providers must still fill it.
func TestSimpleGroupCarriesModels(t *testing.T) {
	group := simpleGroup("five-hour", "5h Session", 0.5, "2026-08-02T18:40:00Z")
	if len(group.Models) != 1 {
		t.Fatalf("expected a mirrored model entry, got %d", len(group.Models))
	}
	if group.Models[0].RemainingFraction != group.RemainingFraction {
		t.Fatal("model fraction must mirror the group")
	}
}

// The sidecar reports a disabled credential as "disabled" even when the host
// recorded a different status, and Pi renders that string directly.
func TestAccountStatusMirrorsSidecar(t *testing.T) {
	cases := []struct {
		file authFile
		want string
	}{
		{authFile{Disabled: true, Status: "error"}, "disabled"},
		{authFile{Disabled: true, Status: ""}, "disabled"},
		{authFile{Status: "active"}, "active"},
		{authFile{Status: ""}, "active"},
		{authFile{Status: "error"}, "error"},
	}
	for _, c := range cases {
		if got := accountStatus(c.file); got != c.want {
			t.Fatalf("accountStatus(%+v) = %q, want %q", c.file, got, c.want)
		}
	}
}

// Group order follows the provider's model ordering, so decoding must not go
// through a map.
func TestDecodeAntigravityModelsPreservesOrder(t *testing.T) {
	raw := []byte(`{
		"gemini-3.6-pro":   {"displayName":"Pro","quotaInfo":{"remainingFraction":0.5,"resetTime":"2026-08-09T00:00:00Z"}},
		"gemini-3.6-flash": {"displayName":"Flash","quotaInfo":{"remainingFraction":1.0}},
		"no-quota-model":   {"displayName":"Skipped"}
	}`)

	models, err := decodeAntigravityModels(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models without quota must be skipped, got %d", len(models))
	}
	if models[0].ID != "gemini-3.6-pro" || models[1].ID != "gemini-3.6-flash" {
		t.Fatalf("payload order was not preserved: %v", models)
	}
	if models[0].DisplayName != "Pro" {
		t.Fatalf("display name = %q", models[0].DisplayName)
	}
}

// The provider returns its catalogue in a different order on every call, so
// group order must come from the plugin, not the payload.
func TestAntigravityGroupOrderIsStable(t *testing.T) {
	forward := groupAntigravityModels([]quotaModel{
		{ID: "gemini-flash-a", RemainingFraction: 1},
		{ID: "gemini-pro-a", RemainingFraction: 1},
		{ID: "gemini-thinking-a", RemainingFraction: 1},
	})
	reversed := groupAntigravityModels([]quotaModel{
		{ID: "gemini-thinking-a", RemainingFraction: 1},
		{ID: "gemini-pro-a", RemainingFraction: 1},
		{ID: "gemini-flash-a", RemainingFraction: 1},
	})

	var a, b []string
	for _, g := range forward {
		a = append(a, g.ID)
	}
	for _, g := range reversed {
		b = append(b, g.ID)
	}
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatalf("group order depends on payload order: %v vs %v", a, b)
	}
	if a[0] != "pro-models" {
		t.Fatalf("expected pro models first, got %v", a)
	}
}
