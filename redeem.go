package main

import (
	"bufio"
	"bytes"
	"log"
	"os"
	"strings"
	"sync"
)

// RedeemStore manages a plain-text pool of single-use redeem codes. One code
// per line; blank lines and lines starting with "#" are ignored. Popping a
// code removes it from the file atomically so the same code can never be
// issued twice.
//
// The store is cheap to construct; it opens the file on each operation so
// fsnotify reloads and manual edits Just Work.
type RedeemStore struct {
	mu   sync.Mutex
	path string
}

func NewRedeemStore(path string) *RedeemStore {
	return &RedeemStore{path: path}
}

// Path returns the backing file path (used by admin UI / status).
func (s *RedeemStore) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

// SetPath swaps the backing file. Subsequent operations use the new path.
func (s *RedeemStore) SetPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path = path
}

// Count returns the number of usable (non-comment, non-blank) codes.
// Returns 0 if the file does not exist.
func (s *RedeemStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	codes, _ := s.readLocked()
	return len(codes)
}

// Pop returns the first code in the file and removes it. Returns ("", false)
// when the pool is empty or the file is missing.
func (s *RedeemStore) Pop() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	codes, raw := s.readLocked()
	if len(codes) == 0 {
		return "", false
	}
	picked := codes[0]
	// Rewrite the file without the picked code. We preserve the rest verbatim
	// — comments and blank lines — by filtering only the first matching code
	// from the raw line stream.
	out := removeFirstLine(raw, picked)
	if err := atomicWrite(s.path, out); err != nil {
		// Critical: returning the code while the file still holds it would cause
		// the next Pop to re-issue the same code. Refuse the pop instead.
		log.Printf("redeem: pop write failed, refusing to consume code: %v", err)
		return "", false
	}
	return picked, true
}

// Append adds codes to the file (one per line), skipping empties + duplicates
// already present. Returns how many were actually added. Used by admin upload.
func (s *RedeemStore) Append(codes []string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, raw := s.readLocked()
	seen := make(map[string]struct{}, len(existing))
	for _, c := range existing {
		seen[c] = struct{}{}
	}
	var buf bytes.Buffer
	buf.Write(raw)
	if len(raw) > 0 && !bytes.HasSuffix(raw, []byte("\n")) {
		buf.WriteByte('\n')
	}
	added := 0
	for _, c := range codes {
		c = strings.TrimSpace(c)
		if c == "" || strings.HasPrefix(c, "#") {
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		buf.WriteString(c)
		buf.WriteByte('\n')
		added++
	}
	if added > 0 {
		_ = atomicWrite(s.path, buf.Bytes())
	}
	return added
}

// readLocked reads the file and returns (codes, rawBytes). Missing file is a
// non-error; both return values are empty in that case. Caller must hold s.mu.
func (s *RedeemStore) readLocked() ([]string, []byte) {
	if s.path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, nil
	}
	codes := make([]string, 0, 32)
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		codes = append(codes, line)
	}
	return codes, raw
}

// removeFirstLine returns raw with the first line equal to target (modulo
// whitespace) removed. Preserves line endings and comments.
func removeFirstLine(raw []byte, target string) []byte {
	var out bytes.Buffer
	removed := false
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := sc.Text()
		if !removed && strings.TrimSpace(line) == target {
			removed = true
			continue
		}
		// Strip trailing \r so Windows-edited files (\r\n) don't accumulate
		// stray carriage returns on each rewrite cycle.
		out.WriteString(strings.TrimRight(line, "\r"))
		out.WriteByte('\n')
	}
	return out.Bytes()
}
