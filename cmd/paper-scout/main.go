package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"paper-scout/internal/config"
	"paper-scout/internal/httpapi"
	"paper-scout/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	envFile := flag.String("env-file", "", "optional dotenv path; environment takes precedence")
	flag.Parse()
	cfg, err := config.Load(*envFile)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	db, err := store.Open(startup, cfg.SQLitePath)
	cancel()
	if err != nil {
		return err
	}
	defer db.Close()
	if err := os.MkdirAll(cfg.CacheDir, 0700); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{
		Handler: httpapi.New(db), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	slog.Info("server listening", "addr", listener.Addr().String())
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
			return err
		}
	}
	return nil
}
