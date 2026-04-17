package main

import (
	"sync"
	"time"
)

// bucket is a single-mutex token bucket. rpm==0 means unlimited (always allow).
//
// Refill is continuous: each call computes how many tokens should have been
// added since lastFill and tops up. Capacity == rpm, so bursts of up to rpm
// requests are allowed immediately after a quiet period.
type bucket struct {
	mu       sync.Mutex
	rpm      int
	tokens   float64
	lastFill time.Time
}

func newBucket(rpm int) *bucket {
	return &bucket{
		rpm:      rpm,
		tokens:   float64(rpm), // start full so cold-start bursts are allowed up to rpm
		lastFill: time.Now(),
	}
}

// allow consumes one token and returns true, or returns false if no token is
// available. rpm==0 short-circuits to true (unlimited).
func (b *bucket) allow() bool {
	if b == nil || b.rpm == 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(b.lastFill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * float64(b.rpm) / 60.0
		if b.tokens > float64(b.rpm) {
			b.tokens = float64(b.rpm)
		}
		b.lastFill = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// setRPM reconfigures the bucket while keeping token state. The new cap is
// enforced immediately.
func (b *bucket) setRPM(rpm int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rpm = rpm
	if b.tokens > float64(rpm) {
		b.tokens = float64(rpm)
	}
}

// LimiterSet holds one global bucket plus per-key and per-client-token buckets.
//
// Per-key and per-client buckets are lazily materialized on first use, so new
// keys/clients don't require explicit registration — they're known at reload
// time but only populated when they actually see traffic.
type LimiterSet struct {
	mu       sync.RWMutex
	global   *bucket
	accounts map[string]*bucket
	clients  map[string]*bucket

	globalRPM  int
	accountRPM int
	clientRPM  int
}

// NewLimiterSet constructs a LimiterSet from a LimitsConfig snapshot.
func NewLimiterSet(cfg LimitsConfig) *LimiterSet {
	return &LimiterSet{
		global:     newBucket(cfg.GlobalRPM),
		accounts:   map[string]*bucket{},
		clients:    map[string]*bucket{},
		globalRPM:  cfg.GlobalRPM,
		accountRPM: cfg.AccountRPM,
		clientRPM:  cfg.ClientRPM,
	}
}

// GlobalAllow reports whether a request may proceed under the global cap.
func (ls *LimiterSet) GlobalAllow() bool {
	if ls == nil {
		return true
	}
	ls.mu.RLock()
	b := ls.global
	ls.mu.RUnlock()
	return b.allow()
}

// AccountAllow reports whether the given upstream key has budget left.
// upstreamKey == "" is treated as unlimited.
func (ls *LimiterSet) AccountAllow(upstreamKey string) bool {
	if ls == nil || upstreamKey == "" {
		return true
	}
	ls.mu.RLock()
	rpm := ls.accountRPM
	if rpm == 0 {
		ls.mu.RUnlock()
		return true
	}
	b, ok := ls.accounts[upstreamKey]
	ls.mu.RUnlock()
	if !ok {
		ls.mu.Lock()
		b, ok = ls.accounts[upstreamKey] // recheck after promoting lock
		if !ok {
			b = newBucket(rpm)
			ls.accounts[upstreamKey] = b
		}
		ls.mu.Unlock()
	}
	return b.allow()
}

// ClientAllow reports whether the given client Bearer token has budget left.
// clientToken == "" is treated as unlimited (anonymous traffic is subject to
// the global cap only).
func (ls *LimiterSet) ClientAllow(clientToken string) bool {
	if ls == nil || clientToken == "" {
		return true
	}
	ls.mu.RLock()
	rpm := ls.clientRPM
	if rpm == 0 {
		ls.mu.RUnlock()
		return true
	}
	b, ok := ls.clients[clientToken]
	ls.mu.RUnlock()
	if !ok {
		ls.mu.Lock()
		b, ok = ls.clients[clientToken]
		if !ok {
			b = newBucket(rpm)
			ls.clients[clientToken] = b
		}
		ls.mu.Unlock()
	}
	return b.allow()
}

// Reload updates caps and optionally prunes disappeared keys. currentUpstream
// and currentClient (when non-nil) act as whitelists — buckets for keys not in
// those lists are dropped so closed tokens don't linger in memory. Pass nil to
// skip pruning for that category.
func (ls *LimiterSet) Reload(cfg LimitsConfig, currentUpstream, currentClient []string) {
	if ls == nil {
		return
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()

	// Global
	if cfg.GlobalRPM != ls.globalRPM {
		ls.global.setRPM(cfg.GlobalRPM)
		ls.globalRPM = cfg.GlobalRPM
	}

	// Account
	if cfg.AccountRPM != ls.accountRPM {
		for _, b := range ls.accounts {
			b.setRPM(cfg.AccountRPM)
		}
		ls.accountRPM = cfg.AccountRPM
	}
	if currentUpstream != nil {
		alive := make(map[string]struct{}, len(currentUpstream))
		for _, k := range currentUpstream {
			alive[k] = struct{}{}
		}
		for k := range ls.accounts {
			if _, ok := alive[k]; !ok {
				delete(ls.accounts, k)
			}
		}
	}

	// Client
	if cfg.ClientRPM != ls.clientRPM {
		for _, b := range ls.clients {
			b.setRPM(cfg.ClientRPM)
		}
		ls.clientRPM = cfg.ClientRPM
	}
	if currentClient != nil {
		alive := make(map[string]struct{}, len(currentClient))
		for _, k := range currentClient {
			alive[k] = struct{}{}
		}
		for k := range ls.clients {
			if _, ok := alive[k]; !ok {
				delete(ls.clients, k)
			}
		}
	}
}
