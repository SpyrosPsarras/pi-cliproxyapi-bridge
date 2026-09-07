package main

import (
	"sort"
	"sync"
)

// usageRecord is the subset of the host's usage.handle payload the bridge
// needs: which provider and model served the request and the token counters
// reported for it.
type usageRecord struct {
	Provider string `json:"Provider"`
	Model    string `json:"Model"`
	Detail   struct {
		InputTokens         int64 `json:"InputTokens"`
		OutputTokens        int64 `json:"OutputTokens"`
		ReasoningTokens     int64 `json:"ReasoningTokens"`
		CachedTokens        int64 `json:"CachedTokens"`
		CacheReadTokens     int64 `json:"CacheReadTokens"`
		CacheCreationTokens int64 `json:"CacheCreationTokens"`
		TotalTokens         int64 `json:"TotalTokens"`
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
	tokenStatsMu sync.Mutex
	tokenStats   = map[tokenKey]*modelTokenUsage{}
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
	entry.Cached += d.CachedTokens + d.CacheReadTokens + d.CacheCreationTokens
	if d.TotalTokens > 0 {
		entry.Total += d.TotalTokens
	} else {
		entry.Total += d.InputTokens + d.OutputTokens
	}
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
