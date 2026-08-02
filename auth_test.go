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

func restrictedConfig(allowed ...string) pluginConfig {
	cfg := defaultConfig()
	deny := false
	cfg.AllowAllAPIKeys = &deny
	cfg.AllowedKeys = allowed
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

// Operators copy the shortened key shown in the UI, so a unique tail is
// accepted as well as the whole key.
func TestAllowListAcceptsKeyTail(t *testing.T) {
	cfg := restrictedConfig("0123456789abcdef")
	if _, ok := authenticate(cfg, knownKeys, bearer(testKey)); !ok {
		t.Fatal("expected a key tail to match")
	}
}

// A short suffix would authorize unrelated keys, so it must not match.
func TestAllowListIgnoresShortSuffix(t *testing.T) {
	cfg := restrictedConfig("def")
	if _, ok := authenticate(cfg, knownKeys, bearer(testKey)); ok {
		t.Fatal("expected a too-short suffix to be ignored")
	}
}

// An empty list with the checkbox off must deny everyone rather than fall
// back to allowing all keys.
func TestEmptyAllowListDeniesEveryone(t *testing.T) {
	if _, ok := authenticate(restrictedConfig(), knownKeys, bearer(testKey)); ok {
		t.Fatal("expected an empty allow-list to refuse access")
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
