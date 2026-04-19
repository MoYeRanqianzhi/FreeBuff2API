package main

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
// for free-mode requests; without it the server returns 426.
type sessionManager struct {
	mu        sync.RWMutex
	instances map[string]string // upstreamKey → instanceId
	client    *http.Client
}

func newSessionManager(client *http.Client) *sessionManager {
	return &sessionManager{
		instances: make(map[string]string),
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

	// "disabled" means waiting room is off — no instanceId needed
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
		log.Printf("freebuff session: queued for key=%s, polling for admission...", fingerprint(upstreamKey))
		return sm.pollUntilActive(ctx, baseURL, upstreamKey, result.InstanceID)
	}

	return "", fmt.Errorf("freebuff session unexpected status=%q for key=%s", result.Status, fingerprint(upstreamKey))
}

// invalidate clears the cached instanceId for a key, forcing re-request on next use.
func (sm *sessionManager) invalidate(upstreamKey string) {
	sm.mu.Lock()
	delete(sm.instances, upstreamKey)
	sm.mu.Unlock()
}

// pollUntilActive polls GET /api/v1/freebuff/session until the session becomes
// active or the context is cancelled. Admission tick is ~15s per codebuff config.
func (sm *sessionManager) pollUntilActive(ctx context.Context, baseURL, upstreamKey, instanceID string) (string, error) {
	const pollInterval = 5 * time.Second
	const maxWait = 3 * time.Minute

	deadline := time.Now().Add(maxWait)
	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("freebuff session: timed out waiting for admission key=%s", fingerprint(upstreamKey))
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/freebuff/session", nil)
		if err != nil {
			return "", err
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
			return result.InstanceID, nil
		}

		if result.Status == "queued" {
			continue
		}

		return "", fmt.Errorf("freebuff session: unexpected poll status=%q for key=%s", result.Status, fingerprint(upstreamKey))
	}
}

// injectInstanceID adds freebuff_instance_id to codebuff_metadata if available.
func (sm *sessionManager) injectInstanceID(metadata map[string]any, upstreamKey string) {
	id := sm.getInstanceID(upstreamKey)
	if id != "" && id != "disabled" {
		metadata["freebuff_instance_id"] = id
	}
}
