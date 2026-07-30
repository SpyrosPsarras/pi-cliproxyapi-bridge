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

func TestUsageDocumentOmitsEmptyUnsupportedList(t *testing.T) {
	raw, err := json.Marshal(usageDocument{SchemaVersion: 1, Accounts: []usageAccount{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "unsupportedProviders") {
		t.Fatalf("expected unsupportedProviders to be omitted: %s", raw)
	}
}
