package main

import (
	"encoding/json"
	"testing"
)

func withCatalog(t *testing.T, entries map[string]catalogEntry) {
	t.Helper()
	catalog.mu.Lock()
	previous := catalog.entries
	catalog.entries = entries
	catalog.mu.Unlock()
	t.Cleanup(func() {
		catalog.mu.Lock()
		catalog.entries = previous
		catalog.mu.Unlock()
	})
}

func boolPtr(v bool) *bool { return &v }

// models.dev supplies metadata when the proxy id matches a catalogue id.
func TestResolveModelMetaFromCatalog(t *testing.T) {
	withCatalog(t, map[string]catalogEntry{
		"gpt-5.6-sol": {Name: "GPT-5.6 Sol", ContextWindow: 1_050_000, MaxTokens: 128_000},
	})

	entry, source := resolveModelMeta(defaultConfig(), nil, "gpt-5.6-sol")
	if source != "catalog" {
		t.Fatalf("source = %q, want catalog", source)
	}
	if entry.ContextWindow != 1_050_000 {
		t.Fatalf("context window = %d", entry.ContextWindow)
	}
}

// Proxy ids often differ from catalogue ids, so an alias rebinds the lookup.
func TestResolveModelMetaViaAlias(t *testing.T) {
	withCatalog(t, map[string]catalogEntry{
		"gpt-5.6-sol": {ContextWindow: 1_050_000},
	})

	cfg := defaultConfig()
	cfg.advanced.ModelAliases = map[string]string{"house-model": "gpt-5.6-sol"}

	entry, source := resolveModelMeta(cfg, nil, "house-model")
	if source != "alias" {
		t.Fatalf("source = %q, want alias", source)
	}
	if entry.ContextWindow != 1_050_000 {
		t.Fatalf("alias did not carry catalogue metadata: %+v", entry)
	}
}

// CPAM is operator-curated, so it outranks models.dev.
func TestCPAMOutranksCatalog(t *testing.T) {
	withCatalog(t, map[string]catalogEntry{
		"claude-opus-4-8": {ContextWindow: 200_000, Cost: &modelCost{Input: 99}},
	})
	cpam := map[string]catalogEntry{
		"claude-opus-4-8": {ContextWindow: 1_000_000, Cost: &modelCost{Input: 5, Output: 25}},
	}

	entry, source := resolveModelMeta(defaultConfig(), cpam, "claude-opus-4-8")
	if source != "cpam" {
		t.Fatalf("source = %q, want cpam", source)
	}
	if entry.Cost.Input != 5 || entry.ContextWindow != 1_000_000 {
		t.Fatalf("CPAM values were not preferred: %+v", entry)
	}
}

// A partial CPAM row must still pick up what only models.dev knows.
func TestCPAMGapsFallBackToCatalog(t *testing.T) {
	withCatalog(t, map[string]catalogEntry{
		"shared-model": {Name: "Shared", ContextWindow: 128_000, Reasoning: boolPtr(true)},
	})
	cpam := map[string]catalogEntry{
		"shared-model": {Cost: &modelCost{Input: 1, Output: 2}},
	}

	entry, _ := resolveModelMeta(defaultConfig(), cpam, "shared-model")
	if entry.Cost.Input != 1 {
		t.Fatal("expected the CPAM price to win")
	}
	if entry.ContextWindow != 128_000 || entry.Name != "Shared" {
		t.Fatalf("expected catalogue to fill the gaps: %+v", entry)
	}
}

// An explicit override is the last word, and only touches the fields it sets.
func TestOverrideWinsAndIsPartial(t *testing.T) {
	withCatalog(t, map[string]catalogEntry{
		"my-model": {Name: "Catalogue name", ContextWindow: 128_000, MaxTokens: 8_000},
	})

	cfg := defaultConfig()
	cfg.advanced.ModelOverrides = map[string]modelOverride{
		"my-model": {ContextWindow: 262_144},
	}

	entry, source := resolveModelMeta(cfg, nil, "my-model")
	if source != "override" {
		t.Fatalf("source = %q, want override", source)
	}
	if entry.ContextWindow != 262_144 {
		t.Fatalf("override was not applied: %d", entry.ContextWindow)
	}
	if entry.MaxTokens != 8_000 || entry.Name != "Catalogue name" {
		t.Fatalf("override discarded unrelated fields: %+v", entry)
	}
}

// Unknown models must survive with defaults rather than being dropped.
func TestUnknownModelKeepsDefaults(t *testing.T) {
	withCatalog(t, map[string]catalogEntry{})

	entry, source := resolveModelMeta(defaultConfig(), nil, "mystery-model")
	if source != "" {
		t.Fatalf("source = %q, want empty", source)
	}

	model := buildCustomModel(
		upstreamModel{ID: "mystery-model", OwnedBy: "Xiaomi"},
		entry, false, "", map[string]string{"openai-completions": "https://example.test/v1"},
	)
	if model.ContextWindow != defaultContextWindow || model.MaxTokens != defaultMaxTokens {
		t.Fatalf("expected defaults, got %+v", model)
	}
	if model.MetadataSource != "default" {
		t.Fatalf("metadata source = %q, want default", model.MetadataSource)
	}
}

// Contract v1 must match the sidecar, which had no v2 sections.
func TestDiscoveryV1DropsV2Sections(t *testing.T) {
	encoded, err := json.Marshal(discoveryDocument{
		SchemaVersion:    1,
		BuiltinProviders: map[string]builtinProvider{},
		CustomModelPool: []customModel{
			{ID: "m", FromCatalog: true, MetadataSource: "catalog"},
		},
		Upstream:  &discoveryUpstream{UpstreamVersion: "v7.2.98"},
		Catalog:   &catalogStatus{Source: "models.dev"},
		Providers: []discoveryProviderHealth{{Provider: "codex"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var v1 map[string]any
	if err := json.Unmarshal(shapeDiscovery(encoded, contractV1), &v1); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, section := range []string{"upstream", "catalog", "providerHealth"} {
		if _, present := v1[section]; present {
			t.Fatalf("v1 document must not carry %q", section)
		}
	}
	pool := v1["customModelPool"].([]any)[0].(map[string]any)
	if _, present := pool["metadataSource"]; present {
		t.Fatal("v1 models must not carry metadataSource")
	}

	// v2 keeps everything.
	var v2 map[string]any
	if err := json.Unmarshal(shapeDiscovery(encoded, contractLatest), &v2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := v2["catalog"]; !present {
		t.Fatal("v2 document lost the catalog section")
	}
}

// A model published by both its maker and a reseller must keep the maker's
// limits, regardless of map iteration order.
func TestCatalogPrefersFirstPartyProvider(t *testing.T) {
	entry := func(context, output int) modelsDevEntry {
		var e modelsDevEntry
		e.Limit.Context, e.Limit.Output = context, output
		return e
	}
	payload := modelsDevPayload{
		"xiaomi":   {Models: map[string]modelsDevEntry{"mimo-v2-flash": entry(262_144, 65_536)}},
		"qiniu-ai": {Models: map[string]modelsDevEntry{"mimo-v2-flash": entry(256_000, 256_000)}},
	}

	// Repeat: Go randomizes map iteration, so a wrong rule passes sometimes.
	for i := 0; i < 20; i++ {
		if got := flattenCatalog(payload)["mimo-v2-flash"].ContextWindow; got != 262_144 {
			t.Fatalf("context window = %d, want the first-party 262144", got)
		}
	}
}

// A catalogue row with zeroed limits is real data, not a miss, so it must not
// be replaced with the generic defaults.
func TestZeroedCatalogLimitsAreNotOverwritten(t *testing.T) {
	model := buildCustomModel(
		upstreamModel{ID: "voxtral-mini-latest", OwnedBy: "Mistral"},
		catalogEntry{ContextWindow: 0, MaxTokens: 0},
		true, "catalog",
		map[string]string{"openai-completions": "https://example.test/v1"},
	)
	if model.ContextWindow != 0 || model.MaxTokens != 0 {
		t.Fatalf("zeroed catalogue limits were replaced: %+v", model)
	}
}

// Popular open models are republished by dozens of hosts with differing limits.
// The winner must be the same on every run, or a restart silently changes a
// model's context window.
func TestCatalogChoiceIsDeterministic(t *testing.T) {
	entry := func(context, output int) modelsDevEntry {
		var e modelsDevEntry
		e.Limit.Context, e.Limit.Output = context, output
		return e
	}
	payload := modelsDevPayload{}
	for i, provider := range []string{"greenpt", "qvac", "neon", "hyper", "scaleway", "cortecs"} {
		payload[provider] = struct {
			Models map[string]modelsDevEntry `json:"models"`
		}{Models: map[string]modelsDevEntry{"gpt-oss-120b": entry(131_072, 32_768+i)}}
	}

	first := flattenCatalog(payload)["gpt-oss-120b"]
	for i := 0; i < 30; i++ {
		if got := flattenCatalog(payload)["gpt-oss-120b"]; got != first {
			t.Fatalf("catalogue choice varies between runs: %+v vs %+v", got, first)
		}
	}
}

// With no first-party entry, the limits most hosts agree on win, since
// agreement is better evidence than any single reseller.
func TestCatalogUsesConsensusWithoutVendor(t *testing.T) {
	entry := func(context, output int) modelsDevEntry {
		var e modelsDevEntry
		e.Limit.Context, e.Limit.Output = context, output
		return e
	}
	common := entry(131_072, 32_768)
	payload := modelsDevPayload{
		"aki-io":  {Models: map[string]modelsDevEntry{"gpt-oss-120b": entry(128_000, 32_768)}},
		"greenpt": {Models: map[string]modelsDevEntry{"gpt-oss-120b": common}},
		"neon":    {Models: map[string]modelsDevEntry{"gpt-oss-120b": common}},
		"qvac":    {Models: map[string]modelsDevEntry{"gpt-oss-120b": common}},
	}

	for i := 0; i < 20; i++ {
		got := flattenCatalog(payload)["gpt-oss-120b"]
		if got.ContextWindow != 131_072 || got.MaxTokens != 32_768 {
			t.Fatalf("expected the consensus limits, got %+v", got)
		}
	}
}

// A first-party entry still wins outright, even against a popular consensus.
func TestVendorBeatsConsensus(t *testing.T) {
	entry := func(context, output int) modelsDevEntry {
		var e modelsDevEntry
		e.Limit.Context, e.Limit.Output = context, output
		return e
	}
	resold := entry(1_000_000, 128_000)
	payload := modelsDevPayload{
		"xiaomi":      {Models: map[string]modelsDevEntry{"mimo-v2.5": entry(1_048_576, 131_072)}},
		"opencode-go": {Models: map[string]modelsDevEntry{"mimo-v2.5": resold}},
		"llmgateway":  {Models: map[string]modelsDevEntry{"mimo-v2.5": resold}},
	}

	for i := 0; i < 20; i++ {
		if got := flattenCatalog(payload)["mimo-v2.5"].ContextWindow; got != 1_048_576 {
			t.Fatalf("context window = %d, want the vendor's 1048576", got)
		}
	}
}

// Consensus covers capability flags too: one dissenting host must not flip
// reasoning off for a model eighteen others report as reasoning-capable.
func TestConsensusCoversReasoningFlag(t *testing.T) {
	yes, no := true, false
	entry := func(reasoning *bool) modelsDevEntry {
		var e modelsDevEntry
		e.Limit.Context, e.Limit.Output = 131_072, 32_768
		e.Reasoning = reasoning
		return e
	}
	payload := modelsDevPayload{
		"greenpt": {Models: map[string]modelsDevEntry{"gpt-oss-120b": entry(&yes)}},
		"neon":    {Models: map[string]modelsDevEntry{"gpt-oss-120b": entry(&yes)}},
		"qvac":    {Models: map[string]modelsDevEntry{"gpt-oss-120b": entry(&yes)}},
		"aki-io":  {Models: map[string]modelsDevEntry{"gpt-oss-120b": entry(&no)}},
	}

	for i := 0; i < 20; i++ {
		got := flattenCatalog(payload)["gpt-oss-120b"]
		if got.Reasoning == nil || !*got.Reasoning {
			t.Fatalf("expected reasoning to follow the majority, got %v", got.Reasoning)
		}
	}
}
