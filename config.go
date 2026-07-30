package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// fingerprintPattern matches the only accepted client key fingerprint form.
var fingerprintPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type pluginConfig struct {
	ClientAuth clientAuthConfig `yaml:"client-auth"`
	Management managementConfig `yaml:"management"`
	CPAM       cpamConfig       `yaml:"cpam"`
	Cache      cacheConfig      `yaml:"cache"`
}

type clientAuthConfig struct {
	Keys []clientKey `yaml:"keys"`
}

// clientKey authorizes one ordinary CLIProxyAPI API key by fingerprint.
// The raw key is never stored in configuration.
type clientKey struct {
	ID          string   `yaml:"id"`
	Fingerprint string   `yaml:"fingerprint"`
	Permissions []string `yaml:"permissions"`
}

func (k clientKey) allows(permission string) bool {
	for _, p := range k.Permissions {
		if strings.EqualFold(strings.TrimSpace(p), permission) {
			return true
		}
	}
	return false
}

type managementConfig struct {
	BaseURL string `yaml:"base-url"`
	KeyEnv  string `yaml:"key-env"`
	KeyFile string `yaml:"key-file"`
}

type cpamConfig struct {
	// Mode is auto (probe when base-url is set), on (require), or off.
	Mode         string `yaml:"mode"`
	BaseURL      string `yaml:"base-url"`
	AdminKeyEnv  string `yaml:"admin-key-env"`
	AdminKeyFile string `yaml:"admin-key-file"`
}

type cacheConfig struct {
	UsageTTLSeconds        int `yaml:"usage-ttl-seconds"`
	CapabilitiesTTLSeconds int `yaml:"capabilities-ttl-seconds"`
}

func defaultConfig() pluginConfig {
	return pluginConfig{
		Management: managementConfig{
			BaseURL: "http://127.0.0.1:8317/v0/management",
			KeyEnv:  "MANAGEMENT_PASSWORD",
		},
		CPAM: cpamConfig{Mode: "auto"},
		Cache: cacheConfig{
			UsageTTLSeconds:        60,
			CapabilitiesTTLSeconds: 300,
		},
	}
}

// parseConfig decodes the plugins.configs.<id> YAML block supplied at
// plugin.register. Invalid client keys are rejected outright so a malformed
// allow-list can never widen access.
func parseConfig(raw []byte) (pluginConfig, error) {
	cfg := defaultConfig()
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return pluginConfig{}, fmt.Errorf("decode plugin config: %w", err)
		}
	}

	if strings.TrimSpace(cfg.Management.BaseURL) == "" {
		cfg.Management.BaseURL = defaultConfig().Management.BaseURL
	}
	cfg.Management.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.Management.BaseURL), "/")
	cfg.CPAM.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.CPAM.BaseURL), "/")

	if strings.TrimSpace(cfg.CPAM.Mode) == "" {
		cfg.CPAM.Mode = "auto"
	}
	cfg.CPAM.Mode = strings.ToLower(strings.TrimSpace(cfg.CPAM.Mode))
	switch cfg.CPAM.Mode {
	case "auto", "on", "off":
	default:
		return pluginConfig{}, fmt.Errorf("cpam.mode must be auto, on, or off")
	}

	if cfg.Cache.UsageTTLSeconds <= 0 {
		cfg.Cache.UsageTTLSeconds = defaultConfig().Cache.UsageTTLSeconds
	}
	if cfg.Cache.CapabilitiesTTLSeconds <= 0 {
		cfg.Cache.CapabilitiesTTLSeconds = defaultConfig().Cache.CapabilitiesTTLSeconds
	}

	seenID := map[string]bool{}
	seenPrint := map[string]bool{}
	for i := range cfg.ClientAuth.Keys {
		key := &cfg.ClientAuth.Keys[i]
		key.ID = strings.TrimSpace(key.ID)
		key.Fingerprint = strings.ToLower(strings.TrimSpace(key.Fingerprint))

		if key.ID == "" {
			return pluginConfig{}, fmt.Errorf("client-auth.keys[%d]: id is required", i)
		}
		if !fingerprintPattern.MatchString(key.Fingerprint) {
			return pluginConfig{}, fmt.Errorf("client-auth.keys[%d] (%s): fingerprint must be sha256:<64 lowercase hex>", i, key.ID)
		}
		if seenID[key.ID] {
			return pluginConfig{}, fmt.Errorf("client-auth.keys: duplicate id %q", key.ID)
		}
		if seenPrint[key.Fingerprint] {
			return pluginConfig{}, fmt.Errorf("client-auth.keys: duplicate fingerprint for id %q", key.ID)
		}
		seenID[key.ID] = true
		seenPrint[key.Fingerprint] = true

		if len(key.Permissions) == 0 {
			key.Permissions = []string{permissionUsage}
		}
	}

	return cfg, nil
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
	return readSecret(c.Management.KeyEnv, c.Management.KeyFile)
}

func (c pluginConfig) cpamAdminKey() string {
	return readSecret(c.CPAM.AdminKeyEnv, c.CPAM.AdminKeyFile)
}
