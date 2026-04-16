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

func fingerprint(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:6] + "…" + key[len(key)-2:]
}
