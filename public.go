package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// PublicHandler exposes the subset of OAuth needed for the crowdfunding login
// page at /login.html. It reuses AdminHandler's oauth core but strips every
// field that could hint at pool size, admin presence, or individual account
// state. Responses never contain authToken, user id, label, or the full email.
type PublicHandler struct {
	admin *AdminHandler
}

func NewPublicHandler(a *AdminHandler) *PublicHandler {
	return &PublicHandler{admin: a}
}

func (p *PublicHandler) mount(mux *http.ServeMux) {
	mux.HandleFunc("/public/oauth/start", p.handleStart)
	mux.HandleFunc("/public/oauth/poll", p.handlePoll)
	mux.HandleFunc("/public/github/config", p.handleGitHubConfig)
	mux.HandleFunc("/public/oauth/github/callback", p.handleGitHubCallback)
}

// handleStart kicks off the device-code flow. Response contains only what the
// browser needs to continue polling — no pool info, no upstream base URL.
func (p *PublicHandler) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	res, err := p.admin.oauthStart(r.Context(), "fp_pub_")
	if err != nil {
		// Surface a generic error; don't leak whether the upstream is codebuff
		// or anything else.
		writeErr(w, http.StatusBadGateway, "failed to start login")
		return
	}
	writeOK(w, map[string]any{
		"login_url":        res.LoginURL,
		"fingerprint_id":   res.FingerprintID,
		"fingerprint_hash": res.FingerprintHash,
		"expires_at":       res.ExpiresAt,
	})
}

// handlePoll surfaces only pending/done + a masked email. The server logs the
// full email + label for ops visibility; the wire response never does.
func (p *PublicHandler) handlePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := r.URL.Query()
	fpID := q.Get("fp")
	fpHash := q.Get("fph")
	expiresAt := q.Get("exp")
	if fpID == "" || fpHash == "" || expiresAt == "" {
		writeErr(w, http.StatusBadRequest, "missing params")
		return
	}
	res, err := p.admin.oauthPoll(r.Context(), fpID, fpHash, expiresAt)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "poll failed")
		return
	}
	if res.Pending {
		writeOK(w, map[string]any{"pending": true})
		return
	}
	// Success — log full info on the server, return the minimum the
	// contributor needs: masked email + exactly one reward field depending on
	// the configured incentive mode. Redeem/donor values are plaintext because
	// the device-code flow is one-shot (only the session that initiated the
	// fingerprint can retrieve them).
	log.Printf("public oauth: saved credential label=%s email=%s donor=%s redeem=%s from=%s",
		res.Label, maskEmail(res.Email), fingerprint(res.DonorKey), fingerprint(res.RedeemCode), clientIP(r))

	body := map[string]any{
		"done":         true,
		"email_masked": maskEmail(res.Email),
	}
	if res.DonorKey != "" {
		body["donor_key"] = res.DonorKey
	}
	if res.RedeemCode != "" {
		body["redeem_code"] = res.RedeemCode
		body["redeem_usage"] = res.RedeemUsage
	}
	cfg := p.admin.reloader.Current()
	if cfg.Incentive.Mode == IncentiveModeNone {
		body["thank_you"] = true
	}
	writeOK(w, body)
}

// maskEmail redacts the local part of an email down to its first two chars:
//
//	john.doe@gmail.com → jo***@gmail.com
//	x@gmail.com        → ***@gmail.com
//	(no @)             → ***
func maskEmail(s string) string {
	s = strings.TrimSpace(s)
	at := strings.LastIndex(s, "@")
	if at < 0 {
		return "***"
	}
	user := s[:at]
	domain := s[at:]
	if len(user) <= 2 {
		return "***" + domain
	}
	return user[:2] + "***" + domain
}

// clientIP extracts the best-effort source IP for audit logs. Prefers
// X-Forwarded-For (first hop) when present, falls back to RemoteAddr.
// Never trusted for auth — only for logging.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	addr := r.RemoteAddr
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		return addr[:i]
	}
	return addr
}

// handleGitHubConfig returns public-safe GitHub config (repo + client_id, no secret).
func (p *PublicHandler) handleGitHubConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg := p.admin.reloader.Current()
	body := map[string]any{
		"repo": cfg.GitHub.Repo,
	}
	if cfg.GitHub.ClientID != "" {
		body["client_id"] = cfg.GitHub.ClientID
	}
	writeOK(w, body)
}

// handleGitHubCallback exchanges a GitHub OAuth code for a token, stars the
// configured repo, and redirects the user to the repo page.
func (p *PublicHandler) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg := p.admin.reloader.Current()
	if cfg.GitHub.Repo == "" || cfg.GitHub.ClientID == "" || cfg.GitHub.ClientSecret == "" {
		http.Redirect(w, r, "https://github.com/"+cfg.GitHub.Repo, http.StatusFound)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, "https://github.com/"+cfg.GitHub.Repo, http.StatusFound)
		return
	}

	token, err := exchangeGitHubCode(cfg.GitHub.ClientID, cfg.GitHub.ClientSecret, code)
	if err != nil {
		log.Printf("github oauth exchange failed: %v", err)
		http.Redirect(w, r, "https://github.com/"+cfg.GitHub.Repo, http.StatusFound)
		return
	}

	if err := starGitHubRepo(token, cfg.GitHub.Repo); err != nil {
		log.Printf("github star failed: %v", err)
	} else {
		log.Printf("github star success: %s", cfg.GitHub.Repo)
	}

	http.Redirect(w, r, "https://github.com/"+cfg.GitHub.Repo, http.StatusFound)
}

func exchangeGitHubCode(clientID, clientSecret, code string) (string, error) {
	data := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
	}
	req, err := http.NewRequest(http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("github oauth error: %s", result.Error)
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("empty access_token")
	}
	return result.AccessToken, nil
}

func starGitHubRepo(token, repo string) error {
	req, err := http.NewRequest(http.MethodPut, "https://api.github.com/user/starred/"+repo, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "freebuff2api/1.0")
	req.ContentLength = 0

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("star request: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("star status %d", resp.StatusCode)
	}
	return nil
}
