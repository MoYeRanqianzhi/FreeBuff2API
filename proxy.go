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

	upstreamKey, keyIdx := p.keys.Next()
	if upstreamKey == "" {
		if canFallback {
			log.Printf("→ OpenRouter (FreeBuff pool empty, fallback token=%s)", fingerprint(downstreamToken))
			forwardToOpenRouter(w, r, body, p.client, cfg, downstreamToken)
			return
		}
		writeSanitized(w, http.StatusServiceUnavailable,
			`{"error":{"message":"号池无可用账号，请联系管理员添加上游账号","type":"pool_empty"}}`)
		return
	}
	log.Printf("→ upstream key[%d]=%s", keyIdx, fingerprint(upstreamKey))

	runID, err := p.startAgentRun(r.Context(), cfg.Upstream.BaseURL, upstreamKey)
	if err != nil {
		p.keys.MarkFailure(keyIdx)
		log.Printf("startAgentRun error (key[%d]=%s): %v", keyIdx, fingerprint(upstreamKey), err)
		if canFallback {
			log.Printf("→ OpenRouter (startAgentRun failed, fallback token=%s)", fingerprint(downstreamToken))
			forwardToOpenRouter(w, r, body, p.client, cfg, downstreamToken)
			return
		}
		writeSanitized(w, http.StatusBadGateway,
			`{"error":{"message":"上游服务异常，请稍后重试","type":"upstream_error"}}`)
		return
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
		log.Printf("upstream error (key[%d]=%s): %v", keyIdx, fingerprint(upstreamKey), err)
		if canFallback {
			log.Printf("→ OpenRouter (upstream network error, fallback token=%s)", fingerprint(downstreamToken))
			forwardToOpenRouter(w, r, body, p.client, cfg, downstreamToken)
			return
		}
		writeSanitized(w, http.StatusBadGateway,
			`{"error":{"message":"上游服务异常，请稍后重试","type":"upstream_error"}}`)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusForbidden ||
		resp.StatusCode == http.StatusPaymentRequired ||
		resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode >= 500 {
		p.keys.MarkFailure(keyIdx)
		log.Printf("upstream status %d on key[%d]=%s — marked failure",
			resp.StatusCode, keyIdx, fingerprint(upstreamKey))
		// 5xx gateway-ish failures are eligible for OpenRouter fallback.
		if canFallback && (resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout) {
			log.Printf("→ OpenRouter (upstream %d, fallback token=%s)", resp.StatusCode, fingerprint(downstreamToken))
			forwardToOpenRouter(w, r, body, p.client, cfg, downstreamToken)
			return
		}
		// No fallback — return a sanitized error instead of leaking upstream body.
		status, sanitized := sanitizeUpstreamError(resp.StatusCode)
		if sanitized != "" {
			writeSanitized(w, status, sanitized)
			return
		}
	} else if resp.StatusCode < 400 {
		p.keys.MarkSuccess(keyIdx)
	} else if resp.StatusCode == http.StatusBadRequest {
		// 400 from upstream — hide the raw message but don't change status.
		status, sanitized := sanitizeUpstreamError(resp.StatusCode)
		if sanitized != "" {
			writeSanitized(w, status, sanitized)
			return
		}
	}

	isStream := false
	if s, ok := req["stream"]; ok {
		if b, ok := s.(bool); ok {
			isStream = b
		}
	}

	if isStream && resp.StatusCode == http.StatusOK {
		p.handleStream(w, resp)
	} else {
		p.handleNonStream(w, resp)
	}
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
