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

	"paper-scout/internal/agent"
	"paper-scout/internal/arxiv"
	"paper-scout/internal/auth"
	"paper-scout/internal/config"
	"paper-scout/internal/deepseek"
	"paper-scout/internal/documents"
	"paper-scout/internal/embeddings"
	"paper-scout/internal/httpapi"
	"paper-scout/internal/research"
	"paper-scout/internal/researchapp"
	"paper-scout/internal/researchmemory"
	"paper-scout/internal/researchtools"
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
	recovery, finishRecovery := context.WithTimeout(ctx, 15*time.Second)
	err = db.RecoverRuns(recovery)
	finishRecovery()
	if err != nil {
		return err
	}
	runs := research.New(ctx, db)
	defer func() { runs.Close(); runs.Wait() }()
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
	catalog, err := arxiv.New(db.DB, "")
	if err != nil {
		return err
	}
	docs, err := documents.New(db.DB, documents.Config{CacheDir: cfg.CacheDir})
	if err != nil {
		return err
	}
	analyzer, err := research.NewAnalyzer(db, docs, research.AnalyzerConfig{})
	if err != nil {
		return err
	}
	registry, err := researchtools.New(db, catalog, docs, retrievalService, analyzer)
	if err != nil {
		return err
	}
	provider, providerErr := deepseek.New(deepseek.ConfigFromEnv())
	if providerErr != nil {
		runs.SetExecutor(func(ctx context.Context, r store.Run) {
			if _, _, err := runs.Start(ctx, r.UserID, r.ID); err == nil {
				persist, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, _ = runs.Finish(persist, r.UserID, r.ID, "failed", "provider_unavailable", nil)
			}
		})
	} else {
		runner, err := agent.New(db, runs, provider, registry, agent.DefaultLimits())
		if err != nil {
			return err
		}
		if cfg.ResearchMemoryEnabled {
			researchapp.Attach(db, runs, runner, retrievalService, researchmemory.New(db.DB, embeddingClient))
		} else {
			researchapp.Attach(db, runs, runner, retrievalService)
		}
	}
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{
		Handler: httpapi.NewWithRuns(db, runs, retrievalService), ReadHeaderTimeout: 5 * time.Second,
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
