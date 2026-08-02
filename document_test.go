package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The Pi client iterates account groups directly, so a null array would break
// rendering. Every account must serialize groups as a list.
func TestUsageAccountAlwaysSerializesGroupsArray(t *testing.T) {
	doc := usageDocument{
		SchemaVersion: 1,
		Accounts: []usageAccount{
			{Provider: "codex", Supported: true, Groups: []quotaGroup{}},
			{Provider: "antigravity", Supported: false, Groups: []quotaGroup{}},
		},
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"groups":null`) {
		t.Fatalf("document contains a null groups array: %s", raw)
	}

	var round usageDocument
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, account := range round.Accounts {
		if account.Groups == nil {
			t.Fatalf("provider %s decoded with nil groups", account.Provider)
		}
	}
}

// The sidecar always emits unsupportedProviders as an array, and the Pi client
// iterates it, so a null must never be served.
func TestUsageDocumentAlwaysSerializesUnsupportedArray(t *testing.T) {
	raw, err := json.Marshal(usageDocument{
		SchemaVersion: 1,
		Accounts:      []usageAccount{},
		Unsupported:   []string{},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"unsupportedProviders":null`) {
		t.Fatalf("document contains a null unsupportedProviders array: %s", raw)
	}
	if !strings.Contains(string(raw), `"unsupportedProviders":[]`) {
		t.Fatalf("expected an empty array: %s", raw)
	}
}

// Contract v1 must be byte-compatible with the sidecar, which has no client or
// cache section.
func TestContractV1OmitsV2Sections(t *testing.T) {
	raw, err := json.Marshal(usageDocument{
		SchemaVersion: 1,
		Accounts:      []usageAccount{},
		Unsupported:   []string{},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, section := range []string{`"client"`, `"cache"`} {
		if strings.Contains(string(raw), section) {
			t.Fatalf("v1 document must not contain %s: %s", section, raw)
		}
	}
}
