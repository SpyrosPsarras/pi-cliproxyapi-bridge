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
	if cfg.Management.KeyEnv != "MANAGEMENT_PASSWORD" {
		t.Fatalf("management key env = %q", cfg.Management.KeyEnv)
	}
	if len(cfg.ClientAuth.Keys) != 0 {
		t.Fatal("expected no client keys by default (fail closed)")
	}
	if cfg.Cache.UsageTTLSeconds != 60 {
		t.Fatalf("usage ttl = %d", cfg.Cache.UsageTTLSeconds)
	}
}

func TestParseConfigValidClientKey(t *testing.T) {
	raw := []byte(`
client-auth:
  keys:
    - id: abix
      fingerprint: sha256:` + strings.Repeat("a", 64) + `
      permissions: [usage, analytics]
cpam:
  mode: auto
  base-url: http://cpa-manager-plus:18317
`)
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(cfg.ClientAuth.Keys) != 1 {
		t.Fatalf("expected one key, got %d", len(cfg.ClientAuth.Keys))
	}
	if !cfg.ClientAuth.Keys[0].allows(permissionAnalytics) {
		t.Fatal("expected analytics permission")
	}
}

func TestParseConfigRejectsBadInput(t *testing.T) {
	valid := strings.Repeat("a", 64)
	cases := map[string]string{
		"missing id":        "client-auth:\n  keys:\n    - fingerprint: sha256:" + valid + "\n",
		"short fingerprint": "client-auth:\n  keys:\n    - id: a\n      fingerprint: sha256:abc\n",
		"raw key as print":  "client-auth:\n  keys:\n    - id: a\n      fingerprint: sk-plain-key\n",
		"duplicate id":      "client-auth:\n  keys:\n    - id: a\n      fingerprint: sha256:" + valid + "\n    - id: a\n      fingerprint: sha256:" + strings.Repeat("b", 64) + "\n",
		"duplicate print":   "client-auth:\n  keys:\n    - id: a\n      fingerprint: sha256:" + valid + "\n    - id: b\n      fingerprint: sha256:" + valid + "\n",
		"invalid cpam mode": "cpam:\n  mode: sometimes\n",
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConfig([]byte(raw)); err == nil {
				t.Fatal("expected configuration to be rejected")
			}
		})
	}
}

func TestParseConfigDefaultsPermissionToUsage(t *testing.T) {
	raw := []byte("client-auth:\n  keys:\n    - id: abix\n      fingerprint: sha256:" + strings.Repeat("c", 64) + "\n")
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	key := cfg.ClientAuth.Keys[0]
	if !key.allows(permissionUsage) || key.allows(permissionAnalytics) {
		t.Fatalf("unexpected default permissions: %v", key.Permissions)
	}
}
