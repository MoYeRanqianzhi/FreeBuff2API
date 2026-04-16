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

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request_error"}}`, http.StatusBadRequest)
		return
	}

	if _, ok := req["model"]; !ok || req["model"] == "" {
		req["model"] = cfg.Upstream.DefaultModel
	}

	upstreamKey, keyIdx := p.keys.Next()
	if upstreamKey == "" {
		http.Error(w, `{"error":{"message":"No upstream API keys available","type":"server_error"}}`, http.StatusServiceUnavailable)
		return
	}
	log.Printf("→ upstream key[%d]=%s", keyIdx, fingerprint(upstreamKey))

	runID, err := p.startAgentRun(r.Context(), cfg.Upstream.BaseURL, upstreamKey)
	if err != nil {
		p.keys.MarkFailure(keyIdx)
		log.Printf("startAgentRun error (key[%d]=%s): %v", keyIdx, fingerprint(upstreamKey), err)
		http.Error(w, fmt.Sprintf(`{"error":{"message":"Failed to register agent run: %s","type":"upstream_error"}}`, err.Error()), http.StatusBadGateway)
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
		http.Error(w, fmt.Sprintf(`{"error":{"message":"Upstream error: %s","type":"server_error"}}`, err.Error()), http.StatusBadGateway)
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
	} else if resp.StatusCode < 400 {
		p.keys.MarkSuccess(keyIdx)
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
