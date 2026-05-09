package server

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Registry tracks live router-client sessions keyed by service name and
// implements a short reconnect grace window: if a session disappears, lookups
// for the same service block briefly to give the client a chance to reconnect
// (e.g. during a restart) before returning a 502.
type Registry struct {
	grace time.Duration
	stats *statsRegistry

	mu       sync.Mutex
	sessions map[string]*Session // service -> active session
	// notifiers fires when a service's slot changes (new connect / takeover).
	notifiers map[string]chan struct{}
	// lastSeen tracks the last time we had a live session for the service —
	// used to decide if "wait for reconnect" is reasonable.
	lastSeen map[string]time.Time
}

func NewRegistry(grace time.Duration) *Registry {
	return &Registry{
		grace:     grace,
		stats:     newStatsRegistry(),
		sessions:  make(map[string]*Session),
		notifiers: make(map[string]chan struct{}),
		lastSeen:  make(map[string]time.Time),
	}
}

// Stats returns the per-service stats registry.
func (r *Registry) Stats() *statsRegistry { return r.stats }

// Register inserts a session. If another session was already registered for
// the same service it is closed (last-writer-wins; the new connection is
// considered authoritative — usually it's the same client reconnecting).
func (r *Registry) Register(s *Session) {
	if s.Stats == nil {
		s.Stats = r.stats.Get(s.Service)
	}
	s.Stats.MarkConnected(s.Remote)

	r.mu.Lock()
	old := r.sessions[s.Service]
	r.sessions[s.Service] = s
	r.lastSeen[s.Service] = time.Now()
	ch := r.notifiers[s.Service]
	delete(r.notifiers, s.Service)
	r.mu.Unlock()

	if old != nil && old != s {
		old.Close()
	}
	if ch != nil {
		close(ch) // wake any waiters
	}
}

// Unregister removes the session if it is still the current one for the
// service. Stale unregisters (after a takeover) are ignored.
func (r *Registry) Unregister(s *Session) {
	r.mu.Lock()
	current := r.sessions[s.Service] == s
	if current {
		delete(r.sessions, s.Service)
		r.lastSeen[s.Service] = time.Now()
	}
	r.mu.Unlock()
	if current && s.Stats != nil {
		s.Stats.MarkDisconnected()
	}
}

var ErrNoClient = errors.New("no client connected for service")

// Lookup returns the active session for a service. If none is connected but
// one was seen within the grace window, it waits up to ReconnectGrace for a
// reconnection. Returns ErrNoClient if nothing shows up in time.
func (r *Registry) Lookup(ctx context.Context, service string) (*Session, error) {
	r.mu.Lock()
	if s, ok := r.sessions[service]; ok {
		r.mu.Unlock()
		return s, nil
	}
	last, hadOne := r.lastSeen[service]
	if !hadOne || time.Since(last) > r.grace {
		r.mu.Unlock()
		return nil, ErrNoClient
	}
	ch, ok := r.notifiers[service]
	if !ok {
		ch = make(chan struct{})
		r.notifiers[service] = ch
	}
	remaining := r.grace - time.Since(last)
	r.mu.Unlock()

	if remaining <= 0 {
		return nil, ErrNoClient
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-ch:
		r.mu.Lock()
		s, ok := r.sessions[service]
		r.mu.Unlock()
		if !ok {
			return nil, ErrNoClient
		}
		return s, nil
	case <-timer.C:
		return nil, ErrNoClient
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// HasService reports whether a service has ever been registered (cumulative,
// regardless of current connection state). Used by the cert-issuer / autocert
// HostPolicy to gate ACME challenges on real services only.
func (r *Registry) HasService(service string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[service]; ok {
		return true
	}
	_, ok := r.lastSeen[service]
	return ok
}

// CloseAll terminates every active session.
func (r *Registry) CloseAll() {
	r.mu.Lock()
	sess := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		sess = append(sess, s)
	}
	r.sessions = map[string]*Session{}
	r.mu.Unlock()
	for _, s := range sess {
		s.Close()
	}
}
