package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

const (
	permissionUsage     = "usage"
	permissionAnalytics = "analytics"
)

// authenticate resolves an ordinary CLIProxyAPI API key presented as a Bearer
// token to a configured client entry.
//
// Every rejection path returns the same result so a caller cannot learn
// whether a fingerprint exists, only that access was refused.
func authenticate(cfg pluginConfig, headers http.Header) (clientKey, string, bool) {
	token := bearerToken(headers)
	if token == "" {
		return clientKey{}, "", false
	}

	sum := sha256.Sum256([]byte(token))
	got := hex.EncodeToString(sum[:])

	for _, candidate := range cfg.clients {
		if subtle.ConstantTimeCompare([]byte(got), []byte(candidate.Fingerprint)) == 1 {
			return candidate, maskKey(token), true
		}
	}
	return clientKey{}, "", false
}

// bearerToken extracts a single Bearer credential. Any other authorization
// shape is treated as absent.
func bearerToken(headers http.Header) string {
	if headers == nil {
		return ""
	}
	values := headers.Values("Authorization")
	if len(values) != 1 {
		return ""
	}
	parts := strings.SplitN(strings.TrimSpace(values[0]), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// maskKey renders a key hint that identifies a credential in the UI without
// disclosing enough material to reuse it.
func maskKey(token string) string {
	if len(token) <= 11 {
		return "…"
	}
	return token[:7] + "…" + token[len(token)-4:]
}

// maskEmail reduces an account identifier to a recognizable but non-PII hint.
func maskEmail(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	at := strings.LastIndex(value, "@")
	if at <= 0 {
		if len(value) <= 2 {
			return "…"
		}
		return value[:1] + "***"
	}
	local, domain := value[:at], value[at+1:]
	if len(local) <= 1 {
		return "*@" + domain
	}
	return local[:1] + "***@" + domain
}
