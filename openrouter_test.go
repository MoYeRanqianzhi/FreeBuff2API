package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func newTestProxy(cfg *Config, keys []string) (*ProxyHandler, *KeyPool) {
	pool := NewKeyPoolWithLabels(keys, make([]string, len(keys)))
	r := NewReloader("/dev/null", cfg, pool, nil)
	return NewProxyHandler(r, pool), pool
}

// TestForceOpenRouterPath: token not in api_keys, matches sk-or-, goes directly to OpenRouter.
func TestForceOpenRouterPath(t *testing.T) {
	var orHits int32
	orSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&orHits, 1)
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected OR path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-or-v1-abcdef0123456789abcdef0123456789" {
			t.Errorf("bad auth header: %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"or-1","choices":[]}`))
	}))
	defer orSrv.Close()

	cfg := &Config{
		Upstream: UpstreamConfig{
			BaseURL:      "http://dead.localhost:1",
			CostMode:     "normal",
			DefaultModel: "x",
			OpenRouter:   OpenRouterConfig{BaseURL: orSrv.URL},
		},
	}
	proxy, _ := newTestProxy(cfg, []string{"up-1"})

	body, _ := json.Marshal(map[string]any{"messages": []any{}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	ctx := context.WithValue(req.Context(), ctxKeyDownstreamToken, "sk-or-v1-abcdef0123456789abcdef0123456789")
	ctx = context.WithValue(ctx, ctxKeyForceOpenRouter, true)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	proxy.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&orHits) != 1 {
		t.Fatalf("expected 1 OR hit, got %d", orHits)
	}
	if !strings.Contains(rec.Body.String(), "or-1") {
		t.Fatalf("response body missing OR marker: %s", rec.Body.String())
	}
}

// TestFallbackOnFreeBuffFailure: token in api_keys, FreeBuff startAgentRun fails,
// falls back to OpenRouter.
func TestFallbackOnFreeBuffFailure(t *testing.T) {
	var orHits int32
	orSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&orHits, 1)
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"or-fb","choices":[]}`))
	}))
	defer orSrv.Close()

	// FreeBuff mock: agent-runs fails 500.
	fbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agent-runs" {
			w.WriteHeader(500)
			w.Write([]byte(`{"error":"boom"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer fbSrv.Close()

	cfg := &Config{
		Upstream: UpstreamConfig{
			BaseURL:      fbSrv.URL,
			CostMode:     "normal",
			DefaultModel: "x",
			OpenRouter:   OpenRouterConfig{BaseURL: orSrv.URL},
		},
	}
	proxy, _ := newTestProxy(cfg, []string{"up-1"})

	body, _ := json.Marshal(map[string]any{"messages": []any{}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	// Client token IS in server.api_keys (semantically) — here we just stash it on ctx to
	// simulate middleware having accepted it. It must itself be sk-or- to enable fallback.
	ctx := context.WithValue(req.Context(), ctxKeyDownstreamToken, "sk-or-v1-abcdef0123456789abcdef0123456789")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	proxy.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200 via fallback, got %d body=%s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&orHits) != 1 {
		t.Fatalf("expected OR fallback hit, got %d", orHits)
	}
}

// TestFallbackSkipsWhenTokenNotSkOr: FreeBuff fails; since the downstream token is
// not an sk-or- key, we must return 502 instead of falling back.
func TestFallbackSkipsWhenTokenNotSkOr(t *testing.T) {
	orHits := int32(0)
	orSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&orHits, 1)
		w.WriteHeader(200)
	}))
	defer orSrv.Close()

	fbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer fbSrv.Close()

	cfg := &Config{
		Upstream: UpstreamConfig{
			BaseURL:      fbSrv.URL,
			CostMode:     "normal",
			DefaultModel: "x",
			OpenRouter:   OpenRouterConfig{BaseURL: orSrv.URL},
		},
	}
	proxy, _ := newTestProxy(cfg, []string{"up-1"})

	body, _ := json.Marshal(map[string]any{"messages": []any{}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	// Downstream token is something else (not sk-or-).
	ctx := context.WithValue(req.Context(), ctxKeyDownstreamToken, "client-secret-xyz")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&orHits) != 0 {
		t.Fatalf("OR should not be hit, got %d", orHits)
	}
}

// TestForwardToOpenRouterStreaming confirms SSE body passes through verbatim.
func TestForwardToOpenRouterStreaming(t *testing.T) {
	orSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("data: {\"x\":1}\n\ndata: [DONE]\n\n"))
	}))
	defer orSrv.Close()

	cfg := &Config{
		Upstream: UpstreamConfig{OpenRouter: OpenRouterConfig{BaseURL: orSrv.URL}},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	forwardToOpenRouter(rec, req, []byte(`{}`), &http.Client{}, cfg, "sk-or-v1-abcdef0123456789abcdef0123456789")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	data, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(data), "[DONE]") {
		t.Fatalf("stream body not passed through: %s", data)
	}
}
