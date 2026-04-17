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
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// credentialFile mirrors codebuff's common/src/util/credentials.ts userSchema.
// Only authToken is required; other fields are captured for diagnostics.
type credentialFile struct {
	ID              string  `json:"id"`
	Email           string  `json:"email"`
	Name            *string `json:"name"`
	AuthToken       string  `json:"authToken"`
	FingerprintID   string  `json:"fingerprintId"`
	FingerprintHash string  `json:"fingerprintHash"`
}

// LoadKeySources combines inline api_keys from the config with files discovered
// under auths/. Source order: config.yaml api_keys first, then auths/*.json
// sorted by filename. Duplicates across sources are dropped (first wins).
func LoadKeySources(inline []string, authsDir string) (keys, labels []string, err error) {
	seen := make(map[string]struct{})

	for _, k := range inline {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		keys = append(keys, k)
		labels = append(labels, "config.yaml")
	}

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

// DefaultAdminTokenPath is where the admin UI bearer token is read from.
// Missing file or empty content disables the entire /admin/* surface.
const DefaultAdminTokenPath = "token.key"

// Reloader is the callback invoked when the config or auths/ tree changes.
// It re-reads sources, rebuilds the key pool, and propagates new upstream /
// server settings via onConfig (may be nil).
type Reloader struct {
	configPath string
	tokenPath  string
	pool       *KeyPool

	mu         sync.RWMutex
	current    *Config
	adminToken string
	onConfig   func(old, next *Config)
}

func NewReloader(configPath string, initial *Config, pool *KeyPool, onConfig func(old, next *Config)) *Reloader {
	r := &Reloader{
		configPath: configPath,
		tokenPath:  DefaultAdminTokenPath,
		pool:       pool,
		current:    initial,
		onConfig:   onConfig,
	}
	r.adminToken = readAdminToken(r.tokenPath)
	return r
}

// SetAdminTokenPath overrides the default token.key path (used by tests and
// non-default deployments).
func (r *Reloader) SetAdminTokenPath(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokenPath = path
	r.adminToken = readAdminToken(path)
}

// AdminTokenPath returns the current token.key path.
func (r *Reloader) AdminTokenPath() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tokenPath
}

// AdminToken returns the live admin token; empty string means admin UI is disabled.
func (r *Reloader) AdminToken() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.adminToken
}

// ConfigPath returns the config file path (used by admin endpoints to write it).
func (r *Reloader) ConfigPath() string {
	return r.configPath
}

// Current returns a snapshot of the live config. Caller must not mutate.
func (r *Reloader) Current() *Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current
}

// readAdminToken reads+trims the admin token file. Missing / empty / read error
// all resolve to "" (= admin disabled).
func readAdminToken(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Reload re-reads config + auths, applies to the pool, and updates snapshots.
// Safe to call concurrently; serialized by mu.
func (r *Reloader) Reload(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	next, err := LoadConfig(r.configPath)
	if err != nil {
		log.Printf("reload (%s): config load failed — keeping previous: %v", reason, err)
		return
	}

	keys, labels, err := LoadKeySources(next.Auth.APIKeys, next.Auth.Dir)
	if err != nil {
		log.Printf("reload (%s): key load failed — keeping previous pool: %v", reason, err)
		// Still apply non-auth config changes below.
	} else {
		// Apply breaker tuning live.
		r.pool.SetBreakerTuning(next.Auth.Breaker.Threshold, next.Auth.Breaker.Cooldown)
		added, removed, kept := r.pool.Reload(keys, labels)
		log.Printf("reload (%s): keys added=%d removed=%d kept=%d total=%d healthy=%d",
			reason, added, removed, kept, r.pool.Size(), r.pool.HealthySize())
	}

	old := r.current
	r.current = next
	r.adminToken = readAdminToken(r.tokenPath)
	if r.onConfig != nil {
		r.onConfig(old, next)
	}
}

// Watcher watches the config file and auths/ dir for changes.
// Uses fsnotify for near-instant reloads, and a periodic tick as a safety net
// on filesystems where events are unreliable (network mounts, some Docker
// volume drivers).
type Watcher struct {
	configPath   string
	authsDir     string
	tokenPath    string
	pollInterval time.Duration
	reloader     *Reloader
	debounce     time.Duration

	mu      sync.Mutex
	pending *time.Timer
	lastSig string
}

func NewWatcher(configPath string, reloader *Reloader) *Watcher {
	cfg := reloader.Current()
	return &Watcher{
		configPath:   configPath,
		authsDir:     cfg.Auth.Dir,
		tokenPath:    reloader.AdminTokenPath(),
		pollInterval: cfg.Auth.WatchInterval,
		reloader:     reloader,
		debounce:     200 * time.Millisecond,
	}
}

// Start launches fsnotify + polling in background until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) error {
	w.lastSig = w.signature()

	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify init: %w", err)
	}

	// Watch the parent dir of the config file (events on the file itself can be
	// flaky when editors do atomic rename-on-save).
	if w.configPath != "" {
		configDir := filepath.Dir(w.configPath)
		if configDir == "" {
			configDir = "."
		}
		if err := fw.Add(configDir); err != nil {
			log.Printf("watcher: add config dir %q: %v (continuing)", configDir, err)
		}
	}
	if w.authsDir != "" {
		if err := os.MkdirAll(w.authsDir, 0o755); err == nil {
			if err := fw.Add(w.authsDir); err != nil {
				log.Printf("watcher: add auths dir %q: %v (continuing)", w.authsDir, err)
			}
		}
	}
	if w.tokenPath != "" {
		tokenDir := filepath.Dir(w.tokenPath)
		if tokenDir == "" {
			tokenDir = "."
		}
		// Only Add if it isn't already covered by configDir.
		if w.configPath == "" || !sameFile(tokenDir, filepath.Dir(w.configPath)) {
			if err := fw.Add(tokenDir); err != nil {
				log.Printf("watcher: add token dir %q: %v (continuing)", tokenDir, err)
			}
		}
	}

	go func() {
		defer fw.Close()
		poll := time.NewTicker(w.pollInterval)
		defer poll.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-fw.Events:
				if !ok {
					return
				}
				if w.isRelevant(ev) {
					w.schedule(ev.Name)
				}
			case err, ok := <-fw.Errors:
				if !ok {
					return
				}
				log.Printf("watcher: fsnotify error: %v", err)
			case <-poll.C:
				sig := w.signature()
				if sig != w.lastSig {
					w.lastSig = sig
					w.reloader.Reload("poll")
				}
			}
		}
	}()
	return nil
}

// isRelevant filters out noise (e.g., editor swap files).
func (w *Watcher) isRelevant(ev fsnotify.Event) bool {
	name := ev.Name
	base := filepath.Base(name)
	// Ignore swap / temp / backup files.
	if strings.HasPrefix(base, ".") || strings.HasSuffix(base, "~") || strings.HasSuffix(base, ".swp") {
		return false
	}
	// Match config file by path.
	if w.configPath != "" && sameFile(name, w.configPath) {
		return true
	}
	// Match admin token.key by path.
	if w.tokenPath != "" && sameFile(name, w.tokenPath) {
		return true
	}
	// Match auths/*.json by parent dir + suffix.
	if w.authsDir != "" {
		dir := filepath.Dir(name)
		if sameFile(dir, w.authsDir) && strings.HasSuffix(strings.ToLower(base), ".json") {
			return true
		}
	}
	return false
}

// schedule debounces rapid bursts of events (atomic saves often produce 3-4
// events in <50ms) into a single Reload.
func (w *Watcher) schedule(trigger string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending != nil {
		w.pending.Stop()
	}
	w.pending = time.AfterFunc(w.debounce, func() {
		sig := w.signature()
		w.mu.Lock()
		w.lastSig = sig
		w.mu.Unlock()
		w.reloader.Reload("fsnotify:" + filepath.Base(trigger))
	})
}

// signature fingerprints config + auths/ so the poll ticker can detect drift
// that fsnotify might miss.
func (w *Watcher) signature() string {
	var b strings.Builder
	if w.configPath != "" {
		if st, err := os.Stat(w.configPath); err == nil {
			fmt.Fprintf(&b, "cfg:%d:%d|", st.Size(), st.ModTime().UnixNano())
		} else {
			b.WriteString("cfg:missing|")
		}
	}
	if w.tokenPath != "" {
		if st, err := os.Stat(w.tokenPath); err == nil {
			fmt.Fprintf(&b, "tok:%d:%d|", st.Size(), st.ModTime().UnixNano())
		} else {
			b.WriteString("tok:missing|")
		}
	}
	if w.authsDir != "" {
		entries, err := os.ReadDir(w.authsDir)
		if err != nil {
			fmt.Fprintf(&b, "auths:err|")
		} else {
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
			for _, n := range names {
				st, err := os.Stat(filepath.Join(w.authsDir, n))
				if err != nil {
					continue
				}
				fmt.Fprintf(&b, "%s:%d:%d|", n, st.Size(), st.ModTime().UnixNano())
			}
		}
	}
	return b.String()
}

// sameFile compares paths via cleaned absolute representation. Falls back to
// raw string compare if Abs fails.
func sameFile(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}
