package main

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	FreebuffAPIKey string
	FreebuffBaseURL string
	ListenAddr     string
	APIKey         string
	DefaultModel   string
	CostMode       string
	LogLevel       string
}

func LoadConfig() (*Config, error) {
	cfg := &Config{
		FreebuffBaseURL: envOr("FREEBUFF_BASE_URL", "https://codebuff.com"),
		ListenAddr:      envOr("LISTEN_ADDR", ":8080"),
		APIKey:          os.Getenv("API_KEY"),
		DefaultModel:    envOr("DEFAULT_MODEL", "anthropic/claude-sonnet-4"),
		CostMode:        envOr("COST_MODE", "free"),
		LogLevel:        strings.ToLower(envOr("LOG_LEVEL", "info")),
	}

	cfg.FreebuffAPIKey = os.Getenv("FREEBUFF_API_KEY")
	if cfg.FreebuffAPIKey == "" {
		return nil, fmt.Errorf("FREEBUFF_API_KEY is required")
	}

	cfg.FreebuffBaseURL = strings.TrimRight(cfg.FreebuffBaseURL, "/")

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
