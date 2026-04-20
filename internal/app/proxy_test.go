package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// mockUpstream serves /api/v1/agent-runs (always 200 with runId) and
// /api/v1/chat/completions whose status is decided per-call by stepFn.
func mockUpstream(t *testing.T, stepFn func(call int) int) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent-runs":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"runId":"run_test"}`)
		case "/api/v1/chat/completions":
			n := int(atomic.AddInt32(&calls, 1))
			status := stepFn(n)
			w.WriteHeader(status)
			if status == http.StatusOK {
				io.WriteString(w, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"}}]}`)
			} else {
				io.WriteString(w, `{"error":"upstream says account dead: `+strings.Repeat("x", 50)+`"}`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv, &calls
}

func buildTestProxy(t *testing.T, upstreamURL string, keys []string) *ProxyHandler {
	t.Helper()
	cfg := &Config{}
	cfg.Upstream.BaseURL = upstreamURL
	cfg.Upstream.CostMode = "normal"
	cfg.Upstream.DefaultModel = "anthropic/claude-sonnet-4"
	disabled := false
	cfg.Upstream.OpenRouter.Enabled = &disabled
	cfg.applyDefaults()
	labels := make([]string, len(keys))
	for i := range keys {
		labels[i] = "test"
	}
	pool := NewKeyPoolWithLabels(keys, labels)
	reloader := NewReloader("/dev/null", cfg, pool, nil)
	return NewProxyHandler(reloader, pool)
}

func TestRetrySucceedsOnThirdKey(t *testing.T) {
	// 1st call 401, 2nd 429, 3rd 200 — a 3-key pool should succeed.
	srv, _ := mockUpstream(t, func(n int) int {
		switch n {
		case 1:
			return http.StatusUnauthorized
		case 2:
			return http.StatusTooManyRequests
		default:
			return http.StatusOK
		}
	})
	defer srv.Close()

	p := buildTestProxy(t, srv.URL, []string{"k1", "k2", "k3"})
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id":"ok"`) {
		t.Fatalf("body missing success marker: %s", rec.Body.String())
	}

	// k1 should have fails>=1 (401 marks failure), k2 should not (429 doesn't)
	snap := p.keys.Snapshot()
	var k1, k2 *KeyEntry
	for i := range snap {
		if snap[i].Key == "k1" {
			k1 = &snap[i]
		}
		if snap[i].Key == "k2" {
			k2 = &snap[i]
		}
	}
	if k1 == nil || k1.Fails < 1 {
		t.Errorf("k1 should have fails>=1, got %+v", k1)
	}
	if k2 == nil || k2.Fails != 0 {
		t.Errorf("k2 should have fails==0 (429 doesn't mark), got %+v", k2)
	}
}

func TestRetryExhaustionReturnsSanitized(t *testing.T) {
	// All calls return 401 — client should see sanitized zh-CN message, not raw.
	srv, calls := mockUpstream(t, func(_ int) int { return http.StatusUnauthorized })
	defer srv.Close()

	p := buildTestProxy(t, srv.URL, []string{"k1", "k2", "k3", "k4"})
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "account dead") {
		t.Fatalf("upstream error leaked: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "上游账号不可用") {
		t.Fatalf("want zh-CN sanitized msg, got %s", rec.Body.String())
	}
	if got := int(atomic.LoadInt32(calls)); got != 3 {
		t.Errorf("want exactly 3 upstream attempts (maxRetries cap), got %d", got)
	}

	// Decode body to ensure it's valid JSON of the expected shape
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body not valid JSON: %v", err)
	}
	if _, ok := out["error"]; !ok {
		t.Errorf("response missing error field: %v", out)
	}
}

func TestEmptyPoolMessage(t *testing.T) {
	// No keys, no fallback → should hit the "号池无可用账号" branch.
	cfg := &Config{}
	cfg.Upstream.BaseURL = "http://unused"
	cfg.Upstream.CostMode = "normal"
	disabled := false
	cfg.Upstream.OpenRouter.Enabled = &disabled
	cfg.applyDefaults()
	pool := NewKeyPoolWithLabels(nil, nil)
	reloader := NewReloader("/dev/null", cfg, pool, nil)
	p := NewProxyHandler(reloader, pool)

	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "号池无可用账号") {
		t.Fatalf("want 号池无可用账号, got %s", rec.Body.String())
	}
}

func TestClientRPMRejects(t *testing.T) {
	srv, _ := mockUpstream(t, func(_ int) int { return http.StatusOK })
	defer srv.Close()

	// Build proxy with client_rpm=2 and attach a LimiterSet.
	cfg := &Config{}
	cfg.Upstream.BaseURL = srv.URL
	cfg.Upstream.CostMode = "normal"
	disabled := false
	cfg.Upstream.OpenRouter.Enabled = &disabled
	cfg.Limits.ClientRPM = 2
	cfg.applyDefaults()
	pool := NewKeyPoolWithLabels([]string{"k1", "k2"}, []string{"t", "t"})
	reloader := NewReloader("/dev/null", cfg, pool, nil)
	reloader.SetLimiters(NewLimiterSet(cfg.Limits))
	p := NewProxyHandler(reloader, pool)

	fire := func() *httptest.ResponseRecorder {
		body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		// Simulate the ctx stashed by authGuard
		req = req.WithContext(context.WithValue(req.Context(), ctxKeyDownstreamToken, "client-A"))
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		return rec
	}

	if r := fire(); r.Code != http.StatusOK {
		t.Fatalf("1st want 200, got %d", r.Code)
	}
	if r := fire(); r.Code != http.StatusOK {
		t.Fatalf("2nd want 200, got %d", r.Code)
	}
	r := fire()
	if r.Code != http.StatusTooManyRequests {
		t.Fatalf("3rd want 429, got %d: %s", r.Code, r.Body.String())
	}
	if !strings.Contains(r.Body.String(), "请求过于频繁") {
		t.Fatalf("want 请求过于频繁, got %s", r.Body.String())
	}
}

// firePinned runs a single request through the proxy with the pin context
// values that authGuard would have injected for a donor key.
func firePinned(t *testing.T, p *ProxyHandler, idx int, upstream string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), ctxKeyDownstreamToken, "sk-or-v1-test")
	ctx = context.WithValue(ctx, ctxKeyPinnedKeyIdx, idx)
	ctx = context.WithValue(ctx, ctxKeyPinnedUpstream, upstream)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

// TestProxyPinnedKeySuccess verifies a donor-key request hits only the pinned
// upstream and never round-robins to another key.
func TestProxyPinnedKeySuccess(t *testing.T) {
	// mockUpstream accepts every request; we track which Authorization header was used.
	var seenAuths []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenAuths = append(seenAuths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.URL.Path == "/api/v1/agent-runs" {
			io.WriteString(w, `{"runId":"r"}`)
			return
		}
		io.WriteString(w, `{"id":"ok"}`)
	}))
	defer srv.Close()

	p := buildTestProxy(t, srv.URL, []string{"k1", "k2", "k3"})
	rec := firePinned(t, p, 1, "k2")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	for _, a := range seenAuths {
		if a != "Bearer k2" {
			t.Fatalf("pinned request leaked to non-k2 upstream: %v", seenAuths)
		}
	}
}

// TestProxyPinnedKeyAccountRateLimited returns 429 without retrying any other key.
func TestProxyPinnedKeyAccountRateLimited(t *testing.T) {
	srv, calls := mockUpstream(t, func(_ int) int { return http.StatusOK })
	defer srv.Close()

	cfg := &Config{}
	cfg.Upstream.BaseURL = srv.URL
	cfg.Upstream.CostMode = "normal"
	cfg.Upstream.DefaultModel = "anthropic/claude-sonnet-4"
	disabled := false
	cfg.Upstream.OpenRouter.Enabled = &disabled
	cfg.Limits.AccountRPM = 1 // exactly 1 req/min per account
	cfg.applyDefaults()
	pool := NewKeyPoolWithLabels([]string{"k1", "k2"}, []string{"t", "t"})
	reloader := NewReloader("/dev/null", cfg, pool, nil)
	reloader.SetLimiters(NewLimiterSet(cfg.Limits))
	p := NewProxyHandler(reloader, pool)

	// 1st pinned request: consumes k1's single account token
	if r := firePinned(t, p, 0, "k1"); r.Code != http.StatusOK {
		t.Fatalf("1st want 200, got %d", r.Code)
	}
	// 2nd: k1 exhausted → must return 429 without trying k2
	r := firePinned(t, p, 0, "k1")
	if r.Code != http.StatusTooManyRequests {
		t.Fatalf("2nd pinned want 429, got %d: %s", r.Code, r.Body.String())
	}
	if !strings.Contains(r.Body.String(), "绑定账号") {
		t.Fatalf("want 绑定账号 msg, got %s", r.Body.String())
	}
	// Upstream must not have been hit a second time (agent-runs for call 1, chat for call 1 = 1 call)
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("pinned must not retry another key; upstream chat calls=%d", got)
	}
}

// TestProxyPinnedKeyBroken returns 503 when the pinned account is circuit-broken.
func TestProxyPinnedKeyBroken(t *testing.T) {
	srv, calls := mockUpstream(t, func(_ int) int { return http.StatusOK })
	defer srv.Close()

	p := buildTestProxy(t, srv.URL, []string{"k1", "k2"})
	p.keys.TripBreaker(0) // k1 broken

	r := firePinned(t, p, 0, "k1")
	if r.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", r.Code, r.Body.String())
	}
	if !strings.Contains(r.Body.String(), "绑定账号暂不可用") {
		t.Fatalf("want 绑定账号暂不可用 msg, got %s", r.Body.String())
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("broken pinned must not hit upstream, got calls=%d", got)
	}
}

// TestProxyPinnedUpstreamFailureDoesNotFallOver — when the pinned upstream
// returns 401, we return sanitized error. No cross-account retry.
func TestProxyPinnedUpstreamFailureDoesNotFallOver(t *testing.T) {
	srv, calls := mockUpstream(t, func(_ int) int { return http.StatusUnauthorized })
	defer srv.Close()

	p := buildTestProxy(t, srv.URL, []string{"k1", "k2", "k3"})
	r := firePinned(t, p, 0, "k1")
	if r.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (sanitized 401), got %d: %s", r.Code, r.Body.String())
	}
	// Only k1 should have been hit once — not other keys.
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("pinned 401 must not fall over, got %d upstream calls", got)
	}
}

// Ensure concurrent requests don't interfere (round-robin under contention).
func TestRetryConcurrent(t *testing.T) {
	srv, _ := mockUpstream(t, func(_ int) int { return http.StatusOK })
	defer srv.Close()

	p := buildTestProxy(t, srv.URL, []string{"k1", "k2", "k3"})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("concurrent want 200, got %d", rec.Code)
			}
		}()
	}
	wg.Wait()
}
