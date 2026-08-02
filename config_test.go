package main

import (
	"testing"
)

// The plugin must work as soon as it is enabled, with no configuration.
func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if !cfg.allowAll() {
		t.Fatal("quota should be readable by every API key by default")
	}
	if cfg.ShowExtraAnalytics {
		t.Fatal("extra analytics should be off by default")
	}
	if cfg.advanced.ManagementKeyEnv != "MANAGEMENT_PASSWORD" {
		t.Fatalf("management key env = %q", cfg.advanced.ManagementKeyEnv)
	}
	if cfg.advanced.UsageTTLSeconds != 60 || cfg.advanced.CapabilitiesTTLSeconds != 300 {
		t.Fatalf("unexpected cache defaults: %+v", cfg.advanced)
	}
}

func TestParseConfigEverydayFields(t *testing.T) {
	const key = "sk-one-0123456789abcdef"
	cfg, err := parseConfig([]byte(`
allow_all_api_keys: false
` + keyFieldName(key) + `: true
` + keyFieldName("sk-two-0123456789abcdef") + `: false
show_extra_analytics: true
`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.allowAll() {
		t.Fatal("expected the selection to be enforced")
	}
	if !cfg.keySelected(key) {
		t.Fatal("expected the ticked key to be selected")
	}
	if cfg.keySelected("sk-two-0123456789abcdef") {
		t.Fatal("an unticked key must not be selected")
	}
	if !cfg.ShowExtraAnalytics {
		t.Fatal("expected extra analytics to be enabled")
	}
}

// Advanced settings are optional and only override the specific defaults named.
func TestAdvancedOverridesSelectedDefaults(t *testing.T) {
	cfg, err := parseConfig([]byte(`
advanced: '{"usage_ttl_seconds": 15, "cpam_url": "http://cpam:18317/"}'
`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.advanced.UsageTTLSeconds != 15 {
		t.Fatalf("usage ttl = %d, want 15", cfg.advanced.UsageTTLSeconds)
	}
	if cfg.advanced.CPAMURL != "http://cpam:18317" {
		t.Fatalf("cpam url = %q, trailing slash should be trimmed", cfg.advanced.CPAMURL)
	}
	if cfg.advanced.ManagementURL != defaultAdvanced().ManagementURL {
		t.Fatalf("unrelated default was lost: %q", cfg.advanced.ManagementURL)
	}
}

func TestParseConfigRejectsMalformedAdvanced(t *testing.T) {
	if _, err := parseConfig([]byte("advanced: 'not json'\n")); err == nil {
		t.Fatal("expected malformed advanced JSON to be rejected")
	}
}
