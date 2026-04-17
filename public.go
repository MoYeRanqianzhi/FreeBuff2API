package main

import (
	"log"
	"net/http"
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
