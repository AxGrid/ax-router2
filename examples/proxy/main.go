// Example: Proxy-mode client. Forwards every router request to a local
// service (HTTP or WebSocket).
//
// Run with:
//   AXR_SERVER=router.example.com:7000 AXR_TOKEN=secret-foo \
//   AXR_TARGET=http://localhost:8080 \
//     go run ./examples/proxy
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/axgrid/ax-router2/client"
)

func main() {
	c, err := client.NewProxy(client.Config{
		ServerAddr: env("AXR_SERVER", "localhost:7000"),
		Token:      env("AXR_TOKEN", ""),
		Service:    env("AXR_SERVICE", ""), // required if AXR_TOKEN is a wildcard
	}, env("AXR_TARGET", "http://localhost:8080"))
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := c.Run(ctx); err != nil && err != context.Canceled {
		log.Fatal(err)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
