package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/axgrid/ax-router2/server"
)

// Build identification — overridden at link time via:
//
//	-ldflags="-X main.version=$VERSION -X main.build=$BUILD -X main.commit=$COMMIT"
var (
	version = "dev"
	build   = "0"
	commit  = ""
)

// versionLine is the canonical one-liner shown by -version and at startup.
//
//	"ax-router 0.1.1 build 42 (a3f9c1) linux/amd64"
func versionLine() string {
	out := fmt.Sprintf("ax-router %s build %s", version, build)
	if commit != "" {
		out += fmt.Sprintf(" (%s)", commit)
	}
	out += fmt.Sprintf(" %s/%s", runtime.GOOS, runtime.GOARCH)
	return out
}

func main() {
	envFile := flag.String("env", ".env", "path to .env file (optional)")
	showVersion := flag.Bool("version", false, "print version + build info and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(versionLine())
		return
	}

	if _, err := os.Stat(*envFile); err == nil {
		// A malformed .env must not take the whole service into supervisor's
		// FATAL state, so we don't os.Exit here — the ambient environment
		// (e.g. supervisor's `environment=`) may already carry the config, and
		// LoadConfig below fails cleanly if a required value is truly missing.
		//
		// We deliberately do NOT print the godotenv error: on a parse failure
		// it echoes the offending line, which for this file is the
		// secret-bearing AXR_TOKENS map. Emit a clean, actionable hint instead.
		if err := godotenv.Load(*envFile); err != nil {
			log.Printf("WARNING: %s is malformed and was not loaded "+
				"(it must be plain KEY=value lines, not a quoted/escaped blob); "+
				"falling back to the ambient environment", *envFile)
		}
	}

	cfg, err := server.LoadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("init: %v", err)
	}

	log.Print(versionLine())

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := srv.Run(ctx); err != nil {
		log.Fatalf("run: %v", err)
	}
}
