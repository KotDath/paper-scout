// T21: production Dart/Flutter -> real Go HTTP/auth/memory/SQLite/Qwen3.
// Only report_b's optional T20 snapshot is an explicitly labelled HTTP fixture.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/crypto/bcrypt"
	"paper-scout/internal/embeddings"
	"paper-scout/internal/httpapi"
	"paper-scout/internal/researchmemory"
	"paper-scout/internal/retrieval"
	"paper-scout/internal/store"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func mustJSON(v any) string { b, err := json.Marshal(v); must(err); return string(b) }

func main() {
	dir := flag.String("runtime", "", "temporary directory in T21 worktree")
	ui := flag.Bool("ui", false, "serve for claude-in-mobile instead of running Dart")
	video := flag.Bool("video-fallbacks", false, "explicit T22 synthetic fallback scenes; requires --ui")
	flag.Parse()
	if *dir == "" {
		panic("--runtime required")
	}
	if *video && !*ui {
		panic("video-fallbacks requires --ui; not live research evidence")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(*dir, "memory.sqlite"))
	must(err)
	defer db.Close()
	hash, err := bcrypt.GenerateFromPassword([]byte("integration-password-only"), bcrypt.MinCost)
	must(err)
	for _, id := range []string{"owner_a", "owner_b"} {
		must(db.SeedAccounts(ctx, []store.SeedAccount{{User: store.User{ID: id, Login: id, CreatedAt: "2026-01-01T00:00:00Z"}, PasswordHash: string(hash)}}))
	}
	must(seedResearch(ctx, db, "a"))
	must(seedResearch(ctx, db, "b"))
	_, err = db.DB.Exec(`UPDATE user_papers SET note='T21 FIXTURE: Prefer long-dialogue evaluations' WHERE user_id='owner_a' AND paper_version_id='paper_a'`)
	must(err)
	encoder, err := embeddings.New(ctx, embeddings.Config{BaseURL: "http://127.0.0.1:11434"})
	must(err)
	memory := researchmemory.New(db.DB, encoder)
	// Real bounded refresh and inference; no fake vectors or stored search result.
	result, err := memory.Search(ctx, "owner_a", researchmemory.SearchRequest{Query: "episodic memory consolidation forgetting", Limit: 5})
	must(err)
	if result.RetrievalMode != "hybrid" {
		panic("real hybrid memory required")
	}
	var claim researchmemory.Hit
	var note researchmemory.Hit
	for _, h := range result.Hits {
		if h.Kind == "claim" && h.PaperVersionID == "paper_a" {
			claim = h
		}
		if h.Kind == "user_note" && h.PaperVersionID == "paper_a" {
			note = h
		}
	}
	if claim.MemoryID == "" || note.MemoryID == "" {
		panic("missing real recalled claim/note")
	}
	// Include a different card of the same report to verify that navigation
	// through it cannot hide a correction made to the first card.
	var otherID string
	must(db.DB.QueryRow(`SELECT id FROM research_memories WHERE owner='owner_a' AND kind='claim' AND paper_id='paper_a' AND id!=? ORDER BY facet LIMIT 1`, claim.MemoryID).Scan(&otherID))
	otherEntry, err := memory.Get(ctx, "owner_a", otherID)
	must(err)
	// Immutable fixture of the agreed nullable T20 extension, no agent changes.
	snapshot := map[string]any{"retrieval_mode": result.RetrievalMode, "reasons": []string{"T21_HTTP_SNAPSHOT_FIXTURE_NO_AGENT_RECALL"}, "recalled": []researchmemory.Hit{claim, otherEntry.Hit, note}, "referenced": []researchmemory.Hit{claim}}
	// Additional mode/nullable snapshots are HTTP boundary fixtures, never
	// represented as a real Ollama outage or completed agent research.
	for _, key := range []string{"c", "d", "e"} {
		must(seedResearch(ctx, db, key))
	}
	_, err = memory.Refresh(ctx, "owner_a")
	must(err)
	_, err = memory.Refresh(ctx, "owner_a")
	must(err)
	// A second run makes retaining a manually selected older run observable.
	_, err = db.DB.Exec(`INSERT INTO runs(id,user_id,conversation_id,client_request_id,request_message,status,created_at,budget_json) VALUES('run_b_newer','owner_a','conv_b','b-newer','T21 second run fixture','failed','2026-01-03T00:00:00Z','{}')`)
	must(err)
	var handler http.Handler = httpapi.New(db, retrieval.New(db.DB, encoder))
	if *video {
		handler = videoFallbacks(ctx, db, handler)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v1/reports/report_") && r.URL.Path != "/api/v1/reports/report_a" {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, r)
			if response.Code == 200 {
				var report map[string]any
				must(json.Unmarshal(response.Body.Bytes(), &report))
				switch r.URL.Path {
				case "/api/v1/reports/report_b":
					report["research_memory"] = snapshot
					report["markdown"] = fmt.Sprintf("T21 HTTP SNAPSHOT FIXTURE. No live research or automatic recall.\n\nPrevious result [memory:%s]. Unvalidated marker [memory:unissued] must remain plain text.", claim.MemoryID)
				case "/api/v1/reports/report_c", "/api/v1/reports/report_d":
					mode := "lexical_only"
					if strings.HasSuffix(r.URL.Path, "_d") {
						mode = "hybrid_partial"
					}
					report["research_memory"] = map[string]any{"retrieval_mode": mode, "reasons": []string{"T21_MODE_HTTP_FIXTURE_NO_REAL_OUTAGE"}, "recalled": []researchmemory.Hit{}, "referenced": []researchmemory.Hit{}}
				case "/api/v1/reports/report_e":
					report["research_memory"] = nil
				}
				w.Header().Set("Content-Type", "application/json")
				must(json.NewEncoder(w).Encode(report))
				return
			}
			for key, values := range response.Header() {
				w.Header()[key] = values
			}
			w.WriteHeader(response.Code)
			_, _ = w.Write(response.Body.Bytes())
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	fmt.Println("T21: synthetic research and HTTP snapshot fixture; real Go/SQLite/auth/Qwen3; no DeepSeek or arXiv calls")
	if *ui {
		fmt.Println("T21 UI API:", server.URL)
		must(os.WriteFile(filepath.Join(*dir, "ui.json"), []byte(mustJSON(map[string]any{"base": server.URL, "claim": claim.MemoryID, "note": note.MemoryID, "other_claim": otherID})), 0600))
		done, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		<-done.Done()
		return
	}
	command := exec.Command("dart", "run", "integration/flutter_memory.dart")
	command.Env = append(os.Environ(), "SCOUT_BASE="+server.URL)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	must(command.Run())
}
