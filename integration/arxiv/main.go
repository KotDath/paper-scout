// T04 integration executable: real HTTP boundary + migrated SQLite + adapter.
// Synthetic Atom is only for reproducible provider failures, not live evidence.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"paper-scout/internal/arxiv"
	"paper-scout/internal/store"
)

func main() {
	live := flag.Bool("live", false, "contact actual arXiv; requires -db")
	dbPath := flag.String("db", "", "temporary SQLite path inside this worktree")
	endpoint := flag.String("endpoint", "", "official endpoint override for live check")
	expectCache := flag.Bool("expect-cache", false, "require persisted cache from earlier live process")
	flag.Parse()
	if err := run(*live, *dbPath, *endpoint, *expectCache); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func require(ok bool, message string) {
	if !ok {
		panic(message)
	}
}

func run(live bool, dbPath, endpoint string, expectCache bool) error {
	ctx := context.Background()
	if dbPath == "" {
		if live {
			return errors.New("live check requires explicit -db")
		}
		dir, err := os.MkdirTemp(".bb/runtime/t04", "integration-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		dbPath = filepath.Join(dir, "arxiv.sqlite")
	}
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer func() { db.Close() }()
	if live {
		client, err := arxiv.New(db.DB, endpoint)
		if err != nil {
			return err
		}
		result, err := client.Search(ctx, arxiv.SearchRequest{Query: `ti:MemGPT`, Limit: 3})
		if err != nil {
			return err
		}
		require(len(result.Papers) > 0, "live search returned no papers")
		require(result.CacheHit == expectCache, "unexpected live cache provenance")
		paper := result.Papers[0]
		exact := fmt.Sprintf("%sv%d", paper.ArxivID, paper.Version)
		imported, err := client.Get(ctx, exact)
		if err != nil {
			return err
		}
		require(imported.Papers[0].ID == paper.ID, "live search/Get source identity differs")
		again, err := client.Get(ctx, paper.AbstractURL)
		if err != nil {
			return err
		}
		require(again.CacheHit && again.Attempts == 0 && again.Papers[0].ID == paper.ID, "live repeat not deduplicated")
		var count int
		err = db.DB.QueryRow("SELECT COUNT(*) FROM paper_versions WHERE arxiv_id = ? AND version = ?", paper.ArxivID, paper.Version).Scan(&count)
		if err != nil {
			return err
		}
		require(count == 1, "duplicate live source persisted")
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"mode": "live", "search_cache_hit": result.CacheHit, "search_attempts": result.Attempts,
			"get_cache_hit": imported.CacheHit, "get_attempts": imported.Attempts, "repeat_cache_hit": again.CacheHit,
			"fetched_at": result.FetchedAt, "source_count": count, "paper": paper,
		})
	}

	var mu sync.Mutex
	counts := map[string]int{}
	var starts []time.Time
	var queries []string
	redirectHits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Development tooling can probe listening ports with GET /. Only the
		// unique redirect destination is evidence of following the response.
		if r.URL.Path != "/must-not-follow-redirect" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		redirectHits++
		mu.Unlock()
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/query" {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query().Get("search_query")
		key := query
		if key == "" {
			key = r.URL.Query().Get("id_list")
		}
		mu.Lock()
		counts[key]++
		n := counts[key]
		starts = append(starts, time.Now())
		queries = append(queries, query)
		mu.Unlock()
		require(r.Method == "GET" && r.URL.Path == "/api/query", "unexpected HTTP request")
		require(r.UserAgent() == "PaperScout/0.1 (local research prototype)", "unexpected User-Agent")
		w.Header().Set("Content-Type", "application/atom+xml")
		switch query {
		case "retry":
			if n == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(503)
				return
			}
		case "unavailable":
			w.WriteHeader(503)
			return
		case "bad":
			fmt.Fprint(w, `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>http://arxiv.org/api/errors#bad</id></entry></feed>`)
			return
		case "malformed":
			fmt.Fprint(w, `<html>not Atom</html>`)
			return
		case "oversized":
			fmt.Fprint(w, strings.Repeat("x", (2<<20)+1))
			return
		case "redirect":
			http.Redirect(w, r, target.URL+"/must-not-follow-redirect", 302)
			return
		case "wait":
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
			return
		}
		if key == "2301.00002v1" {
			fmt.Fprint(w, `<feed xmlns="http://www.w3.org/2005/Atom"/>`)
			return
		}
		id := "2301.00001v2"
		if key == "2301.00001v1" {
			id = key
		}
		if strings.HasPrefix(query, "legacy") {
			id = "hep-ex/0307015v1"
		}
		entryID := id
		if strings.HasPrefix(query, "legacy") {
			entryID = "hep-ex/0307015"
		}
		entry := fmt.Sprintf(`<entry><id>http://arxiv.org/abs/%s</id><title> Synthetic
 metadata </title><summary>Fixture abstract; not scientific evidence.</summary><author><name>A. Author</name></author><published>2023-01-01T00:00:00Z</published><updated>2023-01-02T00:00:00Z</updated><link href="http://arxiv.org/pdf/%s"/><link href="http://127.0.0.1/private"/></entry>`, entryID, id)
		fmt.Fprint(w, `<feed xmlns="http://www.w3.org/2005/Atom">`+entry)
		if query == "normal" {
			fmt.Fprint(w, entry)
		}
		fmt.Fprint(w, `</feed>`)
	}))
	defer server.Close()
	client, err := arxiv.New(db.DB, server.URL+"/api/query")
	if err != nil {
		return err
	}
	search := func(query string) arxiv.Result {
		r, err := client.Search(ctx, arxiv.SearchRequest{Query: query, Limit: 3})
		if err != nil {
			panic(err)
		}
		return r
	}
	first := search("normal")
	require(!first.CacheHit && first.Attempts == 1 && len(first.Papers) == 1, "search/dedup failed")
	require(first.Papers[0].Title == "Synthetic metadata" && first.Papers[0].PDFURL == "https://arxiv.org/pdf/2301.00001v2", "metadata or canonical URL failed")
	second := search("normal")
	require(second.CacheHit && second.Attempts == 0 && second.Papers[0].ID == first.Papers[0].ID, "repeat cache failed")
	for _, id := range []string{"arXiv:2301.00001v2", "https://arxiv.org/pdf/2301.00001v2.pdf", "http://arxiv.org/abs/2301.00001v2"} {
		got, err := client.Get(ctx, id)
		if err != nil {
			return err
		}
		require(got.Papers[0].ID == first.Papers[0].ID, "URL/ID normalization differs")
	}
	old, err := client.Get(ctx, "2301.00001v1")
	if err != nil {
		return err
	}
	require(old.Papers[0].ID != first.Papers[0].ID && old.Papers[0].Version == 1, "versions collapsed")
	latest, err := client.Get(ctx, "2301.00001")
	if err != nil {
		return err
	}
	require(latest.Papers[0].ID == first.Papers[0].ID, "latest was not resolved")
	legacy := search("legacy")
	require(legacy.Papers[0].ArxivID == "hep-ex/0307015" && legacy.Papers[0].Version == 1, "legacy link version resolution failed")
	_, err = client.Get(ctx, "2301.00002v1")
	require(errors.Is(err, arxiv.ErrNotFound), "missing paper was not reported")
	for _, id := range []string{"http://127.0.0.1/x", "https://arxiv.org.evil/abs/2301.00001v1", "https://arxiv.org:443/abs/2301.00001v1", "https://arxiv.org/abs/2301.00001v1?x=1", "https://arxiv.org/abs/../2301.00001v1", "2300.00001", "2301.00000", "2301.00001v0"} {
		_, err := client.Get(ctx, id)
		require(err != nil, "unsafe/invalid identifier accepted")
	}
	_, err = client.Search(ctx, arxiv.SearchRequest{Query: "normal", From: "2024-01-02", To: "2023-01-01"})
	require(err != nil, "reversed dates accepted")
	_, err = client.Search(ctx, arxiv.SearchRequest{Query: "normal", Limit: 11})
	require(err != nil, "oversized limit accepted")
	_, err = client.Search(ctx, arxiv.SearchRequest{Query: "date", From: "2023-01-01", To: "2023-02-01", Limit: 1})
	if err != nil {
		return err
	}
	fmt.Println("PASS: HTTP Atom, duplicate entries, canonical IDs/links, latest/exact/legacy versions, not-found, validation and dates")

	other, err := arxiv.New(db.DB, server.URL+"/api/query")
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	for _, concurrentClient := range []*arxiv.Client{client, other, client} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := concurrentClient.Search(ctx, arxiv.SearchRequest{Query: "concurrent", Limit: 1})
			require(err == nil && len(r.Papers) == 1, "concurrent query failed")
		}()
	}
	wg.Wait()
	mu.Lock()
	require(counts["concurrent"] == 1, "concurrent duplicate network requests")
	mu.Unlock()
	_, err = db.DB.Exec("UPDATE arxiv_query_cache SET expires_at = 0 WHERE cache_key LIKE '%search_query=normal%'")
	if err != nil {
		return err
	}
	require(!search("normal").CacheHit, "expired cache reused")
	var count int
	err = db.DB.QueryRow("SELECT COUNT(*) FROM paper_versions").Scan(&count)
	if err != nil {
		return err
	}
	require(count == 3, "version upsert duplicated sources")
	if err := db.Close(); err != nil {
		return err
	}
	db, err = store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	client, err = arxiv.New(db.DB, server.URL+"/api/query")
	if err != nil {
		return err
	}
	require(search("normal").CacheHit, "cache lost across DB reopen")
	fmt.Println("PASS: multiple clients collapse concurrent misses; expiry refresh upserts; cache persists across DB reopen")

	retried := search("retry")
	require(retried.Attempts == 2, "transient failure was not retried")
	for _, query := range []string{"bad", "malformed", "oversized", "redirect", "unavailable"} {
		result, err := client.Search(ctx, arxiv.SearchRequest{Query: query, Limit: 1})
		require(err != nil, "provider failure accepted")
		require(!result.CacheHit, "failure served as cache")
		if query == "unavailable" {
			require(result.Attempts == 3, "unbounded or missing retries")
		}
	}
	cancelled, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err = client.Search(cancelled, arxiv.SearchRequest{Query: "cancel", Limit: 1})
	require(errors.Is(err, context.DeadlineExceeded), "limiter wait was not cancelled")
	var failureCaches int
	err = db.DB.QueryRow("SELECT COUNT(*) FROM arxiv_query_cache WHERE cache_key LIKE '%search_query=bad%' OR cache_key LIKE '%search_query=unavailable%'").Scan(&failureCaches)
	if err != nil {
		return err
	}
	require(failureCaches == 0, "provider failures cached")
	_, err = client.Search(ctx, arxiv.SearchRequest{Query: "wait", Limit: 1})
	var providerError *arxiv.Error
	require(errors.As(err, &providerError) && providerError.Status == 429 && providerError.Attempts == 1, "long Retry-After was retried prematurely")
	mu.Lock()
	defer mu.Unlock()
	require(redirectHits == 0 && counts["cancel"] == 0, "redirect/cancel issued forbidden network request")
	for i := 1; i < len(starts); i++ {
		require(starts[i].Sub(starts[i-1]) >= 3*time.Second, "shared 3-second limiter violated")
	}
	require(queries[0] == "normal", "default query unexpectedly date restricted")
	require(strings.Contains(strings.Join(queries, "|"), "(date) AND submittedDate:[202301010000 TO 202302012359]"), "date restriction missing")
	fmt.Printf("PASS: bounded retries, Retry-After, invalid/oversized Atom, redirect rejection, cancellation; %d requests all >=3s apart\n", len(starts))
	return nil
}
