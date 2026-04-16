package main

import (
	"context"
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

	proxy := NewProxyHandler(cfg)

	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", proxy)
	mux.HandleFunc("/v1/models", handleModels)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      withMiddleware(mux, cfg),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("FreeBuff2API listening on %s", cfg.ListenAddr)
		log.Printf("Upstream: %s", cfg.FreebuffBaseURL)
		log.Printf("Default model: %s | Cost mode: %s", cfg.DefaultModel, cfg.CostMode)
		log.Printf("Upstream API keys: %d (round-robin)", len(cfg.FreebuffAPIKeys))
		for i, k := range cfg.FreebuffAPIKeys {
			log.Printf("  key[%d] = %s", i, fingerprint(k))
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}
