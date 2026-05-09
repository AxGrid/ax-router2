package client

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ProxyClient runs in Proxy mode: each forwarded request is dialed onward to
// TargetURL byte-for-byte. Both regular HTTP and Upgrade flows (WebSocket) go
// through the same code path.
type ProxyClient struct {
	*Client
	target *url.URL

	// HostRewrite, if true, rewrites Host: to the target's host. Default
	// false — keeps the original Host so apps can inspect the public name.
	HostRewrite bool

	// DialTimeout for the local target connection.
	DialTimeout time.Duration

	// TLSConfig used when target.Scheme == "https" / "wss".
	TLSConfig *tls.Config
}

// NewProxy builds a Client that forwards each incoming request to targetURL.
func NewProxy(cfg Config, targetURL string) (*ProxyClient, error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("parse target: %w", err)
	}
	switch u.Scheme {
	case "http", "https", "ws", "wss":
	default:
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("target URL is missing host")
	}
	pc := &ProxyClient{target: u, DialTimeout: 5 * time.Second}
	c, err := newClient(cfg, pc)
	if err != nil {
		return nil, err
	}
	pc.Client = c
	return pc, nil
}

func (pc *ProxyClient) Serve(stream net.Conn) {
	defer stream.Close()
	br := bufio.NewReader(stream)
	req, err := http.ReadRequest(br)
	if err != nil {
		if err != io.EOF {
			pc.log.Printf("proxy: read request: %v", err)
		}
		return
	}

	addr := pc.target.Host
	if !strings.Contains(addr, ":") {
		addr += ":" + defaultPort(pc.target.Scheme)
	}

	var conn net.Conn
	useTLS := pc.target.Scheme == "https" || pc.target.Scheme == "wss"
	d := net.Dialer{Timeout: pc.DialTimeout}
	if useTLS {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, pc.TLSConfig)
	} else {
		conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		pc.log.Printf("proxy: dial %s: %v", addr, err)
		writeBadGateway(stream)
		return
	}
	defer conn.Close()

	if pc.HostRewrite {
		req.Host = pc.target.Host
	}
	// We use one stream per request, no keep-alive needed on the upstream.
	req.Header.Set("Connection", "close")
	req.Close = true

	if err := req.Write(conn); err != nil {
		pc.log.Printf("proxy: write upstream: %v", err)
		return
	}

	// public(stream) → upstream: any leftover from bufio + everything else.
	go func() {
		defer func() {
			if cw, ok := conn.(closeWriter); ok {
				_ = cw.CloseWrite()
			}
		}()
		if buffered := br.Buffered(); buffered > 0 {
			peek, _ := br.Peek(buffered)
			if len(peek) > 0 {
				if _, err := conn.Write(peek); err != nil {
					return
				}
				_, _ = br.Discard(len(peek))
			}
		}
		_, _ = io.Copy(conn, stream)
	}()
	// upstream → public(stream): response (and any frames after upgrade).
	_, _ = io.Copy(stream, conn)
}

type closeWriter interface{ CloseWrite() error }

func defaultPort(scheme string) string {
	switch scheme {
	case "https", "wss":
		return "443"
	default:
		return "80"
	}
}

func writeBadGateway(w io.Writer) {
	const body = "bad gateway: upstream unreachable\n"
	fmt.Fprintf(w, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: %d\r\nContent-Type: text/plain; charset=utf-8\r\nConnection: close\r\n\r\n%s",
		len(body), body)
}
