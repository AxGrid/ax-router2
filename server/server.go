package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/axgrid/ax-router2/internal/protocol"
	"golang.org/x/crypto/acme/autocert"
)

// Server is the public HTTP/WS reverse router. It listens on:
//   - Public HTTP (and optionally HTTPS) port — terminates browser traffic
//   - Control TCP port — accepts router-clients via a small handshake +
//     yamux multiplexer.
type Server struct {
	cfg      *Config
	registry *Registry
	tokens   *tokenStore
	admin    *adminHandler
	certs    *certIssuer // nil unless an ACME mode is active

	tls      *tls.Config
	autocert *autocert.Manager
	dnsACME  *dnsACME // non-nil only in TLSDNS mode

	mu          sync.Mutex
	listeners   []net.Listener
	httpServers []*http.Server
}

func New(cfg *Config) (*Server, error) {
	registry := NewRegistry(cfg.ReconnectGrace)
	tokens := newTokenStore(cfg.Tokens, cfg.TokensFile)

	tlsCfg, mgr, dnsMgr, err := buildTLS(cfg, registry)
	if err != nil {
		return nil, err
	}

	var certs *certIssuer
	switch {
	case mgr != nil:
		certs = newCertIssuer(mgr, nil)
	case dnsMgr != nil:
		certs = newCertIssuer(nil, dnsMgr.cfg)
	}

	admin, err := newAdminHandler(cfg, registry, tokens, certs)
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:      cfg,
		registry: registry,
		tokens:   tokens,
		admin:    admin,
		certs:    certs,
		tls:      tlsCfg,
		autocert: mgr,
		dnsACME:  dnsMgr,
	}, nil
}

// Run blocks until ctx is cancelled or a listener fails fatally.
func (s *Server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start the DNS-01 cert manager (if any) before opening TLS listeners so
	// it has a chance to load cached certs.
	if s.dnsACME != nil {
		if err := s.dnsACME.start(ctx); err != nil {
			return fmt.Errorf("dns acme start: %w", err)
		}
	}

	// Token hot-reload watcher.
	go s.tokens.watch(ctx)

	errc := make(chan error, 4)

	// Public HTTP listener.
	pubLn, err := net.Listen("tcp", s.cfg.PublicAddr)
	if err != nil {
		return fmt.Errorf("listen public: %w", err)
	}
	s.track(pubLn)

	pubMux := s.publicHandler()
	httpSrv := &http.Server{
		Handler:           s.buildHTTPHandler(pubMux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.trackServer(httpSrv)
	go func() { errc <- httpSrv.Serve(pubLn) }()

	// Optional TLS listener.
	if s.cfg.PublicTLSAddr != "" && s.tls != nil {
		tlsLn, err := tls.Listen("tcp", s.cfg.PublicTLSAddr, s.tls)
		if err != nil {
			return fmt.Errorf("listen public tls: %w", err)
		}
		s.track(tlsLn)
		tlsSrv := &http.Server{
			Handler:           pubMux,
			ReadHeaderTimeout: 10 * time.Second,
		}
		s.trackServer(tlsSrv)
		go func() { errc <- tlsSrv.Serve(tlsLn) }()
	}

	// Control listener.
	ctlLn, err := net.Listen("tcp", s.cfg.ControlAddr)
	if err != nil {
		return fmt.Errorf("listen control: %w", err)
	}
	s.track(ctlLn)
	go func() { errc <- s.runControl(ctx, ctlLn) }()

	log.Printf("ax-router: public=%s tls=%s control=%s base=%s tlsmode=%s",
		s.cfg.PublicAddr, s.cfg.PublicTLSAddr, s.cfg.ControlAddr, s.cfg.BaseDomain, s.cfg.TLS.Mode)

	select {
	case <-ctx.Done():
		return s.shutdown()
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			_ = s.shutdown()
			return err
		}
		return s.shutdown()
	}
}

func (s *Server) track(l net.Listener) {
	s.mu.Lock()
	s.listeners = append(s.listeners, l)
	s.mu.Unlock()
}

func (s *Server) trackServer(h *http.Server) {
	s.mu.Lock()
	s.httpServers = append(s.httpServers, h)
	s.mu.Unlock()
}

func (s *Server) shutdown() error {
	s.mu.Lock()
	servers := s.httpServers
	listeners := s.listeners
	s.httpServers = nil
	s.listeners = nil
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(ctx)
	}
	for _, l := range listeners {
		_ = l.Close()
	}
	s.registry.CloseAll()
	return nil
}

// buildHTTPHandler wraps the public handler chain for the plain-HTTP listener.
// Layering (outer → inner):
//
//  1. autocert.HTTPHandler — intercepts /.well-known/acme-challenge/* before
//     anything else (only when autocert is active).
//  2. httpsRedirector — 301s every request to its HTTPS counterpart, EXCEPT
//     when the cert for that host is still being issued (in which case we
//     show the loading page so the browser doesn't stall in TLS handshake).
//     Only active when AXR_HTTPS_REDIRECT=true and a TLS listener is up.
//  3. publicHandler — normal apex / proxy routing.
func (s *Server) buildHTTPHandler(pub http.Handler) http.Handler {
	redirectActive := s.cfg.HTTPSRedirect && s.cfg.PublicTLSAddr != "" && s.tls != nil
	inner := pub
	if redirectActive {
		inner = s.httpsRedirector(pub)
	}
	if s.autocert != nil {
		return s.autocert.HTTPHandler(inner)
	}
	return inner
}

// httpsRedirector returns a handler that 301s plain-HTTP requests to HTTPS,
// preserving path + query + Host. ACME challenges are NOT seen here — they
// are absorbed by autocert.HTTPHandler one layer up.
//
// Exception: if the host belongs to a service whose cert is still being
// issued, we show the loading page on plain HTTP instead of redirecting.
// Redirecting would land the user on HTTPS where the TLS handshake would
// block until issuance completes (usually fine, but feels broken).
func (s *Server) httpsRedirector(fallback http.Handler) http.Handler {
	tlsPort := portFromAddr(s.cfg.PublicTLSAddr)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /health must answer on plain HTTP for monitoring probes that don't
		// speak TLS. Pass through to the public handler.
		if r.URL.Path == "/health" {
			fallback.ServeHTTP(w, r)
			return
		}

		host := stripPort(r.Host)
		base := strings.ToLower(s.cfg.BaseDomain)

		if s.certs != nil &&
			!strings.EqualFold(host, base) &&
			!strings.EqualFold(host, "www."+base) {
			if service := extractService(host, base); service != "" {
				if s.certs.renderIssuingPage(w, r, service+"."+base) {
					return
				}
			}
		}

		target := "https://" + host
		if tlsPort != "" && tlsPort != "443" {
			target += ":" + tlsPort
		}
		target += r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}

func portFromAddr(addr string) string {
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		return addr[i+1:]
	}
	return ""
}

// publicHandler routes incoming HTTP(S) requests to either the apex admin
// dashboard or the right router-client.
func (s *Server) publicHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /health is auth-free and host-agnostic — k8s/loadbalancer probes
		// often dial the IP directly with an arbitrary Host header. This
		// shadows any service-level /health on a sub-host; downstream services
		// should expose their own health on a different path.
		if r.URL.Path == "/health" {
			s.serveHealth(w, r)
			return
		}

		host := stripPort(r.Host)
		base := strings.ToLower(s.cfg.BaseDomain)
		if strings.EqualFold(host, base) || strings.EqualFold(host, "www."+base) {
			s.admin.ServeHTTP(w, r)
			return
		}

		service := extractService(host, base)
		if service == "" {
			http.Error(w, "unknown host", http.StatusNotFound)
			return
		}

		// While the cert is being issued for this service, intercept the
		// request and show the loading page. The state is keyed on the
		// bare service host (<service>.<base>); deeper subdomains share it
		// since they hit the same cert in autocert mode.
		if s.certs != nil {
			if s.certs.renderIssuingPage(w, r, service+"."+base) {
				return
			}
		}

		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ReconnectGrace+time.Second)
		defer cancel()
		sess, err := s.registry.Lookup(ctx, service)
		if err != nil {
			http.Error(w, "service unavailable", http.StatusBadGateway)
			return
		}
		if isUpgrade(r) {
			forwardUpgrade(w, r, sess)
			return
		}
		forwardHTTP(w, r, sess)
	})
}

// runControl accepts router-client connections on the control port.
func (s *Server) runControl(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handleControl(conn)
	}
}

// serviceNameRE allows a single subdomain label: 1-31 chars, lowercase
// alphanumeric or hyphen, must start with alphanumeric.
var serviceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

func (s *Server) handleControl(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	hello, err := protocol.ReadHello(conn)
	if err != nil {
		log.Printf("control: bad handshake from %s: %v", conn.RemoteAddr(), err)
		_ = conn.Close()
		return
	}
	bound, ok := s.tokens.Lookup(hello.Token)
	if !ok {
		_ = protocol.WriteAck(conn, protocol.StatusBadAuth, protocol.Ack{Error: "invalid token"})
		_ = conn.Close()
		log.Printf("control: bad auth from %s", conn.RemoteAddr())
		return
	}

	var service string
	switch bound {
	case "*":
		service = strings.ToLower(strings.TrimSpace(hello.Service))
		if !serviceNameRE.MatchString(service) {
			_ = protocol.WriteAck(conn, protocol.StatusBadRequest, protocol.Ack{
				Error: "wildcard token requires a valid 'service' name in Hello (regex: " + serviceNameRE.String() + ")",
			})
			_ = conn.Close()
			return
		}
	default:
		service = bound
		// Reject if the client tried to claim a different name than its token allows.
		if hello.Service != "" && !strings.EqualFold(hello.Service, bound) {
			_ = protocol.WriteAck(conn, protocol.StatusBadRequest, protocol.Ack{
				Error: "token is bound to service " + bound,
			})
			_ = conn.Close()
			return
		}
	}

	if err := protocol.WriteAck(conn, protocol.StatusOK, protocol.Ack{Service: service}); err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})

	sess, err := newSession(service, hello.Token, conn)
	if err != nil {
		log.Printf("control: yamux init failed: %v", err)
		_ = conn.Close()
		return
	}
	s.registry.Register(sess)
	log.Printf("control: %s registered service=%q", conn.RemoteAddr(), service)

	// Pre-warm an ACME certificate for the bare service host so users hitting
	// HTTPS see normal flow instead of a stalled handshake.
	if s.certs != nil {
		s.certs.Issue(service + "." + strings.ToLower(s.cfg.BaseDomain))
	}

	go sess.watch()

	<-sess.Closed()
	s.registry.Unregister(sess)
	log.Printf("control: %s disconnected service=%q", conn.RemoteAddr(), service)
}

// serveHealth answers liveness/readiness probes. 200 "ok\n" is the normal
// case; 500 fires when the token file failed its last reload — operator-
// actionable degraded state where new clients can't authenticate.
func (s *Server) serveHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if _, _, lastErr := s.tokens.Snapshot(); lastErr != "" {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "fail: tokens: %s\n", lastErr)
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}

func stripPort(host string) string {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		return host[:i]
	}
	return host
}

// extractService pulls the service-name label out of a Host header.
//
// "api.foo.router.com:443" with base="router.com" → "foo".
// "deep.nested.foo.router.com" with base="router.com" → "foo".
// "router.com" → "" (apex, served by the admin dashboard).
func extractService(host, base string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	base = strings.ToLower(base)
	if host == "" || host == base {
		return ""
	}
	suffix := "." + base
	if !strings.HasSuffix(host, suffix) {
		return ""
	}
	inner := host[:len(host)-len(suffix)]
	if i := strings.LastIndexByte(inner, '.'); i >= 0 {
		return inner[i+1:]
	}
	return inner
}
