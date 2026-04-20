package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// errSessionQueued is returned when an upstream key's freebuff session is
// still in the waiting room. The proxy uses the embedded queue info to
// build a user-friendly error with estimated wait time.
type errSessionQueued struct {
	Position        int
	QueueDepth      int
	EstimatedWaitMs int64
}

func (e *errSessionQueued) Error() string {
	return fmt.Sprintf("session queued (position %d/%d, ~%ds)",
		e.Position, e.QueueDepth, e.EstimatedWaitMs/1000)
}

type queueInfo struct {
	InstanceID      string
	Position        int
	QueueDepth      int
	EstimatedWaitMs int64
	LastChecked     time.Time
}

// sessionManager maintains per-upstream-key freebuff session instance IDs.
// Sessions are checked lazily: status updates happen when a request tries
// a key, not via background polling.
type sessionManager struct {
	mu          sync.RWMutex
	instances   map[string]string     // upstreamKey → instanceId (active/disabled)
	queueStatus map[string]*queueInfo // upstreamKey → queue info
	client      *http.Client
}

func newSessionManager(client *http.Client) *sessionManager {
	return &sessionManager{
		instances:   make(map[string]string),
		queueStatus: make(map[string]*queueInfo),
		client:      client,
	}
}

func (sm *sessionManager) getInstanceID(upstreamKey string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.instances[upstreamKey]
}

func (sm *sessionManager) getQueueInfo(upstreamKey string) *queueInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	qi, ok := sm.queueStatus[upstreamKey]
	if !ok {
		return nil
	}
	cp := *qi
	return &cp
}

// ensureSession returns a cached active instanceId, or checks/joins the
// waiting room. Returns errSessionQueued (not a real failure) when the key
// is still in queue — callers must not treat this as a breaker event.
func (sm *sessionManager) ensureSession(ctx context.Context, baseURL, upstreamKey string) (string, error) {
	if id := sm.getInstanceID(upstreamKey); id != "" {
		return id, nil
	}

	// Known queued — GET to check if admitted without mutating queue position.
	if qi := sm.getQueueInfo(upstreamKey); qi != nil {
		id, err := sm.pollOnce(ctx, baseURL, upstreamKey, qi.InstanceID)
		if err == nil {
			return id, nil
		}
		var qErr *errSessionQueued
		if errors.As(err, &qErr) {
			return "", err
		}
		// Non-queue error (superseded, none, network) — clear and fall through to POST.
		sm.invalidate(upstreamKey)
	}

	return sm.requestSession(ctx, baseURL, upstreamKey)
}

// requestSession POSTs to /api/v1/freebuff/session to join or take over.
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

	return sm.handleResponse(data, upstreamKey)
}

// pollOnce does a non-mutating GET to check if a queued session is admitted.
func (sm *sessionManager) pollOnce(ctx context.Context, baseURL, upstreamKey, instanceID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/freebuff/session", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+upstreamKey)
	req.Header.Set("X-Freebuff-Instance-Id", instanceID)

	resp, err := sm.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("freebuff session poll failed: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("freebuff session poll status %d: %s", resp.StatusCode, string(data))
	}

	return sm.handleResponse(data, upstreamKey)
}

// handleResponse parses the JSON from POST or GET /session and updates
// internal state. Returns instanceId on success, errSessionQueued when
// queued, or a plain error for unexpected/terminal states.
func (sm *sessionManager) handleResponse(data []byte, upstreamKey string) (string, error) {
	var r struct {
		Status          string `json:"status"`
		InstanceID      string `json:"instanceId"`
		Position        int    `json:"position"`
		QueueDepth      int    `json:"queueDepth"`
		EstimatedWaitMs int64  `json:"estimatedWaitMs"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", fmt.Errorf("freebuff session parse error: %w", err)
	}

	switch r.Status {
	case "disabled":
		sm.mu.Lock()
		sm.instances[upstreamKey] = "disabled"
		delete(sm.queueStatus, upstreamKey)
		sm.mu.Unlock()
		return "", nil

	case "active", "ended":
		if r.InstanceID == "" {
			return "", fmt.Errorf("freebuff session %s but no instanceId for key=%s", r.Status, fingerprint(upstreamKey))
		}
		log.Printf("freebuff session: %s instanceId=%s for key=%s", r.Status, r.InstanceID[:8], fingerprint(upstreamKey))
		sm.mu.Lock()
		sm.instances[upstreamKey] = r.InstanceID
		delete(sm.queueStatus, upstreamKey)
		sm.mu.Unlock()
		return r.InstanceID, nil

	case "queued":
		sm.mu.Lock()
		sm.queueStatus[upstreamKey] = &queueInfo{
			InstanceID:      r.InstanceID,
			Position:        r.Position,
			QueueDepth:      r.QueueDepth,
			EstimatedWaitMs: r.EstimatedWaitMs,
			LastChecked:     time.Now(),
		}
		delete(sm.instances, upstreamKey)
		sm.mu.Unlock()
		return "", &errSessionQueued{
			Position:        r.Position,
			QueueDepth:      r.QueueDepth,
			EstimatedWaitMs: r.EstimatedWaitMs,
		}

	case "none", "superseded":
		sm.mu.Lock()
		delete(sm.instances, upstreamKey)
		delete(sm.queueStatus, upstreamKey)
		sm.mu.Unlock()
		return "", fmt.Errorf("freebuff session %s for key=%s", r.Status, fingerprint(upstreamKey))
	}

	return "", fmt.Errorf("freebuff session unexpected status=%q for key=%s", r.Status, fingerprint(upstreamKey))
}

// SessionStatus returns "active", "queued", or "" for a key.
// For queued keys it also returns the cached queue info.
func (sm *sessionManager) sessionStatus(upstreamKey string) (status string, qi *queueInfo) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if id := sm.instances[upstreamKey]; id != "" {
		return "active", nil
	}
	if q, ok := sm.queueStatus[upstreamKey]; ok {
		cp := *q
		return "queued", &cp
	}
	return "", nil
}

// invalidate clears all cached state for a key.
func (sm *sessionManager) invalidate(upstreamKey string) {
	sm.mu.Lock()
	delete(sm.instances, upstreamKey)
	delete(sm.queueStatus, upstreamKey)
	sm.mu.Unlock()
}

// warmUp fires a single POST /session for each key at startup so they
// enter the queue early. No background polling — status is updated
// lazily when requests come in.
func (sm *sessionManager) warmUp(baseURL string, keys []string) {
	log.Printf("freebuff session: warming up %d keys", len(keys))
	for _, key := range keys {
		go func(k string) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			sm.requestSession(ctx, baseURL, k)
		}(key)
		time.Sleep(100 * time.Millisecond)
	}
}

// injectInstanceID adds freebuff_instance_id to codebuff_metadata if available.
func (sm *sessionManager) injectInstanceID(metadata map[string]any, upstreamKey string) {
	id := sm.getInstanceID(upstreamKey)
	if id != "" && id != "disabled" {
		metadata["freebuff_instance_id"] = id
	}
}
