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

// version is overridden at build time via -ldflags="-X main.version=...".
var version = "dev"

func main() {
	envFile := flag.String("env", ".env", "path to .env file (optional)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("ax-router %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
		return
	}

	if _, err := os.Stat(*envFile); err == nil {
		if err := godotenv.Load(*envFile); err != nil {
			log.Fatalf("load %s: %v", *envFile, err)
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

	log.Printf("ax-router %s %s/%s", version, runtime.GOOS, runtime.GOARCH)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := srv.Run(ctx); err != nil {
		log.Fatalf("run: %v", err)
	}
}
