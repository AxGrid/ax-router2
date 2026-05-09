package server

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// forwardHTTP proxies a regular HTTP request through a yamux stream to the
// target client. The request and response bodies are streamed; nothing is
// fully buffered. Per-request bytes / latency are recorded into the session
// Stats.
func forwardHTTP(w http.ResponseWriter, r *http.Request, sess *Session) {
	start := time.Now()
	stream, err := sess.OpenStream()
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	defer stream.Close()

	sess.Stats.HTTPStreamStart()
	defer sess.Stats.HTTPStreamEnd()

	cw := &counter{Conn: stream}

	// Write request asynchronously so a slow upload doesn't block reading
	// the response (e.g. a server that 401s before the upload is finished).
	writeDone := make(chan error, 1)
	go func() { writeDone <- r.Write(cw) }()

	br := bufio.NewReader(cw)
	resp, err := http.ReadResponse(br, r)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		<-writeDone
		return
	}
	defer resp.Body.Close()

	dst := w.Header()
	for k, vv := range resp.Header {
		dst[k] = vv
	}
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}

	<-writeDone
	sess.Stats.HTTPCompleted(time.Since(start), cw.WrittenBytes(), cw.ReadBytes())
}

// forwardUpgrade proxies a connection-upgrading request (WebSocket and
// friends). The whole exchange — including the eventual 101 response and any
// frames that follow — is shuttled byte-for-byte between the public side and
// the yamux stream.
func forwardUpgrade(w http.ResponseWriter, r *http.Request, sess *Session) {
	hij, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacker not supported", http.StatusInternalServerError)
		return
	}

	stream, err := sess.OpenStream()
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}

	cw := &counter{Conn: stream}

	// Send the upgrade request first (including any pre-upgrade body); this
	// must happen before Hijack because r.Body draws from the http.Server-
	// owned conn.
	if err := r.Write(cw); err != nil {
		stream.Close()
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}

	pubConn, pubBuf, err := hij.Hijack()
	if err != nil {
		stream.Close()
		return
	}

	sess.Stats.WSStart()

	// Bidirectional pump with byte counting.
	done := make(chan struct{}, 2)
	report := func() {
		sess.Stats.AddWSBytes(cw.WrittenBytes(), cw.ReadBytes())
		cw.ResetCounters()
	}
	// Periodic flush of byte counts so the dashboard updates while the WS
	// is alive.
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	go func() {
		for {
			select {
			case <-tick.C:
				sess.Stats.AddWSBytes(cw.WrittenBytes(), cw.ReadBytes())
				cw.ResetCounters()
			case <-done:
				return
			}
		}
	}()

	go func() {
		defer pubConn.Close()
		defer stream.Close()
		defer func() { done <- struct{}{} }()
		// public -> stream: drain whatever bufio already buffered, then raw.
		if pubBuf != nil && pubBuf.Reader.Buffered() > 0 {
			n := pubBuf.Reader.Buffered()
			peek, _ := pubBuf.Reader.Peek(n)
			if len(peek) > 0 {
				if _, err := cw.Write(peek); err != nil {
					return
				}
				_, _ = pubBuf.Reader.Discard(len(peek))
			}
		}
		_, _ = io.Copy(cw, pubConn)
	}()
	go func() {
		defer pubConn.Close()
		defer stream.Close()
		defer func() { done <- struct{}{} }()
		_, _ = io.Copy(pubConn, cw)
	}()

	// Wait for both directions to terminate, then finalize counters.
	<-done
	<-done
	report()
	sess.Stats.WSEnd()
	close(done)
}

// counter wraps a net.Conn and tracks bytes Read / Written for stats
// reporting. Safe for concurrent Read/Write because each direction has its
// own atomic counter.
type counter struct {
	net.Conn
	read    atomic.Uint64
	written atomic.Uint64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.read.Add(uint64(n))
	}
	return n, err
}

func (c *counter) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.written.Add(uint64(n))
	}
	return n, err
}

func (c *counter) ReadBytes() uint64    { return c.read.Load() }
func (c *counter) WrittenBytes() uint64 { return c.written.Load() }

func (c *counter) ResetCounters() {
	c.read.Store(0)
	c.written.Store(0)
}

// isUpgrade reports whether the request is an HTTP/1.1 protocol upgrade
// (WebSocket, h2c, etc.).
func isUpgrade(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Connection"), "upgrade") {
		// Connection may be a comma-separated list (e.g. "keep-alive, Upgrade").
		conn := strings.ToLower(r.Header.Get("Connection"))
		hasUpgrade := false
		for _, tok := range strings.Split(conn, ",") {
			if strings.TrimSpace(tok) == "upgrade" {
				hasUpgrade = true
				break
			}
		}
		if !hasUpgrade {
			return false
		}
	}
	return r.Header.Get("Upgrade") != ""
}
