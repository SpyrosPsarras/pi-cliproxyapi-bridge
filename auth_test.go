package main

import (
	"net/http"
	"strings"
	"testing"
)

const testKey = "sk-real-key-0123456789abcdef"

var knownKeys = []string{testKey, "sk-other-key-98765432100000"}

func bearer(token string) http.Header {
	return http.Header{"Authorization": []string{"Bearer " + token}}
}

// With the default configuration any key CLIProxyAPI accepts may read quota,
// so the plugin works as soon as it is enabled.
func TestAuthenticateAcceptsAnyKnownKeyByDefault(t *testing.T) {
	client, ok := authenticate(defaultConfig(), knownKeys, bearer(testKey))
	if !ok {
		t.Fatal("expected a known key to authenticate by default")
	}
	if !client.allows(permissionUsage) {
		t.Fatal("expected usage permission")
	}
	if strings.Contains(client.KeyHint, "0123456789") {
		t.Fatalf("hint %q leaks key material", client.KeyHint)
	}
}

// A string that is not a CLIProxyAPI key must never authenticate, even when
// the allow-list is open.
func TestAuthenticateRejectsUnknownKey(t *testing.T) {
	if _, ok := authenticate(defaultConfig(), knownKeys, bearer("sk-not-a-real-key-000000")); ok {
		t.Fatal("expected an unknown key to be refused")
	}
}

func TestAuthenticateRejectsMalformedCredentials(t *testing.T) {
	cfg := defaultConfig()
	cases := map[string]http.Header{
		"missing":        {},
		"wrong scheme":   {"Authorization": []string{"Basic " + testKey}},
		"plugin key":     {"X-Plugin-Key": []string{testKey}},
		"duplicate auth": {"Authorization": []string{"Bearer " + testKey, "Bearer x"}},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := authenticate(cfg, knownKeys, headers); ok {
				t.Fatal("expected authentication to be refused")
			}
		})
	}
}

// restrictedConfig turns off the blanket allow and ticks each named key's
// checkbox, mirroring what the management UI writes.
func restrictedConfig(allowed ...string) pluginConfig {
	cfg := defaultConfig()
	deny := false
	cfg.AllowAllAPIKeys = &deny
	cfg.selectedKeys = map[string]bool{}
	for _, key := range allowed {
		cfg.selectedKeys[keyFieldName(key)] = true
	}
	return cfg
}

func TestAllowListRestrictsAccess(t *testing.T) {
	cfg := restrictedConfig(testKey)

	if _, ok := authenticate(cfg, knownKeys, bearer(testKey)); !ok {
		t.Fatal("expected a listed key to authenticate")
	}
	if _, ok := authenticate(cfg, knownKeys, bearer("sk-other-key-98765432100000")); ok {
		t.Fatal("expected an unlisted key to be refused")
	}
}

// Checkbox names must be stable and safe to use as YAML keys, and must never
// embed usable key material.
func TestKeyFieldName(t *testing.T) {
	name := keyFieldName(testKey)
	if name != keyFieldName(testKey) {
		t.Fatal("field name must be stable for the same key")
	}
	if name == keyFieldName("sk-other-key-98765432100000") {
		t.Fatal("distinct keys must map to distinct fields")
	}
	if strings.Contains(name, testKey) {
		t.Fatalf("field name %q embeds the raw key", name)
	}
	for _, r := range name {
		safe := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !safe {
			t.Fatalf("field name %q contains %q, which is unsafe as a YAML key", name, r)
		}
	}
}

// With no checkbox ticked nobody may read quota, rather than falling back to
// allowing everyone.
func TestNoTickedKeyDeniesEveryone(t *testing.T) {
	if _, ok := authenticate(restrictedConfig(), knownKeys, bearer(testKey)); ok {
		t.Fatal("expected an empty selection to refuse access")
	}
}

func TestAnalyticsPermissionFollowsSetting(t *testing.T) {
	cfg := defaultConfig()
	if client, _ := authenticate(cfg, knownKeys, bearer(testKey)); client.allows(permissionAnalytics) {
		t.Fatal("analytics must be off unless enabled")
	}

	cfg.ShowExtraAnalytics = true
	if client, _ := authenticate(cfg, knownKeys, bearer(testKey)); !client.allows(permissionAnalytics) {
		t.Fatal("expected analytics permission when enabled")
	}
}

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"design.tmb@gmail.com": "d***@gmail.com",
		"":                     "",
		"nodomain":             "n***",
	}
	for input, want := range cases {
		if got := maskEmail(input); got != want {
			t.Fatalf("maskEmail(%q) = %q, want %q", input, got, want)
		}
	}
}
