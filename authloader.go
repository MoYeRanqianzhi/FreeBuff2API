package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// credentialFile mirrors codebuff's common/src/util/credentials.ts userSchema.
// Only authToken is required; other fields are captured for diagnostics.
type credentialFile struct {
	ID              string `json:"id"`
	Email           string `json:"email"`
	Name            *string `json:"name"`
	AuthToken       string `json:"authToken"`
	FingerprintID   string `json:"fingerprintId"`
	FingerprintHash string `json:"fingerprintHash"`
}

// LoadKeySources reads keys from both env and auths/ dir, preserving label
// provenance (env vs filename) so logs and /status can identify the source.
//
// Order: env keys first (by declaration), then auths/ files sorted by name.
// Duplicates across sources are de-duplicated — the first occurrence wins.
func LoadKeySources(envRaw, authsDir string) (keys, labels []string, err error) {
	seen := make(map[string]struct{})

	// 1) env
	for _, k := range parseKeys(envRaw) {
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		keys = append(keys, k)
		labels = append(labels, "env")
	}

	// 2) auths/*.json
	fileKeys, fileLabels, ferr := loadAuthsDir(authsDir)
	if ferr != nil && !errors.Is(ferr, os.ErrNotExist) {
		return nil, nil, ferr
	}
	for i, k := range fileKeys {
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		keys = append(keys, k)
		labels = append(labels, fileLabels[i])
	}

	return keys, labels, nil
}

func loadAuthsDir(dir string) (keys, labels []string, err error) {
	if dir == "" {
		return nil, nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		full := filepath.Join(dir, name)
		data, rerr := os.ReadFile(full)
		if rerr != nil {
			log.Printf("auths: skip %s (read error: %v)", name, rerr)
			continue
		}
		var cred credentialFile
		if jerr := json.Unmarshal(data, &cred); jerr != nil {
			log.Printf("auths: skip %s (invalid JSON: %v)", name, jerr)
			continue
		}
		tok := strings.TrimSpace(cred.AuthToken)
		if tok == "" {
			log.Printf("auths: skip %s (empty authToken)", name)
			continue
		}
		keys = append(keys, tok)
		labels = append(labels, "auths/"+name)
	}
	return keys, labels, nil
}

// AuthsWatcher periodically re-reads env + auths/ and reloads the KeyPool.
// It tracks a directory signature (file names + sizes + mtimes) to avoid
// reloading when nothing changed.
type AuthsWatcher struct {
	dir        string
	envRaw     string
	pool       *KeyPool
	interval   time.Duration
	lastSig    string
}

func NewAuthsWatcher(pool *KeyPool, envRaw, dir string, interval time.Duration) *AuthsWatcher {
	return &AuthsWatcher{
		dir:      dir,
		envRaw:   envRaw,
		pool:     pool,
		interval: interval,
	}
}

// Start runs the watcher until ctx is cancelled. Safe to call once.
func (w *AuthsWatcher) Start(ctx context.Context) {
	if w.interval <= 0 {
		return
	}
	// Seed signature so the initial Start doesn't cause a spurious reload
	// (initial load already happened in main).
	w.lastSig = w.signature()

	go func() {
		t := time.NewTicker(w.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				w.Tick()
			}
		}
	}()
}

// Tick checks the signature and reloads if changed.
func (w *AuthsWatcher) Tick() {
	sig := w.signature()
	if sig == w.lastSig {
		return
	}
	w.lastSig = sig

	keys, labels, err := LoadKeySources(w.envRaw, w.dir)
	if err != nil {
		log.Printf("auths watcher: load error: %v", err)
		return
	}
	if len(keys) == 0 {
		log.Printf("auths watcher: reload skipped (no keys found)")
		return
	}
	added, removed, kept := w.pool.Reload(keys, labels)
	log.Printf("auths watcher: reloaded keys — added=%d removed=%d kept=%d total=%d",
		added, removed, kept, w.pool.Size())
}

// signature produces a fingerprint of the auths dir (name+size+mtime of each
// .json). The env string is also hashed so env changes trigger a reload too
// (rare — typically requires restart — but cheap to include).
func (w *AuthsWatcher) signature() string {
	var b strings.Builder
	fmt.Fprintf(&b, "env:%d|", len(w.envRaw))
	b.WriteString(w.envRaw)
	b.WriteByte('|')

	if w.dir == "" {
		return b.String()
	}
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		fmt.Fprintf(&b, "err:%v", err)
		return b.String()
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		full := filepath.Join(w.dir, name)
		st, err := os.Stat(full)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s:%d:%d|", name, st.Size(), st.ModTime().UnixNano())
	}
	return b.String()
}
