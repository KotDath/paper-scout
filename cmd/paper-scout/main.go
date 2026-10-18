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

	"paper-scout/internal/auth"
	"paper-scout/internal/config"
	"paper-scout/internal/embeddings"
	"paper-scout/internal/httpapi"
	"paper-scout/internal/retrieval"
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
	seed := flag.Bool("seed", false, "create missing configured accounts, then exit")
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
	if *seed {
		seedCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := auth.New(db).SeedFromEnvironment(seedCtx); err != nil {
			return err
		}
		slog.Info("account seed succeeded")
		return nil
	}
	if err := os.MkdirAll(cfg.CacheDir, 0700); err != nil {
		return err
	}
	embeddingConfig, err := embeddings.ConfigFromEnv()
	if err != nil {
		return err
	}
	preflight, cancelEmbedding := context.WithTimeout(ctx, 10*time.Second)
	embeddingClient, embeddingErr := embeddings.New(preflight, embeddingConfig)
	cancelEmbedding()
	if embeddingErr != nil {
		// Dependency failures permit explicit lexical-only reads. Recreate the client
		// after restoring Ollama; no secrets or provider response bodies are logged.
		slog.Warn("embedding preflight failed; retrieval is lexical_only until restart")
	}
	retrievalService := retrieval.New(db.DB, embeddingClient)
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{
		Handler: httpapi.New(db, retrievalService), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 130 * time.Second,
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
