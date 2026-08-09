package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// usageDocument matches the schema the Pi plugin already renders, so the
// bridge can replace the sidecar without a client-side contract change.
//
// Contract v1 is the sidecar's shape exactly. The client and cache sections are
// v2 additions and are omitted for v1 callers.
type usageDocument struct {
	SchemaVersion int             `json:"schemaVersion"`
	GeneratedAt   string          `json:"generatedAt"`
	Client        *clientIdentity `json:"client,omitempty"`
	Cache         *cacheInfo      `json:"cache,omitempty"`
	Accounts      []usageAccount  `json:"accounts"`
	Unsupported   []string        `json:"unsupportedProviders"`
}

type clientIdentity struct {
	KeyHint string `json:"keyHint"`
}

type cacheInfo struct {
	UpdatedAt string `json:"updatedAt"`
	Stale     bool   `json:"stale"`
	TTLMs     int    `json:"ttlMs"`
}

type usageAccount struct {
	Provider      string       `json:"provider"`
	Account       string       `json:"account"`
	AuthIndex     string       `json:"authIndex"`
	Label         string       `json:"label"`
	Status        string       `json:"status"`
	Disabled      bool         `json:"disabled"`
	Unavailable   bool         `json:"unavailable"`
	Success       int          `json:"success"`
	Failed        int          `json:"failed"`
	LastRequestAt string       `json:"lastRequestAt,omitempty"`
	Supported     bool         `json:"supported"`
	Error         string       `json:"error,omitempty"`
	Groups        []quotaGroup `json:"groups"`
}

type quotaGroup struct {
	ID                string       `json:"id"`
	Label             string       `json:"label"`
	RemainingFraction float64      `json:"remainingFraction"`
	ResetTime         string       `json:"resetTime,omitempty"`
	Models            []quotaModel `json:"models,omitempty"`
}

// quotaModel is a per-model breakdown within a group, which providers like
// Antigravity report instead of a single aggregate number.
type quotaModel struct {
	ID                string  `json:"id"`
	DisplayName       string  `json:"displayName"`
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime,omitempty"`
}

// providerSupported reports whether the bridge knows how to read quota for a
// credential provider. Unsupported providers are surfaced explicitly instead
// of being silently dropped.
func providerSupported(provider string) bool {
	switch normalizeProvider(provider) {
	case "claude", "codex", "antigravity", "github-copilot":
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
	case "copilot", "github-copilot", "github_copilot":
		return "github-copilot"
	default:
		return p
	}
}

// accountStatus mirrors the sidecar: a disabled credential reports as disabled
// regardless of the status the host recorded for it.
func accountStatus(file authFile) string {
	if file.Disabled {
		return "disabled"
	}
	if status := strings.TrimSpace(file.Status); status != "" {
		return status
	}
	return "active"
}

// buildUsage assembles the quota document for every credential the host knows
// about, reading each provider through the fixed management passthrough.
func buildUsage(ctx context.Context, cfg pluginConfig) (usageDocument, error) {
	files, err := cfg.fetchAuthFiles(ctx)
	if err != nil {
		return usageDocument{}, err
	}

	doc := usageDocument{
		SchemaVersion: 1,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Accounts:      []usageAccount{},
		Unsupported:   []string{},
	}

	unsupported := map[string]bool{}
	for _, file := range files {
		provider := normalizeProvider(file.Provider)
		if provider == "" {
			provider = normalizeProvider(file.Type)
		}

		account := usageAccount{
			Provider:      provider,
			Account:       maskEmail(firstNonEmpty(file.Email, file.Label, file.Name)),
			AuthIndex:     file.AuthIndex,
			Label:         maskEmail(firstNonEmpty(file.Label, file.Email)),
			Status:        accountStatus(file),
			Disabled:      file.Disabled,
			Unavailable:   file.Unavailable,
			Success:       file.Success,
			Failed:        file.Failed,
			LastRequestAt: file.UpdatedAt,
			Supported:     providerSupported(provider),
			Groups:        []quotaGroup{},
		}

		if !account.Supported {
			if provider != "" {
				unsupported[provider] = true
			}
			doc.Accounts = append(doc.Accounts, account)
			continue
		}

		// A single credential failing must not fail the whole document. The
		// reason is reported per account, mirroring the sidecar, so an expired
		// login is visible rather than silently showing no quota.
		groups, errQuota := fetchQuota(ctx, cfg, provider, file)
		if errQuota != nil {
			account.Error = errQuota.Error()
		} else if len(groups) > 0 {
			// Groups stays an empty slice otherwise, never null.
			account.Groups = groups
		}
		doc.Accounts = append(doc.Accounts, account)
	}

	for provider := range unsupported {
		doc.Unsupported = append(doc.Unsupported, provider)
	}
	sort.Strings(doc.Unsupported)
	return doc, nil
}

func fetchQuota(ctx context.Context, cfg pluginConfig, provider string, file authFile) ([]quotaGroup, error) {
	switch provider {
	case "claude":
		return fetchClaudeQuota(ctx, cfg, file.AuthIndex)
	case "codex":
		return fetchCodexQuota(ctx, cfg, file.AuthIndex)
	case "antigravity":
		return fetchAntigravityQuota(ctx, cfg, file)
	case "github-copilot":
		return fetchCopilotQuota(ctx, cfg, file.AuthIndex)
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

// claudeLimit is one entry of the structured `limits` array, which is where
// Anthropic now reports quota. Entries are self-describing, so a model-scoped
// window added later (Opus and Sonnet were joined by Fable) is picked up
// without a code change. Percent is a percentage, like Utilization above.
type claudeLimit struct {
	Kind     string   `json:"kind"`
	Percent  *float64 `json:"percent"`
	ResetsAt string   `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

type claudeUsagePayload struct {
	// Limits is the current shape and takes precedence when present.
	Limits []claudeLimit `json:"limits"`

	// The flat windows below are the older shape. Anthropic still sends the
	// keys but now nulls the model-scoped ones, so they serve only as a
	// fallback for accounts still answering the old way.
	FiveHour       *claudeWindow `json:"five_hour"`
	SevenDay       *claudeWindow `json:"seven_day"`
	SevenDayOpus   *claudeWindow `json:"seven_day_opus"`
	SevenDaySonnet *claudeWindow `json:"seven_day_sonnet"`
}

// scopedGroupID turns a model display name into a stable group id, e.g.
// "Fable" -> "seven-day-fable". Opus keeps the id it has always had.
func scopedGroupID(model string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r == ' ', r == '_', r == '-':
			return '-'
		default:
			return -1
		}
	}, strings.ToLower(strings.TrimSpace(model)))
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "scoped"
	}
	return "seven-day-" + slug
}

// groupsFromLimits reads the structured array. An entry with a null percent is
// a window the account does not have, and is skipped rather than reported as
// full quota.
func groupsFromLimits(limits []claudeLimit) []quotaGroup {
	var groups []quotaGroup
	for _, l := range limits {
		if l.Percent == nil {
			continue
		}
		remaining := remainingFromPercent(*l.Percent)
		switch l.Kind {
		case "session":
			groups = append(groups, simpleGroup("five-hour", "5h Session", remaining, l.ResetsAt))
		case "weekly_all":
			groups = append(groups, simpleGroup("seven-day", "7d Weekly", remaining, l.ResetsAt))
		case "weekly_scoped":
			name := ""
			if l.Scope != nil && l.Scope.Model != nil {
				if name = l.Scope.Model.DisplayName; name == "" {
					name = l.Scope.Model.ID
				}
			}
			if name == "" {
				name = "Scoped"
			}
			groups = append(groups, simpleGroup(scopedGroupID(name), "7d "+name, remaining, l.ResetsAt))
		}
	}
	return groups
}

// groupsFromFlatWindows reads the older top-level fields.
func groupsFromFlatWindows(payload claudeUsagePayload) []quotaGroup {
	var groups []quotaGroup
	add := func(w *claudeWindow, id, label string) {
		if w == nil {
			return
		}
		groups = append(groups, simpleGroup(id, label, remainingFromPercent(w.Utilization), w.ResetsAt))
	}
	add(payload.FiveHour, "five-hour", "5h Session")
	add(payload.SevenDay, "seven-day", "7d Weekly")
	add(payload.SevenDayOpus, "seven-day-opus", "7d Opus")
	add(payload.SevenDaySonnet, "seven-day-sonnet", "7d Sonnet")
	return groups
}

// simpleGroup builds a group for providers that report a single number per
// window. The sidecar always emits a models entry mirroring the group, and Pi
// renders it, so the shape is preserved here.
func simpleGroup(id, label string, remaining float64, resetTime string) quotaGroup {
	return quotaGroup{
		ID:                id,
		Label:             label,
		RemainingFraction: remaining,
		ResetTime:         resetTime,
		Models: []quotaModel{{
			ID:                id,
			DisplayName:       label,
			RemainingFraction: remaining,
			ResetTime:         resetTime,
		}},
	}
}

// providerFailure describes an upstream refusal in a form safe to show a
// client: a status code, never the response body.
func providerFailure(status int) error {
	return fmt.Errorf("provider API failed: %d", status)
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
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, providerFailure(resp.StatusCode)
	}

	var payload claudeUsagePayload
	if err := json.Unmarshal([]byte(resp.Body), &payload); err != nil {
		return nil, err
	}
	// The structured array describes every window the account actually has,
	// including model-scoped ones like Fable that the flat fields now report
	// as null. Fall back to the flat fields only when it is absent.
	if groups := groupsFromLimits(payload.Limits); len(groups) > 0 {
		return groups, nil
	}
	return groupsFromFlatWindows(payload), nil
}

// codexWindow is one Codex rate-limit window. Absent windows carry a null
// used_percent and must be skipped rather than reported as full quota.
type codexWindow struct {
	UsedPercent        *float64 `json:"used_percent"`
	ResetAt            *int64   `json:"reset_at"`
	LimitWindowSeconds *int64   `json:"limit_window_seconds"`
}

type codexUsagePayload struct {
	RateLimit *struct {
		Primary   *codexWindow `json:"primary_window"`
		Secondary *codexWindow `json:"secondary_window"`
	} `json:"rate_limit"`
}

func fetchCodexQuota(ctx context.Context, cfg pluginConfig, authIndex string) ([]quotaGroup, error) {
	resp, err := cfg.managementAPICall(ctx, apiCallRequest{
		AuthIndex: authIndex,
		Method:    http.MethodGet,
		URL:       "https://chatgpt.com/backend-api/wham/usage",
		Header: map[string]string{
			"Authorization": "Bearer $TOKEN$",
			"User-Agent":    "codex-cli/1.0.0",
		},
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, providerFailure(resp.StatusCode)
	}

	var payload codexUsagePayload
	if err := json.Unmarshal([]byte(resp.Body), &payload); err != nil {
		return nil, err
	}
	if payload.RateLimit == nil {
		return nil, fmt.Errorf("no Codex quota windows found")
	}

	groups := []quotaGroup{}
	for _, w := range []struct {
		id     string
		name   string
		window *codexWindow
	}{
		{"primary-window", "Primary", payload.RateLimit.Primary},
		{"secondary-window", "Secondary", payload.RateLimit.Secondary},
	} {
		// A window the account does not have reports null, which is not the
		// same as a window with full quota remaining.
		if w.window == nil || w.window.UsedPercent == nil {
			continue
		}
		groups = append(groups, simpleGroup(
			w.id,
			codexWindowLabel(w.window.LimitWindowSeconds, w.name),
			remainingFromPercent(*w.window.UsedPercent),
			unixToRFC3339(w.window.ResetAt),
		))
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("no Codex quota windows found")
	}
	return groups, nil
}

// codexWindowLabel names a window by its actual duration when the provider
// reports one, since the same slot means different things per account.
func codexWindowLabel(limitWindowSeconds *int64, fallback string) string {
	if limitWindowSeconds == nil || *limitWindowSeconds <= 0 {
		return fallback + " Window"
	}
	seconds := *limitWindowSeconds
	const daySeconds = int64(24 * 60 * 60)
	if seconds >= daySeconds && seconds%daySeconds == 0 {
		return fmt.Sprintf("%dd Window", seconds/daySeconds)
	}
	minutes := int64(math.Round(float64(seconds) / 60))
	if minutes < 60 {
		return fmt.Sprintf("%dm Window", minutes)
	}
	return fmt.Sprintf("%dh Window", int64(math.Round(float64(minutes)/60)))
}

func unixToRFC3339(ts *int64) string {
	if ts == nil || *ts == 0 {
		return ""
	}
	return time.Unix(*ts, 0).UTC().Format(time.RFC3339)
}

// remainingFromPercent converts a 0-100 utilization percentage into the
// 0-1 remaining fraction the Pi status line renders.
func remainingFromPercent(percent float64) float64 {
	return clampFraction(1 - percent/100)
}

// ---- GitHub Copilot ----

type copilotUsagePayload struct {
	QuotaSnapshots struct {
		Premium struct {
			Unlimited        bool     `json:"unlimited"`
			PercentRemaining *float64 `json:"percent_remaining"`
			Remaining        *float64 `json:"remaining"`
			Entitlement      *float64 `json:"entitlement"`
		} `json:"premium_interactions"`
	} `json:"quota_snapshots"`
	QuotaResetDateUTC string `json:"quota_reset_date_utc"`
	QuotaResetDate    string `json:"quota_reset_date"`
}

func fetchCopilotQuota(ctx context.Context, cfg pluginConfig, authIndex string) ([]quotaGroup, error) {
	resp, err := cfg.managementAPICall(ctx, apiCallRequest{
		AuthIndex: authIndex,
		Method:    http.MethodGet,
		URL:       "https://api.github.com/copilot_internal/user",
		Header: map[string]string{
			"Authorization":        "Bearer $TOKEN$",
			"Accept":               "application/vnd.github+json",
			"X-GitHub-Api-Version": "2022-11-28",
		},
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, providerFailure(resp.StatusCode)
	}

	var payload copilotUsagePayload
	if err := json.Unmarshal([]byte(resp.Body), &payload); err != nil {
		return nil, err
	}

	premium := payload.QuotaSnapshots.Premium
	label := "Premium Requests"
	var fraction float64
	switch {
	case premium.Unlimited:
		fraction = 1
		label = "Premium Requests (Unlimited)"
	case premium.PercentRemaining != nil:
		fraction = clampFraction(*premium.PercentRemaining / 100)
	case premium.Remaining != nil && premium.Entitlement != nil && *premium.Entitlement > 0:
		fraction = clampFraction(*premium.Remaining / *premium.Entitlement)
		label = fmt.Sprintf("Premium Requests (%d/%d)",
			int(*premium.Entitlement-*premium.Remaining), int(*premium.Entitlement))
	}

	return []quotaGroup{simpleGroup(
		"premium-requests",
		label,
		fraction,
		firstNonEmpty(payload.QuotaResetDateUTC, payload.QuotaResetDate),
	)}, nil
}

// ---- Antigravity ----

type antigravityBucket struct {
	ModelID           string  `json:"modelId"`
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime"`
}

// fetchAntigravityQuota reads per-model quota. The model catalogue is the
// authoritative source because it reports every model the account can use; the
// project-scoped endpoint covers only provisioned buckets and is the fallback.
func fetchAntigravityQuota(ctx context.Context, cfg pluginConfig, file authFile) ([]quotaGroup, error) {
	groups, err := fetchAntigravityModelQuota(ctx, cfg, file.AuthIndex)
	if err == nil && len(groups) > 0 {
		return groups, nil
	}
	if projectID := strings.TrimSpace(file.ProjectID); projectID != "" {
		if fallback, errProject := fetchAntigravityProjectQuota(ctx, cfg, file.AuthIndex, projectID); errProject == nil && len(fallback) > 0 {
			return fallback, nil
		}
	}
	return groups, err
}

func fetchAntigravityProjectQuota(ctx context.Context, cfg pluginConfig, authIndex, projectID string) ([]quotaGroup, error) {
	body, err := json.Marshal(map[string]string{"project": projectID})
	if err != nil {
		return nil, err
	}
	resp, err := cfg.managementAPICall(ctx, apiCallRequest{
		AuthIndex: authIndex,
		Method:    http.MethodPost,
		URL:       "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota",
		Header: map[string]string{
			"Authorization": "Bearer $TOKEN$",
			"Content-Type":  "application/json",
		},
		Data: string(body),
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, providerFailure(resp.StatusCode)
	}

	var payload struct {
		Buckets []antigravityBucket `json:"buckets"`
	}
	if err := json.Unmarshal([]byte(resp.Body), &payload); err != nil {
		return nil, err
	}

	models := make([]quotaModel, 0, len(payload.Buckets))
	for _, bucket := range payload.Buckets {
		if strings.TrimSpace(bucket.ModelID) == "" {
			continue
		}
		models = append(models, quotaModel{
			ID:                bucket.ModelID,
			DisplayName:       bucket.ModelID,
			RemainingFraction: clampFraction(bucket.RemainingFraction),
			ResetTime:         bucket.ResetTime,
		})
	}
	return groupAntigravityModels(models), nil
}

type antigravityModelEntry struct {
	DisplayName string `json:"displayName"`
	QuotaInfo   *struct {
		RemainingFraction *float64 `json:"remainingFraction"`
		ResetTime         string   `json:"resetTime"`
	} `json:"quotaInfo"`
}

func fetchAntigravityModelQuota(ctx context.Context, cfg pluginConfig, authIndex string) ([]quotaGroup, error) {
	resp, err := cfg.managementAPICall(ctx, apiCallRequest{
		AuthIndex: authIndex,
		Method:    http.MethodPost,
		URL:       "https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
		Header: map[string]string{
			"Authorization": "Bearer $TOKEN$",
			"Content-Type":  "application/json",
			"User-Agent":    "antigravity/1.11.5 windows/amd64",
		},
		Data: "{}",
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, providerFailure(resp.StatusCode)
	}

	var payload struct {
		Models json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal([]byte(resp.Body), &payload); err != nil {
		return nil, err
	}

	models, err := decodeAntigravityModels(payload.Models)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("no Antigravity quota reported")
	}
	return groupAntigravityModels(models), nil
}

// decodeAntigravityModels walks the models object as a token stream. Decoding
// into a map would work too, but streaming keeps model entries in payload order
// within each group.
func decodeAntigravityModels(raw json.RawMessage) ([]quotaModel, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))

	if token, err := decoder.Token(); err != nil {
		return nil, err
	} else if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("invalid Antigravity quota payload")
	}

	var models []quotaModel
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		id, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("invalid Antigravity quota payload")
		}

		var entry antigravityModelEntry
		if err := decoder.Decode(&entry); err != nil {
			return nil, err
		}
		if entry.QuotaInfo == nil || entry.QuotaInfo.RemainingFraction == nil {
			continue
		}
		models = append(models, quotaModel{
			ID:                id,
			DisplayName:       firstNonEmpty(entry.DisplayName, id),
			RemainingFraction: clampFraction(*entry.QuotaInfo.RemainingFraction),
			ResetTime:         entry.QuotaInfo.ResetTime,
		})
	}
	return models, nil
}

// antigravityGroupOrder fixes the order groups are reported in. The provider
// returns its model catalogue in a different order on every call, so preserving
// the payload order would reshuffle the Pi UI between refreshes.
var antigravityGroupOrder = []string{"Pro Models", "Flash Models", "Thinking Models", "Other Models"}

// groupAntigravityModels folds per-model quota into model families. A group
// reports the smallest remaining fraction among its models, since that is the
// limit a user actually hits first.
func groupAntigravityModels(models []quotaModel) []quotaGroup {
	grouped := map[string]*quotaGroup{}

	for _, model := range models {
		name := categorizeAntigravityModel(model.ID)
		group, seen := grouped[name]
		if !seen {
			group = &quotaGroup{
				ID:                strings.ToLower(strings.ReplaceAll(name, " ", "-")),
				Label:             name,
				RemainingFraction: 1,
			}
			grouped[name] = group
		}
		group.Models = append(group.Models, model)
		if model.RemainingFraction < group.RemainingFraction {
			group.RemainingFraction = model.RemainingFraction
		}
		if model.ResetTime != "" && (group.ResetTime == "" || model.ResetTime < group.ResetTime) {
			group.ResetTime = model.ResetTime
		}
	}

	groups := make([]quotaGroup, 0, len(grouped))
	for _, name := range antigravityGroupOrder {
		if group, ok := grouped[name]; ok {
			sort.Slice(group.Models, func(i, j int) bool { return group.Models[i].ID < group.Models[j].ID })
			groups = append(groups, *group)
		}
	}
	return groups
}

func categorizeAntigravityModel(modelID string) string {
	lowered := strings.ToLower(modelID)
	switch {
	case strings.Contains(lowered, "pro"):
		return "Pro Models"
	case strings.Contains(lowered, "flash"):
		return "Flash Models"
	case strings.Contains(lowered, "thinking"):
		return "Thinking Models"
	default:
		return "Other Models"
	}
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
