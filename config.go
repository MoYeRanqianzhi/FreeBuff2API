package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	FreebuffAPIKeyEnv  string // raw env string, re-read on reload
	AuthsDir           string
	AuthsWatchInterval time.Duration
	FreebuffBaseURL    string
	ListenAddr         string
	APIKey             string
	DefaultModel       string
	CostMode           string
	LogLevel           string
}

func LoadConfig() (*Config, error) {
	raw := os.Getenv("FREEBUFF_API_KEY")
	if raw == "" {
		raw = os.Getenv("FREEBUFF_API_KEYS")
	}

	cfg := &Config{
		FreebuffAPIKeyEnv:  raw,
		AuthsDir:           envOr("AUTHS_DIR", "auths"),
		AuthsWatchInterval: parseDurationEnv("AUTHS_WATCH_INTERVAL", 15*time.Second),
		FreebuffBaseURL:    envOr("FREEBUFF_BASE_URL", "https://www.codebuff.com"),
		ListenAddr:         envOr("LISTEN_ADDR", ":8080"),
		APIKey:             os.Getenv("API_KEY"),
		DefaultModel:       envOr("DEFAULT_MODEL", "anthropic/claude-sonnet-4"),
		CostMode:           envOr("COST_MODE", "free"),
		LogLevel:           strings.ToLower(envOr("LOG_LEVEL", "info")),
	}

	cfg.FreebuffBaseURL = strings.TrimRight(cfg.FreebuffBaseURL, "/")

	return cfg, nil
}

// parseKeys tolerates comma/semicolon/newline separators and dedups.
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDurationEnv(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	// Accept bare seconds (e.g. "15")
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	return fallback
}

func fingerprint(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:6] + "…" + key[len(key)-2:]
}
