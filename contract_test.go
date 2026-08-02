package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A client that knows nothing about contracts must receive v1.
func TestContractDefaultsToV1(t *testing.T) {
	cases := map[string]http.Header{
		"no header":  {},
		"nil header": nil,
		"empty":      {contractHeader: []string{""}},
		"garbage":    {contractHeader: []string{"latest"}},
		"zero":       {contractHeader: []string{"0"}},
		"negative":   {contractHeader: []string{"-2"}},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			if got := contractFrom(headers); got != contractV1 {
				t.Fatalf("contract = %d, want %d", got, contractV1)
			}
		})
	}
}

func TestContractHonoursRequestedVersion(t *testing.T) {
	if got := contractFrom(http.Header{contractHeader: []string{"2"}}); got != 2 {
		t.Fatalf("contract = %d, want 2", got)
	}
	// A newer client than this plugin gets the newest shape it can serve,
	// rather than an error.
	if got := contractFrom(http.Header{contractHeader: []string{"99"}}); got != contractLatest {
		t.Fatalf("contract = %d, want %d", got, contractLatest)
	}
}

func sampleUsageJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(usageDocument{
		SchemaVersion: 1,
		GeneratedAt:   "2026-08-02T10:00:00Z",
		Accounts:      []usageAccount{{Provider: "codex", Supported: true, Groups: []quotaGroup{}}},
		Unsupported:   []string{},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// v1 output must stay exactly what the sidecar served, so an unmigrated client
// sees no change at all.
func TestShapeForContractV1IsUnchanged(t *testing.T) {
	encoded := sampleUsageJSON(t)
	shaped := shapeForContract(encoded, contractV1, authenticatedClient{KeyHint: "sk-abc…1234"}, time.Now(), time.Minute)

	if string(shaped) != string(encoded) {
		t.Fatalf("v1 payload was modified:\n got %s\nwant %s", shaped, encoded)
	}
	if strings.Contains(string(shaped), "sk-abc") {
		t.Fatal("v1 payload must not carry the client hint")
	}
}

// v2 adds cache provenance and the client hint on top of the same accounts.
func TestShapeForContractV2AddsSections(t *testing.T) {
	shaped := shapeForContract(sampleUsageJSON(t), contractLatest,
		authenticatedClient{KeyHint: "sk-abc…1234"}, time.Unix(1785000000, 0), 90*time.Second)

	var doc usageDocument
	if err := json.Unmarshal(shaped, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Client == nil || doc.Client.KeyHint != "sk-abc…1234" {
		t.Fatalf("expected the client hint, got %+v", doc.Client)
	}
	if doc.Cache == nil || doc.Cache.TTLMs != 90000 {
		t.Fatalf("expected cache provenance, got %+v", doc.Cache)
	}
	if len(doc.Accounts) != 1 || doc.Accounts[0].Provider != "codex" {
		t.Fatal("v2 must preserve the account payload")
	}
}
