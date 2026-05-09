package client

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
)

// HandlerClient runs in Handler mode: each forwarded request is dispatched to
// the supplied http.Handler. WebSocket / Upgrade flows work via the standard
// http.Hijacker interface.
type HandlerClient struct {
	*Client
	handler http.Handler
}

// NewHandler builds a Client that calls h for every forwarded request.
func NewHandler(cfg Config, h http.Handler) (*HandlerClient, error) {
	if h == nil {
		return nil, errors.New("handler required")
	}
	hc := &HandlerClient{handler: h}
	c, err := newClient(cfg, hc)
	if err != nil {
		return nil, err
	}
	hc.Client = c
	return hc, nil
}

func (hc *HandlerClient) Serve(stream net.Conn) {
	defer stream.Close()
	br := bufio.NewReader(stream)
	req, err := http.ReadRequest(br)
	if err != nil {
		if err != io.EOF {
			hc.log.Printf("read request: %v", err)
		}
		return
	}
	// Without this, ServeHTTP often complains about RequestURI being set.
	// We keep it for clarity; net/http only objects when r.RequestURI is set
	// for outbound use, not inbound.
	rw := newStreamRW(stream, br)
	hc.handler.ServeHTTP(rw, req)
	rw.finish()
}

// streamRW is an http.ResponseWriter that writes an HTTP/1.1 response onto a
// raw stream. It supports http.Flusher and http.Hijacker (for WebSocket).
type streamRW struct {
	stream net.Conn
	br     *bufio.Reader
	bw     *bufio.Writer

	header http.Header
	status int

	wroteHeader bool
	chunked     bool
	chunkedW    io.WriteCloser // when chunked, writes go through this
	hijacked    bool
	noBody      bool
}

func newStreamRW(stream net.Conn, br *bufio.Reader) *streamRW {
	return &streamRW{
		stream: stream,
		br:     br,
		bw:     bufio.NewWriter(stream),
		header: make(http.Header),
	}
}

func (s *streamRW) Header() http.Header { return s.header }

func (s *streamRW) WriteHeader(code int) {
	if s.wroteHeader || s.hijacked {
		return
	}
	s.wroteHeader = true
	s.status = code

	// Decide framing. If the handler set Content-Length or
	// Transfer-Encoding, trust it. Otherwise default to chunked so streaming
	// (SSE etc.) works without buffering.
	switch code {
	case http.StatusSwitchingProtocols, http.StatusNoContent, http.StatusNotModified:
		s.noBody = true
	default:
		if s.header.Get("Content-Length") == "" && s.header.Get("Transfer-Encoding") == "" {
			s.header.Set("Transfer-Encoding", "chunked")
			s.chunked = true
		}
	}
	// One stream = one request. Tell the peer we won't reuse it.
	s.header.Set("Connection", "close")

	fmt.Fprintf(s.bw, "HTTP/1.1 %d %s\r\n", code, http.StatusText(code))
	_ = s.header.Write(s.bw)
	_, _ = s.bw.WriteString("\r\n")

	if s.chunked {
		s.chunkedW = httputil.NewChunkedWriter(s.bw)
	}
}

func (s *streamRW) Write(p []byte) (int, error) {
	if s.hijacked {
		return 0, errors.New("Write after Hijack")
	}
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	if s.noBody {
		return 0, nil
	}
	if s.chunked {
		n, err := s.chunkedW.Write(p)
		if err != nil {
			return n, err
		}
		// Flush so chunks go out promptly (SSE-friendly).
		if err := s.bw.Flush(); err != nil {
			return n, err
		}
		return n, nil
	}
	n, err := s.bw.Write(p)
	if err != nil {
		return n, err
	}
	return n, s.bw.Flush()
}

// Flush implements http.Flusher.
func (s *streamRW) Flush() {
	if s.hijacked {
		return
	}
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	_ = s.bw.Flush()
}

// Hijack implements http.Hijacker so handlers (e.g. gorilla/websocket) can
// take over the connection for a protocol upgrade.
func (s *streamRW) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if s.hijacked {
		return nil, nil, errors.New("already hijacked")
	}
	if s.wroteHeader {
		return nil, nil, errors.New("Hijack after WriteHeader")
	}
	s.hijacked = true
	if err := s.bw.Flush(); err != nil {
		return nil, nil, err
	}
	rw := bufio.NewReadWriter(s.br, s.bw)
	return s.stream, rw, nil
}

// finish writes any pending framing bits (e.g. chunked terminator) and flushes.
func (s *streamRW) finish() {
	if s.hijacked {
		return
	}
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	if s.chunked && s.chunkedW != nil {
		_ = s.chunkedW.Close() // writes "0\r\n"
		_, _ = s.bw.WriteString("\r\n")
	}
	_ = s.bw.Flush()
}

// Compile-time interface checks.
var (
	_ http.ResponseWriter = (*streamRW)(nil)
	_ http.Flusher        = (*streamRW)(nil)
	_ http.Hijacker       = (*streamRW)(nil)
)
