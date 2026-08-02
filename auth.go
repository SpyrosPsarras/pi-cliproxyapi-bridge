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
	// an arbitrary string can never pass by matching a permissive suffix.
	if !matchesAny(token, knownKeys, true) {
		return authenticatedClient{}, false
	}
	if !cfg.allowAll() && !matchesAny(token, cfg.AllowedKeys, false) {
		return authenticatedClient{}, false
	}

	permissions := []string{permissionUsage}
	if cfg.ShowExtraAnalytics {
		permissions = append(permissions, permissionAnalytics)
	}
	return authenticatedClient{KeyHint: maskKey(token), Permissions: permissions}, true
}

// matchesAny reports whether token matches an entry. Operators may paste a
// full key or the unique tail shown in the UI, so a suffix match is accepted
// for allow-list entries; identity checks require the whole key.
func matchesAny(token string, entries []string, exactOnly bool) bool {
	matched := false
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(entry)) == 1 {
			matched = true
			continue
		}
		// A short suffix would authorize far too many keys.
		if !exactOnly && len(entry) >= 8 && len(entry) < len(token) && strings.HasSuffix(token, entry) {
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
