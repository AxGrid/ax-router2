package server

import (
	"crypto/subtle"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/axgrid/ax-router2/web"
)

// adminHandler is the apex (Host == BaseDomain) handler. It serves the
// embedded React dashboard plus a REST snapshot endpoint and a 1-Hz WebSocket
// stream of stats.
type adminHandler struct {
	registry  *Registry
	tokens    *tokenStore
	certs     *certIssuer
	cfg       *Config
	startedAt time.Time

	// Embedded webapp (built into web/dist by Vite).
	staticFS http.Handler
}

func newAdminHandler(cfg *Config, reg *Registry, tok *tokenStore, certs *certIssuer) (*adminHandler, error) {
	sub, err := fs.Sub(web.FS, "dist")
	if err != nil {
		return nil, err
	}
	return &adminHandler{
		registry:  reg,
		tokens:    tok,
		certs:     certs,
		cfg:       cfg,
		startedAt: time.Now(),
		staticFS:  http.FileServer(http.FS(sub)),
	}, nil
}

func (a *adminHandler) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if a.cfg.AdminUser == "" || a.cfg.AdminPass == "" {
		return true
	}
	user, pass, ok := r.BasicAuth()
	if !ok ||
		subtle.ConstantTimeCompare([]byte(user), []byte(a.cfg.AdminUser)) != 1 ||
		subtle.ConstantTimeCompare([]byte(pass), []byte(a.cfg.AdminPass)) != 1 {
		w.Header().Set("WWW-Authenticate", `Basic realm="ax-router admin"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func (a *adminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !a.requireAuth(w, r) {
		return
	}
	switch {
	case r.URL.Path == "/__router/api/state":
		a.serveState(w, r)
	case r.URL.Path == "/__router/ws":
		a.serveWS(w, r)
	default:
		// Static files (or fall back to index.html for client-side routes).
		a.serveStatic(w, r)
	}
}

func (a *adminHandler) serveStatic(w http.ResponseWriter, r *http.Request) {
	// http.FileServer already serves index.html automatically for "/", and
	// returns 404 for anything else not in dist/.
	a.staticFS.ServeHTTP(w, r)
}

// State is the full dashboard snapshot.
type State struct {
	BaseDomain   string                   `json:"baseDomain"`
	StartedAtMs  int64                    `json:"startedAtMs"`
	Now          int64                    `json:"nowMs"`
	Services     []StatsSnapshot          `json:"services"`
	TLSMode      string                   `json:"tlsMode"`
	Certs        map[string]IssuanceState `json:"certs,omitempty"`
	TokenCount   int                      `json:"tokenCount"`
	TokensReload int64                    `json:"tokensReloadedAtMs"`
	TokensError  string                   `json:"tokensError,omitempty"`
}

func (a *adminHandler) snapshot() State {
	count, last, lastErr := a.tokens.Snapshot()
	st := State{
		BaseDomain:  a.cfg.BaseDomain,
		StartedAtMs: a.startedAt.UnixMilli(),
		Now:         time.Now().UnixMilli(),
		Services:    a.registry.stats.SnapshotAll(),
		TLSMode:     string(a.cfg.TLS.Mode),
		TokenCount:  count,
		TokensError: lastErr,
	}
	if a.certs != nil {
		st.Certs = a.certs.SnapshotAll()
	}
	if !last.IsZero() {
		st.TokensReload = last.UnixMilli()
	}
	return st
}

func (a *adminHandler) serveState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(a.snapshot())
}

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 64 << 10,
	// Same-origin only is the default. We're served from the apex; the JS
	// app is loaded from the same origin too.
	CheckOrigin: func(r *http.Request) bool {
		o := r.Header.Get("Origin")
		if o == "" {
			return true
		}
		host := r.Host
		// Strip "http(s)://" prefix from origin and compare hosts.
		o = strings.TrimPrefix(o, "https://")
		o = strings.TrimPrefix(o, "http://")
		return o == host
	},
}

func (a *adminHandler) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Drain client reads (we don't expect any messages but want to detect
	// close).
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
		}
	}()

	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	// Send first snapshot immediately so the UI has data without waiting.
	if err := writeJSON(conn, a.snapshot()); err != nil {
		return
	}
	for {
		select {
		case <-closed:
			return
		case <-tick.C:
			if err := writeJSON(conn, a.snapshot()); err != nil {
				return
			}
		}
	}
}

func writeJSON(conn *websocket.Conn, v any) error {
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return conn.WriteJSON(v)
}
