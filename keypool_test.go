package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestKeyPoolRoundRobin(t *testing.T) {
	p := NewKeyPool([]string{"a", "b", "c"})
	want := []string{"a", "b", "c", "a", "b", "c", "a"}
	for i, w := range want {
		k, _ := p.Next()
		if k != w {
			t.Fatalf("call %d: got %q want %q", i, k, w)
		}
	}
}

func TestKeyPoolBreakerTrips(t *testing.T) {
	p := NewKeyPoolWithLabels([]string{"a", "b"}, []string{"t", "t"})
	// Fail key 0 three times — should trip.
	for i := 0; i < DefaultBreakerThreshold; i++ {
		_, idx := p.Next()
		if idx == 0 {
			p.MarkFailure(0)
		} else {
			p.MarkFailure(1) // won't affect our probe
			// retry to ensure we hit key 0
			i--
		}
	}
	// Force-break key 0 directly to avoid round-robin flakiness above.
	p.MarkFailure(0)
	p.MarkFailure(0)
	p.MarkFailure(0)

	snap := p.Snapshot()
	if !snap[0].Broken {
		t.Fatalf("expected key[0] broken, got %+v", snap[0])
	}
	// Next() must now only return key 1.
	for i := 0; i < 10; i++ {
		k, idx := p.Next()
		if k != "b" || idx != 1 {
			t.Fatalf("iteration %d: expected key b@1, got %q@%d", i, k, idx)
		}
	}
}

func TestKeyPoolBreakerCooldownExpires(t *testing.T) {
	p := NewKeyPoolWithLabels([]string{"a"}, []string{"t"})
	p.cooldown = 10 * time.Millisecond
	p.MarkFailure(0)
	p.MarkFailure(0)
	p.MarkFailure(0)
	if !p.Snapshot()[0].Broken {
		t.Fatalf("expected broken")
	}
	// During cooldown it still returns (only key available).
	k, _ := p.Next()
	if k != "a" {
		t.Fatalf("fallback: got %q", k)
	}
	time.Sleep(20 * time.Millisecond)
	k, _ = p.Next()
	if k != "a" {
		t.Fatalf("post-cooldown: %q", k)
	}
	if p.Snapshot()[0].Broken {
		t.Fatalf("expected broken cleared after cooldown")
	}
}

func TestKeyPoolMarkSuccessResets(t *testing.T) {
	p := NewKeyPool([]string{"a"})
	p.MarkFailure(0)
	p.MarkFailure(0)
	p.MarkSuccess(0)
	if p.Snapshot()[0].Fails != 0 {
		t.Fatalf("fails should reset")
	}
}

func TestKeyPoolAllBrokenFallback(t *testing.T) {
	p := NewKeyPoolWithLabels([]string{"a", "b"}, []string{"t", "t"})
	// Trip both.
	for i := 0; i < DefaultBreakerThreshold; i++ {
		p.MarkFailure(0)
		p.MarkFailure(1)
	}
	if p.HealthySize() != 0 {
		t.Fatalf("expected 0 healthy, got %d", p.HealthySize())
	}
	k, idx := p.Next()
	if k == "" || idx < 0 {
		t.Fatalf("should still return a key (fallback)")
	}
}

func TestKeyPoolReloadKeepsBreakerState(t *testing.T) {
	p := NewKeyPoolWithLabels([]string{"a", "b"}, []string{"t", "t"})
	for i := 0; i < DefaultBreakerThreshold; i++ {
		p.MarkFailure(0)
	}
	added, removed, kept := p.Reload([]string{"a", "c"}, []string{"t", "t"})
	if kept != 1 || added != 1 || removed != 1 {
		t.Fatalf("reload counts: added=%d removed=%d kept=%d", added, removed, kept)
	}
	// Find 'a' in snapshot and verify it remained broken.
	for _, e := range p.Snapshot() {
		if e.Key == "a" && !e.Broken {
			t.Fatalf("'a' breaker state lost across reload")
		}
	}
}

func TestKeyPoolConcurrent(t *testing.T) {
	keys := []string{"a", "b", "c", "d"}
	p := NewKeyPool(keys)
	const N = 10000
	counts := make(map[string]int)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, _ := p.Next()
			mu.Lock()
			counts[k]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	expected := N / len(keys)
	for _, k := range keys {
		if counts[k] != expected {
			t.Fatalf("key %q: got %d want %d", k, counts[k], expected)
		}
	}
}

func TestFingerprint(t *testing.T) {
	if got := fingerprint("short"); got != "***" {
		t.Fatalf("short: %q", got)
	}
	if got := fingerprint("cb-abc123xyz"); got != "cb-abc…yz" {
		t.Fatalf("long: %q", got)
	}
}

func TestLoadKeySourcesInlineAndAuths(t *testing.T) {
	dir := t.TempDir()
	writeCred(t, filepath.Join(dir, "alice.json"), "tok-alice")
	writeCred(t, filepath.Join(dir, "bob.json"), "tok-bob")
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	keys, labels, err := LoadKeySources([]string{"tok-inline"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tok-inline", "tok-alice", "tok-bob"}
	if len(keys) != len(want) {
		t.Fatalf("keys=%v want=%v", keys, want)
	}
	for i, w := range want {
		if keys[i] != w {
			t.Fatalf("keys[%d]=%q want %q", i, keys[i], w)
		}
	}
	if labels[0] != "config.yaml" || labels[1] != "auths/alice.json" || labels[2] != "auths/bob.json" {
		t.Fatalf("labels=%v", labels)
	}
}

func TestLoadKeySourcesDedup(t *testing.T) {
	dir := t.TempDir()
	writeCred(t, filepath.Join(dir, "a.json"), "same-token")
	keys, _, err := LoadKeySources([]string{"same-token"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected dedup to 1, got %v", keys)
	}
}

func TestLoadKeySourcesMissingDirIgnored(t *testing.T) {
	keys, _, err := LoadKeySources([]string{"inline-only"}, filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 1 || keys[0] != "inline-only" {
		t.Fatalf("got %v", keys)
	}
}

func writeCred(t *testing.T, path, tok string) {
	t.Helper()
	body := `{"id":"u","email":"e","name":null,"authToken":"` + tok +
		`","fingerprintId":"f","fingerprintHash":"h"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
