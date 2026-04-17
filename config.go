package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration structure for FreeBuff2API.
//
// The struct is flat and human-friendly; every knob currently controllable
// via env variables has a corresponding YAML field. Unspecified fields fall
// back to sensible defaults (see ApplyDefaults).
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Upstream UpstreamConfig `yaml:"upstream"`
	Auth     AuthConfig     `yaml:"auth"`
	Logging  LoggingConfig  `yaml:"logging"`
	Limits   LimitsConfig   `yaml:"limits"`
}

// LimitsConfig holds optional multi-tier RPM caps. Zero means unlimited at
// that tier. When a limiter rejects, the proxy returns HTTP 429 immediately —
// no internal queueing.
type LimitsConfig struct {
	// GlobalRPM caps total requests/minute across the entire proxy. 0 = unlimited.
	GlobalRPM int `yaml:"global_rpm"`
	// AccountRPM caps requests/minute against each single upstream key.
	// 0 = unlimited. When a per-account bucket is empty, the proxy skips that
	// account in round-robin (automatic load balancing).
	AccountRPM int `yaml:"account_rpm"`
	// ClientRPM caps requests/minute from each single client Bearer token.
	// 0 = unlimited.
	ClientRPM int `yaml:"client_rpm"`
}

type ServerConfig struct {
	// ListenAddr is the host:port the HTTP server binds to. Default: ":8080".
	ListenAddr string `yaml:"listen"`
	// APIKeys is the list of client Bearer tokens accepted by /v1/* endpoints.
	// Empty list disables downstream auth (unless OpenRouter fallback needs it).
	APIKeys []string `yaml:"api_keys"`
}

type UpstreamConfig struct {
	// BaseURL of the codebuff/FreeBuff backend.
	BaseURL string `yaml:"base_url"`
	// CostMode forwarded to the backend: "free" or "normal".
	CostMode string `yaml:"cost_mode"`
	// DefaultModel used when a request omits the model field.
	DefaultModel string `yaml:"default_model"`
	// OpenRouter configures the sk-or-* fallback path.
	OpenRouter OpenRouterConfig `yaml:"openrouter"`
}

type OpenRouterConfig struct {
	// BaseURL of the OpenRouter API. Default: "https://openrouter.ai/api/v1".
	BaseURL string `yaml:"base_url"`
	// Enabled toggles the fallback path. Default: true (pointer so "unset" vs "false" are distinguishable).
	// When false, sk-or-* tokens are not accepted and FreeBuff outages are not fallen back on.
	Enabled *bool `yaml:"enabled"`
}

// OpenRouterEnabled resolves the tri-state pointer to bool, defaulting to true.
func (c OpenRouterConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

type AuthConfig struct {
	// APIKeys is an inline list of FreeBuff authTokens.
	APIKeys []string `yaml:"api_keys"`
	// Dir is the directory scanned for codebuff credentials.json files.
	Dir string `yaml:"dir"`
	// WatchInterval is the poll interval for auth file changes. Default: 15s.
	WatchInterval time.Duration `yaml:"watch_interval"`
	// Breaker holds circuit breaker tuning.
	Breaker BreakerConfig `yaml:"breaker"`
}

type BreakerConfig struct {
	// Threshold is consecutive failures that trip the breaker. Default: 3.
	Threshold int `yaml:"threshold"`
	// Cooldown is how long a tripped breaker stays open. Default: 12h.
	Cooldown time.Duration `yaml:"cooldown"`
}

type LoggingConfig struct {
	// Level is one of debug/info/warn/error. Default: info.
	Level string `yaml:"level"`
}

// DefaultConfigPath is relative to the current working directory.
const DefaultConfigPath = "config.yaml"

// LoadConfig reads a YAML config file from the given path and applies defaults.
// The file must exist; missing or unreadable config is a fatal error.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		return nil, fmt.Errorf("config path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.ListenAddr == "" {
		c.Server.ListenAddr = ":8080"
	}
	if c.Upstream.BaseURL == "" {
		c.Upstream.BaseURL = "https://www.codebuff.com"
	}
	c.Upstream.BaseURL = strings.TrimRight(c.Upstream.BaseURL, "/")
	if c.Upstream.CostMode == "" {
		c.Upstream.CostMode = "free"
	}
	if c.Upstream.DefaultModel == "" {
		c.Upstream.DefaultModel = "anthropic/claude-sonnet-4"
	}
	if c.Upstream.OpenRouter.BaseURL == "" {
		c.Upstream.OpenRouter.BaseURL = "https://openrouter.ai/api/v1"
	}
	c.Upstream.OpenRouter.BaseURL = strings.TrimRight(c.Upstream.OpenRouter.BaseURL, "/")
	if c.Auth.Dir == "" {
		c.Auth.Dir = "auths"
	}
	if c.Auth.WatchInterval <= 0 {
		c.Auth.WatchInterval = 15 * time.Second
	}
	if c.Auth.Breaker.Threshold <= 0 {
		c.Auth.Breaker.Threshold = 3
	}
	if c.Auth.Breaker.Cooldown <= 0 {
		c.Auth.Breaker.Cooldown = 12 * time.Hour
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}

	c.Auth.APIKeys = dedupStrings(c.Auth.APIKeys)
	c.Server.APIKeys = dedupStrings(c.Server.APIKeys)
}

func dedupStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, k := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

// Validate surfaces errors only when the config is so broken the server can't
// start — missing keys are not a Validate error because auths/ may supply
// them separately (checked in main.go after LoadKeySources).
func (c *Config) Validate() error {
	if c.Upstream.CostMode != "free" && c.Upstream.CostMode != "normal" {
		return fmt.Errorf("upstream.cost_mode must be \"free\" or \"normal\", got %q", c.Upstream.CostMode)
	}
	if c.Upstream.OpenRouter.IsEnabled() {
		u := c.Upstream.OpenRouter.BaseURL
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("upstream.openrouter.base_url must start with http:// or https://, got %q", u)
		}
	}
	if c.Limits.GlobalRPM < 0 || c.Limits.AccountRPM < 0 || c.Limits.ClientRPM < 0 {
		return fmt.Errorf("limits.*_rpm must be >= 0 (0 = unlimited)")
	}
	return nil
}

func fingerprint(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:6] + "…" + key[len(key)-2:]
}
