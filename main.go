package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	configPath := flag.String("config", DefaultConfigPath, "path to YAML config (default: ./config.yaml)")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	keys, labels, err := LoadKeySources(cfg.Auth.APIKeys, cfg.Auth.Dir)
	if err != nil {
		log.Fatalf("load keys error: %v", err)
	}
	if len(keys) == 0 {
		log.Fatalf("no FreeBuff API keys found: set auth.api_keys in %q or place credentials JSON files under %q",
			*configPath, cfg.Auth.Dir)
	}

	pool := NewKeyPoolWithLabels(keys, labels)
	pool.SetBreakerTuning(cfg.Auth.Breaker.Threshold, cfg.Auth.Breaker.Cooldown)

	reloader := NewReloader(*configPath, cfg, pool, nil)
	proxy := NewProxyHandler(reloader, pool)

	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", proxy)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/status/keys", func(w http.ResponseWriter, _ *http.Request) {
		writeKeyStatus(w, pool)
	})

	srv := &http.Server{
		Addr:         cfg.Server.ListenAddr,
		Handler:      withMiddleware(mux, reloader),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher := NewWatcher(*configPath, reloader)
	if err := watcher.Start(ctx); err != nil {
		log.Fatalf("watcher start: %v", err)
	}

	go func() {
		log.Printf("FreeBuff2API listening on %s (config=%s)", cfg.Server.ListenAddr, *configPath)
		log.Printf("Upstream: %s", cfg.Upstream.BaseURL)
		log.Printf("Default model: %s | Cost mode: %s", cfg.Upstream.DefaultModel, cfg.Upstream.CostMode)
		log.Printf("Auths dir: %s | watch interval: %s", cfg.Auth.Dir, cfg.Auth.WatchInterval)
		log.Printf("Upstream API keys: %d (round-robin, breaker=%d fails/%s cooldown)",
			pool.Size(), cfg.Auth.Breaker.Threshold, cfg.Auth.Breaker.Cooldown)
		for _, e := range pool.Snapshot() {
			log.Printf("  %s  %s", fingerprint(e.Key), e.Label)
		}
		if n := len(cfg.Server.APIKeys); n > 0 {
			log.Printf("Client API key authentication: enabled (%d key(s))", n)
		} else {
			log.Print("Client API key authentication: disabled")
		}
		if cfg.Upstream.OpenRouter.IsEnabled() {
			log.Printf("OpenRouter fallback: enabled (base_url=%s)", cfg.Upstream.OpenRouter.BaseURL)
		} else {
			log.Print("OpenRouter fallback: disabled")
		}
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Print("shutting down...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	srv.Shutdown(shutdownCtx)
}

func writeKeyStatus(w http.ResponseWriter, pool *KeyPool) {
	type keyView struct {
		Index       int    `json:"index"`
		Fingerprint string `json:"fingerprint"`
		Label       string `json:"label"`
		Fails       int    `json:"fails"`
		Broken      bool   `json:"broken"`
		BrokenUntil string `json:"broken_until,omitempty"`
	}
	snap := pool.Snapshot()
	out := struct {
		Total     int       `json:"total"`
		Healthy   int       `json:"healthy"`
		Threshold int       `json:"breaker_threshold"`
		Cooldown  string    `json:"breaker_cooldown"`
		Keys      []keyView `json:"keys"`
	}{
		Total:     len(snap),
		Healthy:   pool.HealthySize(),
		Threshold: pool.Threshold(),
		Cooldown:  pool.Cooldown().String(),
		Keys:      make([]keyView, 0, len(snap)),
	}
	for i, e := range snap {
		kv := keyView{
			Index:       i,
			Fingerprint: fingerprint(e.Key),
			Label:       e.Label,
			Fails:       e.Fails,
			Broken:      e.Broken,
		}
		if e.Broken {
			kv.BrokenUntil = e.BrokenUntil.Format(time.RFC3339)
		}
		out.Keys = append(out.Keys, kv)
	}
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Fprintln(w, string(b))
}
