package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Admin surface. Entire /admin/* tree is disabled (404) when token.key is
// missing or empty, so the feature's existence is not disclosed by default.

type AdminHandler struct {
	reloader *Reloader
	pool     *KeyPool
}

func NewAdminHandler(reloader *Reloader, pool *KeyPool) *AdminHandler {
	return &AdminHandler{reloader: reloader, pool: pool}
}

// adminGuard gates every /admin/* request. When token.key is empty or missing,
// all admin paths return 404 without disclosing the feature.
//
// When the admin token is set:
//   - GET /admin/ (UI) passes through without a token so the login screen can load.
//   - /admin/api/* requires Authorization: Bearer <t> or X-Admin-Token: <t>.
func (a *AdminHandler) adminGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := a.reloader.AdminToken()
		if tok == "" {
			http.NotFound(w, r)
			return
		}

		// Static UI under /admin/ (but not /admin/api/*) is public so the page
		// can render a login prompt.
		if !strings.HasPrefix(r.URL.Path, "/admin/api/") {
			next.ServeHTTP(w, r)
			return
		}

		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == r.Header.Get("Authorization") {
			got = "" // no Bearer prefix
		}
		if got == "" {
			got = r.Header.Get("X-Admin-Token")
		}
		if got == "" || got != tok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"ok":    false,
				"error": "unauthorized",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mount registers the /admin/api/* routes on the provided mux. The caller is
// expected to wrap the mux with adminGuard.
func (a *AdminHandler) mount(mux *http.ServeMux) {
	mux.HandleFunc("/admin/api/status", a.handleStatus)
	mux.HandleFunc("/admin/api/config", a.handleConfig)
	mux.HandleFunc("/admin/api/keys", a.handleKeys)
	mux.HandleFunc("/admin/api/keys/", a.handleKeySub)
	mux.HandleFunc("/admin/api/reload", a.handleReload)
	mux.HandleFunc("/admin/api/oauth/start", a.handleOAuthStart)
	mux.HandleFunc("/admin/api/oauth/poll", a.handleOAuthPoll)
}

// -------------------------------- handlers --------------------------------

func (a *AdminHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	type keyView struct {
		Index       int    `json:"index"`
		Fingerprint string `json:"fingerprint"`
		Label       string `json:"label"`
		Fails       int    `json:"fails"`
		Broken      bool   `json:"broken"`
		BrokenUntil string `json:"broken_until,omitempty"`
	}
	snap := a.pool.Snapshot()
	keys := make([]keyView, 0, len(snap))
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
		keys = append(keys, kv)
	}
	writeOK(w, map[string]any{
		"total":             len(snap),
		"healthy":           a.pool.HealthySize(),
		"breaker_threshold": a.pool.Threshold(),
		"breaker_cooldown":  a.pool.Cooldown().String(),
		"keys":              keys,
	})
}

func (a *AdminHandler) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.getConfig(w, r)
	case http.MethodPut:
		a.putConfig(w, r)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *AdminHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	raw, err := os.ReadFile(a.reloader.ConfigPath())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("read config: %v", err))
		return
	}
	redacted, err := redactYAMLKeys(raw)
	if err != nil {
		// Fall back to raw if YAML is malformed — admin needs to see it to fix it.
		redacted = raw
	}
	writeOK(w, map[string]any{"yaml": string(redacted)})
}

func (a *AdminHandler) putConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML string `json:"yaml"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(body.YAML) == "" {
		writeErr(w, http.StatusBadRequest, "yaml field is empty")
		return
	}

	// Validate structure + semantic rules before touching the file.
	candidate := &Config{}
	if err := yaml.Unmarshal([]byte(body.YAML), candidate); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("yaml parse: %v", err))
		return
	}
	candidate.applyDefaults()
	if err := candidate.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// If the submitted YAML has placeholder fingerprints in api_keys, merge the
	// real values back from the live config so the admin isn't forced to retype
	// every key just to edit an unrelated field.
	merged, err := mergeRedactedKeys([]byte(body.YAML), a.reloader.Current())
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("merge redacted keys: %v", err))
		return
	}

	if err := atomicWrite(a.reloader.ConfigPath(), merged); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("write config: %v", err))
		return
	}

	// fsnotify should pick this up within ~200ms; trigger explicit reload anyway
	// to give the client a synchronous confirmation.
	a.reloader.Reload("admin-put-config")
	writeOK(w, map[string]any{"reloaded": true})
}

func (a *AdminHandler) handleKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		Label string `json:"label"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	label := strings.TrimSpace(body.Label)
	token := strings.TrimSpace(body.Token)
	if !isValidLabel(label) {
		writeErr(w, http.StatusBadRequest, "label must match [a-zA-Z0-9_-]{1,64}")
		return
	}
	if token == "" {
		writeErr(w, http.StatusBadRequest, "token required")
		return
	}

	dir := a.reloader.Current().Auth.Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("ensure auths dir: %v", err))
		return
	}
	path := filepath.Join(dir, label+".json")
	if _, err := os.Stat(path); err == nil {
		writeErr(w, http.StatusConflict, "label already exists")
		return
	}

	cred := credentialFile{
		ID:        label,
		Email:     label + "@admin.local",
		AuthToken: token,
	}
	data, _ := json.MarshalIndent(cred, "", "  ")
	if err := atomicWrite(path, data); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("write: %v", err))
		return
	}
	a.reloader.Reload("admin-post-key")
	writeOK(w, map[string]any{"path": path})
}

func (a *AdminHandler) handleKeySub(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, "/admin/api/keys/")
	if sub == "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	parts := strings.SplitN(sub, "/", 2)

	// /admin/api/keys/{idx}/trip or /reset
	if len(parts) == 2 {
		idx, err := strconv.Atoi(parts[0])
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid index")
			return
		}
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		switch parts[1] {
		case "trip":
			a.pool.TripBreaker(idx)
			writeOK(w, map[string]any{"idx": idx, "action": "trip"})
		case "reset":
			a.pool.MarkSuccess(idx)
			writeOK(w, map[string]any{"idx": idx, "action": "reset"})
		default:
			writeErr(w, http.StatusNotFound, "unknown action")
		}
		return
	}

	// /admin/api/keys/{label} — DELETE
	label := parts[0]
	if !isValidLabel(label) {
		writeErr(w, http.StatusBadRequest, "invalid label")
		return
	}
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	dir := a.reloader.Current().Auth.Dir
	path := filepath.Join(dir, label+".json")
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeErr(w, http.StatusNotFound, "no such label")
			return
		}
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("remove: %v", err))
		return
	}
	a.reloader.Reload("admin-delete-key")
	writeOK(w, map[string]any{"deleted": label})
}

func (a *AdminHandler) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	a.reloader.Reload("admin-manual")
	writeOK(w, map[string]any{"reloaded": true})
}

// -------------------------------- OAuth --------------------------------

// oauthStartResult holds the device-code handle returned to the browser.
// Callers decide which subset to JSON-encode.
type oauthStartResult struct {
	LoginURL        string
	FingerprintID   string
	FingerprintHash string
	ExpiresAt       string
}

// oauthPollResult describes the polling outcome. Pending=true → keep polling.
// When Pending=false, the credential has already been saved to auths/ and the
// reloader has been kicked; callers decide how much of Email/Name/Label to
// disclose to their clients.
type oauthPollResult struct {
	Pending bool
	Label   string
	Email   string
	Name    string
}

// oauthStart triggers the codebuff device-code flow and returns the login URL
// the user should open in their browser. Shared by admin and public handlers.
func (a *AdminHandler) oauthStart(ctx context.Context, fpPrefix string) (*oauthStartResult, error) {
	cfg := a.reloader.Current()
	fpID := fpPrefix + randomHex(8)

	body, _ := json.Marshal(map[string]string{"fingerprintId": fpID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.Upstream.BaseURL+"/api/auth/cli/code", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "freebuff2api/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codebuff: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codebuff %d: %s", resp.StatusCode, string(raw))
	}
	var codeResp struct {
		LoginURL        string `json:"loginUrl"`
		FingerprintHash string `json:"fingerprintHash"`
		ExpiresAt       string `json:"expiresAt"`
	}
	if err := json.Unmarshal(raw, &codeResp); err != nil {
		return nil, fmt.Errorf("bad codebuff response")
	}
	return &oauthStartResult{
		LoginURL:        codeResp.LoginURL,
		FingerprintID:   fpID,
		FingerprintHash: codeResp.FingerprintHash,
		ExpiresAt:       codeResp.ExpiresAt,
	}, nil
}

// oauthPoll checks the codebuff CLI status endpoint and, on success, writes
// the credential file under auths/ and kicks a reload. Returns Pending=true
// while codebuff is still waiting on the user to complete login. Shared by
// admin and public handlers.
func (a *AdminHandler) oauthPoll(ctx context.Context, fpID, fpHash, expiresAt string) (*oauthPollResult, error) {
	cfg := a.reloader.Current()
	statusURL := fmt.Sprintf("%s/api/auth/cli/status?fingerprintId=%s&fingerprintHash=%s&expiresAt=%s",
		cfg.Upstream.BaseURL,
		url.QueryEscape(fpID),
		url.QueryEscape(fpHash),
		url.QueryEscape(expiresAt))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "freebuff2api/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codebuff: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		return &oauthPollResult{Pending: true}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codebuff %d: %s", resp.StatusCode, string(raw))
	}

	var statusResp struct {
		User *struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Email     string `json:"email"`
			AuthToken string `json:"authToken"`
		} `json:"user"`
	}
	if err := json.Unmarshal(raw, &statusResp); err != nil || statusResp.User == nil {
		return &oauthPollResult{Pending: true}, nil
	}
	u := statusResp.User
	if u.AuthToken == "" {
		return &oauthPollResult{Pending: true}, nil
	}

	// Derive a filename from login info.
	label := sanitizeLabel(u.Email)
	if label == "" {
		label = sanitizeLabel(u.Name)
	}
	if label == "" {
		label = "oauth-" + randomHex(4)
	}

	// Save credential file. Create auths/ on demand.
	dir := cfg.Auth.Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("ensure auths dir: %w", err)
	}
	path := filepath.Join(dir, label+".json")
	// If label already exists, append random suffix so we don't overwrite.
	if _, err := os.Stat(path); err == nil {
		label = label + "-" + randomHex(3)
		path = filepath.Join(dir, label+".json")
	}

	cred := credentialFile{
		ID:        u.ID,
		Email:     u.Email,
		AuthToken: u.AuthToken,
	}
	if u.Name != "" {
		cred.Name = &u.Name
	}
	data, _ := json.MarshalIndent(cred, "", "  ")
	if err := atomicWrite(path, data); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	a.reloader.Reload("oauth-login")
	return &oauthPollResult{
		Label: label,
		Email: u.Email,
		Name:  u.Name,
	}, nil
}

// handleOAuthStart initiates the codebuff Device Code Flow (admin variant).
// POST /admin/api/oauth/start
func (a *AdminHandler) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	res, err := a.oauthStart(r.Context(), "fp_admin_")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeOK(w, map[string]any{
		"login_url":        res.LoginURL,
		"fingerprint_id":   res.FingerprintID,
		"fingerprint_hash": res.FingerprintHash,
		"expires_at":       res.ExpiresAt,
	})
}

// handleOAuthPoll checks login status and saves the credential on success
// (admin variant — returns full email/name/label for the admin UI).
// GET /admin/api/oauth/poll?fp=<id>&fph=<hash>&exp=<expiresAt>
func (a *AdminHandler) handleOAuthPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := r.URL.Query()
	fpID := q.Get("fp")
	fpHash := q.Get("fph")
	expiresAt := q.Get("exp")
	if fpID == "" || fpHash == "" || expiresAt == "" {
		writeErr(w, http.StatusBadRequest, "missing fp/fph/exp params")
		return
	}
	res, err := a.oauthPoll(r.Context(), fpID, fpHash, expiresAt)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if res.Pending {
		writeOK(w, map[string]any{"pending": true})
		return
	}
	writeOK(w, map[string]any{
		"done":  true,
		"label": res.Label,
		"email": res.Email,
		"name":  res.Name,
	})
}

func sanitizeLabel(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Split(s, "@")[0]
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, s)
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

func randomHex(n int) string {
	b := make([]byte, n)
	io.ReadFull(rand.Reader, b)
	return fmt.Sprintf("%x", b)
}

// --------------------------------- helpers ---------------------------------

var labelRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func isValidLabel(s string) bool {
	return labelRE.MatchString(s)
}

// atomicWrite writes data to path via a sibling .tmp + rename. On Windows this
// is atomic as long as no other process holds the target open.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// redactYAMLKeys replaces real key values in server.api_keys / auth.api_keys
// with fingerprints so the admin UI can display the config without leaking
// secrets on the wire.
func redactYAMLKeys(raw []byte) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 {
		return raw, nil
	}
	redactListAt(&root, []string{"server", "api_keys"})
	redactListAt(&root, []string{"auth", "api_keys"})
	out, err := yaml.Marshal(&root)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// redactListAt navigates node by map keys and redacts scalar items under the
// final list node.
func redactListAt(root *yaml.Node, path []string) {
	node := root
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	for _, key := range path {
		if node == nil || node.Kind != yaml.MappingNode {
			return
		}
		var found *yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				found = node.Content[i+1]
				break
			}
		}
		if found == nil {
			return
		}
		node = found
	}
	if node == nil || node.Kind != yaml.SequenceNode {
		return
	}
	for _, item := range node.Content {
		if item.Kind == yaml.ScalarNode && item.Value != "" {
			item.Value = fingerprint(item.Value)
		}
	}
}

// mergeRedactedKeys replaces any api_keys list item that looks like a fingerprint
// (contains "…") with the matching live key from current. Raw keys submitted by
// the admin pass through unchanged.
func mergeRedactedKeys(raw []byte, current *Config) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 {
		return raw, nil
	}
	if current != nil {
		mergeListAt(&root, []string{"server", "api_keys"}, current.Server.APIKeys)
		mergeListAt(&root, []string{"auth", "api_keys"}, current.Auth.APIKeys)
	}
	out, err := yaml.Marshal(&root)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func mergeListAt(root *yaml.Node, path []string, live []string) {
	node := root
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	for _, key := range path {
		if node == nil || node.Kind != yaml.MappingNode {
			return
		}
		var found *yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				found = node.Content[i+1]
				break
			}
		}
		if found == nil {
			return
		}
		node = found
	}
	if node == nil || node.Kind != yaml.SequenceNode {
		return
	}
	// Build fingerprint → real key map from live set.
	fp := make(map[string]string, len(live))
	for _, k := range live {
		fp[fingerprint(k)] = k
	}
	for _, item := range node.Content {
		if item.Kind == yaml.ScalarNode && strings.Contains(item.Value, "…") {
			if real, ok := fp[item.Value]; ok {
				item.Value = real
			}
		}
	}
}

// writeOK / writeErr / writeJSON — shared response helpers.

func writeOK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": data})
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"ok": false, "error": msg})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	b, _ := json.Marshal(body)
	w.Write(b)
}
