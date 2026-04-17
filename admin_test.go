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
	keys, labels, donors, _ := LoadKeySources(cfg.Auth.APIKeys, cfg.Auth.Dir)
	pool := NewKeyPoolWithDonors(keys, labels, donors)
	reloader := NewReloader(cfgPath, cfg, pool, nil)
	reloader.SetAdminTokenPath(tokPath)

	redeem := NewRedeemStore(filepath.Join(dir, "redeem.txt"))
	admin := NewAdminHandler(reloader, pool, redeem)
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
	admin := NewAdminHandler(reloader, pool, NewRedeemStore(filepath.Join(dir, "redeem.txt")))
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

func TestAdminConfigReturnsRawKeys(t *testing.T) {
	// Admin panel is token-gated, so keys are returned in the clear (matches
	// CLIProxyAPI UX). Redaction was removed in v0.9.1.
	srv, _, _, _ := newAdminTestServer(t, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/config", nil)
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get config got %d body=%s", rec.Code, rec.Body.String())
	}
	body, _ := io.ReadAll(rec.Result().Body)
	if !strings.Contains(string(body), "cb_live_testkey_000001") {
		t.Fatalf("raw key should be visible to admin, got: %s", string(body))
	}
}

// TestDeleteKeyWithNumericLabel guards against the v0.9.1 bug where a label
// composed entirely of digits (e.g. the GitHub numeric login "1843865995")
// could collide with the /keys/{idx}/{action} routing and return
// "invalid index".
func TestDeleteKeyWithNumericLabel(t *testing.T) {
	srv, _, _, dir := newAdminTestServer(t, "")
	// Drop a numeric-labelled credential into auths/.
	numericLabel := "1843865995"
	credPath := filepath.Join(dir, "auths", numericLabel+".json")
	if err := os.MkdirAll(filepath.Dir(credPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credPath, []byte(`{"authToken":"cb_live_num_0001"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/keys/"+numericLabel, nil)
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete numeric label got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(credPath); !os.IsNotExist(err) {
		t.Fatalf("credential file not removed: err=%v", err)
	}
}

// TestAdminDonorGenerateAndClear exercises the POST/DELETE lifecycle of the
// donor-key endpoint. The on-disk credential JSON must carry the new key
// after POST, and lose it after DELETE.
func TestAdminDonorGenerateAndClear(t *testing.T) {
	srv, reloader, pool, dir := newAdminTestServer(t, "")
	// Seed an alice.json so the label is routable.
	credPath := filepath.Join(dir, "auths", "alice.json")
	if err := os.WriteFile(credPath, []byte(`{"authToken":"cb_live_alice_00001"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reloader.Reload("seed")

	// POST → generate a new donor key.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/api/keys/alice/donor", nil)
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	donor, _ := resp.Data["donor_key"].(string)
	if !strings.HasPrefix(donor, "sk-or-v1-") {
		t.Fatalf("generated donor key lacks prefix: %q", donor)
	}

	// Disk should carry the donor key.
	data, _ := os.ReadFile(credPath)
	if !strings.Contains(string(data), donor) {
		t.Fatalf("credential file missing donor key: %s", string(data))
	}

	// Pool should resolve it after the reload baked in.
	if _, _, ok := pool.ResolveDonorKey(donor); !ok {
		t.Fatal("pool did not pick up the new donor key after POST")
	}

	// DELETE → clear.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/admin/api/keys/alice/donor", nil)
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE got %d body=%s", rec.Code, rec.Body.String())
	}
	data, _ = os.ReadFile(credPath)
	if strings.Contains(string(data), "donorKey") && strings.Contains(string(data), "sk-or-v1-") {
		t.Fatalf("credential file still has donor key after DELETE: %s", string(data))
	}
	if _, _, ok := pool.ResolveDonorKey(donor); ok {
		t.Fatal("pool still resolves cleared donor key")
	}
}

// TestAdminDonorCustomValue verifies that POST with {"key":"..."} uses the
// supplied value verbatim (lets operators rotate or set a human-memorable key).
func TestAdminDonorCustomValue(t *testing.T) {
	srv, _, _, dir := newAdminTestServer(t, "")
	credPath := filepath.Join(dir, "auths", "bob.json")
	if err := os.WriteFile(credPath, []byte(`{"authToken":"cb_live_bob_00001"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{"key": "sk-or-v1-custom_xyz"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/api/keys/bob/donor", bytes.NewReader(body))
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST custom got %d body=%s", rec.Code, rec.Body.String())
	}
	data, _ := os.ReadFile(credPath)
	if !strings.Contains(string(data), "sk-or-v1-custom_xyz") {
		t.Fatalf("custom key not persisted: %s", string(data))
	}
}

// TestAdminStatusReturnsDonorKey verifies donor_key comes through /status.
func TestAdminStatusReturnsDonorKey(t *testing.T) {
	srv, _, pool, _ := newAdminTestServer(t, "")
	// The seed config has one inline key (idx 0). SetDonorKey can't persist
	// it to disk for inline keys, but it's good enough to verify the JSON shape.
	pool.SetDonorKey(0, "sk-or-v1-seeded")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sk-or-v1-seeded") {
		t.Fatalf("donor_key missing from status body: %s", rec.Body.String())
	}
}

func TestAdminRedeemEndpoint(t *testing.T) {
	srv, _, _, dir := newAdminTestServer(t, "")
	// newAdminTestServer wires NewRedeemStore(dir/redeem.txt); seed it.
	codesFile := filepath.Join(dir, "redeem.txt")
	if err := os.WriteFile(codesFile, []byte("SEED-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// GET
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/redeem", nil)
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET redeem got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"remaining":1`) {
		t.Fatalf("expected remaining:1, body=%s", rec.Body.String())
	}

	// POST codes array
	body, _ := json.Marshal(map[string]any{"codes": []string{"NEW-1", "NEW-2", "SEED-1"}})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/admin/api/redeem", bytes.NewReader(body))
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST redeem got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"added":2`) || !strings.Contains(rec.Body.String(), `"remaining":3`) {
		t.Fatalf("expected added:2 remaining:3, got %s", rec.Body.String())
	}

	// POST text
	body, _ = json.Marshal(map[string]any{"text": "LINE-A\nLINE-B\n"})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/admin/api/redeem", bytes.NewReader(body))
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST redeem text got %d", rec.Code)
	}
	raw, _ := os.ReadFile(codesFile)
	for _, want := range []string{"SEED-1", "NEW-1", "NEW-2", "LINE-A", "LINE-B"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %q in %s", want, string(raw))
		}
	}
}

func TestAdminStatusIncludesIncentive(t *testing.T) {
	srv, _, _, dir := newAdminTestServer(t, "")
	if err := os.WriteFile(filepath.Join(dir, "redeem.txt"), []byte("C1\nC2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status got %d", rec.Code)
	}
	for _, want := range []string{`"mode":"donor_key"`, `"redeem_remaining":2`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("status missing %q; body=%s", want, rec.Body.String())
		}
	}
}

// TestDeleteKeyToleratesTrailingSlash makes sure `DELETE /admin/api/keys/foo/`
// is handled like `DELETE /admin/api/keys/foo` rather than misrouted to the
// index-based endpoint.
func TestDeleteKeyToleratesTrailingSlash(t *testing.T) {
	srv, _, _, dir := newAdminTestServer(t, "")
	credPath := filepath.Join(dir, "auths", "alice.json")
	if err := os.MkdirAll(filepath.Dir(credPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credPath, []byte(`{"authToken":"cb_live_alice_0001"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/keys/alice/", nil)
	req.Header.Set("X-Admin-Token", "testadmin")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete w/ trailing slash got %d body=%s", rec.Code, rec.Body.String())
	}
}
