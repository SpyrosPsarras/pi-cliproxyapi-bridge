package main

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Discovery contract defaults, matching the sidecar so v1 stays byte-compatible.
const (
	defaultContextWindow = 128_000
	defaultMaxTokens     = 16_000
	fallbackGroupSlug    = "lproxy-misc"
	fallbackGroupAPI     = "openai-completions"
)

// defaultDiscoveryExcludes filters ids the client should never offer.
var defaultDiscoveryExcludes = []string{"*:*", "lproxy/*"}

// nativeOwners map upstream `owned_by` values onto Pi's built-in providers.
var (
	nativeAnthropicOwners = map[string]bool{"anthropic": true}
	nativeOpenAIOwners    = map[string]bool{"openai": true}
)

// suggestedGroups turn an `owned_by` value into a custom-provider slug. These
// are server hints; the client lets the user regroup.
var suggestedGroups = []struct {
	owners map[string]bool
	slug   string
	api    string
}{
	{map[string]bool{"zai": true}, "lproxy-glm", "openai-completions"},
	{map[string]bool{"Mistral": true}, "lproxy-mistral", "openai-completions"},
	{map[string]bool{"google": true, "antigravity": true}, "lproxy-gemini", "openai-completions"},
	{map[string]bool{"Ollama": true, "Ollama pay": true}, "lproxy-ollama", "openai-completions"},
	{map[string]bool{"Xiaomi": true}, "lproxy-mimo", "openai-completions"},
	{map[string]bool{"OpenRouter": true}, "lproxy-openrouter", "openai-completions"},
	{map[string]bool{"cerebras": true}, "lproxy-cerebras", "openai-completions"},
}

// reasoningPatterns force reasoning=true regardless of what the catalogue says.
var reasoningPatterns = []*regexp.Regexp{
	regexp.MustCompile(`.*-thinking$`),
	regexp.MustCompile(`^gpt-5(\.\d+)?(-.*)?$`),
	regexp.MustCompile(`^gemini-3(\.\d+)?(-.*)?$`),
}

// discoveryDocument is the /.well-known/pi contract.
//
// Contract v1 is the sidecar's shape exactly; Upstream and Catalog are v2
// additions and are omitted for v1 callers.
type discoveryDocument struct {
	SchemaVersion     int                        `json:"schemaVersion"`
	GeneratedAt       string                     `json:"generatedAt"`
	Lproxy            lproxyInfo                 `json:"lproxy"`
	Upstream          *discoveryUpstream         `json:"upstream,omitempty"`
	BaseURLs          map[string]string          `json:"baseUrls"`
	DiscoveryExcludes []string                   `json:"discoveryExcludes"`
	BuiltinProviders  map[string]builtinProvider `json:"builtinProviders"`
	CustomModelPool   []customModel              `json:"customModelPool"`
	Counts            discoveryCounts            `json:"counts"`
	Catalog           *catalogStatus             `json:"catalog,omitempty"`
	Providers         []discoveryProviderHealth  `json:"providerHealth,omitempty"`
}

type lproxyInfo struct {
	Endpoint        string `json:"endpoint"`
	UpstreamVersion string `json:"upstreamVersion"`
}

// discoveryUpstream is where the client actually looks for the proxy version.
// The sidecar only ever filled lproxy.upstreamVersion, and with null at that,
// so the client's version display was always "(unknown)".
type discoveryUpstream struct {
	UpstreamVersion string `json:"upstreamVersion"`
	Panel           string `json:"panel,omitempty"`
}

// catalogStatus reports how much model metadata was resolved, so a client can
// tell "no price data" from "catalogue unavailable", and can show exactly which
// models still need a manual alias or override.
type catalogStatus struct {
	Source       string   `json:"source"`
	Entries      int      `json:"entries"`
	Matched      int      `json:"matched"`
	Unmatched    int      `json:"unmatched"`
	UnmatchedIDs []string `json:"unmatchedIds,omitempty"`
	FetchedAt    string   `json:"fetchedAt,omitempty"`
	Stale        bool     `json:"stale"`
	Unavailable  bool     `json:"unavailable,omitempty"`
}

// discoveryProviderHealth surfaces credential state per provider, so a model
// that cannot currently run is visible before it is selected.
type discoveryProviderHealth struct {
	Provider  string `json:"provider"`
	Accounts  int    `json:"accounts"`
	Healthy   int    `json:"healthy"`
	Degraded  int    `json:"degraded"`
	Disabled  int    `json:"disabled"`
	Reachable bool   `json:"reachable"`
}

type builtinProvider struct {
	API             string         `json:"api"`
	APIAlternatives []string       `json:"apiAlternatives,omitempty"`
	BaseURL         string         `json:"baseUrl"`
	Models          []builtinModel `json:"models"`
}

type builtinModel struct {
	ID            string `json:"id"`
	OwnedBy       string `json:"owned_by"`
	Name          string `json:"name,omitempty"`
	Reasoning     *bool  `json:"reasoning,omitempty"`
	ContextWindow int    `json:"contextWindow,omitempty"`
	MaxTokens     int    `json:"maxTokens,omitempty"`
}

type customModel struct {
	ID                    string     `json:"id"`
	OwnedBy               string     `json:"owned_by"`
	API                   string     `json:"api"`
	BaseURL               string     `json:"baseUrl"`
	SuggestedGroup        string     `json:"suggestedGroup"`
	SuggestedProviderName string     `json:"suggestedProviderName"`
	Name                  string     `json:"name,omitempty"`
	ContextWindow         int        `json:"contextWindow"`
	MaxTokens             int        `json:"maxTokens"`
	Cost                  *modelCost `json:"cost"`
	Reasoning             bool       `json:"reasoning"`
	// FromCatalog reports whether metadata was resolved at all, and
	// MetadataSource says how: catalog, alias, override, or default.
	FromCatalog    bool   `json:"fromCatalog,omitempty"`
	MetadataSource string `json:"metadataSource,omitempty"`
}

type discoveryCounts struct {
	UpstreamTotal int `json:"upstreamTotal"`
	AfterExcludes int `json:"afterExcludes"`
	Builtin       int `json:"builtin"`
	Custom        int `json:"custom"`
}

type upstreamModel struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by"`
}

// loadCPAMEntries converts CPAM's price table into catalogue entries. CPAM is
// operator-curated, so it wins over models.dev, but it only covers the models
// someone has actually priced.
func loadCPAMEntries(ctx context.Context, cfg pluginConfig) map[string]catalogEntry {
	prices, err := cfg.fetchCPAMPrices(ctx)
	if err != nil {
		return nil
	}

	entries := make(map[string]catalogEntry, len(prices.Prices))
	for id, row := range prices.Prices {
		entry := catalogEntry{
			Cost: &modelCost{
				Input:      row.Prompt,
				Output:     row.Completion,
				CacheRead:  row.CacheRead,
				CacheWrite: row.CacheCreation,
			},
		}

		// The upstream payload CPAM stored alongside the price also carries
		// context limits, which differ per vendor shape.
		var raw struct {
			ContextLength  int `json:"context_length"`
			MaxInputTokens int `json:"max_input_tokens"`
			MaxOutput      int `json:"max_output_tokens"`
			TopProvider    struct {
				ContextLength      int `json:"context_length"`
				MaxCompletionToken int `json:"max_completion_tokens"`
			} `json:"top_provider"`
		}
		if row.RawJSON != "" {
			_ = json.Unmarshal([]byte(row.RawJSON), &raw)
		}
		entry.ContextWindow = firstPositive(raw.ContextLength, raw.MaxInputTokens, raw.TopProvider.ContextLength)
		entry.MaxTokens = firstPositive(raw.MaxOutput, raw.TopProvider.MaxCompletionToken)

		entries[id] = entry
	}
	return entries
}

func firstPositive(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

// mergeEntry fills gaps in dst from src without overwriting what dst already
// knows, so a higher-priority source can be partial.
func mergeEntry(dst, src catalogEntry) catalogEntry {
	if dst.Name == "" {
		dst.Name = src.Name
	}
	if dst.ContextWindow == 0 {
		dst.ContextWindow = src.ContextWindow
	}
	if dst.MaxTokens == 0 {
		dst.MaxTokens = src.MaxTokens
	}
	if dst.Reasoning == nil {
		dst.Reasoning = src.Reasoning
	}
	if dst.Cost == nil {
		dst.Cost = src.Cost
	}
	return dst
}

// resolveModelMeta resolves metadata for one model id.
//
// Precedence: explicit override, then CPAM's curated table, then models.dev
// (optionally via an alias). Proxy ids frequently differ from catalogue ids, so
// the alias lets an operator rebind a model instead of losing its context
// window. Each source only fills what the previous one left empty.
func resolveModelMeta(cfg pluginConfig, cpam map[string]catalogEntry, id string) (entry catalogEntry, source string) {
	lookupID := id
	if alias := strings.TrimSpace(cfg.advanced.ModelAliases[id]); alias != "" {
		lookupID = alias
	}

	if found, ok := cpam[id]; ok {
		entry = found
		source = "cpam"
	} else if found, ok := cpam[lookupID]; ok {
		entry = found
		source = "cpam"
	}

	if found, ok := catalog.lookup(lookupID); ok {
		entry = mergeEntry(entry, found)
		if source == "" {
			source = "catalog"
			if lookupID != id {
				source = "alias"
			}
		}
	}

	override, hasOverride := cfg.advanced.ModelOverrides[id]
	if !hasOverride {
		return entry, source
	}

	// Only the fields actually set are applied, so an override can correct one
	// number without discarding the rest of the resolved row.
	if override.Name != "" {
		entry.Name = override.Name
	}
	if override.ContextWindow > 0 {
		entry.ContextWindow = override.ContextWindow
	}
	if override.MaxTokens > 0 {
		entry.MaxTokens = override.MaxTokens
	}
	if override.Reasoning != nil {
		entry.Reasoning = override.Reasoning
	}
	if override.Cost != nil {
		entry.Cost = override.Cost
	}
	return entry, "override"
}

// buildDiscovery assembles the model catalogue served at the well-known path.
func buildDiscovery(ctx context.Context, cfg pluginConfig, publicBaseURL string) (discoveryDocument, error) {
	models, err := cfg.fetchModels(ctx)
	if err != nil {
		return discoveryDocument{}, err
	}
	catalog.refresh(ctx)
	cpamEntries := loadCPAMEntries(ctx, cfg)

	base := strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	baseURLs := map[string]string{
		"openai-completions": base,
		"openai-responses":   base,
		"anthropic-messages": strings.TrimSuffix(base, "/v1"),
	}

	doc := discoveryDocument{
		SchemaVersion:     1,
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
		Lproxy:            lproxyInfo{Endpoint: base},
		BaseURLs:          baseURLs,
		DiscoveryExcludes: defaultDiscoveryExcludes,
		BuiltinProviders:  map[string]builtinProvider{},
		CustomModelPool:   []customModel{},
	}

	var anthropicModels, openaiModels []builtinModel
	matched, unmatched := 0, 0
	unmatchedIDs := []string{}

	for _, model := range models {
		doc.Counts.UpstreamTotal++
		if model.ID == "" || excludedModel(model.ID, defaultDiscoveryExcludes) {
			continue
		}
		doc.Counts.AfterExcludes++

		entry, source := resolveModelMeta(cfg, cpamEntries, model.ID)
		resolved := source != ""
		if resolved {
			matched++
		} else {
			unmatched++
			unmatchedIDs = append(unmatchedIDs, model.ID)
		}

		switch {
		case nativeAnthropicOwners[model.OwnedBy]:
			anthropicModels = append(anthropicModels, buildBuiltinModel(model, entry, resolved))
		case nativeOpenAIOwners[model.OwnedBy]:
			openaiModels = append(openaiModels, buildBuiltinModel(model, entry, resolved))
		default:
			doc.CustomModelPool = append(doc.CustomModelPool,
				buildCustomModel(model, entry, resolved, source, baseURLs))
		}
	}

	if len(anthropicModels) > 0 {
		doc.BuiltinProviders["anthropic"] = builtinProvider{
			API:     "anthropic-messages",
			BaseURL: baseURLs["anthropic-messages"],
			Models:  anthropicModels,
		}
	}
	if len(openaiModels) > 0 {
		doc.BuiltinProviders["openai"] = builtinProvider{
			API:             "openai-responses",
			APIAlternatives: []string{"openai-completions"},
			BaseURL:         baseURLs["openai-responses"],
			Models:          openaiModels,
		}
	}

	doc.Counts.Builtin = len(anthropicModels) + len(openaiModels)
	doc.Counts.Custom = len(doc.CustomModelPool)

	entries, fetchedAt, stale, _ := catalog.status()
	sort.Strings(unmatchedIDs)
	doc.Catalog = &catalogStatus{
		Source:       "models.dev",
		Entries:      entries,
		Matched:      matched,
		Unmatched:    unmatched,
		UnmatchedIDs: unmatchedIDs,
		Stale:        stale,
		Unavailable:  entries == 0,
	}
	if !fetchedAt.IsZero() {
		doc.Catalog.FetchedAt = fetchedAt.UTC().Format(time.RFC3339)
	}

	if version, errVersion := cfg.cpaVersion(ctx); errVersion == nil && version != "" {
		doc.Lproxy.UpstreamVersion = version
		doc.Upstream = &discoveryUpstream{UpstreamVersion: version}
		if info, ok := cfg.probeCPAM(ctx); ok {
			doc.Upstream.Panel = "cpam"
			_ = info
		}
	}

	doc.Providers = providerHealth(ctx, cfg)
	return doc, nil
}

func buildBuiltinModel(model upstreamModel, entry catalogEntry, inCatalog bool) builtinModel {
	out := builtinModel{ID: model.ID, OwnedBy: model.OwnedBy}
	if inCatalog {
		out.Name = entry.Name
		out.ContextWindow = entry.ContextWindow
		out.MaxTokens = entry.MaxTokens
		out.Reasoning = entry.Reasoning
	}
	if forced := forcedReasoning(model.ID); forced != nil {
		out.Reasoning = forced
	}
	return out
}

func buildCustomModel(model upstreamModel, entry catalogEntry, resolved bool, source string, baseURLs map[string]string) customModel {
	slug, api := classifyCustom(model.OwnedBy)
	out := customModel{
		ID:                    model.ID,
		OwnedBy:               model.OwnedBy,
		API:                   api,
		BaseURL:               baseURLs[api],
		SuggestedGroup:        strings.TrimPrefix(slug, "lproxy-"),
		SuggestedProviderName: slug,
		ContextWindow:         defaultContextWindow,
		MaxTokens:             defaultMaxTokens,
		Cost:                  &modelCost{},
		FromCatalog:           resolved,
		MetadataSource:        source,
	}

	// Many proxy models are absent from the catalogue; those keep the defaults
	// above rather than being dropped, and say so via metadataSource.
	if resolved {
		out.Name = entry.Name
		// A catalogue row can exist with zeroed limits. Mirroring it keeps the
		// document honest: a real zero is not the same as "unknown, assume 128k".
		out.ContextWindow = entry.ContextWindow
		out.MaxTokens = entry.MaxTokens
		if entry.Cost != nil {
			out.Cost = entry.Cost
		}
		if entry.Reasoning != nil {
			out.Reasoning = *entry.Reasoning
		}
	} else {
		out.MetadataSource = "default"
	}
	if forced := forcedReasoning(model.ID); forced != nil {
		out.Reasoning = *forced
	}
	return out
}

func classifyCustom(ownedBy string) (slug, api string) {
	for _, group := range suggestedGroups {
		if group.owners[ownedBy] {
			return group.slug, group.api
		}
	}
	return fallbackGroupSlug, fallbackGroupAPI
}

func forcedReasoning(modelID string) *bool {
	for _, pattern := range reasoningPatterns {
		if pattern.MatchString(modelID) {
			yes := true
			return &yes
		}
	}
	return nil
}

// excludedModel applies the glob-ish exclude patterns from the contract.
func excludedModel(id string, excludes []string) bool {
	for _, pattern := range excludes {
		if globMatch(pattern, id) {
			return true
		}
	}
	return false
}

// globMatch supports the `*` wildcard used by the discovery excludes.
func globMatch(pattern, value string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == value
	}
	if !strings.HasPrefix(value, parts[0]) {
		return false
	}
	rest := value[len(parts[0]):]
	for i := 1; i < len(parts)-1; i++ {
		idx := strings.Index(rest, parts[i])
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(parts[i]):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}

// providerHealth summarizes credential state per provider so the client can
// warn that a model's provider has no working account.
func providerHealth(ctx context.Context, cfg pluginConfig) []discoveryProviderHealth {
	files, err := cfg.fetchAuthFiles(ctx)
	if err != nil {
		return nil
	}

	byProvider := map[string]*discoveryProviderHealth{}
	for _, file := range files {
		provider := normalizeProvider(firstNonEmpty(file.Provider, file.Type))
		if provider == "" {
			continue
		}
		health, seen := byProvider[provider]
		if !seen {
			health = &discoveryProviderHealth{Provider: provider}
			byProvider[provider] = health
		}
		health.Accounts++
		switch {
		case file.Disabled:
			health.Disabled++
		case strings.EqualFold(file.Status, "error"), file.Unavailable:
			health.Degraded++
		default:
			health.Healthy++
		}
	}

	out := make([]discoveryProviderHealth, 0, len(byProvider))
	for _, health := range byProvider {
		health.Reachable = health.Healthy > 0
		out = append(out, *health)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}
