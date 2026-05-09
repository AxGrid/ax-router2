---
name: ax-router-client
description: Use this skill when the user wants to expose a Go service through an ax-router2 reverse router — either by generating a fresh client (handler-mode or proxy-mode) or by integrating the client into an existing Go project. Triggers on phrases like "expose this service via ax-router", "add ax-router client", "publish this through router.example.com", "wire up ax-router2", "tunnel this Go server through the router".
---

# ax-router-client

You are creating a Go client for the **ax-router2** reverse HTTP/WS router
(`github.com/axgrid/ax-router2`). The router is a public server; this skill
generates the *client* that registers a local service with it. Two modes
are supported:

| Mode | API | When to use |
|---|---|---|
| Handler | `client.NewHandler(cfg, http.Handler)` | The user owns the `http.Handler` (mux, chi, gin). The library reads each forwarded request, runs the handler, writes the response back. WebSocket via `http.Hijacker`. |
| Proxy | `client.NewProxy(cfg, "http://localhost:8080")` | An existing local HTTP server is already listening. The client byte-forwards each request — no app changes needed. Works for HTTP and WebSocket alike. |

## Workflow

Follow these steps in order. Skip any step that the user already answered
in their initial request — don't re-ask.

### 1 · Gather parameters

Use `AskUserQuestion` with these questions (drop ones already answered):

1. **Mode** (Handler / Proxy). Recommend the answer that matches the
   project state: if there's a clear `http.Handler` or `*http.ServeMux`
   already in the codebase, recommend Handler; if there's a separate HTTP
   server already running on a known port, recommend Proxy.
2. **Router server address** (`host:port`, e.g. `router.example.com:7000`).
   This is the **control** TCP port (default 7000), not 80/443.
3. **Token** — the credential issued by the router operator.
4. **Service name** — subdomain label under the router's base domain.
   Required when the token is a wildcard (`*`); ignored when bound.
   Validate against `^[a-z0-9][a-z0-9-]{0,30}$` before continuing.
5. **(Proxy mode only) Target URL** — `http://localhost:8080` /
   `https://internal.svc:8443` / `ws://…`. Both schemes work; the client
   chooses TLS based on `https://` or `wss://`.

Allow the user to source any of these from environment variables instead
of hard-coding — `os.Getenv("AXR_TOKEN")` etc. is the recommended pattern.

### 2 · Locate where the code goes

Decide one of:

* **New standalone client.** Create a new directory (default
  `cmd/router-client/` if the project uses `cmd/`-style layout, otherwise
  `./router-client/`). Add a fresh `main.go` and a `go.mod` if missing.
* **Add to existing main.** If the project already has a `main` that runs
  an `http.Server`, integrate alongside: keep the existing server, start
  the ax-router2 client in a goroutine, share the same `context.Context`
  for cancellation. Do **not** replace the existing local listener — many
  users want both.
* **Add to an existing package.** If the user points to a specific file,
  insert the client wiring there.

If unsure which layout applies, ask the user with two clear options
("standalone binary" vs "integrate with existing main").

### 3 · Add the dependency

```sh
go get github.com/axgrid/ax-router2/client@latest
```

If `go.mod` doesn't exist, run `go mod init <module-path>` first; ask the
user for the module path if it's not obvious from the directory name or
existing repo metadata.

### 4 · Generate the code

Use one of the templates below. Always:

* Wire `signal.NotifyContext(…, os.Interrupt, syscall.SIGTERM)` so
  `Run(ctx)` exits cleanly on Ctrl-C / SIGTERM.
* Source secrets from environment by default; never hard-code the token.
* Use `log.Fatal` for fatal init errors; log non-fatal reconnect events
  through the library's default logger.

#### Handler mode

```go
package main

import (
	"context"
	"errors"
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
	// TODO: replace with the real handlers / mux from the project.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from %s\n", r.Host)
	})

	c, err := client.NewHandler(client.Config{
		ServerAddr: env("AXR_SERVER", "<router-host>:7000"),
		Token:      mustEnv("AXR_TOKEN"),
		Service:    env("AXR_SERVICE", "<service-name>"),
	}, mux)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("env %s is required", k)
	}
	return v
}
```

#### Proxy mode

```go
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/axgrid/ax-router2/client"
)

func main() {
	c, err := client.NewProxy(client.Config{
		ServerAddr: env("AXR_SERVER", "<router-host>:7000"),
		Token:      mustEnv("AXR_TOKEN"),
		Service:    env("AXR_SERVICE", "<service-name>"),
	}, env("AXR_TARGET", "http://localhost:8080"))
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("env %s is required", k)
	}
	return v
}
```

#### Integration into an existing main

When grafting onto a project that already calls `http.ListenAndServe` (or
similar):

* Keep the existing server. Make sure the same `http.Handler` is reachable
  from a variable (extract it if it's currently inline).
* Run the router client alongside in a goroutine; share the `ctx`.
* If the project is small enough to lose its existing local server, ask
  the user before removing it.

```go
ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer cancel()

// Existing local listener stays.
go func() {
	srv := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("local: %v", err)
	}
}()

// New: same mux exposed through ax-router2.
rc, err := client.NewHandler(client.Config{
	ServerAddr: env("AXR_SERVER", "router.example.com:7000"),
	Token:      mustEnv("AXR_TOKEN"),
	Service:    env("AXR_SERVICE", "<service-name>"),
}, mux)
if err != nil {
	log.Fatal(err)
}
if err := rc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
	log.Fatal(err)
}
```

### 5 · Tidy & verify

After writing the file(s):

1. `go mod tidy` — pulls the dependency, updates `go.sum`.
2. `go build ./...` — confirm it compiles.
3. **Do not run** the binary yourself; the user has the credentials and
   their own runtime context. Print the exact command they should run:

   ```sh
   AXR_SERVER=<host>:7000 AXR_TOKEN=<token> AXR_SERVICE=<name> \
     go run ./cmd/router-client
   ```

### 6 · Tell the user how to verify it worked

Once running, the service is reachable at `https://<service>.<router-base>`
and any subdomain underneath. The router operator's dashboard at the apex
(e.g. `router.example.com`) will show the new connection within ~1 second.

## Reference: full Config

```go
type Config struct {
	ServerAddr   string        // host:port of the router's control TCP listener
	Token        string        // credential
	Service      string        // desired service name (required for wildcard tokens)
	ReconnectMin time.Duration // backoff floor on reconnect; default 500ms
	ReconnectMax time.Duration // backoff ceiling on reconnect; default 15s
	Logger       *log.Logger   // optional; default log.Default()
}
```

The library handles dial-and-handshake, yamux multiplexing, automatic
reconnect with exponential backoff, and per-stream HTTP serving. The user
only configures the four fields above and provides either an
`http.Handler` or a target URL.

## Edge cases to handle

* **TLS upstream in proxy mode.** If target URL is `https://…` or `wss://…`,
  the client uses `tls.Dial`. Ask whether the upstream cert needs to skip
  verification (lab/dev) and, if yes, configure `ProxyClient.TLSConfig`
  with `InsecureSkipVerify: true` after construction.
* **WebSocket in handler mode.** No special wiring needed — the synthetic
  `ResponseWriter` already supports `http.Hijacker`. `gorilla/websocket`,
  `nhooyr.io/websocket`, and `coder.com/websocket` all work.
* **Service name collisions.** Last-writer-wins on the server; an old
  client is closed when a new one with the same service name connects.
  No client-side handling required.
* **Token rotation.** The control connection re-handshakes on every
  reconnect, so updating `AXR_TOKEN` via env + restart is enough.

## Out of scope for this skill

* Running or operating the **server** side of ax-router2. If the user
  asks about server config, point them at `.env.example` in the
  `github.com/axgrid/ax-router2` repository and stop.
* Generating a non-Go client. There is no client library for other
  languages yet; if asked, suggest `client.NewProxy` from a thin Go
  wrapper as a workaround.
