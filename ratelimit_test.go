package main

import (
	"testing"
)

func TestBucketUnlimited(t *testing.T) {
	b := newBucket(0)
	for i := 0; i < 100; i++ {
		if !b.allow() {
			t.Fatalf("unlimited bucket should always allow, failed at %d", i)
		}
	}
}

func TestBucketBurstAndExhaust(t *testing.T) {
	b := newBucket(5)
	// Should allow burst of 5 immediately (starts full).
	for i := 0; i < 5; i++ {
		if !b.allow() {
			t.Fatalf("allow #%d in initial burst should succeed", i+1)
		}
	}
	// 6th should be rejected (insufficient time to refill 1 token).
	if b.allow() {
		t.Fatal("6th call should fail — burst exhausted")
	}
}

func TestLimiterSetGlobal(t *testing.T) {
	ls := NewLimiterSet(LimitsConfig{GlobalRPM: 2})
	if !ls.GlobalAllow() {
		t.Fatal("first global call should succeed")
	}
	if !ls.GlobalAllow() {
		t.Fatal("second global call should succeed")
	}
	if ls.GlobalAllow() {
		t.Fatal("third global call should fail — bucket empty")
	}
}

func TestLimiterSetAccountIndependent(t *testing.T) {
	ls := NewLimiterSet(LimitsConfig{AccountRPM: 1})
	// k1 gets its one token
	if !ls.AccountAllow("k1") {
		t.Fatal("k1 first should succeed")
	}
	// k1 exhausted
	if ls.AccountAllow("k1") {
		t.Fatal("k1 second should fail")
	}
	// k2 is independent
	if !ls.AccountAllow("k2") {
		t.Fatal("k2 first should succeed even though k1 is exhausted")
	}
}

func TestLimiterSetEmptyKeyUnlimited(t *testing.T) {
	ls := NewLimiterSet(LimitsConfig{AccountRPM: 1, ClientRPM: 1})
	// Empty key / token always allows (anonymous traffic)
	for i := 0; i < 5; i++ {
		if !ls.AccountAllow("") {
			t.Fatal("empty upstream key should always allow")
		}
		if !ls.ClientAllow("") {
			t.Fatal("empty client token should always allow")
		}
	}
}

func TestLimiterSetReloadRPM(t *testing.T) {
	ls := NewLimiterSet(LimitsConfig{AccountRPM: 2})
	// prime k1's bucket
	ls.AccountAllow("k1")
	ls.AccountAllow("k1")
	if ls.AccountAllow("k1") {
		t.Fatal("k1 should be out of tokens at rpm=2")
	}
	// Reload to rpm=5 — existing bucket's rpm should be updated, but tokens stay
	// capped by rpm (still ~0). Tokens will accumulate over time.
	ls.Reload(LimitsConfig{AccountRPM: 5}, []string{"k1"}, nil)
	if ls.AccountAllow("k1") {
		t.Fatal("k1 should still have no tokens right after reload")
	}
}

func TestLimiterSetReloadPrunes(t *testing.T) {
	ls := NewLimiterSet(LimitsConfig{AccountRPM: 1})
	ls.AccountAllow("k1")
	ls.AccountAllow("k2")
	ls.mu.RLock()
	before := len(ls.accounts)
	ls.mu.RUnlock()
	if before != 2 {
		t.Fatalf("want 2 accounts before reload, got %d", before)
	}
	// Reload with whitelist containing only k1 → k2 should be pruned.
	ls.Reload(LimitsConfig{AccountRPM: 1}, []string{"k1"}, nil)
	ls.mu.RLock()
	after := len(ls.accounts)
	_, k1Alive := ls.accounts["k1"]
	_, k2Alive := ls.accounts["k2"]
	ls.mu.RUnlock()
	if after != 1 || !k1Alive || k2Alive {
		t.Fatalf("after reload want k1 only, got count=%d k1=%v k2=%v", after, k1Alive, k2Alive)
	}
}

func TestLimiterSetNil(t *testing.T) {
	var ls *LimiterSet
	// Nil receiver should be safe and always allow (for deployments without
	// a LimiterSet configured).
	if !ls.GlobalAllow() || !ls.AccountAllow("k") || !ls.ClientAllow("c") {
		t.Fatal("nil LimiterSet should allow everything")
	}
}
