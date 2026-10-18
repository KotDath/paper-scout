package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"paper-scout/internal/deepseek"
	"paper-scout/internal/documents"
	"paper-scout/internal/research"
	"paper-scout/internal/store"
)

// Exercise bounded preparation through production HTTP ingestion, SQLite and
// the real provider client. Allocation accounting includes document reload,
// request serialization, HTTP, result/evidence creation and persistence.
func largeDocuments(ctx context.Context, directory string, db *store.Store) error {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		textBytes := 1 << 20
		if strings.HasSuffix(r.URL.Path, "2602.00002v1") {
			textBytes = 4 << 20
		}
		paragraph := strings.Repeat("Memory consolidation preserves source evidence. ", textBytes/47+1)
		fmt.Fprint(w, `<article><h2>Methods</h2><p>`+paragraph+`</p><p>Results retain original coordinates.</p><p>Limitations remain explicit.</p></article>`)
	}))
	defer source.Close()
	target, _ := url.Parse(source.URL)
	docs, err := documents.New(db.DB, documents.Config{CacheDir: filepath.Join(directory, "large-cache"), HTTPClient: &http.Client{Transport: sourceTransport{target}}})
	if err != nil {
		return err
	}
	provider := &providerFixture{}
	server := httptest.NewServer(provider)
	defer server.Close()
	client, err := deepseek.New(deepseek.Config{BaseURL: server.URL, Model: "fixture", APIKey: "fixture"})
	if err != nil {
		return err
	}
	analyzer, err := research.NewAnalyzer(db, docs, research.AnalyzerConfig{MaxSegmentRunes: 100000, MaxRequests: 1, OutputTokens: 512})
	if err != nil {
		return err
	}
	for i, size := range []int{1 << 20, 4 << 20} {
		id := fmt.Sprintf("2602.0000%dv1", i+1)
		if err = register(ctx, db, id); err != nil {
			return err
		}
		doc, err := docs.Import(ctx, documents.Source{PaperVersionID: id, ArxivID: id})
		if err != nil {
			return err
		}
		if doc.ExtractionStatus != "full_text" || len(doc.Text) < size || len(doc.Chunks) < 500 {
			return errors.New("large HTTP source ingestion not confirmed")
		}
		for _, scenario := range []struct{ cancel, tailGap bool }{{false, false}, {true, false}, {false, true}} {
			cancelAfterResponse := scenario.cancel
			if scenario.tailGap {
				// An unread tail gap must not be visited while preparing the
				// one allowed prefix. This detects eager whole-document work
				// independently of allocation thresholds or wall-clock speed.
				if _, err = db.DB.ExecContext(ctx, "DELETE FROM chunks WHERE id=?", doc.Chunks[len(doc.Chunks)-1].ID); err != nil {
					return err
				}
			}
			provider.reset("success")
			r, err := run(ctx, db)
			if err != nil {
				return err
			}
			if err = analyzer.SelectSource(ctx, "alice", r.ID, doc.PaperVersionID, "large document resource check"); err != nil {
				return err
			}
			callCtx, cancel := context.WithCancel(ctx)
			g := &gateway{client: client, maxRequests: 32, maxTokens: 250000}
			complete := g.complete
			if cancelAfterResponse {
				complete = func(c context.Context, request deepseek.Request) (deepseek.Response, error) {
					response, callErr := g.complete(c, request)
					cancel() // Real context cancellation before a later packet can be prepared.
					return response, callErr
				}
			}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			started := time.Now()
			out, callErr := analyzer.Analyze(callCtx, "alice", r.ID, doc.ID, "Methods and limitations?", complete)
			elapsed := time.Since(started)
			runtime.ReadMemStats(&after)
			cancel()
			allocated := after.TotalAlloc - before.TotalAlloc
			// Broad linear ceiling allows SQL/driver/runtime overhead, while rejecting
			// the previous repeated whole-document rune copies (gigabytes at 1 MiB).
			ceiling := uint64(128*len(doc.Text) + (16 << 20))
			if allocated > ceiling {
				return fmt.Errorf("large analysis allocated %d bytes for %d source bytes (limit %d)", allocated, len(doc.Text), ceiling)
			}
			code := "budget_exceeded"
			covered := 100000
			if cancelAfterResponse {
				code = "cancelled"
				covered = 0
			}
			if callErr == nil || out.Status != "partial" || out.ErrorCode != code || out.CoveredRunes != covered || g.requests != 1 {
				return fmt.Errorf("large analysis status=%s code=%s coverage=%d requests=%d err=%v", out.Status, out.ErrorCode, out.CoveredRunes, g.requests, callErr)
			}
			if cancelAfterResponse && !errors.Is(callErr, context.Canceled) {
				return errors.New("large analysis lost context cancellation")
			}
			got, err := analyzer.Analysis(ctx, "alice", out.ID)
			if err != nil || got.Status != out.Status || got.CoveredRunes != out.CoveredRunes || got.ErrorCode != out.ErrorCode {
				return errors.New("large partial analysis was not persisted")
			}
			if err = verifyTrace(ctx, db, doc, out); err != nil {
				return err
			}
			var steps int
			if err = db.DB.QueryRowContext(ctx, "SELECT count(*) FROM analysis_segments WHERE analysis_id=?", out.ID).Scan(&steps); err != nil {
				return err
			}
			if steps != 1 {
				return errors.New("large preparation exceeded its one-request window")
			}
			if _, err = db.FinishRun(ctx, "alice", r.ID, "partial", code, nil); err != nil {
				return err
			}
			fmt.Printf("PASS: large HTTP source bytes=%d chunks=%d MaxRequests=1 cancelled=%t unread_tail_gap=%t provider_requests=%d covered=%d allocated_bytes=%d ceiling=%d elapsed=%s; exact prefix/evidence and durable partial verified\n", len(doc.Text), len(doc.Chunks), cancelAfterResponse, scenario.tailGap, g.requests, out.CoveredRunes, allocated, ceiling, elapsed.Round(time.Millisecond))
		}
	}
	return nil
}
