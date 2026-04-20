package app

import (
	"sort"
	"strings"
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
	donors    []string // parallel to entries; "" = no donor key bound to this account
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
	p.Reload(keys, labels, nil)
	return p
}

// NewKeyPoolWithLabels constructs a pool preserving per-key labels.
// donorKeys may be nil; when provided, its length must match keys.
func NewKeyPoolWithLabels(keys, labels []string) *KeyPool {
	p := &KeyPool{
		threshold: DefaultBreakerThreshold,
		cooldown:  DefaultBreakerCooldown,
	}
	p.Reload(keys, labels, nil)
	return p
}

// NewKeyPoolWithDonors is like NewKeyPoolWithLabels but also seeds donor keys.
// Used by main at startup when authloader provides all three parallel arrays.
func NewKeyPoolWithDonors(keys, labels, donorKeys []string) *KeyPool {
	p := &KeyPool{
		threshold: DefaultBreakerThreshold,
		cooldown:  DefaultBreakerCooldown,
	}
	p.Reload(keys, labels, donorKeys)
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

// NextAvailable selects a key via round-robin, skipping broken ones and any
// key for which filter(key, idx) returns false. Unlike Next, it does NOT fall
// back to a broken key — if every candidate is filtered out, ok=false.
//
// filter=nil behaves the same as Next but without the "best-broken" fallback.
func (p *KeyPool) NextAvailable(filter func(key string, idx int) bool) (string, int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.entries)
	if n == 0 {
		return "", -1, false
	}
	now := time.Now()
	for tries := 0; tries < n; tries++ {
		idx := int((atomic.AddUint64(&p.counter, 1) - 1) % uint64(n))
		e := p.entries[idx]
		if isBroken(e, now) {
			continue
		}
		if filter != nil && !filter(e.Key, idx) {
			continue
		}
		return e.Key, idx, true
	}
	return "", -1, false
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

// TripBreaker forces idx into the broken state for the current cooldown.
// Used by the admin UI to manually take a key out of rotation.
func (p *KeyPool) TripBreaker(idx int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.entries) {
		return
	}
	e := p.entries[idx]
	e.Fails = p.threshold
	e.Broken = true
	e.BrokenUntil = time.Now().Add(p.cooldown)
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
//
// donorKeys may be nil (no donor data) or same length as keys; any other length
// is ignored. When provided, the incoming donor key for a surviving entry
// overwrites the in-memory value — disk truth wins, which prevents admin edits
// and fsnotify reloads from drifting.
func (p *KeyPool) Reload(keys, labels, donorKeys []string) (added, removed, kept int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	prev := make(map[string]*KeyEntry, len(p.entries))
	for _, e := range p.entries {
		prev[e.Key] = e
	}

	useLabels := len(labels) == len(keys)
	useDonors := len(donorKeys) == len(keys)
	next := make([]*KeyEntry, 0, len(keys))
	nextDonors := make([]string, 0, len(keys))
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
		var donor string
		if useDonors {
			donor = donorKeys[i]
		}
		if old, ok := prev[k]; ok {
			old.Label = label
			next = append(next, old)
			kept++
		} else {
			next = append(next, &KeyEntry{Key: k, Label: label})
			added++
		}
		nextDonors = append(nextDonors, donor)
	}
	for k := range prev {
		if _, ok := seen[k]; !ok {
			removed++
		}
	}

	// Keep deterministic order: group by label then key. Donor array must move
	// with its entry, so sort in lock-step via an index permutation.
	idx := make([]int, len(next))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ea, eb := next[idx[a]], next[idx[b]]
		if ea.Label != eb.Label {
			return ea.Label < eb.Label
		}
		return ea.Key < eb.Key
	})
	sortedEntries := make([]*KeyEntry, len(next))
	sortedDonors := make([]string, len(next))
	for i, j := range idx {
		sortedEntries[i] = next[j]
		sortedDonors[i] = nextDonors[j]
	}

	p.entries = sortedEntries
	p.donors = sortedDonors
	return
}

// IsBroken reports whether the key at idx is currently circuit-broken.
// Out-of-range idx returns false (so the caller's pin logic degrades into
// "just try it and let the upstream fail").
func (p *KeyPool) IsBroken(idx int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.entries) {
		return false
	}
	return isBroken(p.entries[idx], time.Now())
}

// GetDonorKey returns the donor key bound to idx, or "" if none.
func (p *KeyPool) GetDonorKey(idx int) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if idx < 0 || idx >= len(p.donors) {
		return ""
	}
	return p.donors[idx]
}

// SetDonorKey replaces the donor key at idx. "" clears it. Out-of-range is a
// no-op. Note: this only updates memory; callers must also persist the change
// to disk (via credentialFile.DonorKey) so hot-reloads don't revert it.
func (p *KeyPool) SetDonorKey(idx int, donor string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.donors) {
		return
	}
	p.donors[idx] = strings.TrimSpace(donor)
}

// ResolveDonorKey searches for the upstream index bound to a donor key string.
// Matching is exact (case-sensitive, no trimming — trim upstream if you need to).
// Returns (idx, upstreamKey, true) on hit, or (-1, "", false) otherwise.
func (p *KeyPool) ResolveDonorKey(donor string) (int, string, bool) {
	if donor == "" {
		return -1, "", false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for i, d := range p.donors {
		if d == donor {
			return i, p.entries[i].Key, true
		}
	}
	return -1, "", false
}

// DonorSnapshot returns donor keys aligned to Snapshot() order. Intended for
// admin status output; callers should not mutate the returned slice.
func (p *KeyPool) DonorSnapshot() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, len(p.donors))
	copy(out, p.donors)
	return out
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
