package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// fingerprintPattern matches the only accepted client key fingerprint form.
var fingerprintPattern = regexp.MustCompile(`^(?:sha256:)?[0-9a-f]{64}$`)

// clientKeyPattern parses "alias:fingerprint[:permissions]" in one step, so an
// optional sha256: prefix cannot be confused with the field separator.
var clientKeyPattern = regexp.MustCompile(`^([A-Za-z0-9._-]{1,64}):(?:sha256:)?([0-9a-fA-F]{64})(?::(.*))?$`)

// pluginConfig mirrors plugins.configs.pi-bridge.
//
// Every field is a flat scalar or a list of scalars so the management UI can
// render real inputs instead of raw JSON blobs.
type pluginConfig struct {
	// ClientKeys entries are "alias:fingerprint[:permissions]", where
	// permissions is a "+"-separated list defaulting to "usage".
	ClientKeys []string `yaml:"client_keys"`

	ManagementURL     string `yaml:"management_url"`
	ManagementKeyEnv  string `yaml:"management_key_env"`
	ManagementKeyFile string `yaml:"management_key_file"`

	CPAMEnabled      string `yaml:"cpam_enabled"`
	CPAMURL          string `yaml:"cpam_url"`
	CPAMAdminKeyEnv  string `yaml:"cpam_admin_key_env"`
	CPAMAdminKeyFile string `yaml:"cpam_admin_key_file"`

	UsageTTLSeconds        int `yaml:"usage_ttl_seconds"`
	CapabilitiesTTLSeconds int `yaml:"capabilities_ttl_seconds"`

	// clients holds the parsed form of ClientKeys.
	clients []clientKey `yaml:"-"`
}

// clientKey authorizes one ordinary CLIProxyAPI API key by fingerprint.
// The raw key is never stored in configuration.
type clientKey struct {
	ID          string
	Fingerprint string
	Permissions []string
}

func (k clientKey) allows(permission string) bool {
	for _, p := range k.Permissions {
		if strings.EqualFold(strings.TrimSpace(p), permission) {
			return true
		}
	}
	return false
}

func defaultConfig() pluginConfig {
	return pluginConfig{
		ManagementURL:          "http://127.0.0.1:8317/v0/management",
		ManagementKeyEnv:       "MANAGEMENT_PASSWORD",
		CPAMEnabled:            "auto",
		UsageTTLSeconds:        60,
		CapabilitiesTTLSeconds: 300,
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

	if strings.TrimSpace(cfg.ManagementURL) == "" {
		cfg.ManagementURL = defaultConfig().ManagementURL
	}
	cfg.ManagementURL = strings.TrimRight(strings.TrimSpace(cfg.ManagementURL), "/")
	cfg.CPAMURL = strings.TrimRight(strings.TrimSpace(cfg.CPAMURL), "/")

	if strings.TrimSpace(cfg.CPAMEnabled) == "" {
		cfg.CPAMEnabled = "auto"
	}
	cfg.CPAMEnabled = strings.ToLower(strings.TrimSpace(cfg.CPAMEnabled))
	switch cfg.CPAMEnabled {
	case "auto", "on", "off":
	default:
		return pluginConfig{}, fmt.Errorf("cpam_enabled must be auto, on, or off")
	}

	if cfg.UsageTTLSeconds <= 0 {
		cfg.UsageTTLSeconds = defaultConfig().UsageTTLSeconds
	}
	if cfg.CapabilitiesTTLSeconds <= 0 {
		cfg.CapabilitiesTTLSeconds = defaultConfig().CapabilitiesTTLSeconds
	}

	clients, err := parseClientKeys(cfg.ClientKeys)
	if err != nil {
		return pluginConfig{}, err
	}
	cfg.clients = clients

	return cfg, nil
}

// parseClientKeys converts "alias:fingerprint[:perm+perm]" entries into
// authorization records.
func parseClientKeys(entries []string) ([]clientKey, error) {
	clients := make([]clientKey, 0, len(entries))
	seenID := map[string]bool{}
	seenPrint := map[string]bool{}

	for i, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		match := clientKeyPattern.FindStringSubmatch(entry)
		if match == nil {
			return nil, fmt.Errorf("client_keys[%d]: expected alias:fingerprint[:permissions] with a 64-hex fingerprint", i)
		}

		alias := match[1]
		fingerprint := strings.ToLower(match[2])

		if seenID[alias] {
			return nil, fmt.Errorf("client_keys: duplicate alias %q", alias)
		}
		if seenPrint[fingerprint] {
			return nil, fmt.Errorf("client_keys: duplicate fingerprint for alias %q", alias)
		}
		seenID[alias] = true
		seenPrint[fingerprint] = true

		permissions := []string{permissionUsage}
		if raw := strings.TrimSpace(match[3]); raw != "" {
			parsed := make([]string, 0, 2)
			for _, p := range strings.Split(raw, "+") {
				if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
					parsed = append(parsed, p)
				}
			}
			if len(parsed) > 0 {
				permissions = parsed
			}
		}

		clients = append(clients, clientKey{ID: alias, Fingerprint: fingerprint, Permissions: permissions})
	}
	return clients, nil
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
	return readSecret(c.ManagementKeyEnv, c.ManagementKeyFile)
}

func (c pluginConfig) cpamAdminKey() string {
	return readSecret(c.CPAMAdminKeyEnv, c.CPAMAdminKeyFile)
}
