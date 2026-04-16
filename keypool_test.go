package main

import (
	"sync"
	"testing"
)

func TestParseKeys(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{"k1", []string{"k1"}},
		{"k1,k2,k3", []string{"k1", "k2", "k3"}},
		{"k1;k2", []string{"k1", "k2"}},
		{"k1\nk2\nk3", []string{"k1", "k2", "k3"}},
		{" k1 , k2 ,, k3 ", []string{"k1", "k2", "k3"}},
		{"k1,k2,k1", []string{"k1", "k2"}}, // dedup
	}
	for _, c := range cases {
		got := parseKeys(c.raw)
		if len(got) != len(c.want) {
			t.Fatalf("parseKeys(%q) len=%d want=%d (%v)", c.raw, len(got), len(c.want), got)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("parseKeys(%q)[%d]=%q want %q", c.raw, i, got[i], c.want[i])
			}
		}
	}
}

func TestKeyPoolRoundRobin(t *testing.T) {
	keys := []string{"a", "b", "c"}
	p := NewKeyPool(keys)

	want := []string{"a", "b", "c", "a", "b", "c", "a"}
	for i, w := range want {
		k, idx := p.Next()
		if k != w {
			t.Fatalf("call %d: got %q want %q", i, k, w)
		}
		if idx != i%len(keys) {
			t.Fatalf("call %d: idx=%d want %d", i, idx, i%len(keys))
		}
	}
}

func TestKeyPoolConcurrentEvenDistribution(t *testing.T) {
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
		got := counts[k]
		// Atomic counter guarantees exact even distribution when N is a multiple of len(keys).
		if got != expected {
			t.Fatalf("key %q: got %d want %d", k, got, expected)
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
