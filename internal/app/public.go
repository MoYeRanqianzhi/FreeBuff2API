package app

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
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
	mux.HandleFunc("/public/oauth/events", p.handleEvents)
	mux.HandleFunc("/public/github/config", p.handleGitHubConfig)
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

// handleEvents is an SSE endpoint that blocks until the OAuth flow completes,
// then sends a single "done" event. This gives authorize.html near-zero-latency
// notification of auth success so it can redirect to the GitHub repo immediately.
func (p *PublicHandler) handleEvents(w http.ResponseWriter, r *http.Request) {
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

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher.Flush()

	ctx := r.Context()
	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(10 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout:
			fmt.Fprintf(w, "event: timeout\ndata: {}\n\n")
			flusher.Flush()
			return
		case <-ticker.C:
			res, err := p.admin.oauthPoll(ctx, fpID, fpHash, expiresAt)
			if err != nil {
				continue
			}
			if !res.Pending {
				fmt.Fprintf(w, "event: done\ndata: {}\n\n")
				flusher.Flush()
				return
			}
		}
	}
}

// handleGitHubConfig returns the configured GitHub repo name (public-safe).
func (p *PublicHandler) handleGitHubConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg := p.admin.reloader.Current()
	writeOK(w, map[string]any{
		"repo": cfg.GitHub.Repo,
	})
}
