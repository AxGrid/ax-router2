package server

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIssuingInterceptorPending(t *testing.T) {
	srv := newServerForTest(t, "router.test", "tok", "*")

	srv.certs = &certIssuer{
		states: map[string]*IssuanceState{
			"foo.router.test": {
				Host:        "foo.router.test",
				Status:      IssuancePending,
				StartedAtMs: time.Now().Add(-4 * time.Second).UnixMilli(),
			},
		},
		issueFn: func(_ context.Context, _ string) error { return nil },
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "api.foo.router.test"
	srv.publicHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if ct := rec.Result().Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("expected HTML, got Content-Type=%q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Issuing certificate") {
		t.Fatalf("body did not mention 'Issuing certificate':\n%s", body[:min(400, len(body))])
	}
	if !strings.Contains(body, "foo.router.test") {
		t.Fatalf("body did not mention service host")
	}
	if !strings.Contains(body, "elapsed") {
		t.Fatalf("body did not show elapsed timer")
	}
}

func TestIssuingInterceptorError(t *testing.T) {
	srv := newServerForTest(t, "router.test", "tok", "*")
	srv.certs = &certIssuer{
		states: map[string]*IssuanceState{
			"foo.router.test": {
				Host:   "foo.router.test",
				Status: IssuanceError,
				Error:  "rate limit exceeded",
			},
		},
		issueFn: func(_ context.Context, _ string) error { return nil },
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "foo.router.test"
	srv.publicHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Could not issue") {
		t.Fatalf("expected error variant, got body: %s", body[:min(400, len(body))])
	}
	if !strings.Contains(body, "rate limit exceeded") {
		t.Fatalf("expected error detail in body")
	}
}

func TestIssuerStatusLifecycle(t *testing.T) {
	c := &certIssuer{
		states: map[string]*IssuanceState{},
	}
	done := make(chan struct{})
	c.issueFn = func(_ context.Context, _ string) error {
		<-done
		return nil
	}

	c.Issue("foo.router.test")
	if got := c.Status("foo.router.test"); got.Status != IssuancePending {
		t.Fatalf("expected pending, got %q", got.Status)
	}

	// Idempotent — second call must not start a second goroutine.
	c.Issue("foo.router.test")

	close(done)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c.Status("foo.router.test").Status == IssuanceReady {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("issuer never transitioned to Ready")
}

func TestHTTPSRedirect(t *testing.T) {
	srv := newServerForTest(t, "router.test", "tok", "*")
	// Pretend a TLS listener will be active so the redirect kicks in.
	srv.cfg.HTTPSRedirect = true
	srv.cfg.PublicTLSAddr = ":8443"
	srv.tls = &tls.Config{} //nolint:gosec // test fixture

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/some/path?q=1", nil)
	req.Host = "foo.router.test"
	srv.buildHTTPHandler(srv.publicHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("expected 301, got %d", rec.Code)
	}
	loc := rec.Result().Header.Get("Location")
	want := "https://foo.router.test:8443/some/path?q=1"
	if loc != want {
		t.Fatalf("Location = %q, want %q", loc, want)
	}
}

func TestHTTPSRedirectStandardPort(t *testing.T) {
	srv := newServerForTest(t, "router.test", "tok", "*")
	srv.cfg.HTTPSRedirect = true
	srv.cfg.PublicTLSAddr = ":443"
	srv.tls = &tls.Config{} //nolint:gosec // test fixture

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.Host = "foo.router.test"
	srv.buildHTTPHandler(srv.publicHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("expected 301, got %d", rec.Code)
	}
	loc := rec.Result().Header.Get("Location")
	if loc != "https://foo.router.test/x" {
		t.Fatalf("Location = %q (port 443 should be omitted)", loc)
	}
}

func TestHTTPSRedirectShowsIssuingPage(t *testing.T) {
	srv := newServerForTest(t, "router.test", "tok", "*")
	srv.cfg.HTTPSRedirect = true
	srv.cfg.PublicTLSAddr = ":8443"
	srv.tls = &tls.Config{} //nolint:gosec // test fixture
	srv.certs = &certIssuer{
		states: map[string]*IssuanceState{
			"foo.router.test": {
				Host:        "foo.router.test",
				Status:      IssuancePending,
				StartedAtMs: time.Now().Add(-2 * time.Second).UnixMilli(),
			},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "foo.router.test"
	srv.buildHTTPHandler(srv.publicHandler()).ServeHTTP(rec, req)

	if rec.Code == http.StatusMovedPermanently {
		t.Fatalf("expected NOT to redirect while cert is pending, got 301 → %s",
			rec.Result().Header.Get("Location"))
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (issuing page), got %d", rec.Code)
	}
}

func TestNoRedirectByDefault(t *testing.T) {
	srv := newServerForTest(t, "router.test", "tok", "foo")
	// HTTPSRedirect defaults to false. publicHandler should run.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "foo.router.test"
	srv.buildHTTPHandler(srv.publicHandler()).ServeHTTP(rec, req)

	// Without a connected client we expect 502 — that proves we got into
	// publicHandler and out of redirect logic.
	if rec.Code == http.StatusMovedPermanently {
		t.Fatalf("did not expect redirect; got 301")
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 (no client), got %d", rec.Code)
	}
}

// newServerForTest builds a *Server suitable for unit tests of the public
// handler. It does not start any listeners.
func newServerForTest(t *testing.T, base, token, service string) *Server {
	t.Helper()
	cfg := &Config{
		BaseDomain:     base,
		ReconnectGrace: 200 * time.Millisecond,
		Tokens:         map[string]string{token: service},
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
