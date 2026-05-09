// Package client connects a local service to an ax-router server. There are
// two ways to serve traffic:
//
//   - Handler mode: pass an http.Handler (e.g. *http.ServeMux) to NewHandler;
//     the library reads each forwarded request, runs the handler, and writes
//     the response back. WebSocket upgrades go through http.Hijacker.
//
//   - Proxy mode: pass a target URL (e.g. "http://localhost:8080") to NewProxy;
//     the library byte-for-byte forwards the request to the target service
//     and shuttles the response back. Works for both HTTP and WebSocket.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/axgrid/ax-router2/internal/protocol"
)

// Config is the shared configuration for both client modes.
type Config struct {
	// ServerAddr — host:port of the router's control listener.
	ServerAddr string
	// Token — credential. Bound to a fixed service name on the server, OR
	// configured as a wildcard ("*") in which case Service below is required.
	Token string
	// Service — desired service name. Required when Token is a wildcard
	// token; ignored when the token is bound to a specific service. Last
	// writer wins on the server side, so the same Service from a reconnect
	// transparently takes over.
	Service string

	// ReconnectMin / ReconnectMax — backoff window for reconnect after the
	// control connection drops. Zero values use sensible defaults.
	ReconnectMin time.Duration
	ReconnectMax time.Duration

	// Logger — optional. Defaults to log.Default().
	Logger *log.Logger
}

// streamHandler turns one yamux stream into work. Implementations are provided
// by handler.go (Handler mode) and proxy.go (Proxy mode).
type streamHandler interface {
	Serve(stream net.Conn)
}

// Client is the long-running router-client. Use NewHandler or NewProxy.
type Client struct {
	cfg Config
	h   streamHandler
	log *log.Logger
}

func newClient(cfg Config, h streamHandler) (*Client, error) {
	if cfg.ServerAddr == "" {
		return nil, errors.New("ServerAddr required")
	}
	if cfg.Token == "" {
		return nil, errors.New("Token required")
	}
	if cfg.ReconnectMin == 0 {
		cfg.ReconnectMin = 500 * time.Millisecond
	}
	if cfg.ReconnectMax == 0 {
		cfg.ReconnectMax = 15 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	return &Client{cfg: cfg, h: h, log: cfg.Logger}, nil
}

// Run dials the router server, registers, and serves incoming streams until
// ctx is cancelled. Reconnects on disconnect with exponential backoff.
func (c *Client) Run(ctx context.Context) error {
	backoff := c.cfg.ReconnectMin
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			c.log.Printf("ax-router-client: %v (reconnect in %s)", err, backoff)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > c.cfg.ReconnectMax {
			backoff = c.cfg.ReconnectMax
		}
	}
}

func (c *Client) runOnce(ctx context.Context) error {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", c.cfg.ServerAddr)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	// Handshake.
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := protocol.WriteHello(conn, protocol.Hello{Token: c.cfg.Token, Service: c.cfg.Service}); err != nil {
		_ = conn.Close()
		return fmt.Errorf("write hello: %w", err)
	}
	status, ack, err := protocol.ReadAck(conn)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("read ack: %w", err)
	}
	if status != protocol.StatusOK {
		_ = conn.Close()
		return fmt.Errorf("server rejected: status=%d msg=%q", status, ack.Error)
	}
	_ = conn.SetDeadline(time.Time{})
	c.log.Printf("ax-router-client: connected to %s as service=%q", c.cfg.ServerAddr, ack.Service)

	// yamux server side — we Accept streams that the public server opens.
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	mux, err := yamux.Server(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("yamux: %w", err)
	}

	// Cancel when ctx is done — closes mux which makes Accept return.
	go func() {
		<-ctx.Done()
		_ = mux.Close()
	}()

	for {
		stream, err := mux.AcceptStream()
		if err != nil {
			_ = mux.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("accept: %w", err)
		}
		go c.h.Serve(stream)
	}
}
