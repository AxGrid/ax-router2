package server

import (
	"errors"
	"io"
	"net"
	"sync"

	"github.com/hashicorp/yamux"
)

// Session is one connected router-client. Streams are opened by the public
// server side and read by the client side.
type Session struct {
	Service string
	Token   string
	Remote  string // peer addr, for logging
	Stats   *Stats // never nil; set by the registry on Register

	mux *yamux.Session

	closeOnce sync.Once
	closed    chan struct{}
}

func newSession(service, token string, conn net.Conn) (*Session, error) {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard // silence yamux's chatty stdlib logger
	// Public server is the active opener of streams.
	mux, err := yamux.Client(conn, cfg)
	if err != nil {
		return nil, err
	}
	return &Session{
		Service: service,
		Token:   token,
		Remote:  conn.RemoteAddr().String(),
		mux:     mux,
		closed:  make(chan struct{}),
	}, nil
}

// OpenStream pushes a new bidi stream to the client.
func (s *Session) OpenStream() (net.Conn, error) {
	if s.mux.IsClosed() {
		return nil, errors.New("session closed")
	}
	return s.mux.OpenStream()
}

func (s *Session) Closed() <-chan struct{} {
	return s.closed
}

func (s *Session) Close() {
	s.closeOnce.Do(func() {
		_ = s.mux.Close()
		close(s.closed)
	})
}

// watch fires Close when the underlying yamux dies.
func (s *Session) watch() {
	<-s.mux.CloseChan()
	s.Close()
}
