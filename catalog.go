package main

import (
	"context"
	"strings"
	"sync"
	"time"
)

const (
	modelsDevURL      = "https://models.dev/api.json"
	catalogTTL        = 24 * time.Hour
	catalogFetchLimit = 10 * time.Second
)

// catalogEntry is the metadata models.dev knows about one model id.
type catalogEntry struct {
	Name          string
	Reasoning     *bool
	ContextWindow int
	MaxTokens     int
	Cost          *modelCost
}

type modelCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// modelsDevPayload mirrors the parts of models.dev/api.json this plugin uses.
type modelsDevPayload map[string]struct {
	Models map[string]modelsDevEntry `json:"models"`
}

type modelsDevEntry struct {
	Name      string `json:"name"`
	Reasoning *bool  `json:"reasoning"`
	Limit     struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Cost *struct {
		Input      *float64 `json:"input"`
		Output     *float64 `json:"output"`
		CacheRead  *float64 `json:"cache_read"`
		CacheWrite *float64 `json:"cache_write"`
	} `json:"cost"`
}

// preferredCatalogProviders win when several providers publish the same model
// id, so a model keeps the metadata of the vendor that actually makes it.
var preferredCatalogProviders = map[string]bool{
	"anthropic": true, "openai": true, "google": true,
	"mistral": true, "zai": true, "xai": true,
	"xiaomi": true, "moonshotai": true, "deepseek": true,
	"meta": true, "alibaba": true, "cohere": true, "minimax": true,
}

// resellerCatalogProviders republish other vendors' models, often with
// different limits, so they never displace a first-party entry.
var resellerCatalogProviders = map[string]bool{
	"openrouter": true, "qiniu-ai": true, "together": true,
	"fireworks-ai": true, "deepinfra": true, "novita": true,
	"groq": true, "cerebras": true,
}

// modelCatalog caches models.dev metadata.
//
// A failed refresh keeps the previous snapshot rather than dropping metadata:
// models.dev being unreachable must not strip context windows and prices from
// the catalogue the client already had.
type modelCatalog struct {
	mu        sync.RWMutex
	entries   map[string]catalogEntry
	fetchedAt time.Time
	lastError string
}

var catalog = &modelCatalog{}

// lookup returns metadata for a model id, if models.dev knows it. Many proxy
// models are not in the catalogue at all, which is expected.
func (c *modelCatalog) lookup(id string) (catalogEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[id]
	return entry, ok
}

func (c *modelCatalog) status() (entries int, fetchedAt time.Time, stale bool, lastError string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	stale = len(c.entries) > 0 && time.Since(c.fetchedAt) > catalogTTL
	return len(c.entries), c.fetchedAt, stale, c.lastError
}

// refresh reloads the catalogue when the cached copy has expired.
func (c *modelCatalog) refresh(ctx context.Context) {
	c.mu.RLock()
	fresh := len(c.entries) > 0 && time.Since(c.fetchedAt) < catalogTTL
	c.mu.RUnlock()
	if fresh {
		return
	}

	fetchCtx, cancel := context.WithTimeout(ctx, catalogFetchLimit)
	defer cancel()

	var payload modelsDevPayload
	if err := getJSON(fetchCtx, modelsDevURL, "", &payload); err != nil {
		c.mu.Lock()
		c.lastError = err.Error()
		c.mu.Unlock()
		return
	}

	entries := flattenCatalog(payload)
	if len(entries) == 0 {
		c.mu.Lock()
		c.lastError = "models.dev returned no usable entries"
		c.mu.Unlock()
		return
	}

	c.mu.Lock()
	c.entries = entries
	c.fetchedAt = time.Now()
	c.lastError = ""
	c.mu.Unlock()
}

func flattenCatalog(payload modelsDevPayload) map[string]catalogEntry {
	// Popular open models are republished by dozens of hosts with differing
	// limits and no first-party entry, so candidates are collected first and
	// resolved once all providers are known.
	candidates := map[string][]catalogCandidate{}

	for provider, data := range payload {
		for id, meta := range data.Models {
			if strings.TrimSpace(id) == "" {
				continue
			}

			entry := catalogEntry{
				Name:          meta.Name,
				Reasoning:     meta.Reasoning,
				ContextWindow: meta.Limit.Context,
				MaxTokens:     meta.Limit.Output,
			}
			if meta.Cost != nil {
				cost := modelCost{}
				if meta.Cost.Input != nil {
					cost.Input = *meta.Cost.Input
				}
				if meta.Cost.Output != nil {
					cost.Output = *meta.Cost.Output
				}
				if meta.Cost.CacheRead != nil {
					cost.CacheRead = *meta.Cost.CacheRead
				}
				if meta.Cost.CacheWrite != nil {
					cost.CacheWrite = *meta.Cost.CacheWrite
				}
				entry.Cost = &cost
			}
			candidates[id] = append(candidates[id], catalogCandidate{provider: provider, entry: entry})
		}
	}

	entries := make(map[string]catalogEntry, len(candidates))
	for id, list := range candidates {
		entries[id] = pickCatalogEntry(list)
	}
	return entries
}

type catalogCandidate struct {
	provider string
	entry    catalogEntry
}

// pickCatalogEntry chooses the metadata to publish for one model id.
//
// The model's own vendor wins outright. Failing that, the most commonly
// reported metadata wins, since agreement among independent hosts is better
// evidence than any single one; ties break on provider name so the result is
// identical on every run.
func pickCatalogEntry(candidates []catalogCandidate) catalogEntry {
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if catalogProviderRank(candidate.provider) < catalogProviderRank(best.provider) {
			best = candidate
		}
	}
	if catalogProviderRank(best.provider) == 0 {
		return best.entry
	}

	type shape struct {
		context, output int
		reasoning       bool
		hasReasoning    bool
	}
	shapeOf := func(entry catalogEntry) shape {
		return shape{
			context:      entry.ContextWindow,
			output:       entry.MaxTokens,
			reasoning:    entry.Reasoning != nil && *entry.Reasoning,
			hasReasoning: entry.Reasoning != nil,
		}
	}

	votes := map[shape]int{}
	for _, candidate := range candidates {
		votes[shapeOf(candidate.entry)]++
	}

	best = candidates[0]
	bestVotes := votes[shapeOf(best.entry)]
	for _, candidate := range candidates[1:] {
		count := votes[shapeOf(candidate.entry)]
		switch {
		case count > bestVotes:
			best, bestVotes = candidate, count
		case count == bestVotes:
			if rank := catalogProviderRank(candidate.provider) - catalogProviderRank(best.provider); rank < 0 ||
				(rank == 0 && candidate.provider < best.provider) {
				best = candidate
			}
		}
	}
	return best.entry
}

// catalogProviderRank orders providers by how authoritative they are for a
// model's metadata: the vendor first, resellers last.
func catalogProviderRank(provider string) int {
	switch {
	case preferredCatalogProviders[provider]:
		return 0
	case resellerCatalogProviders[provider]:
		return 2
	default:
		return 1
	}
}
