package main

import (
	"errors"
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
}

type ServerConfig struct {
	// ListenAddr is the host:port the HTTP server binds to. Default: ":8080".
	ListenAddr string `yaml:"listen"`
	// APIKey guards client access to /v1/* endpoints; empty disables the check.
	APIKey string `yaml:"api_key"`
}

type UpstreamConfig struct {
	// BaseURL of the codebuff/FreeBuff backend.
	BaseURL string `yaml:"base_url"`
	// CostMode forwarded to the backend: "free" or "normal".
	CostMode string `yaml:"cost_mode"`
	// DefaultModel used when a request omits the model field.
	DefaultModel string `yaml:"default_model"`
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
// If the path is empty, it falls back to env variables (legacy mode).
func LoadConfig(path string) (*Config, error) {
	cfg := &Config{}

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// File missing is acceptable: callers may supply everything via env.
				// We return an empty config that defaults + env fill in downstream.
			} else {
				return nil, fmt.Errorf("read %s: %w", path, err)
			}
		} else {
			if err := yaml.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("parse %s: %w", path, err)
			}
		}
	}

	cfg.applyEnvOverrides()
	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyEnvOverrides lets env variables win over config file values for ops
// conveniences (e.g. setting API keys via docker-compose env without editing
// a mounted file). Applied before defaults so empties don't overwrite YAML.
func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		c.Server.ListenAddr = v
	}
	if v := os.Getenv("API_KEY"); v != "" {
		c.Server.APIKey = v
	}
	if v := os.Getenv("FREEBUFF_BASE_URL"); v != "" {
		c.Upstream.BaseURL = v
	}
	if v := os.Getenv("COST_MODE"); v != "" {
		c.Upstream.CostMode = v
	}
	if v := os.Getenv("DEFAULT_MODEL"); v != "" {
		c.Upstream.DefaultModel = v
	}
	if v := os.Getenv("FREEBUFF_API_KEY"); v != "" {
		c.Auth.APIKeys = append(c.Auth.APIKeys, parseKeys(v)...)
	} else if v := os.Getenv("FREEBUFF_API_KEYS"); v != "" {
		c.Auth.APIKeys = append(c.Auth.APIKeys, parseKeys(v)...)
	}
	if v := os.Getenv("AUTHS_DIR"); v != "" {
		c.Auth.Dir = v
	}
	if v := os.Getenv("AUTHS_WATCH_INTERVAL"); v != "" {
		if d, err := parseDurationFlexible(v); err == nil {
			c.Auth.WatchInterval = d
		}
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		c.Logging.Level = strings.ToLower(v)
	}
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

	// Dedup inline api_keys so reload is idempotent.
	seen := make(map[string]struct{}, len(c.Auth.APIKeys))
	out := c.Auth.APIKeys[:0]
	for _, k := range c.Auth.APIKeys {
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
	c.Auth.APIKeys = out
}

// Validate surfaces errors only when the config is so broken the server can't
// start — missing keys are not a Validate error because auths/ may supply
// them separately (checked in main.go after LoadKeySources).
func (c *Config) Validate() error {
	if c.Upstream.CostMode != "free" && c.Upstream.CostMode != "normal" {
		return fmt.Errorf("upstream.cost_mode must be \"free\" or \"normal\", got %q", c.Upstream.CostMode)
	}
	return nil
}

// parseKeys tolerates comma/semicolon/newline separators and dedups.
// Kept for env-variable parsing (YAML users put one key per list item).
func parseKeys(raw string) []string {
	if raw == "" {
		return nil
	}
	replacer := strings.NewReplacer(",", "\n", ";", "\n", "\r", "\n")
	normalized := replacer.Replace(raw)
	seen := make(map[string]struct{})
	out := make([]string, 0, 4)
	for _, line := range strings.Split(normalized, "\n") {
		k := strings.TrimSpace(line)
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

// parseDurationFlexible accepts both Go durations ("15s", "12h") and bare seconds ("15").
func parseDurationFlexible(v string) (time.Duration, error) {
	if d, err := time.ParseDuration(v); err == nil {
		return d, nil
	}
	// try bare seconds
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return 0, fmt.Errorf("invalid duration %q", v)
}

func fingerprint(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:6] + "…" + key[len(key)-2:]
}
