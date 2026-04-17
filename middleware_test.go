package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// authTestEnv bundles the pieces needed to exercise authGuard — shared pool so
// tests can set donor keys before the request.
type authTestEnv struct {
	pool     *KeyPool
	reloader *Reloader
}

func newAuthEnv(t *testing.T, cfg *Config) *authTestEnv {
	t.Helper()
	pool := NewKeyPoolWithDonors(
		[]string{"upstream-tok"},
		[]string{"test"},
		[]string{""},
	)
	r := NewReloader("/dev/null", cfg, pool, nil)
	return &authTestEnv{pool: pool, reloader: r}
}

func runAuth(t *testing.T, cfg *Config, authHeader string) (statusCode int, ctxForceOR bool, ctxToken string) {
	t.Helper()
	status, force, tok, _, _ := runAuthFull(t, newAuthEnv(t, cfg), authHeader)
	return status, force, tok
}

// runAuthFull returns pinned idx (or -1 if no pin) alongside the existing data.
func runAuthFull(t *testing.T, env *authTestEnv, authHeader string) (statusCode int, ctxForceOR bool, ctxToken string, pinnedIdx int, pinnedUpstream string) {
	t.Helper()
	pinnedIdx = -1
	var capturedForce bool
	var capturedToken string
	handler := authGuard(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if v, ok := req.Context().Value(ctxKeyForceOpenRouter).(bool); ok {
			capturedForce = v
		}
		if v, ok := req.Context().Value(ctxKeyDownstreamToken).(string); ok {
			capturedToken = v
		}
		if v, ok := req.Context().Value(ctxKeyPinnedKeyIdx).(int); ok {
			pinnedIdx = v
		}
		if v, ok := req.Context().Value(ctxKeyPinnedUpstream).(string); ok {
			pinnedUpstream = v
		}
		w.WriteHeader(http.StatusOK)
	}), env.reloader, env.pool)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, capturedForce, capturedToken, pinnedIdx, pinnedUpstream
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

func TestAuthGuardDonorKeyHit(t *testing.T) {
	// Donor key should be accepted even when server.api_keys is empty (the
	// operator may have turned off client auth entirely).
	enabled := false
	cfg := &Config{
		Upstream: UpstreamConfig{OpenRouter: OpenRouterConfig{Enabled: &enabled}},
	}
	env := newAuthEnv(t, cfg)
	env.pool.SetDonorKey(0, "fb_donor_secret123")

	status, force, tok, pinned, upstream := runAuthFull(t, env, "Bearer fb_donor_secret123")
	if status != http.StatusOK {
		t.Fatalf("donor key should be accepted; got %d", status)
	}
	if force {
		t.Fatal("donor must not trigger OpenRouter fallback")
	}
	if tok != "fb_donor_secret123" {
		t.Fatalf("token not stashed: %q", tok)
	}
	if pinned != 0 {
		t.Fatalf("pinned idx not set: %d", pinned)
	}
	if upstream != "upstream-tok" {
		t.Fatalf("pinned upstream not set: %q", upstream)
	}
}

func TestAuthGuardDonorKeyMiss(t *testing.T) {
	// A fb_donor_ prefix that does not match any registered donor should 401,
	// not fall through to api_keys / sk-or branches.
	cfg := &Config{
		Server: ServerConfig{APIKeys: []string{"k1", "fb_donor_fake"}}, // even if pretend-listed, prefix branch runs first
	}
	env := newAuthEnv(t, cfg)
	status, _, _, pinned, _ := runAuthFull(t, env, "Bearer fb_donor_fake")
	if status != http.StatusUnauthorized {
		t.Fatalf("unregistered donor should 401, got %d", status)
	}
	if pinned != -1 {
		t.Fatalf("pinned idx should remain unset, got %d", pinned)
	}
}

func TestAuthGuardDonorKeyDoesNotAffectRegularTokens(t *testing.T) {
	// A client using a regular api_key must still work with donor branch enabled.
	cfg := &Config{Server: ServerConfig{APIKeys: []string{"k1"}}}
	env := newAuthEnv(t, cfg)
	env.pool.SetDonorKey(0, "fb_donor_existing")
	status, _, tok, pinned, _ := runAuthFull(t, env, "Bearer k1")
	if status != http.StatusOK {
		t.Fatalf("want 200, got %d", status)
	}
	if tok != "k1" {
		t.Fatalf("token: %q", tok)
	}
	if pinned != -1 {
		t.Fatalf("pinned must remain unset for non-donor token, got %d", pinned)
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
