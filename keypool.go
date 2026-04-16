package main

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultBreakerCooldown is how long a key stays circuit-broken after tripping.
const DefaultBreakerCooldown = 12 * time.Hour

// DefaultBreakerThreshold is consecutive failures required to trip the breaker.
const DefaultBreakerThreshold = 3

// KeyEntry represents one upstream API key plus its runtime state.
type KeyEntry struct {
	Key    string
	Label  string // source: env or filename
	Fails  int
	Broken bool
	// BrokenUntil is when the breaker expires and the key can be retried.
	BrokenUntil time.Time
}

// KeyPool is a thread-safe round-robin selector with circuit breakers.
//
// Selection skips any key whose breaker is active (BrokenUntil > now). If every
// key is broken, the pool returns the key whose breaker expires soonest — this
// provides graceful degradation instead of hard-failing every request.
//
// Keys can be reloaded at runtime; breaker state is preserved for surviving
// keys while removed keys drop out and new keys start clean.
type KeyPool struct {
	mu        sync.RWMutex
	entries   []*KeyEntry
	counter   uint64
	threshold int
	cooldown  time.Duration
}

func NewKeyPool(keys []string) *KeyPool {
	p := &KeyPool{
		threshold: DefaultBreakerThreshold,
		cooldown:  DefaultBreakerCooldown,
	}
	labels := make([]string, len(keys))
	for i := range keys {
		labels[i] = "env"
	}
	p.Reload(keys, labels)
	return p
}

// NewKeyPoolWithLabels constructs a pool preserving per-key labels.
func NewKeyPoolWithLabels(keys, labels []string) *KeyPool {
	p := &KeyPool{
		threshold: DefaultBreakerThreshold,
		cooldown:  DefaultBreakerCooldown,
	}
	p.Reload(keys, labels)
	return p
}

// SetBreakerTuning updates threshold + cooldown without clearing breaker state.
// Zero or negative values are ignored (keep previous tuning).
func (p *KeyPool) SetBreakerTuning(threshold int, cooldown time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if threshold > 0 {
		p.threshold = threshold
	}
	if cooldown > 0 {
		p.cooldown = cooldown
	}
}

// Threshold returns the current breaker threshold (primarily for tests/diagnostics).
func (p *KeyPool) Threshold() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.threshold
}

// Cooldown returns the current breaker cooldown.
func (p *KeyPool) Cooldown() time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cooldown
}

func (p *KeyPool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.entries)
}

// HealthySize returns the number of keys whose breaker is not currently active.
func (p *KeyPool) HealthySize() int {
	now := time.Now()
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := 0
	for _, e := range p.entries {
		if !isBroken(e, now) {
			n++
		}
	}
	return n
}

// Snapshot returns a copy of all entries for status reporting.
func (p *KeyPool) Snapshot() []KeyEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]KeyEntry, len(p.entries))
	for i, e := range p.entries {
		out[i] = *e
	}
	return out
}

// Next selects a key via round-robin, skipping broken ones.
// If every key is broken, falls back to the one whose cooldown expires soonest.
// Returns ("", -1) only if the pool is empty.
func (p *KeyPool) Next() (string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.entries)
	if n == 0 {
		return "", -1
	}
	now := time.Now()

	for tries := 0; tries < n; tries++ {
		idx := int((atomic.AddUint64(&p.counter, 1) - 1) % uint64(n))
		e := p.entries[idx]
		if !isBroken(e, now) {
			return e.Key, idx
		}
	}

	// All broken — pick the one expiring soonest, so we don't hard-fail.
	bestIdx := 0
	for i, e := range p.entries {
		if e.BrokenUntil.Before(p.entries[bestIdx].BrokenUntil) {
			bestIdx = i
		}
	}
	return p.entries[bestIdx].Key, bestIdx
}

// MarkFailure increments the fail counter for idx and trips the breaker at
// threshold. Safe to call on a stale idx (will be ignored).
func (p *KeyPool) MarkFailure(idx int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.entries) {
		return
	}
	e := p.entries[idx]
	e.Fails++
	if e.Fails >= p.threshold && !e.Broken {
		e.Broken = true
		e.BrokenUntil = time.Now().Add(p.cooldown)
	}
}

// MarkSuccess clears fail counter and breaker state for idx.
func (p *KeyPool) MarkSuccess(idx int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.entries) {
		return
	}
	e := p.entries[idx]
	e.Fails = 0
	e.Broken = false
	e.BrokenUntil = time.Time{}
}

// Reload merges in a new key set. Surviving keys keep their breaker state;
// removed keys drop out; new keys start clean. Order follows the new slice.
//
// labels must be same length as keys (caller's contract). If mismatched, labels
// are ignored and "reload" is used.
func (p *KeyPool) Reload(keys, labels []string) (added, removed, kept int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	prev := make(map[string]*KeyEntry, len(p.entries))
	for _, e := range p.entries {
		prev[e.Key] = e
	}

	useLabels := len(labels) == len(keys)
	next := make([]*KeyEntry, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for i, k := range keys {
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		var label string
		if useLabels {
			label = labels[i]
		} else {
			label = "reload"
		}
		if old, ok := prev[k]; ok {
			old.Label = label
			next = append(next, old)
			kept++
		} else {
			next = append(next, &KeyEntry{Key: k, Label: label})
			added++
		}
	}
	for k := range prev {
		if _, ok := seen[k]; !ok {
			removed++
		}
	}

	// Keep deterministic order: group by label then key for stable snapshots.
	sort.SliceStable(next, func(i, j int) bool {
		if next[i].Label != next[j].Label {
			return next[i].Label < next[j].Label
		}
		return next[i].Key < next[j].Key
	})

	p.entries = next
	return
}

func isBroken(e *KeyEntry, now time.Time) bool {
	if !e.Broken {
		return false
	}
	if now.After(e.BrokenUntil) {
		// Cooldown expired — reset lazily so Next() picks it up.
		e.Broken = false
		e.Fails = 0
		e.BrokenUntil = time.Time{}
		return false
	}
	return true
}
