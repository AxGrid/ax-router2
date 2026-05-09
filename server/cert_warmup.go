package server

import (
	"context"
	"crypto/tls"
	_ "embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"
	"golang.org/x/crypto/acme/autocert"
)

// IssuanceStatus mirrors the lifecycle of one ACME certificate request.
type IssuanceStatus string

const (
	IssuanceUnknown IssuanceStatus = ""
	IssuancePending IssuanceStatus = "pending"
	IssuanceReady   IssuanceStatus = "ready"
	IssuanceError   IssuanceStatus = "error"
)

// IssuanceState is the JSON-friendly per-host issuance record.
type IssuanceState struct {
	Host        string         `json:"host"`
	Status      IssuanceStatus `json:"status"`
	StartedAtMs int64          `json:"startedAtMs,omitempty"`
	DoneAtMs    int64          `json:"doneAtMs,omitempty"`
	ElapsedMs   int64          `json:"elapsedMs,omitempty"`
	Error       string         `json:"error,omitempty"`
}

// certIssuer drives ACME issuance and tracks state per host. At most one of
// (autocertMgr, dnsCfg) is non-nil — picked by the configured TLS mode.
type certIssuer struct {
	mu     sync.RWMutex
	states map[string]*IssuanceState

	autocertMgr *autocert.Manager
	dnsCfg      *certmagic.Config

	// Issuance timeout per host. Defaults to 5 minutes.
	timeout time.Duration

	// issueFn is overridable by tests.
	issueFn func(ctx context.Context, host string) error
}

func newCertIssuer(autocertMgr *autocert.Manager, dnsCfg *certmagic.Config) *certIssuer {
	if autocertMgr == nil && dnsCfg == nil {
		return nil
	}
	c := &certIssuer{
		states:      map[string]*IssuanceState{},
		autocertMgr: autocertMgr,
		dnsCfg:      dnsCfg,
		timeout:     5 * time.Minute,
	}
	c.issueFn = c.defaultIssue
	return c
}

// Issue starts (or no-ops on) issuance for the given host. Idempotent: a
// host already in flight or already ready is left alone; a previous error
// is retried.
func (c *certIssuer) Issue(host string) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))

	c.mu.Lock()
	if s, ok := c.states[host]; ok && (s.Status == IssuancePending || s.Status == IssuanceReady) {
		c.mu.Unlock()
		return
	}
	s := &IssuanceState{
		Host:        host,
		Status:      IssuancePending,
		StartedAtMs: time.Now().UnixMilli(),
	}
	c.states[host] = s
	c.mu.Unlock()

	go c.run(host, s)
}

func (c *certIssuer) run(host string, s *IssuanceState) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	err := c.issueFn(ctx, host)

	c.mu.Lock()
	if err != nil {
		s.Status = IssuanceError
		s.Error = err.Error()
		log.Printf("cert: issuance failed for %s: %v", host, err)
	} else {
		s.Status = IssuanceReady
		log.Printf("cert: issued %s", host)
	}
	s.DoneAtMs = time.Now().UnixMilli()
	c.mu.Unlock()
}

func (c *certIssuer) defaultIssue(ctx context.Context, host string) error {
	switch {
	case c.autocertMgr != nil:
		// GetCertificate is the public entry point that will perform the
		// challenge if the cert is not in cache yet. autocert manages its
		// own internal timeouts; ctx is unused here.
		_ = ctx
		hello := &tls.ClientHelloInfo{ServerName: host}
		_, err := c.autocertMgr.GetCertificate(hello)
		return err
	case c.dnsCfg != nil:
		return c.dnsCfg.ManageSync(ctx, []string{host})
	default:
		return fmt.Errorf("no cert manager configured")
	}
}

// Status returns the current state for a host (zero value if unknown).
func (c *certIssuer) Status(host string) IssuanceState {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.states[host]
	if !ok {
		return IssuanceState{Host: host, Status: IssuanceUnknown}
	}
	out := *s
	if out.Status == IssuancePending {
		out.ElapsedMs = time.Now().UnixMilli() - out.StartedAtMs
	} else if out.DoneAtMs > 0 {
		out.ElapsedMs = out.DoneAtMs - out.StartedAtMs
	}
	return out
}

// SnapshotAll returns issuance records keyed by host.
func (c *certIssuer) SnapshotAll() map[string]IssuanceState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]IssuanceState, len(c.states))
	now := time.Now().UnixMilli()
	for h, s := range c.states {
		v := *s
		if v.Status == IssuancePending {
			v.ElapsedMs = now - v.StartedAtMs
		} else if v.DoneAtMs > 0 {
			v.ElapsedMs = v.DoneAtMs - v.StartedAtMs
		}
		out[h] = v
	}
	return out
}

//go:embed templates/issuing.html
var issuingTemplateRaw string
var issuingTemplate = template.Must(template.New("issuing").Parse(issuingTemplateRaw))

type issuingPageData struct {
	Host        string
	ElapsedSecs int64
	Errored     bool
	Error       string
}

// renderIssuingPage writes the loading/error page for a host whose cert is
// being issued. Returns true if the request was intercepted (status pending
// or error) and false if the caller should fall through to normal handling.
func (c *certIssuer) renderIssuingPage(w http.ResponseWriter, r *http.Request, host string) bool {
	state := c.Status(host)
	switch state.Status {
	case IssuancePending:
		writeIssuingPage(w, issuingPageData{
			Host:        host,
			ElapsedSecs: state.ElapsedMs / 1000,
		}, http.StatusServiceUnavailable)
		return true
	case IssuanceError:
		writeIssuingPage(w, issuingPageData{
			Host:    host,
			Errored: true,
			Error:   state.Error,
		}, http.StatusServiceUnavailable)
		return true
	}
	return false
}

func writeIssuingPage(w http.ResponseWriter, data issuingPageData, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = issuingTemplate.Execute(w, data)
}
