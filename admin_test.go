package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newAdminTestServer creates a tempdir with config.yaml + token.key and returns
// a wired handler + Reloader. The admin token is "testadmin".
func newAdminTestServer(t *testing.T, extraYAML string) (http.Handler, *Reloader, *KeyPool, string) {
	t.Helper()
	dir := t.TempDir()
	authsDir := filepath.Join(dir, "auths")
	if err := os.MkdirAll(authsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	yaml := `server:
  listen: ":0"
upstream:
  base_url: "https://www.codebuff.com"
  cost_mode: "free"
  default_model: "anthropic/claude-sonnet-4"
auth:
  api_keys:
    - "cb_live_testkey_000001"
  dir: "` + filepath.ToSlash(authsDir) + `"
`
	if err := os.WriteFile(cfgPath, []byte(yaml+extraYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	tokPath := filepath.Join(dir, "token.key")
	if err := os.WriteFile(tokPath, []byte("testadmin\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	keys, labels, _ := LoadKeySources(cfg.Auth.APIKeys, cfg.Auth.Dir)
	pool := NewKeyPoolWithLabels(keys, labels)
	reloader := NewReloader(cfgPath, cfg, pool, nil)
	reloader.SetAdminTokenPath(tokPath)

	admin := NewAdminHandler(reloader, pool)
	root := http.NewServeMux()
	adminMux := http.NewServeMux()
	admin.mount(adminMux)
	adminMux.Handle("/admin/", http.NotFoundHandler())
	root.Handle("/admin/", admin.adminGuard(adminMux))

	return root, reloader, pool, dir
}

func TestAdminGuardNoToken(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte("server:\n  listen: \":0\"\nupstream:\n  cost_mode: \"free\"\nauth:\n  api_keys: [\"cb_live_x0000001\"]\n  dir: \""+filepath.ToSlash(filepath.Join(dir, "auths"))+"\"\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "auths"), 0o755)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := NewKeyPool([]string{"k"})
	reloader := NewReloader(cfgPath, cfg, pool, nil)
	reloader.SetAdminTokenPath(filepath.Join(dir, "does-not-exist.key"))
	admin := NewAdminHandler(reloader, pool)
	mux := http.NewServeMux()
	adminMux := http.NewServeMux()
	admin.mount(adminMux)
	mux.Handle("/admin/", admin.adminGuard(adminMux))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing token.key should yield 404; got %d", rec.Code)
	}
}

func TestAdminGuardRejectsBadToken(t *testing.T) {
	srv, _, _, _ := newAdminTestServer(t, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
	req.Header.Set("X-Admin-Token", "wrong")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token should be 401; got %d", rec.Code)
	}
}

func TestAdminStatusOK(t *testing.T) {
	srv, _, _, _ := newAdminTestServer(t, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.OK {
		t.Fatalf("ok=false in body: %s", rec.Body.String())
	}
	if resp.Data["total"].(float64) != 1 {
		t.Fatalf("expected total=1, got %v", resp.Data["total"])
	}
}

func TestAdminPutConfigValidates(t *testing.T) {
	srv, reloader, _, _ := newAdminTestServer(t, "")

	// Invalid cost_mode should be rejected before writing.
	badBody, _ := json.Marshal(map[string]string{"yaml": "upstream:\n  cost_mode: \"whatever\"\n"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/admin/api/config", bytes.NewReader(badBody))
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid cost_mode should 400; got %d", rec.Code)
	}

	// Valid YAML should persist.
	goodYAML := `server:
  listen: ":9999"
upstream:
  base_url: "https://www.codebuff.com"
  cost_mode: "normal"
  default_model: "anthropic/claude-sonnet-4"
auth:
  api_keys:
    - "cb_live_testkey_000001"
  dir: "` + filepath.ToSlash(reloader.Current().Auth.Dir) + `"
`
	goodBody, _ := json.Marshal(map[string]string{"yaml": goodYAML})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/admin/api/config", bytes.NewReader(goodBody))
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid PUT got %d body=%s", rec.Code, rec.Body.String())
	}
	if reloader.Current().Upstream.CostMode != "normal" {
		t.Fatalf("config not applied; cost_mode=%q", reloader.Current().Upstream.CostMode)
	}
}

func TestAdminPostKeyWritesAuthsFile(t *testing.T) {
	srv, reloader, _, _ := newAdminTestServer(t, "")
	body, _ := json.Marshal(map[string]string{"label": "alice", "token": "cb_live_alice_xxxxx"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/api/keys", bytes.NewReader(body))
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST keys got %d body=%s", rec.Code, rec.Body.String())
	}
	path := filepath.Join(reloader.Current().Auth.Dir, "alice.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
	if !strings.Contains(string(data), "cb_live_alice_xxxxx") {
		t.Fatalf("written file missing token: %s", string(data))
	}
}

func TestAdminKeyLabelSanitized(t *testing.T) {
	srv, _, _, _ := newAdminTestServer(t, "")
	for _, label := range []string{"../evil", "../../etc/passwd", "a/b", "../", ""} {
		body, _ := json.Marshal(map[string]string{"label": label, "token": "x"})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/admin/api/keys", bytes.NewReader(body))
		req.Header.Set("X-Admin-Token", "testadmin")
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("label %q should be rejected; got %d", label, rec.Code)
		}
	}
}

func TestAdminTripAndReset(t *testing.T) {
	srv, _, pool, _ := newAdminTestServer(t, "")
	// Trip key 0.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/api/keys/0/trip", nil)
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("trip got %d body=%s", rec.Code, rec.Body.String())
	}
	if !pool.Snapshot()[0].Broken {
		t.Fatal("trip should mark broken")
	}
	// Reset.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/admin/api/keys/0/reset", nil)
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset got %d body=%s", rec.Code, rec.Body.String())
	}
	if pool.Snapshot()[0].Broken {
		t.Fatal("reset should clear broken")
	}
}

func TestAdminConfigRedactsKeys(t *testing.T) {
	srv, _, _, _ := newAdminTestServer(t, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/config", nil)
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get config got %d body=%s", rec.Code, rec.Body.String())
	}
	body, _ := io.ReadAll(rec.Result().Body)
	if strings.Contains(string(body), "cb_live_testkey_000001") {
		t.Fatalf("raw key leaked in redacted config: %s", string(body))
	}
}
