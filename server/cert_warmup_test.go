package server

import (
	"context"
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
