package main

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

const (
	permissionUsage     = "usage"
	permissionAnalytics = "analytics"
)

// authenticatedClient identifies the caller behind an accepted API key.
type authenticatedClient struct {
	KeyHint     string
	Permissions []string
}

func (c authenticatedClient) allows(permission string) bool {
	for _, p := range c.Permissions {
		if strings.EqualFold(p, permission) {
			return true
		}
	}
	return false
}

// authenticate accepts an ordinary CLIProxyAPI API key presented as a Bearer
// token. The key is checked against the keys CLIProxyAPI itself accepts, so
// operators never copy fingerprints into this plugin's configuration.
//
// Every rejection path returns the same result, so a caller cannot learn
// whether a key exists, only that access was refused.
func authenticate(cfg pluginConfig, knownKeys []string, headers http.Header) (authenticatedClient, bool) {
	token := bearerToken(headers)
	if token == "" {
		return authenticatedClient{}, false
	}

	// The key must be a real CLIProxyAPI key regardless of the allow-list, so
	// an arbitrary string can never pass by matching a permissive entry.
	if !isKnownKey(token, knownKeys) {
		return authenticatedClient{}, false
	}
	if !cfg.allowAll() && !matchesAllowList(token, cfg.AllowedKeys) {
		return authenticatedClient{}, false
	}

	permissions := []string{permissionUsage}
	if cfg.ShowExtraAnalytics {
		permissions = append(permissions, permissionAnalytics)
	}
	return authenticatedClient{KeyHint: maskKey(token), Permissions: permissions}, true
}

// isKnownKey reports whether the token is one of the keys CLIProxyAPI accepts.
func isKnownKey(token string, knownKeys []string) bool {
	matched := false
	for _, key := range knownKeys {
		if subtle.ConstantTimeCompare([]byte(token), []byte(strings.TrimSpace(key))) == 1 {
			matched = true
		}
	}
	return matched
}

// matchesAllowList reports whether the token was selected by the operator.
// Entries come from the plugin's own dropdown, which lists keys in masked form
// (sk-abc12…7890), so the masked rendering is the primary match. A full key or
// a sufficiently long tail is accepted too, for configs written by hand.
func matchesAllowList(token string, entries []string) bool {
	masked := maskKey(token)
	matched := false
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(masked), []byte(entry)) == 1 {
			matched = true
			continue
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(entry)) == 1 {
			matched = true
			continue
		}
		// A short suffix would authorize far too many keys.
		if len(entry) >= 8 && len(entry) < len(token) && strings.HasSuffix(token, entry) {
			matched = true
		}
	}
	return matched
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
