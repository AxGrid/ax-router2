package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/axgrid/ax-router2/server"
)

func main() {
	envFile := flag.String("env", ".env", "path to .env file (optional)")
	flag.Parse()

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

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := srv.Run(ctx); err != nil {
		log.Fatalf("run: %v", err)
	}
}
