package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
auth:
  api_keys:
    - tok-1
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ListenAddr != ":8080" {
		t.Errorf("listen default: %s", cfg.Server.ListenAddr)
	}
	if cfg.Upstream.BaseURL != "https://www.codebuff.com" {
		t.Errorf("base_url default: %s", cfg.Upstream.BaseURL)
	}
	if cfg.Upstream.CostMode != "free" {
		t.Errorf("cost_mode default: %s", cfg.Upstream.CostMode)
	}
	if cfg.Auth.WatchInterval != 15*time.Second {
		t.Errorf("watch_interval default: %s", cfg.Auth.WatchInterval)
	}
	if cfg.Auth.Breaker.Threshold != 3 {
		t.Errorf("threshold default: %d", cfg.Auth.Breaker.Threshold)
	}
	if cfg.Auth.Breaker.Cooldown != 12*time.Hour {
		t.Errorf("cooldown default: %s", cfg.Auth.Breaker.Cooldown)
	}
}

func TestLoadConfigFullYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
server:
  listen: ":9090"
  api_keys:
    - "client-secret"
upstream:
  base_url: "https://example.com/"
  cost_mode: "normal"
  default_model: "anthropic/claude-opus-4"
auth:
  api_keys:
    - tok-a
    - tok-b
    - tok-a
  dir: "./creds"
  watch_interval: 5s
  breaker:
    threshold: 5
    cooldown: 1h
logging:
  level: debug
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ListenAddr != ":9090" || len(cfg.Server.APIKeys) != 1 || cfg.Server.APIKeys[0] != "client-secret" {
		t.Errorf("server: %+v", cfg.Server)
	}
	if cfg.Upstream.BaseURL != "https://example.com" {
		t.Errorf("trailing slash not trimmed: %s", cfg.Upstream.BaseURL)
	}
	if cfg.Upstream.CostMode != "normal" || cfg.Upstream.DefaultModel != "anthropic/claude-opus-4" {
		t.Errorf("upstream: %+v", cfg.Upstream)
	}
	if len(cfg.Auth.APIKeys) != 2 {
		t.Errorf("dedup failed: %v", cfg.Auth.APIKeys)
	}
	if cfg.Auth.Dir != "./creds" || cfg.Auth.WatchInterval != 5*time.Second {
		t.Errorf("auth: %+v", cfg.Auth)
	}
	if cfg.Auth.Breaker.Threshold != 5 || cfg.Auth.Breaker.Cooldown != time.Hour {
		t.Errorf("breaker: %+v", cfg.Auth.Breaker)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("logging: %+v", cfg.Logging)
	}
}

func TestLoadConfigMissingFileFails(t *testing.T) {
	if _, err := LoadConfig("/nonexistent/path/here.yaml"); err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestLoadConfigEmptyPathFails(t *testing.T) {
	if _, err := LoadConfig(""); err == nil {
		t.Fatal("expected error for empty config path")
	}
}

func TestLoadConfigAPIKeysList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
server:
  api_keys:
    - "k1"
    - "k2"
    - "k1"
    - ""
auth:
  api_keys: ["tok-1"]
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Server.APIKeys) != 2 || cfg.Server.APIKeys[0] != "k1" || cfg.Server.APIKeys[1] != "k2" {
		t.Fatalf("dedup failed: %+v", cfg.Server.APIKeys)
	}
}

func TestLoadConfigOpenRouterDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
auth:
  api_keys: ["tok-1"]
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Upstream.OpenRouter.IsEnabled() {
		t.Fatal("openrouter should default to enabled")
	}
	if cfg.Upstream.OpenRouter.BaseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("openrouter base_url default: %s", cfg.Upstream.OpenRouter.BaseURL)
	}
}

func TestLoadConfigOpenRouterDisable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
upstream:
  openrouter:
    enabled: false
auth:
  api_keys: ["tok-1"]
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Upstream.OpenRouter.IsEnabled() {
		t.Fatal("openrouter should be disabled when enabled: false")
	}
}

func TestLoadConfigOpenRouterInvalidURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
upstream:
  openrouter:
    base_url: "not-a-url"
auth:
  api_keys: ["tok-1"]
`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected validation error for non-http openrouter base_url")
	}
}

func TestLoadConfigValidateCostMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, `
upstream:
  cost_mode: "bogus"
`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestReloaderPickupConfigChange(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	authDir := filepath.Join(dir, "auths")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfgPath, `
upstream:
  default_model: "model-1"
auth:
  api_keys: ["tok-a"]
  dir: "`+filepath.ToSlash(authDir)+`"
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := NewKeyPoolWithLabels([]string{"tok-a"}, []string{"config.yaml"})
	r := NewReloader(cfgPath, cfg, pool, nil)

	writeFile(t, cfgPath, `
upstream:
  default_model: "model-2"
auth:
  api_keys: ["tok-a", "tok-b"]
  dir: "`+filepath.ToSlash(authDir)+`"
`)
	r.Reload("test")

	now := r.Current()
	if now.Upstream.DefaultModel != "model-2" {
		t.Errorf("default_model not updated: %s", now.Upstream.DefaultModel)
	}
	if pool.Size() != 2 {
		t.Errorf("pool not reloaded: %d", pool.Size())
	}
}

func TestReloaderKeepsBreakerStateAcrossReload(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	authDir := filepath.Join(dir, "auths")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfgPath, `
auth:
  api_keys: ["tok-a", "tok-b"]
  dir: "`+filepath.ToSlash(authDir)+`"
  breaker:
    threshold: 3
    cooldown: 1h
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	keys, labels, _ := LoadKeySources(cfg.Auth.APIKeys, cfg.Auth.Dir)
	pool := NewKeyPoolWithLabels(keys, labels)
	pool.SetBreakerTuning(cfg.Auth.Breaker.Threshold, cfg.Auth.Breaker.Cooldown)
	r := NewReloader(cfgPath, cfg, pool, nil)

	// Trip tok-a.
	for _, e := range pool.Snapshot() {
		if e.Key == "tok-a" {
			break
		}
	}
	// Find index of tok-a
	idxA := -1
	for i, e := range pool.Snapshot() {
		if e.Key == "tok-a" {
			idxA = i
		}
	}
	for i := 0; i < 3; i++ {
		pool.MarkFailure(idxA)
	}

	// Reload with same content — breaker state should survive.
	r.Reload("test")

	brokenKept := false
	for _, e := range pool.Snapshot() {
		if e.Key == "tok-a" && e.Broken {
			brokenKept = true
		}
	}
	if !brokenKept {
		t.Fatal("breaker state lost across reload")
	}
}

func TestWatcherDetectsConfigEdit(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	authDir := filepath.Join(dir, "auths")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfgPath, `
upstream:
  default_model: "m1"
auth:
  api_keys: ["t1"]
  dir: "`+filepath.ToSlash(authDir)+`"
  watch_interval: 100ms
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := NewKeyPoolWithLabels([]string{"t1"}, []string{"config.yaml"})
	r := NewReloader(cfgPath, cfg, pool, nil)
	w := NewWatcher(cfgPath, r)
	// Shorter debounce so the test is quick.
	w.debounce = 30 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}

	// Ensure mtime moves past resolution.
	time.Sleep(20 * time.Millisecond)
	writeFile(t, cfgPath, `
upstream:
  default_model: "m2"
auth:
  api_keys: ["t1", "t2"]
  dir: "`+filepath.ToSlash(authDir)+`"
  watch_interval: 100ms
`)

	// fsnotify + debounce should fire within a few hundred ms; fall back to polling.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.Current().Upstream.DefaultModel == "m2" && pool.Size() == 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("watcher did not pick up change: model=%s size=%d",
		r.Current().Upstream.DefaultModel, pool.Size())
}
