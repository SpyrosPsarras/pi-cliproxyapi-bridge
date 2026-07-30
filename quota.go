package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"
)

// usageDocument matches the schema the Pi plugin already renders, so the
// bridge can replace the sidecar without a client-side contract change.
type usageDocument struct {
	SchemaVersion int            `json:"schemaVersion"`
	GeneratedAt   string         `json:"generatedAt"`
	Client        clientIdentity `json:"client"`
	Cache         cacheInfo      `json:"cache"`
	Accounts      []usageAccount `json:"accounts"`
	Unsupported   []string       `json:"unsupportedProviders,omitempty"`
}

type clientIdentity struct {
	ID      string `json:"id"`
	KeyHint string `json:"keyHint"`
}

type cacheInfo struct {
	UpdatedAt string `json:"updatedAt"`
	Stale     bool   `json:"stale"`
	TTLMs     int    `json:"ttlMs"`
}

type usageAccount struct {
	Provider    string       `json:"provider"`
	Account     string       `json:"account"`
	AuthIndex   string       `json:"authIndex"`
	Label       string       `json:"label"`
	Status      string       `json:"status"`
	Disabled    bool         `json:"disabled"`
	Unavailable bool         `json:"unavailable"`
	Supported   bool         `json:"supported"`
	Groups      []quotaGroup `json:"groups"`
}

type quotaGroup struct {
	ID                string  `json:"id"`
	Label             string  `json:"label"`
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime,omitempty"`
}

// providerSupported reports whether the bridge knows how to read quota for a
// credential provider. Unsupported providers are surfaced explicitly instead
// of being silently dropped.
func providerSupported(provider string) bool {
	switch normalizeProvider(provider) {
	case "claude", "codex":
		return true
	default:
		return false
	}
}

func normalizeProvider(provider string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	switch p {
	case "anthropic", "claude", "claude-web":
		return "claude"
	case "openai", "codex", "chatgpt":
		return "codex"
	default:
		return p
	}
}

// buildUsage assembles the quota document for every credential the host knows
// about, reading each provider through the fixed management passthrough.
func buildUsage(ctx context.Context, cfg pluginConfig, client clientKey, hint string) (usageDocument, error) {
	files, err := cfg.fetchAuthFiles(ctx)
	if err != nil {
		return usageDocument{}, err
	}

	doc := usageDocument{
		SchemaVersion: 1,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Client:        clientIdentity{ID: client.ID, KeyHint: hint},
		Accounts:      []usageAccount{},
	}

	unsupported := map[string]bool{}
	for _, file := range files {
		provider := normalizeProvider(file.Provider)
		if provider == "" {
			provider = normalizeProvider(file.Type)
		}

		account := usageAccount{
			Provider:    provider,
			Account:     maskEmail(firstNonEmpty(file.Email, file.Label, file.Name)),
			AuthIndex:   file.AuthIndex,
			Label:       maskEmail(firstNonEmpty(file.Label, file.Email)),
			Status:      file.Status,
			Disabled:    file.Disabled,
			Unavailable: file.Unavailable,
			Supported:   providerSupported(provider),
			Groups:      []quotaGroup{},
		}

		if !account.Supported {
			if provider != "" {
				unsupported[provider] = true
			}
			doc.Accounts = append(doc.Accounts, account)
			continue
		}

		// A single credential failing must not fail the whole document; the
		// account is still reported with an empty group list so clients never
		// receive a null array.
		if groups, errQuota := fetchQuota(ctx, cfg, provider, file.AuthIndex); errQuota == nil && len(groups) > 0 {
			account.Groups = groups
		}
		doc.Accounts = append(doc.Accounts, account)
	}

	for provider := range unsupported {
		doc.Unsupported = append(doc.Unsupported, provider)
	}
	return doc, nil
}

func fetchQuota(ctx context.Context, cfg pluginConfig, provider, authIndex string) ([]quotaGroup, error) {
	switch provider {
	case "claude":
		return fetchClaudeQuota(ctx, cfg, authIndex)
	case "codex":
		return fetchCodexQuota(ctx, cfg, authIndex)
	default:
		return nil, nil
	}
}

// claudeWindow is one Anthropic quota window. Utilization is a percentage
// (0-100), not a 0-1 fraction.
type claudeWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

type claudeUsagePayload struct {
	FiveHour     claudeWindow  `json:"five_hour"`
	SevenDay     claudeWindow  `json:"seven_day"`
	SevenDayOpus *claudeWindow `json:"seven_day_opus"`
}

func fetchClaudeQuota(ctx context.Context, cfg pluginConfig, authIndex string) ([]quotaGroup, error) {
	resp, err := cfg.managementAPICall(ctx, apiCallRequest{
		AuthIndex: authIndex,
		Method:    http.MethodGet,
		URL:       "https://api.anthropic.com/api/oauth/usage",
		Header: map[string]string{
			"Authorization":     "Bearer $TOKEN$",
			"anthropic-version": "2023-06-01",
		},
	})
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, err
	}

	var payload claudeUsagePayload
	if err := json.Unmarshal([]byte(resp.Body), &payload); err != nil {
		return nil, err
	}
	groups := []quotaGroup{
		{
			ID:                "five-hour",
			Label:             "5h Session",
			RemainingFraction: remainingFromPercent(payload.FiveHour.Utilization),
			ResetTime:         payload.FiveHour.ResetsAt,
		},
		{
			ID:                "seven-day",
			Label:             "7d Weekly",
			RemainingFraction: remainingFromPercent(payload.SevenDay.Utilization),
			ResetTime:         payload.SevenDay.ResetsAt,
		},
	}
	if payload.SevenDayOpus != nil {
		groups = append(groups, quotaGroup{
			ID:                "seven-day-opus",
			Label:             "7d Opus",
			RemainingFraction: remainingFromPercent(payload.SevenDayOpus.Utilization),
			ResetTime:         payload.SevenDayOpus.ResetsAt,
		})
	}
	return groups, nil
}

type codexUsagePayload struct {
	RateLimit struct {
		Primary struct {
			UsedPercent float64 `json:"used_percent"`
			ResetsAt    string  `json:"resets_at"`
		} `json:"primary_window"`
		Secondary struct {
			UsedPercent float64 `json:"used_percent"`
			ResetsAt    string  `json:"resets_at"`
		} `json:"secondary_window"`
	} `json:"rate_limit"`
}

func fetchCodexQuota(ctx context.Context, cfg pluginConfig, authIndex string) ([]quotaGroup, error) {
	resp, err := cfg.managementAPICall(ctx, apiCallRequest{
		AuthIndex: authIndex,
		Method:    http.MethodGet,
		URL:       "https://chatgpt.com/backend-api/wham/usage",
		Header:    map[string]string{"Authorization": "Bearer $TOKEN$"},
	})
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, err
	}

	var payload codexUsagePayload
	if err := json.Unmarshal([]byte(resp.Body), &payload); err != nil {
		return nil, err
	}
	return []quotaGroup{
		{
			ID:                "five-hour",
			Label:             "5h Session",
			RemainingFraction: remainingFromPercent(payload.RateLimit.Primary.UsedPercent),
			ResetTime:         payload.RateLimit.Primary.ResetsAt,
		},
		{
			ID:                "seven-day",
			Label:             "7d Weekly",
			RemainingFraction: remainingFromPercent(payload.RateLimit.Secondary.UsedPercent),
			ResetTime:         payload.RateLimit.Secondary.ResetsAt,
		},
	}, nil
}

// remainingFromPercent converts a 0-100 utilization percentage into the
// 0-1 remaining fraction the Pi status line renders.
func remainingFromPercent(percent float64) float64 {
	return clampFraction(1 - percent/100)
}

func clampFraction(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Min(1, math.Max(0, v))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
