package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeRedeemFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRedeemStorePopConsumes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "codes.txt")
	writeRedeemFile(t, p, "CODE-1\nCODE-2\nCODE-3\n")
	s := NewRedeemStore(p)

	if got := s.Count(); got != 3 {
		t.Fatalf("Count=%d want 3", got)
	}
	c, ok := s.Pop()
	if !ok || c != "CODE-1" {
		t.Fatalf("first pop: %q %v", c, ok)
	}
	c, ok = s.Pop()
	if !ok || c != "CODE-2" {
		t.Fatalf("second pop: %q %v", c, ok)
	}
	if got := s.Count(); got != 1 {
		t.Fatalf("remaining Count=%d want 1", got)
	}
	// File on disk should only contain CODE-3 now.
	raw, _ := os.ReadFile(p)
	if strings.TrimSpace(string(raw)) != "CODE-3" {
		t.Fatalf("disk after 2 pops: %q", string(raw))
	}
}

func TestRedeemStoreEmptyFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "codes.txt")
	writeRedeemFile(t, p, "")
	s := NewRedeemStore(p)
	if _, ok := s.Pop(); ok {
		t.Fatal("empty file should pop !ok")
	}
}

func TestRedeemStoreMissingFile(t *testing.T) {
	s := NewRedeemStore(filepath.Join(t.TempDir(), "nope.txt"))
	if s.Count() != 0 {
		t.Fatalf("missing file Count != 0")
	}
	if _, ok := s.Pop(); ok {
		t.Fatal("missing file pop should be !ok")
	}
}

func TestRedeemStoreIgnoresCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "codes.txt")
	writeRedeemFile(t, p, "# a comment\n\n  \nCODE-A\n# trailing\nCODE-B\n")
	s := NewRedeemStore(p)
	if got := s.Count(); got != 2 {
		t.Fatalf("Count=%d want 2 (ignoring comments + blanks)", got)
	}
	c, _ := s.Pop()
	if c != "CODE-A" {
		t.Fatalf("first real pop: %q", c)
	}
	// Comments should survive the rewrite.
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "# a comment") {
		t.Fatalf("comments lost after pop: %q", string(raw))
	}
}

func TestRedeemStoreConcurrentPop(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "codes.txt")
	var sb strings.Builder
	const N = 200
	for i := 0; i < N; i++ {
		sb.WriteString("CODE-")
		sb.WriteString(string(rune('a' + i%26)))
		// Make sure codes are unique: suffix with index
		for tmp := i; tmp > 0; tmp /= 10 {
			sb.WriteByte(byte('0' + tmp%10))
		}
		sb.WriteByte('\n')
	}
	writeRedeemFile(t, p, sb.String())
	s := NewRedeemStore(p)

	var wg sync.WaitGroup
	got := make(chan string, N+10)
	for i := 0; i < N+10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c, ok := s.Pop(); ok {
				got <- c
			}
		}()
	}
	wg.Wait()
	close(got)
	seen := make(map[string]struct{})
	for c := range got {
		if _, dup := seen[c]; dup {
			t.Fatalf("duplicate code issued: %q", c)
		}
		seen[c] = struct{}{}
	}
	if len(seen) != N {
		t.Fatalf("consumed %d unique codes; want %d", len(seen), N)
	}
	if s.Count() != 0 {
		t.Fatalf("pool should be empty, got %d", s.Count())
	}
}

func TestRedeemStoreAppend(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "codes.txt")
	writeRedeemFile(t, p, "OLD-1\n")
	s := NewRedeemStore(p)
	added := s.Append([]string{"NEW-A", "NEW-B", "OLD-1", "", "# skip me", "NEW-A"})
	if added != 2 {
		t.Fatalf("added=%d want 2 (dedup + skip)", added)
	}
	if s.Count() != 3 {
		t.Fatalf("Count=%d want 3", s.Count())
	}
}
