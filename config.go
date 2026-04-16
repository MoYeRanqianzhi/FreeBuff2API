package main

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	FreebuffAPIKeys []string
	FreebuffBaseURL string
	ListenAddr      string
	APIKey          string
	DefaultModel    string
	CostMode        string
	LogLevel        string
}

func LoadConfig() (*Config, error) {
	cfg := &Config{
		FreebuffBaseURL: envOr("FREEBUFF_BASE_URL", "https://www.codebuff.com"),
		ListenAddr:      envOr("LISTEN_ADDR", ":8080"),
		APIKey:          os.Getenv("API_KEY"),
		DefaultModel:    envOr("DEFAULT_MODEL", "anthropic/claude-sonnet-4"),
		CostMode:        envOr("COST_MODE", "free"),
		LogLevel:        strings.ToLower(envOr("LOG_LEVEL", "info")),
	}

	raw := os.Getenv("FREEBUFF_API_KEY")
	if raw == "" {
		raw = os.Getenv("FREEBUFF_API_KEYS")
	}
	cfg.FreebuffAPIKeys = parseKeys(raw)
	if len(cfg.FreebuffAPIKeys) == 0 {
		return nil, fmt.Errorf("FREEBUFF_API_KEY is required (supports multiple keys separated by comma/semicolon/newline)")
	}

	cfg.FreebuffBaseURL = strings.TrimRight(cfg.FreebuffBaseURL, "/")

	return cfg, nil
}

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

func fingerprint(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:6] + "…" + key[len(key)-2:]
}
