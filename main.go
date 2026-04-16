package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	keys, labels, err := LoadKeySources(cfg.FreebuffAPIKeyEnv, cfg.AuthsDir)
	if err != nil {
		log.Fatalf("load keys error: %v", err)
	}
	if len(keys) == 0 {
		log.Fatalf("no FreeBuff API keys found: set FREEBUFF_API_KEY or place credentials JSON files under %q", cfg.AuthsDir)
	}

	pool := NewKeyPoolWithLabels(keys, labels)
	proxy := NewProxyHandler(cfg, pool)

	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", proxy)
	mux.HandleFunc("/v1/models", handleModels)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/status/keys", func(w http.ResponseWriter, _ *http.Request) {
		writeKeyStatus(w, pool)
	})

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      withMiddleware(mux, cfg),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher := NewAuthsWatcher(pool, cfg.FreebuffAPIKeyEnv, cfg.AuthsDir, cfg.AuthsWatchInterval)
	watcher.Start(ctx)

	go func() {
		log.Printf("FreeBuff2API listening on %s", cfg.ListenAddr)
		log.Printf("Upstream: %s", cfg.FreebuffBaseURL)
		log.Printf("Default model: %s | Cost mode: %s", cfg.DefaultModel, cfg.CostMode)
		log.Printf("Auths dir: %s | watch interval: %s", cfg.AuthsDir, cfg.AuthsWatchInterval)
		log.Printf("Upstream API keys: %d (round-robin, breaker=%d fails/%s cooldown)",
			pool.Size(), DefaultBreakerThreshold, DefaultBreakerCooldown)
		for _, e := range pool.Snapshot() {
			log.Printf("  %s  %s", fingerprint(e.Key), e.Label)
		}
		if cfg.APIKey != "" {
			log.Print("API key authentication: enabled")
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
		Total   int       `json:"total"`
		Healthy int       `json:"healthy"`
		Keys    []keyView `json:"keys"`
	}{
		Total:   len(snap),
		Healthy: pool.HealthySize(),
		Keys:    make([]keyView, 0, len(snap)),
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
