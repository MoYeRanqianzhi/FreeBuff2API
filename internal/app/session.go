package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// sessionManager maintains per-upstream-key freebuff session instance IDs.
// codebuff's waiting room requires a freebuff_instance_id in codebuff_metadata
// for free-mode requests; without it the server returns 428.
type sessionManager struct {
	mu        sync.RWMutex
	instances map[string]string // upstreamKey → instanceId
	pending   map[string]bool   // keys with background poll in flight
	client    *http.Client
}

func newSessionManager(client *http.Client) *sessionManager {
	return &sessionManager{
		instances: make(map[string]string),
		pending:   make(map[string]bool),
		client:    client,
	}
}

// getInstanceID returns the cached instanceId for a key, or "" if none.
func (sm *sessionManager) getInstanceID(upstreamKey string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.instances[upstreamKey]
}

// ensureSession calls POST /api/v1/freebuff/session to join/takeover a session
// and caches the returned instanceId. Returns the instanceId or error.
func (sm *sessionManager) ensureSession(ctx context.Context, baseURL, upstreamKey string) (string, error) {
	if id := sm.getInstanceID(upstreamKey); id != "" {
		return id, nil
	}
	return sm.requestSession(ctx, baseURL, upstreamKey)
}

// requestSession does the actual POST and caches the result.
// When the session is queued, it kicks off a background poll goroutine
// and returns an error immediately so the caller can try another key.
func (sm *sessionManager) requestSession(ctx context.Context, baseURL, upstreamKey string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/freebuff/session", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+upstreamKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := sm.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("freebuff session request failed: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("freebuff session status %d: %s", resp.StatusCode, string(data))
	}

	var result struct {
		Status     string `json:"status"`
		InstanceID string `json:"instanceId"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("freebuff session parse error: %w", err)
	}

	if result.Status == "disabled" {
		log.Printf("freebuff session: waiting room disabled for key=%s", fingerprint(upstreamKey))
		sm.mu.Lock()
		sm.instances[upstreamKey] = "disabled"
		sm.mu.Unlock()
		return "", nil
	}

	if result.Status == "active" && result.InstanceID != "" {
		log.Printf("freebuff session: active instanceId=%s for key=%s", result.InstanceID[:8], fingerprint(upstreamKey))
		sm.mu.Lock()
		sm.instances[upstreamKey] = result.InstanceID
		sm.mu.Unlock()
		return result.InstanceID, nil
	}

	if result.Status == "queued" {
		sm.startBackgroundPoll(baseURL, upstreamKey, result.InstanceID)
		return "", fmt.Errorf("freebuff session: queued for key=%s (polling in background)", fingerprint(upstreamKey))
	}

	return "", fmt.Errorf("freebuff session unexpected status=%q for key=%s", result.Status, fingerprint(upstreamKey))
}

// startBackgroundPoll kicks off a goroutine that polls GET /session until
// the key is admitted, then caches the instanceId. Only one poll per key.
func (sm *sessionManager) startBackgroundPoll(baseURL, upstreamKey, instanceID string) {
	sm.mu.Lock()
	if sm.pending[upstreamKey] {
		sm.mu.Unlock()
		return
	}
	sm.pending[upstreamKey] = true
	sm.mu.Unlock()

	log.Printf("freebuff session: queued for key=%s, background poll started", fingerprint(upstreamKey))

	go func() {
		defer func() {
			sm.mu.Lock()
			delete(sm.pending, upstreamKey)
			sm.mu.Unlock()
		}()
		sm.pollUntilActive(baseURL, upstreamKey, instanceID)
	}()
}

// invalidate clears the cached instanceId for a key, forcing re-request on next use.
func (sm *sessionManager) invalidate(upstreamKey string) {
	sm.mu.Lock()
	delete(sm.instances, upstreamKey)
	sm.mu.Unlock()
}

// pollUntilActive polls GET /api/v1/freebuff/session until the session becomes
// active or times out. Uses its own context (not tied to any request).
func (sm *sessionManager) pollUntilActive(baseURL, upstreamKey, instanceID string) {
	const pollInterval = 5 * time.Second
	const maxWait = 10 * time.Minute

	ctx, cancel := context.WithTimeout(context.Background(), maxWait)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			log.Printf("freebuff session: timed out waiting for admission key=%s", fingerprint(upstreamKey))
			return
		case <-time.After(pollInterval):
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/freebuff/session", nil)
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+upstreamKey)
		req.Header.Set("X-Freebuff-Instance-Id", instanceID)

		resp, err := sm.client.Do(req)
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			continue
		}

		var result struct {
			Status     string `json:"status"`
			InstanceID string `json:"instanceId"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			continue
		}

		if result.Status == "active" && result.InstanceID != "" {
			log.Printf("freebuff session: admitted! instanceId=%s for key=%s", result.InstanceID[:8], fingerprint(upstreamKey))
			sm.mu.Lock()
			sm.instances[upstreamKey] = result.InstanceID
			sm.mu.Unlock()
			return
		}

		if result.Status == "queued" {
			continue
		}

		log.Printf("freebuff session: unexpected poll status=%q for key=%s", result.Status, fingerprint(upstreamKey))
		return
	}
}

// warmUp pre-requests sessions for all keys in the background.
// Called once at startup so sessions are ready when real requests arrive.
func (sm *sessionManager) warmUp(baseURL string, keys []string) {
	log.Printf("freebuff session: warming up %d keys in background", len(keys))
	for _, key := range keys {
		key := key
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sm.requestSession(ctx, baseURL, key)
		}()
		time.Sleep(200 * time.Millisecond)
	}
}

// injectInstanceID adds freebuff_instance_id to codebuff_metadata if available.
func (sm *sessionManager) injectInstanceID(metadata map[string]any, upstreamKey string) {
	id := sm.getInstanceID(upstreamKey)
	if id != "" && id != "disabled" {
		metadata["freebuff_instance_id"] = id
	}
}
