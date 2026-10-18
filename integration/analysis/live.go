package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"paper-scout/internal/arxiv"
	"paper-scout/internal/config"
	"paper-scout/internal/deepseek"
	"paper-scout/internal/documents"
	"paper-scout/internal/research"
	"paper-scout/internal/store"
)

func liveCheck(runtime, env, paperID string, segmentRunes int) error {
	if _, err := config.Load(env); err != nil {
		return err
	}
	cfg := deepseek.ConfigFromEnv()
	if cfg.BaseURL != "" && strings.TrimRight(cfg.BaseURL, "/") != "https://api.deepseek.com" && strings.TrimRight(cfg.BaseURL, "/") != "https://api.deepseek.com/v1" {
		return errors.New("live requires official DeepSeek HTTPS endpoint")
	}
	if cfg.Thinking != "" && cfg.Thinking != "disabled" {
		return errors.New("bounded live check requires thinking disabled")
	}
	client, err := deepseek.New(cfg)
	if err != nil {
		return err
	}
	fmt.Println("PASS: live provider configuration/credentials present; values withheld")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	path := filepath.Join(runtime, "live.sqlite")
	db, err := store.Open(ctx, path)
	if err != nil {
		return err
	}
	defer func() { db.Close() }()
	if err = seed(ctx, db); err != nil {
		return err
	}
	catalog, err := arxiv.New(db.DB, "")
	if err != nil {
		return err
	}
	metadata, err := catalog.Get(ctx, paperID)
	if err != nil {
		return err
	}
	if len(metadata.Papers) != 1 {
		return errors.New("live metadata missing")
	}
	paper := metadata.Papers[0]
	fmt.Printf("LIVE SOURCE: metadata fetched attempts=%d cache_hit=%t\n", metadata.Attempts, metadata.CacheHit)
	docs, err := documents.New(db.DB, documents.Config{CacheDir: filepath.Join(runtime, "cache")})
	if err != nil {
		return err
	}
	doc, err := docs.Import(ctx, documents.Source{PaperVersionID: paper.ID, ArxivID: fmt.Sprintf("%sv%d", paper.ArxivID, paper.Version), Abstract: paper.Abstract})
	if err != nil {
		return err
	}
	if doc.ExtractionStatus != "full_text" || doc.Cached {
		return fmt.Errorf("fresh live full-text fetch not confirmed: extraction=%s cached=%t warnings=%v", doc.ExtractionStatus, doc.Cached, doc.Warnings)
	}
	fmt.Printf("LIVE SOURCE: fresh text format=%s runes=%d warnings=%v\n", doc.Format, len([]rune(doc.Text)), doc.Warnings)
	analyzer, err := research.NewAnalyzer(db, docs, research.AnalyzerConfig{MaxSegmentRunes: segmentRunes, MaxRequests: 8, OutputTokens: 1536})
	if err != nil {
		return err
	}
	r, err := run(ctx, db)
	if err != nil {
		return err
	}
	if err = analyzer.SelectSource(ctx, "alice", r.ID, paper.ID, "Live complete reading of a memory-agent article"); err != nil {
		return err
	}
	g := &gateway{client: client, maxRequests: 8, maxTokens: 180000}
	out, callErr := analyzer.Analyze(ctx, "alice", r.ID, doc.ID, "Какие механизмы и методы предлагают авторы, как оценивают их результаты и какие ограничения признают? Если обсуждается память AI-агентов, отдельно укажи memory, reflection и планирование.", g.complete)
	// This local artifact contains public observations/evidence, never raw provider
	// response, credentials, or private reasoning. The launcher removes its temp dir.
	record, _ := json.MarshalIndent(out, "", "  ")
	if err = os.WriteFile(filepath.Join(runtime, "live-analysis.json"), record, 0600); err != nil {
		return err
	}
	fmt.Printf("LIVE: article=%s title=%s model=%s format=%s hash=%s runes=%d covered=%d parts=%d requests=%d tokens=%d unknown_usage=%d status=%s error=%s\n", doc.SourceURL, paper.Title, cfg.Model, doc.Format, doc.ContentHash, out.TotalRunes, out.CoveredRunes, len(out.SuccessfulRanges), g.requests, g.tokens, g.unknown, out.Status, out.ErrorCode)
	if callErr != nil {
		return callErr
	}
	if out.Status != "complete" || len(out.SuccessfulRanges) < 2 || out.CoveredRunes != out.TotalRunes || out.Synthesis == nil {
		return errors.New("live full sequential analysis not confirmed")
	}
	if err = verifyTrace(ctx, db, doc, out); err != nil {
		return err
	}
	fmt.Printf("LIVE: ranges=%v warnings=%v\n", out.SuccessfulRanges, out.Warnings)
	// Emit a small set of public claims with passages for manual semantic review.
	groups := [][]research.Claim{out.Synthesis.Methods, out.Synthesis.Results, out.Synthesis.Limitations}
	for _, claims := range groups {
		if len(claims) == 0 {
			return errors.New("live synthesis missing methods/results/limitations")
		}
		claim := claims[0]
		fmt.Printf("LIVE CLAIM: %s evidence=%v\n", claim.Text, claim.EvidenceIDs)
		for _, ev := range out.Evidence {
			if ev.ID == claim.EvidenceIDs[0] {
				fmt.Printf("LIVE EVIDENCE: section=%s page=%d url=%s text=%s\n", ev.Section, ev.PageStart, ev.SourceURL, ev.Text)
				break
			}
		}
	}
	if _, err = db.FinishRun(ctx, "alice", r.ID, "completed", "", nil); err != nil {
		return err
	}
	if err = db.Close(); err != nil {
		return err
	}
	db, err = store.Open(ctx, path)
	if err != nil {
		return err
	}
	docs, err = documents.New(db.DB, documents.Config{CacheDir: filepath.Join(runtime, "cache")})
	if err != nil {
		return err
	}
	analyzer, err = research.NewAnalyzer(db, docs, research.AnalyzerConfig{})
	if err != nil {
		return err
	}
	got, err := analyzer.Analysis(ctx, "alice", out.ID)
	if err != nil {
		return err
	}
	if got.Status != "complete" || got.CoveredRunes != out.CoveredRunes || len(got.Evidence) != len(out.Evidence) {
		return errors.New("live durable result changed after reopen")
	}
	fmt.Println("PASS: real arXiv metadata/fresh full text → real DeepSeek sequential analysis and synthesis → original evidence → SQLite reopen; no retrieval was used or claimed as reading")
	return nil
}
