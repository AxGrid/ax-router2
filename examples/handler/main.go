// Example: Handler-mode client. Serves an http.Handler through the router.
//
// Run with:
//   AXR_SERVER=router.example.com:7000 AXR_TOKEN=secret-foo \
//     go run ./examples/handler
//
// Then any request to *.foo.router.example.com lands here.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/axgrid/ax-router2/client"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from handler-mode client\nyou hit Host=%q Path=%q\n", r.Host, r.URL.Path)
	})
	mux.HandleFunc("/headers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		for k, vv := range r.Header {
			for _, v := range vv {
				fmt.Fprintf(w, "%s: %s\n", k, v)
			}
		}
	})

	c, err := client.NewHandler(client.Config{
		ServerAddr: env("AXR_SERVER", "localhost:7000"),
		Token:      env("AXR_TOKEN", ""),
		Service:    env("AXR_SERVICE", ""), // required if AXR_TOKEN is a wildcard
	}, mux)
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
