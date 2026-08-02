package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

func fingerprintOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func testConfig(token string, permissions ...string) pluginConfig {
	cfg := defaultConfig()
	cfg.clients = []clientKey{{
		ID:          "abix",
		Fingerprint: fingerprintOf(token),
		Permissions: permissions,
	}}
	return cfg
}

func TestAuthenticateAcceptsAllowListedKey(t *testing.T) {
	const token = "sk-test-key-0123456789abcdef"
	cfg := testConfig(token, permissionUsage)

	headers := http.Header{"Authorization": []string{"Bearer " + token}}
	client, hint, ok := authenticate(cfg, headers)
	if !ok {
		t.Fatal("expected allow-listed key to authenticate")
	}
	if client.ID != "abix" {
		t.Fatalf("client id = %q, want abix", client.ID)
	}
	if strings.Contains(hint, "0123456789") {
		t.Fatalf("hint %q leaks key material", hint)
	}
}

func TestAuthenticateRejectsUnknownAndMalformed(t *testing.T) {
	cfg := testConfig("sk-real-key-0123456789abcdef", permissionUsage)

	cases := map[string]http.Header{
		"missing":        {},
		"unknown key":    {"Authorization": []string{"Bearer sk-other-key-98765432100000"}},
		"wrong scheme":   {"Authorization": []string{"Basic sk-real-key-0123456789abcdef"}},
		"plugin key":     {"X-Plugin-Key": []string{"sk-real-key-0123456789abcdef"}},
		"duplicate auth": {"Authorization": []string{"Bearer a", "Bearer b"}},
	}

	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := authenticate(cfg, headers); ok {
				t.Fatal("expected authentication to be refused")
			}
		})
	}
}

func TestClientKeyPermissions(t *testing.T) {
	key := clientKey{Permissions: []string{permissionUsage}}
	if !key.allows(permissionUsage) {
		t.Fatal("expected usage permission to be granted")
	}
	if key.allows(permissionAnalytics) {
		t.Fatal("expected analytics permission to be denied")
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
