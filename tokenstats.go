package main

import (
	"maps"
	"sort"
	"sync"
	"time"
)

// usageRecord is the subset of the host's usage.handle payload the bridge
// needs: which provider, model and credential served the request, when, and
// the token counters reported for it.
type usageRecord struct {
	Provider    string    `json:"Provider"`
	Model       string    `json:"Model"`
	AuthIndex   string    `json:"AuthIndex"`
	RequestedAt time.Time `json:"RequestedAt"`
	Detail      struct {
		InputTokens     int64 `json:"InputTokens"`
		OutputTokens    int64 `json:"OutputTokens"`
		ReasoningTokens int64 `json:"ReasoningTokens"`
		CachedTokens    int64 `json:"CachedTokens"`
		TotalTokens     int64 `json:"TotalTokens"`
	} `json:"Detail"`
}

type tokenKey struct {
	provider string
	model    string
}

// modelTokenUsage is one model's cumulative token counters since plugin load.
// Totals are cumulative on purpose: clients diff successive snapshots the same
// way they already diff the request counters.
type modelTokenUsage struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Input     int64  `json:"input"`
	Output    int64  `json:"output"`
	Reasoning int64  `json:"reasoning"`
	Cached    int64  `json:"cached"`
	Total     int64  `json:"total"`
}

var (
	tokenStatsMu      sync.Mutex
	tokenStats        = map[tokenKey]*modelTokenUsage{}
	accountTokenStats = map[string]map[string]int64{}
)

func recordTokenUsage(rec usageRecord) {
	provider := normalizeProvider(rec.Provider)
	if provider == "" || rec.Model == "" {
		return
	}
	tokenStatsMu.Lock()
	defer tokenStatsMu.Unlock()

	key := tokenKey{provider: provider, model: rec.Model}
	entry, ok := tokenStats[key]
	if !ok {
		entry = &modelTokenUsage{Provider: provider, Model: rec.Model}
		tokenStats[key] = entry
	}

	d := rec.Detail
	entry.Input += d.InputTokens
	entry.Output += d.OutputTokens
	entry.Reasoning += d.ReasoningTokens
	// The host copies the cache-read count into both CachedTokens and
	// CacheReadTokens, and falls back to CacheCreationTokens when there is no
	// read, so CachedTokens alone is the total. Adding the others double-counts.
	entry.Cached += d.CachedTokens
	// Reasoning tokens are part of the output count, so input plus output is the
	// whole request when the host sends no total.
	total := d.TotalTokens
	if total <= 0 {
		total = d.InputTokens + d.OutputTokens
	}
	entry.Total += total
	if rec.AuthIndex != "" {
		if accountTokenStats[rec.AuthIndex] == nil {
			accountTokenStats[rec.AuthIndex] = map[string]int64{}
		}
		accountTokenStats[rec.AuthIndex][rec.Model] += total
	}
}

func accountTokenSnapshot(authIndex string) map[string]int64 {
	tokenStatsMu.Lock()
	defer tokenStatsMu.Unlock()
	return maps.Clone(accountTokenStats[authIndex])
}

// tokenModelSnapshot returns the counters sorted by provider then model so the
// payload order is stable between polls.
func tokenModelSnapshot() []modelTokenUsage {
	tokenStatsMu.Lock()
	defer tokenStatsMu.Unlock()

	out := make([]modelTokenUsage, 0, len(tokenStats))
	for _, entry := range tokenStats {
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out
}
