package app

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
)

// openRouterKeyPattern matches OpenRouter API keys (sk-or-v1-<hex> and similar).
// Deliberately lenient: official format is sk-or-v1-<64hex>, but accept any
// sk-or- prefix with a reasonably long suffix to accommodate future formats.
var openRouterKeyPattern = regexp.MustCompile(`^sk-or-[a-zA-Z0-9_\-]{20,}$`)

// IsOpenRouterKey reports whether token looks like an OpenRouter API key.
func IsOpenRouterKey(token string) bool {
	return openRouterKeyPattern.MatchString(token)
}

// forwardToOpenRouter POSTs the pre-read body to OpenRouter /chat/completions,
// using token as Bearer auth. Streams SSE when Content-Type indicates it,
// otherwise streams the body through verbatim.
func forwardToOpenRouter(w http.ResponseWriter, r *http.Request, body []byte, client *http.Client, cfg *Config, token string) {
	targetURL := cfg.Upstream.OpenRouter.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, `{"error":{"message":"Failed to create OpenRouter request","type":"server_error"}}`, http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "freebuff2api/1.0")
	// Pass through optional OpenRouter attribution headers if the client sent them.
	if v := r.Header.Get("HTTP-Referer"); v != "" {
		req.Header.Set("HTTP-Referer", v)
	}
	if v := r.Header.Get("X-Title"); v != "" {
		req.Header.Set("X-Title", v)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("openrouter error: %v", err)
		http.Error(w, fmt.Sprintf(`{"error":{"message":"OpenRouter error: %s","type":"server_error"}}`, err.Error()), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// SSE streaming responses come back chunked; io.Copy + manual flushing works
	// because Go's http server flushes on each Write when Content-Length is unset.
	if flusher, ok := w.(http.Flusher); ok {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				flusher.Flush()
			}
			if rerr == io.EOF {
				return
			}
			if rerr != nil {
				log.Printf("openrouter stream read: %v", rerr)
				return
			}
		}
	}
	io.Copy(w, resp.Body)
}
