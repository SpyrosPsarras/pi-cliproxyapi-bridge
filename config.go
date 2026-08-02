package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// pluginConfig mirrors plugins.configs.pi-bridge.
//
// The everyday settings are checkboxes; everything deployment-specific and
// rarely touched lives in the advanced JSON blob, so the management UI stays a
// short form of toggles.
type pluginConfig struct {
	// AllowAllAPIKeys grants quota to every CLIProxyAPI key. Turning it off
	// restricts access to the keys whose own checkbox is ticked.
	AllowAllAPIKeys *bool `yaml:"allow_all_api_keys"`
	// ShowExtraAnalytics exposes CPA Manager Plus analytics when available.
	ShowExtraAnalytics bool `yaml:"show_extra_analytics"`
	// Advanced holds rarely-changed deployment settings as JSON.
	Advanced string `yaml:"advanced"`

	// selectedKeys holds the per-key checkboxes, keyed by generated field name.
	// The plugin declares one boolean field per proxy API key so operators tick
	// keys in the panel instead of typing them.
	selectedKeys map[string]bool `yaml:"-"`

	advanced advancedConfig `yaml:"-"`
}

// advancedConfig collects settings that have working defaults and normally
// never change. They are parsed from the Advanced JSON blob.
type advancedConfig struct {
	ManagementURL          string `json:"management_url"`
	ManagementKeyEnv       string `json:"management_key_env"`
	ManagementKeyFile      string `json:"management_key_file"`
	CPAMURL                string `json:"cpam_url"`
	CPAMAdminKeyEnv        string `json:"cpam_admin_key_env"`
	CPAMAdminKeyFile       string `json:"cpam_admin_key_file"`
	UsageTTLSeconds        int    `json:"usage_ttl_seconds"`
	CapabilitiesTTLSeconds int    `json:"capabilities_ttl_seconds"`
}

func defaultAdvanced() advancedConfig {
	return advancedConfig{
		ManagementURL:          "http://127.0.0.1:8317/v0/management",
		ManagementKeyEnv:       "MANAGEMENT_PASSWORD",
		CPAMURL:                "http://cpa-manager-plus:18317",
		UsageTTLSeconds:        60,
		CapabilitiesTTLSeconds: 300,
	}
}

func defaultConfig() pluginConfig {
	allowAll := true
	return pluginConfig{
		AllowAllAPIKeys: &allowAll,
		advanced:        defaultAdvanced(),
	}
}

// parseConfig decodes the plugins.configs.<id> YAML block supplied at
// plugin.register.
func parseConfig(raw []byte) (pluginConfig, error) {
	cfg := pluginConfig{}
	block := map[string]any{}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return pluginConfig{}, fmt.Errorf("decode plugin config: %w", err)
		}
		if err := yaml.Unmarshal(raw, &block); err != nil {
			return pluginConfig{}, fmt.Errorf("decode plugin config: %w", err)
		}
	}

	// An omitted checkbox means "all keys", matching the documented default.
	if cfg.AllowAllAPIKeys == nil {
		allowAll := true
		cfg.AllowAllAPIKeys = &allowAll
	}

	// Per-key checkboxes carry generated names, so they are read from the raw
	// block rather than from declared struct fields.
	cfg.selectedKeys = map[string]bool{}
	for name, value := range block {
		if !strings.HasPrefix(name, keyFieldPrefix) {
			continue
		}
		if enabled, ok := value.(bool); ok {
			cfg.selectedKeys[name] = enabled
		}
	}

	adv := defaultAdvanced()
	if blob := strings.TrimSpace(cfg.Advanced); blob != "" {
		if err := json.Unmarshal([]byte(blob), &adv); err != nil {
			return pluginConfig{}, fmt.Errorf("advanced must be a JSON object: %w", err)
		}
	}

	fallback := defaultAdvanced()
	if strings.TrimSpace(adv.ManagementURL) == "" {
		adv.ManagementURL = fallback.ManagementURL
	}
	if strings.TrimSpace(adv.ManagementKeyEnv) == "" && strings.TrimSpace(adv.ManagementKeyFile) == "" {
		adv.ManagementKeyEnv = fallback.ManagementKeyEnv
	}
	if adv.UsageTTLSeconds <= 0 {
		adv.UsageTTLSeconds = fallback.UsageTTLSeconds
	}
	if adv.CapabilitiesTTLSeconds <= 0 {
		adv.CapabilitiesTTLSeconds = fallback.CapabilitiesTTLSeconds
	}
	adv.ManagementURL = strings.TrimRight(strings.TrimSpace(adv.ManagementURL), "/")
	adv.CPAMURL = strings.TrimRight(strings.TrimSpace(adv.CPAMURL), "/")

	cfg.advanced = adv
	return cfg, nil
}

func (c pluginConfig) allowAll() bool {
	return c.AllowAllAPIKeys == nil || *c.AllowAllAPIKeys
}

// keyFieldPrefix marks the generated per-key checkbox fields.
const keyFieldPrefix = "key_"

// keyFieldName derives a stable, YAML-safe field name from an API key. It
// embeds only the masked form, so the config file never carries usable key
// material, and the same key always maps to the same checkbox.
func keyFieldName(apiKey string) string {
	var b strings.Builder
	b.WriteString(keyFieldPrefix)
	for _, r := range maskKey(apiKey) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// keySelected reports whether the operator ticked this key's checkbox.
func (c pluginConfig) keySelected(apiKey string) bool {
	return c.selectedKeys[keyFieldName(apiKey)]
}

// readSecret resolves a secret from a file first, then an environment
// variable. The value itself is never logged or returned to clients.
func readSecret(envName, filePath string) string {
	if p := strings.TrimSpace(filePath); p != "" {
		if raw, err := os.ReadFile(p); err == nil {
			if v := strings.TrimSpace(string(raw)); v != "" {
				return v
			}
		}
	}
	if name := strings.TrimSpace(envName); name != "" {
		return strings.TrimSpace(os.Getenv(name))
	}
	return ""
}

func (c pluginConfig) managementKey() string {
	return readSecret(c.advanced.ManagementKeyEnv, c.advanced.ManagementKeyFile)
}

func (c pluginConfig) cpamAdminKey() string {
	return readSecret(c.advanced.CPAMAdminKeyEnv, c.advanced.CPAMAdminKeyFile)
}
