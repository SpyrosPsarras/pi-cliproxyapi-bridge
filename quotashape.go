package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Provider quota payloads change shape without warning: Anthropic moved its
// windows into a `limits` array and nulled the flat fields it had used before,
// which silently dropped every per-model window until the plugin was rebuilt.
//
// The rules below describe where the numbers live instead of hard-coding it,
// so the same class of change can be answered by editing configuration and
// reloading rather than by shipping a new binary. Built-in defaults match the
// providers as they behave today, and `quota_windows` in the advanced JSON
// replaces the rules for one provider when they stop matching reality.

// quotaRule reads one quota window, or a family of windows, out of a provider
// payload.
//
// Exactly one of Field or Array is set:
//
//	Field — a single window at a fixed location, e.g. Anthropic's "five_hour".
//	Array — a list of self-describing entries, e.g. Anthropic's "limits", each
//	        yielding a window. Entries may be filtered and their labels read
//	        from the entry itself, so a window added upstream appears on its
//	        own.
//
// Paths are dot-separated and may index arrays numerically, e.g.
// "rate_limit.primary_window.used_percent" or "scope.model.display_name".
type quotaRule struct {
	// Field locates a single window object.
	Field string `json:"field,omitempty"`
	// Array locates a list of window objects.
	Array string `json:"array,omitempty"`

	// ID and Label name the resulting window. Both accept "{name}", which is
	// replaced by the text at NameFrom, so array entries can name themselves.
	ID    string `json:"id"`
	Label string `json:"label"`

	// Percent locates the number, relative to the window object. Whether it
	// counts usage or headroom is decided by PercentMeans.
	Percent string `json:"percent"`
	// PercentMeans is "used" (default) or "remaining".
	PercentMeans string `json:"percent_means,omitempty"`

	// Reset locates the reset timestamp, relative to the window object. RFC
	// 3339 text and Unix seconds are both accepted.
	Reset string `json:"reset,omitempty"`

	// NameFrom locates the text substituted into "{name}", relative to the
	// window object.
	NameFrom string `json:"name_from,omitempty"`
	// NameFallback is used when NameFrom is missing or empty.
	NameFallback string `json:"name_fallback,omitempty"`

	// Where restricts an Array rule to entries whose field equals one of the
	// listed values, so several rules can split one array by kind.
	Where map[string][]string `json:"where,omitempty"`
}

// defaultQuotaRules describes the providers as they behave today.
//
// Anthropic is read from the structured array first; the flat fields are kept
// as a fallback for accounts still answering the older way, and are skipped
// automatically when absent. Order matters only for display.
func defaultQuotaRules() map[string][]quotaRule {
	return map[string][]quotaRule{
		"claude": {
			{
				Array: "limits", ID: "five-hour", Label: "5h Session",
				Percent: "percent", Reset: "resets_at",
				Where: map[string][]string{"kind": {"session"}},
			},
			{
				Array: "limits", ID: "seven-day", Label: "7d Weekly",
				Percent: "percent", Reset: "resets_at",
				Where: map[string][]string{"kind": {"weekly_all"}},
			},
			{
				// Model-scoped weekly windows: Opus and Sonnet were joined by
				// Fable. Naming them after the entry means the next one needs
				// no change here.
				Array: "limits", ID: "seven-day-{name}", Label: "7d {name}",
				Percent: "percent", Reset: "resets_at",
				NameFrom: "scope.model.display_name", NameFallback: "Scoped",
				Where: map[string][]string{"kind": {"weekly_scoped"}},
			},
			{Field: "five_hour", ID: "five-hour", Label: "5h Session", Percent: "utilization", Reset: "resets_at"},
			{Field: "seven_day", ID: "seven-day", Label: "7d Weekly", Percent: "utilization", Reset: "resets_at"},
			{Field: "seven_day_opus", ID: "seven-day-opus", Label: "7d Opus", Percent: "utilization", Reset: "resets_at"},
			{Field: "seven_day_sonnet", ID: "seven-day-sonnet", Label: "7d Sonnet", Percent: "utilization", Reset: "resets_at"},
		},
		// Codex labels its windows from their duration ("5h Window"), which is
		// computed rather than read, so its built-in path stays in code. Rules
		// are still honoured for this provider when configured, which is what
		// matters if the payload moves.
	}
}

// applyQuotaRules turns a raw provider payload into groups. A rule that finds
// nothing contributes nothing, so listing both the current and the previous
// shape is safe: whichever the provider answers with is the one that produces
// windows. The first rule to claim a given window id wins, which is what makes
// the older flat fields a fallback rather than a duplicate.
func applyQuotaRules(payload map[string]any, rules []quotaRule) []quotaGroup {
	var groups []quotaGroup
	seen := map[string]bool{}

	emit := func(rule quotaRule, window map[string]any) {
		pct, ok := lookupFloat(window, rule.Percent)
		if !ok {
			// A window the account does not have reports null. Reporting it
			// as full quota would be worse than omitting it.
			return
		}
		remaining := remainingFromPercent(pct)
		if strings.EqualFold(rule.PercentMeans, "remaining") {
			remaining = clampFraction(pct / 100)
		}

		name := rule.NameFallback
		if rule.NameFrom != "" {
			if got, ok := lookupString(window, rule.NameFrom); ok && got != "" {
				name = got
			}
		}
		id := expandName(rule.ID, name, true)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		groups = append(groups, simpleGroup(
			id,
			expandName(rule.Label, name, false),
			remaining,
			lookupTime(window, rule.Reset),
		))
	}

	for _, rule := range rules {
		switch {
		case rule.Array != "":
			entries, ok := lookupSlice(payload, rule.Array)
			if !ok {
				continue
			}
			for _, entry := range entries {
				window, ok := entry.(map[string]any)
				if ok && ruleMatches(rule, window) {
					emit(rule, window)
				}
			}
		case rule.Field != "":
			if window, ok := lookupObject(payload, rule.Field); ok {
				emit(rule, window)
			}
		}
	}
	return groups
}

// ruleMatches applies the Where filter. An entry missing a filtered field does
// not match, so a rule cannot claim entries it was not meant to see.
func ruleMatches(rule quotaRule, window map[string]any) bool {
	for path, allowed := range rule.Where {
		got, ok := lookupString(window, path)
		if !ok {
			return false
		}
		if !containsFold(allowed, got) {
			return false
		}
	}
	return true
}

func containsFold(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

// expandName substitutes "{name}"; for ids the text is slugified so it is
// stable and safe to compare, e.g. "Fable" -> "seven-day-fable".
func expandName(template, name string, slug bool) string {
	if !strings.Contains(template, "{name}") {
		return template
	}
	if slug {
		name = slugify(name)
	}
	return strings.ReplaceAll(template, "{name}", strings.TrimSpace(name))
}

func slugify(value string) string {
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r == ' ', r == '_', r == '-':
			return '-'
		default:
			return -1
		}
	}, value)
	return strings.Trim(out, "-")
}

// --- path lookup -----------------------------------------------------------

// lookupPath walks a dot-separated path. Numeric segments index arrays.
func lookupPath(root any, path string) (any, bool) {
	if path == "" {
		return nil, false
	}
	current := root
	for _, segment := range strings.Split(path, ".") {
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[segment]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			idx, err := strconv.Atoi(segment)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			current = node[idx]
		default:
			return nil, false
		}
	}
	return current, current != nil
}

func lookupFloat(root any, path string) (float64, bool) {
	value, ok := lookupPath(root, path)
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	}
	return 0, false
}

func lookupString(root any, path string) (string, bool) {
	value, ok := lookupPath(root, path)
	if !ok {
		return "", false
	}
	if s, ok := value.(string); ok {
		return s, true
	}
	return "", false
}

func lookupObject(root any, path string) (map[string]any, bool) {
	value, ok := lookupPath(root, path)
	if !ok {
		return nil, false
	}
	obj, ok := value.(map[string]any)
	return obj, ok
}

func lookupSlice(root any, path string) ([]any, bool) {
	value, ok := lookupPath(root, path)
	if !ok {
		return nil, false
	}
	list, ok := value.([]any)
	return list, ok
}

// lookupTime accepts either an RFC 3339 string or Unix seconds, since
// providers disagree and have changed their minds before.
func lookupTime(root any, path string) string {
	if path == "" {
		return ""
	}
	if s, ok := lookupString(root, path); ok {
		return s
	}
	if f, ok := lookupFloat(root, path); ok {
		seconds := int64(f)
		return unixToRFC3339(&seconds)
	}
	return ""
}
