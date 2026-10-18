package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"

	"paper-scout/internal/arxiv"
	"paper-scout/internal/documents"
	"paper-scout/internal/store"
)

// Exercise both production adapters against one fully migrated SQLite database.
// Only the external Atom/HTML bodies are fixtures; no catalog schema is stubbed.
func catalogDocuments(runtime string) error {
	ctx := context.Background()
	var requests atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/api/query" {
			id := r.URL.Query().Get("id_list")
			if id == "" {
				id = "2601.00001v1"
			}
			w.Header().Set("Content-Type", "application/atom+xml")
			fmt.Fprintf(w, `<feed xmlns="http://www.w3.org/2005/Atom"><entry>
<id>https://arxiv.org/abs/%s</id><title>Agent memory fixture</title>
<summary>Public catalog abstract.</summary><author><name>Fixture Author</name></author>
<published>2026-01-01T00:00:00Z</published><updated>2026-01-02T00:00:00Z</updated>
</entry></feed>`, id)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/html/") {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, htmlArticle())
			return
		}
		http.NotFound(w, r)
	}))
	defer fixture.Close()
	path := filepath.Join(runtime, "catalog-documents.sqlite")
	db, err := store.Open(ctx, path)
	if err != nil {
		return err
	}
	defer func() {
		if db != nil {
			db.Close()
		}
	}()
	catalog, err := arxiv.New(db.DB, fixture.URL+"/api/query")
	if err != nil {
		return err
	}
	target, _ := url.Parse(fixture.URL)
	var unexpected atomic.Int32
	cfg := documents.Config{CacheDir: filepath.Join(runtime, "catalog-cache"), HTTPClient: &http.Client{Transport: fixtureTransport{target: target, unexpected: &unexpected}}}
	service, err := documents.New(db.DB, cfg)
	if err != nil {
		return err
	}
	search, err := catalog.Search(ctx, arxiv.SearchRequest{Query: "all:memory", Limit: 1})
	if err != nil {
		return err
	}
	if len(search.Papers) != 1 || search.CacheHit {
		return fmt.Errorf("catalog search did not register fixture metadata")
	}
	var papers []arxiv.Paper
	var docs []documents.Document
	for _, id := range []string{"2601.00001v1", "2601.00001v2"} {
		result, err := catalog.Get(ctx, "https://arxiv.org/abs/"+id)
		if err != nil {
			return err
		}
		paper := result.Papers[0]
		if len(papers) == 0 && paper.ID != search.Papers[0].ID {
			return fmt.Errorf("search and exact metadata have different identities")
		}
		doc, err := service.Import(ctx, documents.Source{PaperVersionID: paper.ID, ArxivID: fmt.Sprintf("%sv%d", paper.ArxivID, paper.Version), Abstract: paper.Abstract})
		if err != nil {
			return err
		}
		if doc.PaperVersionID != paper.ID || doc.SourceURL != paper.HTMLURL || doc.ExtractionStatus != "full_text" {
			return fmt.Errorf("catalog/document version identity differs")
		}
		if err := validate(doc); err != nil {
			return err
		}
		papers, docs = append(papers, paper), append(docs, doc)
	}
	if docs[0].ID == docs[1].ID || docs[0].Chunks[0].ID == docs[1].Chunks[0].ID || docs[0].ContentHash != docs[1].ContentHash {
		return fmt.Errorf("distinct paper versions sharing source bytes lost document/evidence identity")
	}
	if err := db.Close(); err != nil {
		return err
	}
	db, err = store.Open(ctx, path)
	if err != nil {
		return err
	}
	catalog, err = arxiv.New(db.DB, fixture.URL+"/api/query")
	if err != nil {
		return err
	}
	service, err = documents.New(db.DB, cfg)
	if err != nil {
		return err
	}
	before := requests.Load()
	for i, paper := range papers {
		result, err := catalog.Get(ctx, paper.AbstractURL)
		if err != nil || !result.CacheHit || result.Attempts != 0 || !reflect.DeepEqual(result.Papers[0], paper) {
			return fmt.Errorf("catalog metadata/cache changed after reopen: %v", err)
		}
		got, err := service.Get(ctx, docs[i].ID)
		if err != nil || !reflect.DeepEqual(got, docs[i]) {
			return fmt.Errorf("document/evidence changed after reopen: %v", err)
		}
		cached, err := service.Import(ctx, documents.Source{PaperVersionID: paper.ID, ArxivID: fmt.Sprintf("%sv%d", paper.ArxivID, paper.Version), Abstract: paper.Abstract})
		if err != nil || !cached.Cached || cached.ID != got.ID {
			return fmt.Errorf("document import cache changed after reopen: %v", err)
		}
	}
	var count int
	err = db.DB.QueryRowContext(ctx, `SELECT count(*) FROM paper_versions p JOIN documents d ON d.paper_version_id=p.id
 WHERE p.arxiv_id='2601.00001' AND p.title='Agent memory fixture' AND p.abstract='Public catalog abstract.'`).Scan(&count)
	if err != nil || count != 2 || requests.Load() != before || unexpected.Load() != 0 {
		return fmt.Errorf("catalog/document joins, deduplication or offline cache failed: count=%d err=%v", count, err)
	}
	rows, err := db.DB.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("composed schema has foreign key violations")
	}
	if err := rows.Err(); err != nil {
		return err
	}
	fmt.Println("PASS: T04 Atom search/exact versions → T05 HTML import in one production SQLite schema; metadata/document/evidence identity, distinct versions with identical bytes, foreign keys and cache deduplication survive reopen without HTTP")
	return nil
}
