package server

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// tokenStore is a thread-safe, hot-reloadable mapping of token → service
// name (or "*" for wildcard).
//
// Inline tokens (from AXR_TOKENS env) are loaded once at startup and never
// reloaded — they are typically baked into deployment config.
// File tokens (from AXR_TOKENS_FILE, JSON object) are reloaded on:
//   - SIGHUP
//   - filesystem change events on the file (via fsnotify)
type tokenStore struct {
	mu         sync.RWMutex
	inline     map[string]string
	file       map[string]string
	path       string
	lastReload time.Time
	lastErr    string
}

func newTokenStore(inline map[string]string, path string) *tokenStore {
	t := &tokenStore{
		inline: copyMap(inline),
		file:   map[string]string{},
		path:   path,
	}
	if path != "" {
		// Initial file read happens already in LoadConfig via readTokensFile —
		// but inline contained the merged result. Split them out: re-read the
		// file fresh here so reload semantics work.
		_ = t.reload()
	}
	return t
}

// Lookup returns the service name (or "*") bound to a token.
func (s *tokenStore) Lookup(token string) (service string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.inline[token]; ok {
		return v, true
	}
	if v, ok := s.file[token]; ok {
		return v, true
	}
	return "", false
}

// Snapshot returns metadata for the dashboard.
func (s *tokenStore) Snapshot() (count int, lastReload time.Time, lastErr string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.inline) + len(s.file), s.lastReload, s.lastErr
}

func (s *tokenStore) reload() error {
	if s.path == "" {
		return nil
	}
	fresh := map[string]string{}
	if err := readTokensFile(s.path, fresh); err != nil {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.lastReload = time.Now()
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	s.file = fresh
	s.lastReload = time.Now()
	s.lastErr = ""
	s.mu.Unlock()
	return nil
}

// watch starts a goroutine that listens for SIGHUP and fsnotify events to
// trigger Reload. Returns when ctx is done.
func (s *tokenStore) watch(ctx context.Context) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	var fsEvents <-chan fsnotify.Event
	var fsErrors <-chan error
	if s.path != "" {
		w, err := fsnotify.NewWatcher()
		if err != nil {
			log.Printf("token watcher: fsnotify init failed: %v", err)
		} else {
			defer w.Close()
			if err := w.Add(s.path); err != nil {
				log.Printf("token watcher: cannot watch %q: %v", s.path, err)
			} else {
				fsEvents = w.Events
				fsErrors = w.Errors
			}
		}
	}

	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	pending := false
	trigger := func() {
		if !pending {
			pending = true
			debounce.Reset(150 * time.Millisecond)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			log.Print("token watcher: SIGHUP — reloading tokens")
			if err := s.reload(); err != nil {
				log.Printf("token watcher: reload failed: %v", err)
			}
		case ev, ok := <-fsEvents:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
				trigger()
			}
		case err, ok := <-fsErrors:
			if !ok {
				return
			}
			log.Printf("token watcher: fsnotify error: %v", err)
		case <-debounce.C:
			pending = false
			if err := s.reload(); err != nil {
				log.Printf("token watcher: reload failed: %v", err)
			} else {
				log.Print("token watcher: tokens reloaded")
			}
		}
	}
}

func copyMap(m map[string]string) map[string]string {
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}
