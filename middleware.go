package main

import (
	"context"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

type ctxKey string

const (
	ctxKeyDownstreamToken ctxKey = "downstream_token"
	ctxKeyForceOpenRouter ctxKey = "force_openrouter"
)

func withMiddleware(h http.Handler, reloader *Reloader) http.Handler {
	return recovery(cors(logging(authGuard(h, reloader))))
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
	})
}

// authGuard reads the client API key at request time so live config reloads
// take effect immediately (no process restart required).
//
// Matrix:
//   - expected list empty AND openrouter disabled → no-op pass-through
//   - expected list empty AND openrouter enabled  → accept sk-or-* as force-fallback, else pass
//   - token in expected list                     → accept, stash token in ctx
//   - token matches sk-or- AND openrouter enabled → accept, mark force-fallback
//   - otherwise                                  → 401
func authGuard(next http.Handler, reloader *Reloader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := reloader.Current()
		expected := cfg.Server.APIKeys
		orEnabled := cfg.Upstream.OpenRouter.IsEnabled()

		auth := r.Header.Get("Authorization")
		token := ""
		if strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		}

		if len(expected) == 0 && !orEnabled {
			next.ServeHTTP(w, r)
			return
		}

		if token == "" {
			if len(expected) == 0 {
				// No downstream auth configured but OR is on: pass through; proxy will
				// use FreeBuff only (no sk-or token available for fallback).
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, `{"error":{"message":"Missing API key","type":"authentication_error"}}`, http.StatusUnauthorized)
			return
		}

		if containsString(expected, token) {
			ctx := context.WithValue(r.Context(), ctxKeyDownstreamToken, token)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		if orEnabled && IsOpenRouterKey(token) {
			ctx := context.WithValue(r.Context(), ctxKeyDownstreamToken, token)
			ctx = context.WithValue(ctx, ctxKeyForceOpenRouter, true)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		http.Error(w, `{"error":{"message":"Invalid API key","type":"authentication_error"}}`, http.StatusUnauthorized)
	})
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("panic: %v\n%s", err, debug.Stack())
				http.Error(w, `{"error":{"message":"Internal server error","type":"server_error"}}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
