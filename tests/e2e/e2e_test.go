package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/axgrid/ax-router2/client"
	"github.com/axgrid/ax-router2/server"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func waitListen(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("listener never came up at %s", addr)
}

// startServer / startClient run the server and client in background and
// return a cancel that tears them down.
func startStack(t *testing.T, handler http.Handler) (pub, ctl string, cancel func()) {
	t.Helper()
	pub = freeAddr(t)
	ctl = freeAddr(t)
	cfg := &server.Config{
		PublicAddr:     pub,
		ControlAddr:    ctl,
		BaseDomain:     "router.test",
		ReconnectGrace: 500 * time.Millisecond,
		Tokens:         map[string]string{"tok": "foo"},
	}
	srv, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Run(ctx) }()
	waitListen(t, pub)
	waitListen(t, ctl)

	cl, err := client.NewHandler(client.Config{
		ServerAddr:   ctl,
		Token:        "tok",
		ReconnectMin: 50 * time.Millisecond,
		ReconnectMax: 200 * time.Millisecond,
	}, handler)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = cl.Run(ctx) }()

	// Wait for the client to register (poll: hit a known path; expect non-502).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", "http://"+pub+"/__ping", nil)
		req.Host = "ping.foo.router.test"
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusBadGateway {
				return pub, ctl, func() { stop(); wg.Wait() }
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	stop()
	t.Fatal("client never registered")
	return
}

func TestForwardHTTP(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "host=%s path=%s", r.Host, r.URL.Path)
	})
	pub, _, cancel := startStack(t, mux)
	defer cancel()

	req, _ := http.NewRequest("GET", "http://"+pub+"/hello/world", nil)
	req.Host = "api.foo.router.test"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	want := "host=api.foo.router.test path=/hello/world"
	if string(body) != want {
		t.Fatalf("got %q want %q", body, want)
	}
}

func TestForwardPOSTBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(b)
	})
	pub, _, cancel := startStack(t, mux)
	defer cancel()

	body := strings.NewReader(strings.Repeat("X", 4096))
	req, _ := http.NewRequest("POST", "http://"+pub+"/echo", body)
	req.Host = "echo.foo.router.test"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if len(got) != 4096 {
		t.Fatalf("expected 4096 bytes, got %d", len(got))
	}
}

func TestUnknownService502(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	pub, _, cancel := startStack(t, mux)
	defer cancel()

	req, _ := http.NewRequest("GET", "http://"+pub+"/", nil)
	req.Host = "ping.bar.router.test" // service "bar" has no client
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("got status %d, want 502", resp.StatusCode)
	}
}

func TestWildcardTokenAndApexDashboard(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "dyn host=%s", r.Host)
	})

	pub := freeAddr(t)
	ctl := freeAddr(t)
	cfg := &server.Config{
		PublicAddr:     pub,
		ControlAddr:    ctl,
		BaseDomain:     "router.test",
		ReconnectGrace: 500 * time.Millisecond,
		Tokens:         map[string]string{"wildtok": "*"}, // any service
	}
	srv, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Run(ctx) }()
	waitListen(t, pub)
	waitListen(t, ctl)

	cl, err := client.NewHandler(client.Config{
		ServerAddr:   ctl,
		Token:        "wildtok",
		Service:      "dyn",
		ReconnectMin: 50 * time.Millisecond,
		ReconnectMax: 200 * time.Millisecond,
	}, mux)
	if err != nil {
		t.Fatal(err)
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = cl.Run(ctx) }()

	// Wait for registration via a request roundtrip.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", "http://"+pub+"/", nil)
		req.Host = "x.dyn.router.test"
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == 200 && strings.HasPrefix(string(b), "dyn host=") {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
	}

	// Now hit the apex — should hit the admin dashboard.
	req, _ := http.NewRequest("GET", "http://"+pub+"/__router/api/state", nil)
	req.Host = "router.test"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("apex state status=%d body=%q", resp.StatusCode, body)
	}
	var st map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	svcs, ok := st["services"].([]any)
	if !ok || len(svcs) == 0 {
		t.Fatalf("expected at least one service in state, got %#v", st["services"])
	}
	first := svcs[0].(map[string]any)
	if first["service"].(string) != "dyn" {
		t.Fatalf("expected service 'dyn', got %v", first["service"])
	}
	if first["connected"].(bool) != true {
		t.Fatalf("expected dyn to be connected")
	}
	stop()
	wg.Wait()
}

func TestWildcardRejectsInvalidServiceName(t *testing.T) {
	pub := freeAddr(t)
	ctl := freeAddr(t)
	cfg := &server.Config{
		PublicAddr:     pub,
		ControlAddr:    ctl,
		BaseDomain:     "router.test",
		ReconnectGrace: 500 * time.Millisecond,
		Tokens:         map[string]string{"wild": "*"},
	}
	srv, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Run(ctx) }()
	waitListen(t, ctl)

	// Attempt to register "BAD NAME" (uppercase + space).
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {})
	cl, err := client.NewHandler(client.Config{
		ServerAddr: ctl,
		Token:      "wild",
		Service:    "BAD NAME",
	}, mux)
	if err != nil {
		t.Fatal(err)
	}
	cctx, ccancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer ccancel()
	// Run will return because the server rejects, but reconnect logic will
	// retry — we just want to confirm at least the first attempt fails.
	_ = cl.Run(cctx)

	// Fetch state on apex; service should NOT exist.
	req, _ := http.NewRequest("GET", "http://"+pub+"/__router/api/state", nil)
	req.Host = "router.test"
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		var st map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&st)
		_ = resp.Body.Close()
		if svcs, _ := st["services"].([]any); len(svcs) != 0 {
			t.Fatalf("expected no services registered, got %v", svcs)
		}
	}
	stop()
	wg.Wait()
}

// TestUpgradeRoundTrip exercises the byte-pumping Upgrade path with a tiny
// custom (non-WebSocket) protocol — the router doesn't care what's in the
// frames after 101.
func TestUpgradeRoundTrip(t *testing.T) {
	upgraded := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/up", func(w http.ResponseWriter, r *http.Request) {
		hij := w.(http.Hijacker)
		conn, buf, err := hij.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		// 101 response.
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: upgrade\r\nUpgrade: foo\r\n\r\n")
		_ = buf.Flush()
		close(upgraded)
		// Echo loop.
		_, _ = io.Copy(conn, conn)
	})
	pub, _, cancel := startStack(t, mux)
	defer cancel()

	conn, err := net.Dial("tcp", pub)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /up HTTP/1.1\r\nHost: ws.foo.router.test\r\nConnection: upgrade\r\nUpgrade: foo\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 101 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	select {
	case <-upgraded:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not see upgrade")
	}
	// Talk on the upgraded connection.
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 5)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q want %q", got, "hello")
	}
}
