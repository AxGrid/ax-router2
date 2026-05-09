// Example: a small WebSocket broadcaster behind ax-router2.
//
// What it shows:
//   * Plain HTTP route ("/") served alongside a WebSocket route ("/ws").
//   * Real WS handling via gorilla/websocket — works through ax-router2 because
//     the client library exposes http.Hijacker on its synthetic ResponseWriter.
//   * The same handler can simultaneously serve a local listener AND ax-router2,
//     which is the realistic "drop into existing project" pattern: keep your
//     local dev server, expose the same mux to the public router.
//
// Run:
//
//	AXR_SERVER=router.example.com:7000 \
//	AXR_TOKEN=globalsecret               \
//	AXR_SERVICE=chat                     \
//	go run ./examples/websocket
//
// Test locally (without the router) by hitting :8080 directly.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/axgrid/ax-router2/client"
)

func main() {
	hub := newHub()
	go hub.run()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "websocket-broadcaster · host=%s · %d connected client(s)\n",
			r.Host, hub.size())
	})
	mux.HandleFunc("/ws", hub.serveWS)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Local listener — handy for development without going through the router.
	if addr := env("LOCAL_ADDR", ":8080"); addr != "" {
		go func() {
			log.Printf("local listener on %s (try: ws://localhost%s/ws)", addr, addr)
			srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("local: %v", err)
			}
		}()
	}

	// Router-side: the SAME mux is exposed under <SERVICE>.<router>.
	c, err := client.NewHandler(client.Config{
		ServerAddr: env("AXR_SERVER", "localhost:7000"),
		Token:      env("AXR_TOKEN", ""),
		Service:    env("AXR_SERVICE", ""), // required for wildcard tokens
	}, mux)
	if err != nil {
		log.Fatal(err)
	}
	if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

// --- WebSocket hub ----------------------------------------------------------

type hub struct {
	mu      sync.RWMutex
	clients map[*wsClient]struct{}

	register   chan *wsClient
	unregister chan *wsClient
	broadcast  chan []byte
}

type wsClient struct {
	conn *websocket.Conn
	out  chan []byte
}

func newHub() *hub {
	return &hub{
		clients:    map[*wsClient]struct{}{},
		register:   make(chan *wsClient),
		unregister: make(chan *wsClient),
		broadcast:  make(chan []byte, 64),
	}
}

func (h *hub) run() {
	for {
		select {
		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = struct{}{}
			h.mu.Unlock()
		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.out)
			}
			h.mu.Unlock()
		case msg := <-h.broadcast:
			h.mu.RLock()
			for c := range h.clients {
				select {
				case c.out <- msg:
				default: // drop on slow client
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (h *hub) size() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(*http.Request) bool { return true },
}

func (h *hub) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &wsClient{conn: conn, out: make(chan []byte, 16)}
	h.register <- c

	go h.writePump(c)
	h.readPump(c)
}

func (h *hub) readPump(c *wsClient) {
	defer func() {
		h.unregister <- c
		_ = c.conn.Close()
	}()
	c.conn.SetReadLimit(64 << 10)
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		h.broadcast <- msg
	}
}

func (h *hub) writePump(c *wsClient) {
	ping := time.NewTicker(30 * time.Second)
	defer func() {
		ping.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.out:
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ping.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
