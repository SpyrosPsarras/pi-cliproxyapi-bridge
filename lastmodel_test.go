package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func resetLastModels() {
	lastModelsMu.Lock()
	defer lastModelsMu.Unlock()
	lastModels = map[string]lastModelEntry{}
}

func TestRecordLastModelKeepsNewestRequestPerAccount(t *testing.T) {
	resetLastModels()
	recordLastModel(usageRecordJSON(t, `{"Model":"claude-opus-5","AuthIndex":"a1","RequestedAt":"2020-01-01T10:00:00Z"}`))
	recordLastModel(usageRecordJSON(t, `{"Model":"claude-sonnet-5","AuthIndex":"a1","RequestedAt":"2020-01-01T10:05:00Z"}`))
	recordLastModel(usageRecordJSON(t, `{"Model":"gpt-5.6","AuthIndex":"b2","RequestedAt":"2020-01-01T10:01:00Z"}`))

	if got := lastModelFor("a1"); got != "claude-sonnet-5" {
		t.Fatalf("a1 last model = %q, want claude-sonnet-5", got)
	}
	if got := lastModelFor("b2"); got != "gpt-5.6" {
		t.Fatalf("b2 last model = %q, want gpt-5.6", got)
	}
}

// The host dispatches usage records asynchronously, so an older request can
// arrive after a newer one. It must not overwrite the newer model.
func TestRecordLastModelIgnoresOlderRecordArrivingLate(t *testing.T) {
	resetLastModels()
	recordLastModel(usageRecordJSON(t, `{"Model":"claude-sonnet-5","AuthIndex":"a1","RequestedAt":"2020-01-01T10:05:00Z"}`))
	recordLastModel(usageRecordJSON(t, `{"Model":"claude-opus-5","AuthIndex":"a1","RequestedAt":"2020-01-01T10:00:00Z"}`))

	if got := lastModelFor("a1"); got != "claude-sonnet-5" {
		t.Fatalf("last model = %q, want claude-sonnet-5", got)
	}
}

// Codex publishes the image tool's tokens as a second record with the same
// credential and request time. The request's own model must stay.
func TestRecordLastModelKeepsRequestModelOverFollowUpRecord(t *testing.T) {
	resetLastModels()
	recordLastModel(usageRecordJSON(t, `{"Model":"gpt-5.5","AuthIndex":"a1","RequestedAt":"2020-01-01T10:00:00.123456789Z"}`))
	recordLastModel(usageRecordJSON(t, `{"Model":"gpt-image-2","AuthIndex":"a1","RequestedAt":"2020-01-01T10:00:00.123456789Z"}`))

	if got := lastModelFor("a1"); got != "gpt-5.5" {
		t.Fatalf("last model = %q, want gpt-5.5", got)
	}
}

func TestRecordLastModelSkipsRecordsWithoutAccountOrModel(t *testing.T) {
	resetLastModels()
	recordLastModel(usageRecordJSON(t, `{"Model":"claude-opus-5","AuthIndex":"","RequestedAt":"2020-01-01T10:00:00Z"}`))
	recordLastModel(usageRecordJSON(t, `{"Model":"","AuthIndex":"a1","RequestedAt":"2020-01-01T10:00:00Z"}`))

	if got := lastModelFor(""); got != "" {
		t.Fatalf("empty auth index recorded %q", got)
	}
	if got := lastModelFor("a1"); got != "" {
		t.Fatalf("empty model recorded %q", got)
	}
}

// A record without a request time still counts as the newest one seen.
func TestRecordLastModelTreatsMissingTimeAsNow(t *testing.T) {
	resetLastModels()
	recordLastModel(usageRecordJSON(t, `{"Model":"claude-opus-5","AuthIndex":"a1","RequestedAt":"2020-01-01T10:00:00Z"}`))
	recordLastModel(usageRecordJSON(t, `{"Model":"claude-haiku-5","AuthIndex":"a1"}`))

	if got := lastModelFor("a1"); got != "claude-haiku-5" {
		t.Fatalf("last model = %q, want claude-haiku-5", got)
	}
}

// lastModel is read when the document is built, so the cached document pairs
// it with the same snapshot's lastRequestAt.
func TestBuildUsageAttachesLastModelPerAccount(t *testing.T) {
	resetLastModels()
	recordLastModel(usageRecordJSON(t, `{"Model":"some-model-1","AuthIndex":"a1","RequestedAt":"2020-01-01T10:00:00Z"}`))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth-files" {
			http.NotFound(w, r)
			return
		}
		// Unsupported providers skip the quota passthrough.
		_, _ = w.Write([]byte(`{"files":[
			{"auth_index":"a1","provider":"other","updated_at":"2020-01-01T10:00:01Z"},
			{"auth_index":"b2","provider":"other"}]}`))
	}))
	defer server.Close()
	t.Setenv("PI_BRIDGE_TEST_MANAGEMENT_KEY", "test")

	var cfg pluginConfig
	cfg.advanced.ManagementURL = server.URL
	cfg.advanced.ManagementKeyEnv = "PI_BRIDGE_TEST_MANAGEMENT_KEY"

	doc, err := buildUsage(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildUsage: %v", err)
	}
	if len(doc.Accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(doc.Accounts))
	}
	if doc.Accounts[0].LastModel != "some-model-1" {
		t.Fatalf("a1 lastModel = %q, want some-model-1", doc.Accounts[0].LastModel)
	}
	if doc.Accounts[1].LastModel != "" {
		t.Fatalf("b2 has no request yet, got lastModel %q", doc.Accounts[1].LastModel)
	}
}

func lastModelDocJSON(t *testing.T, lastModel string) []byte {
	t.Helper()
	raw, err := json.Marshal(usageDocument{
		SchemaVersion: 1,
		Accounts: []usageAccount{
			{Provider: "codex", AuthIndex: "a1", LastRequestAt: "2020-01-01T10:00:00Z", LastModel: lastModel, Supported: true, Groups: []quotaGroup{}},
			{Provider: "codex", AuthIndex: "b2", Supported: true, Groups: []quotaGroup{}},
		},
		Unsupported: []string{},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// v2 serves the cached lastModel as is. A newer request since the document was
// cached must not pair a newer model with the cached lastRequestAt.
func TestShapeForContractV2ServesCachedLastModel(t *testing.T) {
	resetLastModels()
	recordLastModel(usageRecordJSON(t, `{"Model":"newer-model","AuthIndex":"a1","RequestedAt":"2020-01-01T10:05:00Z"}`))

	shaped := shapeForContract(lastModelDocJSON(t, "cached-model"), contractLatest, authenticatedClient{}, time.Now(), time.Minute)
	var doc usageDocument
	if err := json.Unmarshal(shaped, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Accounts[0].LastModel != "cached-model" {
		t.Fatalf("a1 lastModel = %q, want cached-model", doc.Accounts[0].LastModel)
	}
	if strings.Count(string(shaped), `"lastModel"`) != 1 {
		t.Fatalf("only a1 has served a request: %s", shaped)
	}
}

// v1 must stay byte-identical to the document without lastModel.
func TestShapeForContractV1StripsLastModel(t *testing.T) {
	shaped := shapeForContract(lastModelDocJSON(t, "cached-model"), contractV1, authenticatedClient{}, time.Now(), time.Minute)
	if want := lastModelDocJSON(t, ""); string(shaped) != string(want) {
		t.Fatalf("v1 payload:\n got %s\nwant %s", shaped, want)
	}
}
