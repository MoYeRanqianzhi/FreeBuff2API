package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newReloaderForAuth builds a minimal Reloader carrying the given config for authGuard.
func newReloaderForAuth(t *testing.T, cfg *Config) *Reloader {
	t.Helper()
	pool := NewKeyPoolWithLabels([]string{"upstream-tok"}, []string{"test"})
	return NewReloader("/dev/null", cfg, pool, nil)
}

func runAuth(t *testing.T, cfg *Config, authHeader string) (statusCode int, ctxForceOR bool, ctxToken string) {
	t.Helper()
	r := newReloaderForAuth(t, cfg)
	var capturedForce bool
	var capturedToken string
	handler := authGuard(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if v, ok := req.Context().Value(ctxKeyForceOpenRouter).(bool); ok {
			capturedForce = v
		}
		if v, ok := req.Context().Value(ctxKeyDownstreamToken).(string); ok {
			capturedToken = v
		}
		w.WriteHeader(http.StatusOK)
	}), r)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, capturedForce, capturedToken
}

func TestAuthGuardNoListNoOR_PassThrough(t *testing.T) {
	enabled := false
	cfg := &Config{
		Upstream: UpstreamConfig{OpenRouter: OpenRouterConfig{Enabled: &enabled, BaseURL: "https://openrouter.ai/api/v1"}},
	}
	status, _, _ := runAuth(t, cfg, "")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
}

func TestAuthGuardTokenInList(t *testing.T) {
	cfg := &Config{
		Server:   ServerConfig{APIKeys: []string{"k1", "k2"}},
		Upstream: UpstreamConfig{OpenRouter: OpenRouterConfig{BaseURL: "https://openrouter.ai/api/v1"}},
	}
	status, force, tok := runAuth(t, cfg, "Bearer k2")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if force {
		t.Fatal("k2 is in list; force_openrouter should be false")
	}
	if tok != "k2" {
		t.Fatalf("token not stored in ctx: got %q", tok)
	}
}

func TestAuthGuardSkOrFallback(t *testing.T) {
	cfg := &Config{
		Server:   ServerConfig{APIKeys: []string{"k1"}},
		Upstream: UpstreamConfig{OpenRouter: OpenRouterConfig{BaseURL: "https://openrouter.ai/api/v1"}},
	}
	status, force, tok := runAuth(t, cfg, "Bearer sk-or-v1-abcdef0123456789abcdef0123456789")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if !force {
		t.Fatal("sk-or- outside list should set force_openrouter=true")
	}
	if tok == "" {
		t.Fatal("token must be stashed")
	}
}

func TestAuthGuardSkOrDisabled(t *testing.T) {
	enabled := false
	cfg := &Config{
		Server:   ServerConfig{APIKeys: []string{"k1"}},
		Upstream: UpstreamConfig{OpenRouter: OpenRouterConfig{Enabled: &enabled, BaseURL: "https://openrouter.ai/api/v1"}},
	}
	status, _, _ := runAuth(t, cfg, "Bearer sk-or-v1-abcdef0123456789abcdef0123456789")
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 when OR disabled and token not in list, got %d", status)
	}
}

func TestAuthGuardInvalidToken(t *testing.T) {
	cfg := &Config{
		Server:   ServerConfig{APIKeys: []string{"k1"}},
		Upstream: UpstreamConfig{OpenRouter: OpenRouterConfig{BaseURL: "https://openrouter.ai/api/v1"}},
	}
	status, _, _ := runAuth(t, cfg, "Bearer nope")
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", status)
	}
}

func TestAuthGuardMissingToken(t *testing.T) {
	cfg := &Config{
		Server:   ServerConfig{APIKeys: []string{"k1"}},
		Upstream: UpstreamConfig{OpenRouter: OpenRouterConfig{BaseURL: "https://openrouter.ai/api/v1"}},
	}
	status, _, _ := runAuth(t, cfg, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 when token missing and list non-empty, got %d", status)
	}
}

func TestIsOpenRouterKey(t *testing.T) {
	cases := []struct {
		tok  string
		want bool
	}{
		{"sk-or-v1-abcdef0123456789abcdef0123456789", true},
		{"sk-or-abcdef0123456789abcdef0123456789", true},
		{"sk-or-short", false},
		{"sk-or-", false},
		{"cb_live_xxx", false},
		{"", false},
		{"sk-ant-abc123456789012345678901234567890", false},
	}
	for _, c := range cases {
		if got := IsOpenRouterKey(c.tok); got != c.want {
			t.Errorf("IsOpenRouterKey(%q)=%v want %v", c.tok, got, c.want)
		}
	}
}
