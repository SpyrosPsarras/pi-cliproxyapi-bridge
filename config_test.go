package main

import (
	"strings"
	"testing"
)

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ManagementKeyEnv != "MANAGEMENT_PASSWORD" {
		t.Fatalf("management key env = %q", cfg.ManagementKeyEnv)
	}
	if len(cfg.clients) != 0 {
		t.Fatal("expected no client keys by default (fail closed)")
	}
	if cfg.UsageTTLSeconds != 60 {
		t.Fatalf("usage ttl = %d", cfg.UsageTTLSeconds)
	}
}

func TestParseConfigFlatFields(t *testing.T) {
	raw := []byte(`
client_keys:
  - abix:` + strings.Repeat("a", 64) + `
  - team:` + strings.Repeat("b", 64) + `:usage+analytics
management_url: http://127.0.0.1:8317/v0/management
cpam_enabled: auto
cpam_url: http://cpa-manager-plus:18317
usage_ttl_seconds: 90
`)
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(cfg.clients) != 2 {
		t.Fatalf("expected two clients, got %d", len(cfg.clients))
	}
	if cfg.UsageTTLSeconds != 90 {
		t.Fatalf("usage ttl = %d", cfg.UsageTTLSeconds)
	}

	abix := cfg.clients[0]
	if abix.ID != "abix" || !abix.allows(permissionUsage) || abix.allows(permissionAnalytics) {
		t.Fatalf("unexpected default permissions: %+v", abix)
	}
	team := cfg.clients[1]
	if !team.allows(permissionUsage) || !team.allows(permissionAnalytics) {
		t.Fatalf("expected both permissions, got %v", team.Permissions)
	}
}

// The sha256: prefix is optional and must not be mistaken for a field separator.
func TestParseClientKeyAcceptsPrefixedFingerprint(t *testing.T) {
	digest := strings.Repeat("c", 64)
	for _, entry := range []string{"abix:" + digest, "abix:sha256:" + digest} {
		clients, err := parseClientKeys([]string{entry})
		if err != nil {
			t.Fatalf("parseClientKeys(%q): %v", entry, err)
		}
		if len(clients) != 1 || clients[0].Fingerprint != digest {
			t.Fatalf("entry %q produced %+v", entry, clients)
		}
	}
}

func TestParseClientKeyWithPrefixAndPermissions(t *testing.T) {
	digest := strings.Repeat("d", 64)
	clients, err := parseClientKeys([]string{"abix:sha256:" + digest + ":usage+analytics"})
	if err != nil {
		t.Fatalf("parseClientKeys: %v", err)
	}
	if !clients[0].allows(permissionAnalytics) {
		t.Fatalf("expected analytics permission, got %v", clients[0].Permissions)
	}
}

func TestParseConfigRejectsBadInput(t *testing.T) {
	valid := strings.Repeat("a", 64)
	cases := map[string]string{
		"missing fingerprint": "client_keys:\n  - abix\n",
		"short fingerprint":   "client_keys:\n  - abix:abc\n",
		"raw key as print":    "client_keys:\n  - abix:sk-plain-key-value\n",
		"bad alias":           "client_keys:\n  - 'a b':" + valid + "\n",
		"duplicate alias":     "client_keys:\n  - abix:" + valid + "\n  - abix:" + strings.Repeat("b", 64) + "\n",
		"duplicate print":     "client_keys:\n  - abix:" + valid + "\n  - other:" + valid + "\n",
		"invalid cpam mode":   "cpam_enabled: sometimes\n",
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConfig([]byte(raw)); err == nil {
				t.Fatal("expected configuration to be rejected")
			}
		})
	}
}
