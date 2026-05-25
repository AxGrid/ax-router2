// Command axr exposes a local TCP service through an ax-router2 server.
//
// It is a thin CLI on top of client.NewProxy: it dials the router's control
// port, registers (using a token + optional service name), and forwards every
// inbound request to a local target. Works for both HTTP and WebSocket.
//
// Usage:
//
//	axr [flags] <target>
//
// <target> is one of:
//   - a port number          → http://localhost:<port>
//   - host:port              → http://host:port
//   - a full URL             → http(s)://… or ws(s)://…
//
// Flags (each also reads an AXR_… env var):
//
//	-server     router control addr (AXR_SERVER, default localhost:7000)
//	-token      auth token         (AXR_TOKEN, required)
//	-service    desired service    (AXR_SERVICE, required for wildcard tokens)
//	-host-rewrite  rewrite Host: to the target's host (default: keep public Host)
//	-env        path to .env file to load before reading env (optional)
//	-version    print version + exit
//
// Examples:
//
//	axr -token=secret -service=foo 8080
//	axr -server=router.example.com:7000 -token=secret -service=api localhost:9000
//	axr -token=secret -service=ws ws://localhost:8080
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/axgrid/ax-router2/client"
)

// Build identification — overridden at link time via:
//
//	-ldflags="-X main.version=$VERSION -X main.build=$BUILD -X main.commit=$COMMIT"
var (
	version = "dev"
	build   = "0"
	commit  = ""
)

func versionLine() string {
	out := fmt.Sprintf("axr %s build %s", version, build)
	if commit != "" {
		out += fmt.Sprintf(" (%s)", commit)
	}
	out += fmt.Sprintf(" %s/%s", runtime.GOOS, runtime.GOARCH)
	return out
}

func main() {
	var (
		server      = flag.String("server", envOr("AXR_SERVER", "localhost:7000"), "router control address host:port (AXR_SERVER)")
		token       = flag.String("token", os.Getenv("AXR_TOKEN"), "auth token (AXR_TOKEN)")
		service     = flag.String("service", os.Getenv("AXR_SERVICE"), "desired service name; required for wildcard tokens (AXR_SERVICE)")
		hostRewrite = flag.Bool("host-rewrite", false, "rewrite Host: header to the target's host (default: keep the public Host)")
		envFile     = flag.String("env", "", "load this .env file before reading env vars (optional)")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println(versionLine())
		return
	}

	if *envFile != "" {
		if err := godotenv.Load(*envFile); err != nil {
			log.Fatalf("load %s: %v", *envFile, err)
		}
		// Re-resolve env-defaulted flags so values from the .env file win
		// over the empty defaults captured at flag-parse time.
		if !flagPassed("server") {
			*server = envOr("AXR_SERVER", "localhost:7000")
		}
		if !flagPassed("token") {
			*token = os.Getenv("AXR_TOKEN")
		}
		if !flagPassed("service") {
			*service = os.Getenv("AXR_SERVICE")
		}
	}

	args := flag.Args()
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "axr: exactly one target argument is required")
		usage()
		os.Exit(2)
	}
	target, err := normalizeTarget(args[0])
	if err != nil {
		log.Fatalf("axr: target %q: %v", args[0], err)
	}

	if *token == "" {
		log.Fatal("axr: -token (or AXR_TOKEN) is required")
	}

	c, err := client.NewProxy(client.Config{
		ServerAddr: *server,
		Token:      *token,
		Service:    *service,
	}, target)
	if err != nil {
		log.Fatalf("axr: %v", err)
	}
	c.HostRewrite = *hostRewrite

	log.Print(versionLine())
	svcLabel := *service
	if svcLabel == "" {
		svcLabel = "(token-bound)"
	}
	log.Printf("axr: forwarding %s ⇄ %s [service=%s]", *server, target, svcLabel)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := c.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("axr: %v", err)
	}
}

// normalizeTarget accepts a port, host:port, or full URL and returns a URL
// suitable for client.NewProxy. Bare host:port and bare ports default to
// http://.
func normalizeTarget(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("empty target")
	}
	// Already a URL?
	for _, p := range []string{"http://", "https://", "ws://", "wss://"} {
		if strings.HasPrefix(s, p) {
			return s, nil
		}
	}
	// Bare integer port → http://localhost:<port>
	if n, err := strconv.Atoi(s); err == nil {
		if n < 1 || n > 65535 {
			return "", fmt.Errorf("port out of range")
		}
		return fmt.Sprintf("http://localhost:%d", n), nil
	}
	// Otherwise treat as host[:port] and assume http.
	return "http://" + s, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// flagPassed reports whether a flag was explicitly set on the CLI (as
// opposed to taking its default — including a default sourced from env).
func flagPassed(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func usage() {
	fmt.Fprintf(os.Stderr, `axr — expose a local service through an ax-router2 server

Usage:
  axr [flags] <target>

<target>:
  8080                       short for http://localhost:8080
  localhost:9000             short for http://localhost:9000
  http://localhost:8080      explicit HTTP target
  ws://localhost:8080        explicit WebSocket target

Flags:
`)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
Examples:
  axr -token=secret -service=foo 8080
  axr -server=router.example.com:7000 -token=secret -service=api localhost:9000
  AXR_SERVER=router.com:7000 AXR_TOKEN=secret AXR_SERVICE=foo axr 8080
`)
}
