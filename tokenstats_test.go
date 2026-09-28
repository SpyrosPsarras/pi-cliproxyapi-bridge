package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func resetTokenStats() {
	tokenStatsMu.Lock()
	defer tokenStatsMu.Unlock()
	tokenStats = map[tokenKey]*modelTokenUsage{}
}

func TestRecordTokenUsageAccumulatesPerModel(t *testing.T) {
	resetTokenStats()
	recordTokenUsage(usageRecordJSON(t, `{"Provider":"claude","Model":"claude-opus-5","Detail":{"InputTokens":100,"OutputTokens":20,"TotalTokens":120}}`))
	recordTokenUsage(usageRecordJSON(t, `{"Provider":"claude","Model":"claude-opus-5","Detail":{"InputTokens":10,"OutputTokens":5,"TotalTokens":15}}`))
	recordTokenUsage(usageRecordJSON(t, `{"Provider":"claude","Model":"claude-sonnet-5","Detail":{"InputTokens":7,"OutputTokens":3,"TotalTokens":10}}`))

	snap := tokenModelSnapshot()
	if len(snap) != 2 {
		t.Fatalf("models = %d, want 2", len(snap))
	}
	first := snap[0]
	if first.Model != "claude-opus-5" || first.Input != 110 || first.Output != 25 || first.Total != 135 {
		t.Fatalf("opus totals wrong: %+v", first)
	}
}

func TestRecordTokenUsageNormalizesProviderAndSkipsEmpty(t *testing.T) {
	resetTokenStats()
	recordTokenUsage(usageRecordJSON(t, `{"Provider":"anthropic","Model":"claude-opus-5","Detail":{"TotalTokens":9}}`))
	recordTokenUsage(usageRecordJSON(t, `{"Provider":"claude","Model":"","Detail":{"TotalTokens":9}}`))
	recordTokenUsage(usageRecordJSON(t, `{"Provider":"","Model":"m","Detail":{"TotalTokens":9}}`))

	snap := tokenModelSnapshot()
	if len(snap) != 1 || snap[0].Provider != "claude" {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}

func TestRecordTokenUsageFallsBackToSummedTotal(t *testing.T) {
	resetTokenStats()
	recordTokenUsage(usageRecordJSON(t, `{"Provider":"codex","Model":"gpt-5.6","Detail":{"InputTokens":40,"OutputTokens":8}}`))
	snap := tokenModelSnapshot()
	if len(snap) != 1 || snap[0].Total != 48 {
		t.Fatalf("snapshot = %+v, want one model with total 48", snap)
	}
}

// The host sets CachedTokens and CacheReadTokens to the same cache-read count,
// so summing them would report twice the real number.
func TestRecordTokenUsageCountsCachedAndReasoningOnce(t *testing.T) {
	resetTokenStats()
	recordTokenUsage(usageRecordJSON(t, `{"Provider":"claude","Model":"claude-opus-5","Detail":{"InputTokens":10,"OutputTokens":4,"ReasoningTokens":3,"CachedTokens":80,"CacheReadTokens":80,"TotalTokens":94}}`))
	recordTokenUsage(usageRecordJSON(t, `{"Provider":"claude","Model":"claude-opus-5","Detail":{"InputTokens":5,"OutputTokens":1,"CachedTokens":30,"CacheCreationTokens":30,"TotalTokens":36}}`))

	snap := tokenModelSnapshot()
	if len(snap) != 1 {
		t.Fatalf("models = %d, want 1", len(snap))
	}
	if snap[0].Cached != 110 || snap[0].Reasoning != 3 {
		t.Fatalf("cached = %d (want 110), reasoning = %d (want 3)", snap[0].Cached, snap[0].Reasoning)
	}
}

func TestShapeForContractAttachesModelsOnlyForV2(t *testing.T) {
	resetTokenStats()
	recordTokenUsage(usageRecordJSON(t, `{"Provider":"claude","Model":"claude-opus-5","Detail":{"TotalTokens":500}}`))

	cached, err := json.Marshal(usageDocument{
		SchemaVersion: 1,
		Accounts:      []usageAccount{},
		Unsupported:   []string{},
	})
	if err != nil {
		t.Fatal(err)
	}

	v1 := shapeForContract(cached, contractV1, authenticatedClient{KeyHint: "k"}, time.Now(), time.Minute)
	if !bytes.Equal(v1, cached) {
		t.Fatalf("v1 must stay byte-compatible:\n got %s\nwant %s", v1, cached)
	}

	v2 := shapeForContract(cached, contractLatest, authenticatedClient{KeyHint: "k"}, time.Now(), time.Minute)
	var docV2 usageDocument
	if err := json.Unmarshal(v2, &docV2); err != nil {
		t.Fatal(err)
	}
	if len(docV2.Models) != 1 || docV2.Models[0].Total != 500 || docV2.Models[0].Model != "claude-opus-5" {
		t.Fatalf("v2 models wrong: %+v", docV2.Models)
	}
	if docV2.Client == nil || docV2.Client.KeyHint != "k" {
		t.Fatalf("v2 client hint lost: %+v", docV2.Client)
	}
}

// The host only dispatches usage.handle to plugins that declare the
// capability at registration. A missing flag here is silent: the plugin loads,
// serves quota, and never sees a token counter.
func TestRegistrationDeclaresUsagePluginCapability(t *testing.T) {
	reg := pluginRegistration()
	if !reg.Capabilities.UsagePlugin {
		t.Fatal("registration does not declare usage_plugin; the host will never deliver usage.handle")
	}
	if !reg.Capabilities.ManagementAPI {
		t.Fatal("registration lost the management_api capability")
	}
}

func usageRecordJSON(t *testing.T, raw string) usageRecord {
	t.Helper()
	var rec usageRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}
