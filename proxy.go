package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// sanitizeUpstreamError maps an upstream HTTP status to a client-facing
// (status, jsonBody) pair. body == "" means caller should pass the upstream
// response through unchanged. Successful 2xx never reach this function.
func sanitizeUpstreamError(status int) (int, string) {
	switch {
	case status == http.StatusUnauthorized,
		status == http.StatusPaymentRequired,
		status == http.StatusForbidden:
		return http.StatusServiceUnavailable,
			`{"error":{"message":"上游账号不可用，请稍后重试","type":"upstream_unavailable"}}`
	case status == http.StatusTooManyRequests:
		return http.StatusServiceUnavailable,
			`{"error":{"message":"上游限流，请稍后重试","type":"upstream_throttled"}}`
	case status >= 500:
		return http.StatusBadGateway,
			`{"error":{"message":"上游服务异常，请稍后重试","type":"upstream_error"}}`
	case status == http.StatusBadRequest:
		return http.StatusBadRequest,
			`{"error":{"message":"请求被上游拒绝","type":"invalid_request"}}`
	}
	return status, ""
}

// writeSanitized writes a plain JSON error body with the given status.
func writeSanitized(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

type ProxyHandler struct {
	reloader *Reloader
	client   *http.Client
	keys     *KeyPool
}

func NewProxyHandler(reloader *Reloader, pool *KeyPool) *ProxyHandler {
	return &ProxyHandler{
		reloader: reloader,
		client:   &http.Client{},
		keys:     pool,
	}
}

// limits returns the active LimiterSet or nil if none is attached (in which
// case all the Allow calls short-circuit to true — unlimited).
func (p *ProxyHandler) limits() *LimiterSet {
	return p.reloader.Limiters()
}

func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request_error"}}`, http.StatusMethodNotAllowed)
		return
	}

	cfg := p.reloader.Current()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":{"message":"Failed to read request body","type":"invalid_request_error"}}`, http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Force-OpenRouter path: client Bearer matched sk-or- and was not in the api_keys list.
	if force, _ := r.Context().Value(ctxKeyForceOpenRouter).(bool); force {
		tok, _ := r.Context().Value(ctxKeyDownstreamToken).(string)
		log.Printf("→ OpenRouter (forced, token=%s)", fingerprint(tok))
		forwardToOpenRouter(w, r, body, p.client, cfg, tok)
		return
	}

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request_error"}}`, http.StatusBadRequest)
		return
	}

	if _, ok := req["model"]; !ok || req["model"] == "" {
		req["model"] = cfg.Upstream.DefaultModel
	}

	// downstreamToken is the client-presented Bearer; used to tentatively fall back to
	// OpenRouter if FreeBuff fails AND the token itself is an sk-or- key.
	downstreamToken, _ := r.Context().Value(ctxKeyDownstreamToken).(string)
	canFallback := cfg.Upstream.OpenRouter.IsEnabled() && IsOpenRouterKey(downstreamToken)

	// RPM limit checks (reject-only; no queueing). client → global. Account is
	// handled inside the retry loop as a filter so its rejection falls over to
	// the next healthy account automatically.
	limits := p.limits()
	if !limits.ClientAllow(downstreamToken) {
		writeSanitized(w, http.StatusTooManyRequests,
			`{"error":{"message":"请求过于频繁，请稍后重试","type":"rate_limited"}}`)
		return
	}
	if !limits.GlobalAllow() {
		writeSanitized(w, http.StatusTooManyRequests,
			`{"error":{"message":"服务繁忙，请稍后重试","type":"rate_limited"}}`)
		return
	}

	isStream := false
	if s, ok := req["stream"]; ok {
		if b, ok := s.(bool); ok {
			isStream = b
		}
	}

	// Per-request retry loop across up to maxRetries distinct upstream keys.
	// The loop handles empty pool, all-keys-tried, and fallback-on-exhaustion.
	const maxRetriesCap = 3
	healthy := p.keys.HealthySize()
	maxRetries := maxRetriesCap
	if healthy > 0 && healthy < maxRetries {
		maxRetries = healthy
	}
	if maxRetries < 1 {
		maxRetries = 1
	}

	tried := make(map[int]struct{}, maxRetries)
	lastStatus := 0

	accountRateLimited := false
	for attempt := 0; attempt < maxRetries; attempt++ {
		upstreamKey, keyIdx, ok := p.keys.NextAvailable(func(key string, idx int) bool {
			if _, seen := tried[idx]; seen {
				return false
			}
			// Skip accounts whose per-key bucket is empty — they'll become
			// available again after refill. If that's the only reason we're
			// skipping, remember so the exhaustion branch can tell the user.
			if !limits.AccountAllow(key) {
				accountRateLimited = true
				return false
			}
			return true
		})
		if !ok {
			break
		}
		tried[keyIdx] = struct{}{}
		log.Printf("→ upstream key[%d]=%s (attempt %d/%d)", keyIdx, fingerprint(upstreamKey), attempt+1, maxRetries)

		runID, err := p.startAgentRun(r.Context(), cfg.Upstream.BaseURL, upstreamKey)
		if err != nil {
			p.keys.MarkFailure(keyIdx)
			// Refund the account token we took via AccountAllow — this attempt
			// never actually hit chat/completions on this key.
			limits.AccountRefund(upstreamKey)
			log.Printf("retry %d: startAgentRun key[%d]=%s failed: %v", attempt+1, keyIdx, fingerprint(upstreamKey), err)
			lastStatus = http.StatusBadGateway
			continue
		}

		req["codebuff_metadata"] = map[string]any{
			"run_id":    runID,
			"client_id": "freebuff2api",
			"cost_mode": cfg.Upstream.CostMode,
			"n":         1,
		}
		req["usage"] = map[string]any{"include": true}

		modified, err := json.Marshal(req)
		if err != nil {
			http.Error(w, `{"error":{"message":"Failed to encode request","type":"server_error"}}`, http.StatusInternalServerError)
			return
		}

		targetURL := cfg.Upstream.BaseURL + "/api/v1/chat/completions"
		upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(modified))
		if err != nil {
			http.Error(w, `{"error":{"message":"Failed to create upstream request","type":"server_error"}}`, http.StatusInternalServerError)
			return
		}
		upstream.Header.Set("Authorization", "Bearer "+upstreamKey)
		upstream.Header.Set("Content-Type", "application/json")
		upstream.Header.Set("User-Agent", "freebuff2api/1.0")

		resp, err := p.client.Do(upstream)
		if err != nil {
			p.keys.MarkFailure(keyIdx)
			// Network error before any response — the account wasn't actually
			// billed for this attempt, so refund its rate token.
			limits.AccountRefund(upstreamKey)
			log.Printf("retry %d: upstream net err key[%d]=%s: %v", attempt+1, keyIdx, fingerprint(upstreamKey), err)
			lastStatus = http.StatusBadGateway
			continue
		}

		// Retryable upstream HTTP statuses: try the next key.
		if isRetryableStatus(resp.StatusCode) {
			if resp.StatusCode != http.StatusTooManyRequests {
				// 429 doesn't mark failure (rate limit ≠ key invalid)
				p.keys.MarkFailure(keyIdx)
			}
			log.Printf("retry %d: upstream status %d key[%d]=%s — trying next",
				attempt+1, resp.StatusCode, keyIdx, fingerprint(upstreamKey))
			lastStatus = resp.StatusCode
			// Drain before close so the TCP connection can be reused.
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			continue
		}

		// Non-retryable path (2xx success OR 400 bad request).
		if resp.StatusCode < 400 {
			p.keys.MarkSuccess(keyIdx)
		}

		if resp.StatusCode == http.StatusBadRequest {
			resp.Body.Close()
			writeSanitized(w, http.StatusBadRequest,
				`{"error":{"message":"请求被上游拒绝","type":"invalid_request"}}`)
			return
		}

		// 2xx success
		if isStream && resp.StatusCode == http.StatusOK {
			p.handleStream(w, resp)
		} else {
			p.handleNonStream(w, resp)
		}
		resp.Body.Close()
		return
	}

	// All retries exhausted. If OpenRouter fallback is an option, use it.
	if canFallback {
		log.Printf("→ OpenRouter (all %d upstream retries failed, fallback token=%s)", len(tried), fingerprint(downstreamToken))
		forwardToOpenRouter(w, r, body, p.client, cfg, downstreamToken)
		return
	}

	// Nothing was tried: either the pool is empty, every account is rate-limited,
	// or every account is circuit-broken.
	if len(tried) == 0 {
		if accountRateLimited {
			writeSanitized(w, http.StatusTooManyRequests,
				`{"error":{"message":"所有上游账号繁忙，请稍后重试","type":"rate_limited"}}`)
			return
		}
		if p.keys.Size() == 0 {
			writeSanitized(w, http.StatusServiceUnavailable,
				`{"error":{"message":"号池无可用账号，请联系管理员添加上游账号","type":"pool_empty"}}`)
			return
		}
		// Pool has keys but all are circuit-broken.
		writeSanitized(w, http.StatusServiceUnavailable,
			`{"error":{"message":"所有上游账号均已熔断，请稍后重试","type":"pool_all_broken"}}`)
		return
	}

	// Tried one or more keys, all failed — sanitize based on the last status.
	status, sanitized := sanitizeUpstreamError(lastStatus)
	if sanitized == "" {
		status = http.StatusBadGateway
		sanitized = `{"error":{"message":"上游服务异常，请稍后重试","type":"upstream_error"}}`
	}
	writeSanitized(w, status, sanitized)
}

// isRetryableStatus reports whether a response with this status should trigger
// a retry on a different upstream key (401/402/403/429/5xx). 400 is not
// retryable — a malformed request won't succeed on another account.
func isRetryableStatus(status int) bool {
	switch {
	case status == http.StatusUnauthorized,
		status == http.StatusPaymentRequired,
		status == http.StatusForbidden,
		status == http.StatusTooManyRequests:
		return true
	case status >= 500:
		return true
	}
	return false
}

func (p *ProxyHandler) handleStream(w http.ResponseWriter, resp *http.Response) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":{"message":"Streaming not supported","type":"server_error"}}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			continue
		}

		fmt.Fprintf(w, "%s\n\n", line)
		flusher.Flush()

		if strings.TrimSpace(line) == "data: [DONE]" {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("stream read error: %v", err)
	}
}

func (p *ProxyHandler) handleNonStream(w http.ResponseWriter, resp *http.Response) {
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (p *ProxyHandler) startAgentRun(ctx context.Context, baseURL, apiKey string) (string, error) {
	body := []byte(`{"action":"START","agentId":"base2","ancestorRunIds":[]}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/agent-runs", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "freebuff2api/1.0")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(data))
	}

	var result struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}
	if result.RunID == "" {
		return "", fmt.Errorf("empty runId in response: %s", string(data))
	}
	return result.RunID, nil
}
