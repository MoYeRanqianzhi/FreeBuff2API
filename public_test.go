package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaskEmail(t *testing.T) {
	cases := []struct{ in, want string }{
		{"john.doe@gmail.com", "jo***@gmail.com"},
		{"x@gmail.com", "***@gmail.com"},
		{"ab@gmail.com", "***@gmail.com"},
		{"abc@gmail.com", "ab***@gmail.com"},
		{"no-at-sign", "***"},
		{"", "***"},
		{"  padded@x.io  ", "pa***@x.io"},
	}
	for _, c := range cases {
		got := maskEmail(c.in)
		if got != c.want {
			t.Errorf("maskEmail(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// setupPublicHandler wires a PublicHandler against a mock codebuff.
func setupPublicHandler(t *testing.T, codebuffURL, authsDir string) *PublicHandler {
	t.Helper()
	cfg := &Config{}
	cfg.Upstream.BaseURL = codebuffURL
	cfg.Upstream.CostMode = "free"
	disabled := false
	cfg.Upstream.OpenRouter.Enabled = &disabled
	cfg.Auth.Dir = authsDir
	cfg.applyDefaults()
	pool := NewKeyPoolWithLabels(nil, nil)
	reloader := NewReloader(filepath.Join(authsDir, "config.yaml"), cfg, pool, nil)
	admin := NewAdminHandler(reloader, pool)
	return NewPublicHandler(admin)
}

func TestPublicStartResponseIsSanitized(t *testing.T) {
	// Mock codebuff returning a valid device-code response.
	codebuff := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/cli/code" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"loginUrl":"https://codebuff.com/cli-login?code=ABC","fingerprintHash":"hash123","expiresAt":"2026-04-17T12:00:00Z"}`)
	}))
	defer codebuff.Close()

	testStartResponse(t, codebuff.URL, "2026-04-17T12:00:00Z")
}

// TestPublicStartAcceptsNumericExpiresAt guards against a regression where the
// ExpiresAt field was decoded as `string`, which broke when codebuff switched
// to a numeric unix-ms timestamp and left users seeing "响应非 JSON" / 502.
func TestPublicStartAcceptsNumericExpiresAt(t *testing.T) {
	codebuff := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/cli/code" {
			http.NotFound(w, r)
			return
		}
		// Real codebuff now returns a number here, not an ISO string.
		io.WriteString(w, `{"loginUrl":"https://codebuff.com/cli-login?code=ABC","fingerprintHash":"hash123","expiresAt":1776406544284}`)
	}))
	defer codebuff.Close()

	testStartResponse(t, codebuff.URL, "1776406544284")
}

func testStartResponse(t *testing.T, codebuffURL, wantExpiresAt string) {
	t.Helper()

	tmp := t.TempDir()
	ph := setupPublicHandler(t, codebuffURL, tmp)

	req := httptest.NewRequest(http.MethodPost, "/public/oauth/start", nil)
	rec := httptest.NewRecorder()
	ph.handleStart(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		OK   bool                   `json:"ok"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("want ok:true, got body=%s", rec.Body.String())
	}
	// Response must contain ONLY these fields — no base_url, no admin hint.
	wantFields := map[string]bool{
		"login_url":        true,
		"fingerprint_id":   true,
		"fingerprint_hash": true,
		"expires_at":       true,
	}
	for k := range env.Data {
		if !wantFields[k] {
			t.Errorf("unexpected field in start response: %q (body=%s)", k, rec.Body.String())
		}
	}
	for k := range wantFields {
		if _, ok := env.Data[k]; !ok {
			t.Errorf("missing required field %q in start response", k)
		}
	}
	// fingerprint_id must use the public prefix so ops can separate from admin logins.
	if fpID, _ := env.Data["fingerprint_id"].(string); !strings.HasPrefix(fpID, "fp_pub_") {
		t.Errorf("fingerprint_id should start with fp_pub_, got %q", fpID)
	}
	if got, _ := env.Data["expires_at"].(string); got != wantExpiresAt {
		t.Errorf("expires_at = %q; want %q", got, wantExpiresAt)
	}
}

func TestRawJSONToString(t *testing.T) {
	cases := []struct{ in, want string }{
		{`"hello"`, "hello"},
		{`"2026-04-17T12:00:00Z"`, "2026-04-17T12:00:00Z"},
		{`1776406544284`, "1776406544284"},
		{`  1776406544284  `, "1776406544284"},
		{`null`, ""},
		{``, ""},
		{`""`, ""},
	}
	for _, c := range cases {
		got := rawJSONToString([]byte(c.in))
		if got != c.want {
			t.Errorf("rawJSONToString(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestPublicPollResponseHidesSensitiveFields(t *testing.T) {
	// Mock codebuff /status returning a real user with email/name/authToken.
	codebuff := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/auth/cli/status") {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"user":{"id":"user_secret_123","name":"John Doe","email":"john.doe@gmail.com","authToken":"cb_live_SECRET_TOKEN"}}`)
	}))
	defer codebuff.Close()

	tmp := t.TempDir()
	ph := setupPublicHandler(t, codebuff.URL, tmp)

	req := httptest.NewRequest(http.MethodGet, "/public/oauth/poll?fp=fp_pub_abc&fph=h&exp=2026-04-17T12:00:00Z", nil)
	rec := httptest.NewRecorder()
	ph.handlePoll(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// These MUST NOT appear in the response body.
	forbidden := []string{
		"cb_live_SECRET_TOKEN", // authToken
		"user_secret_123",      // user id
		"John Doe",             // full name
		"john.doe@gmail.com",   // full email (must be masked)
		"\"label\"",            // label key (would hint at auths/ internals)
		"\"name\"",
		"\"authToken\"",
	}
	for _, s := range forbidden {
		if strings.Contains(body, s) {
			t.Errorf("response leaks forbidden substring %q: %s", s, body)
		}
	}

	// Required: done:true + email_masked + donor_key.
	if !strings.Contains(body, `"done":true`) {
		t.Errorf("response missing done:true, got %s", body)
	}
	if !strings.Contains(body, `"email_masked":"jo***@gmail.com"`) {
		t.Errorf("response missing masked email, got %s", body)
	}
	// donor_key must be present in response AND be a sk-or-v1- value.
	var env struct {
		Data struct {
			DonorKey string `json:"donor_key"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(env.Data.DonorKey, "sk-or-v1-") {
		t.Errorf("donor_key missing or malformed: %q", env.Data.DonorKey)
	}

	// Credential must actually have been written (full data, server-side).
	matches, _ := filepath.Glob(filepath.Join(tmp, "*.json"))
	if len(matches) != 1 {
		t.Fatalf("want 1 credential file, got %d: %v", len(matches), matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "cb_live_SECRET_TOKEN") {
		t.Errorf("credential file missing authToken: %s", string(data))
	}
	// Persisted donor key matches what we returned.
	if !strings.Contains(string(data), env.Data.DonorKey) {
		t.Errorf("donor key not persisted to credential file: %s", string(data))
	}
}

func TestPublicPollPendingWhenCodebuff401(t *testing.T) {
	codebuff := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"not yet"}`)
	}))
	defer codebuff.Close()

	tmp := t.TempDir()
	ph := setupPublicHandler(t, codebuff.URL, tmp)

	req := httptest.NewRequest(http.MethodGet, "/public/oauth/poll?fp=fp_pub_abc&fph=h&exp=2026-04-17T12:00:00Z", nil)
	rec := httptest.NewRecorder()
	ph.handlePoll(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"pending":true`) {
		t.Errorf("want pending:true, got %s", rec.Body.String())
	}
}

func TestPublicPollRejectsMissingParams(t *testing.T) {
	ph := setupPublicHandler(t, "http://unused", t.TempDir())
	req := httptest.NewRequest(http.MethodGet, "/public/oauth/poll", nil)
	rec := httptest.NewRecorder()
	ph.handlePoll(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestPublicStartRejectsGET(t *testing.T) {
	ph := setupPublicHandler(t, "http://unused", t.TempDir())
	req := httptest.NewRequest(http.MethodGet, "/public/oauth/start", nil)
	rec := httptest.NewRecorder()
	ph.handleStart(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}

func TestPublicStartErrorIsGeneric(t *testing.T) {
	// Codebuff returning 500 — the public handler must NOT leak the upstream error body.
	codebuff := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"internal codebuff details that must not leak"}`)
	}))
	defer codebuff.Close()

	ph := setupPublicHandler(t, codebuff.URL, t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/public/oauth/start", nil)
	rec := httptest.NewRecorder()
	ph.handleStart(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "internal codebuff details") {
		t.Errorf("leaked upstream error: %s", rec.Body.String())
	}
}

func TestLoginHTMLServed(t *testing.T) {
	// loginHandler() reads the embedded asset; it should serve login.html at
	// the exact path and 404 everything else.
	h := loginHandler()

	req := httptest.NewRequest(http.MethodGet, "/login.html", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 for /login.html, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("want text/html, got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "GitHub 众筹计划") {
		t.Errorf("login.html missing expected copy")
	}
	// login.html must not reference /admin/ — we don't want to hint at admin surface.
	if strings.Contains(body, "/admin/") || strings.Contains(body, "/admin\"") {
		t.Errorf("login.html leaks /admin/ path")
	}

	// Any other path → 404 (no directory traversal, no index listing).
	for _, p := range []string{"/", "/index.html", "/login", "/login.html/"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("want 404 for %q, got %d", p, rec.Code)
		}
	}
}
