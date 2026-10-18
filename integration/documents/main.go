// Integration executable: real HTTP bodies, Poppler processes, migrations and
// SQLite persistence. Local source fixtures do not prove live arXiv availability.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"paper-scout/internal/documents"
	"paper-scout/internal/store"
)

func main() {
	live := flag.Bool("live", false, "fetch public arXiv HTML/PDF, no credentials")
	runtime := flag.String("runtime", "", "required worktree-local temporary directory")
	flag.Parse()
	if *runtime == "" {
		fmt.Fprintln(os.Stderr, "FAIL: runtime required")
		os.Exit(1)
	}
	if err := run(*runtime, *live); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}
func run(runtime string, live bool) error {
	ctx := context.Background()
	dbPath := filepath.Join(runtime, "documents.sqlite")
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer func() { db.Close() }()
	register := func(id string) error { return registerFixture(ctx, db, id) }
	if live {
		s, err := documents.New(db.DB, documents.Config{CacheDir: filepath.Join(runtime, "cache")})
		if err != nil {
			return err
		}
		source := documents.Source{PaperVersionID: "live-2304.03442v1", ArxivID: "2304.03442v1", Abstract: ""}
		if err = register(source.PaperVersionID); err != nil {
			return err
		}
		d, err := s.Import(ctx, source)
		if err != nil {
			return err
		}
		if d.ExtractionStatus != "full_text" {
			return fmt.Errorf("live article ingestion not confirmed: status=%s warnings=%v", d.ExtractionStatus, d.Warnings)
		}
		if err = validate(d); err != nil {
			return err
		}
		if err = restart(dbPath, &db, &s, runtime, d); err != nil {
			return err
		}
		fmt.Printf("PASS: LIVE ordinary HTML-first fetch %s; format=%s segments=%d runes=%d chunks=%d hash=%s warnings=%v; SQLite restart verified\n", d.SourceURL, d.Format, len(d.Segments), len([]rune(d.Text)), len(d.Chunks), d.ContentHash, d.Warnings)
		if d.Format == "html" {
			// A separate fresh database prevents a cache hit. Only HTML is
			// deliberately unavailable here; the PDF bytes come from real HTTPS.
			pdfDB, err := store.Open(ctx, filepath.Join(runtime, "live-pdf.sqlite"))
			if err != nil {
				return err
			}
			defer pdfDB.Close()
			// A small public text PDF keeps the probe within the download budget.
			source = documents.Source{PaperVersionID: "live-1301.3781v1", ArxivID: "1301.3781v1"}
			if err = registerFixture(ctx, pdfDB, source.PaperVersionID); err != nil {
				return err
			}
			pdfService, err := documents.New(pdfDB.DB, documents.Config{CacheDir: filepath.Join(runtime, "pdf-cache"), HTTPClient: &http.Client{Transport: pdfOnlyTransport{}}})
			if err != nil {
				return err
			}
			d, err = pdfService.Import(ctx, source)
			if err != nil {
				return err
			}
		}
		if d.ExtractionStatus != "full_text" || d.Format != "pdf" || len(d.Segments) < 2 {
			return fmt.Errorf("live PDF ingestion not confirmed: status=%s format=%s warnings=%v", d.ExtractionStatus, d.Format, d.Warnings)
		}
		if err = validate(d); err != nil {
			return err
		}
		fmt.Printf("PASS: LIVE PDF network fetch %s (HTML intentionally bypassed for PDF probe when available); pages=%d runes=%d chunks=%d hash=%s warnings=%v\n", d.SourceURL, len(d.Segments), len([]rune(d.Text)), len(d.Chunks), d.ContentHash, d.Warnings)
		return nil
	}
	if err := catalogDocuments(runtime); err != nil {
		return err
	}
	var requests atomic.Int32
	var unexpectedTarget atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		id := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/html/"), "/pdf/")
		if id == "2601.00008v1" {
			<-r.Context().Done()
			return
		}
		if id == "2601.00009v1" {
			w.Header().Set("Location", "https://example.invalid/private")
			w.WriteHeader(302)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/html/") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			switch id {
			case "2601.00001v1":
				fmt.Fprint(w, htmlArticle())
			case "2601.00003v1":
				fmt.Fprint(w, "<html><body><article><h1>Abstract</h1><p>Short metadata only.</p></article></body></html>")
			case "2601.00007v1":
				fmt.Fprint(w, strings.Repeat("x", 5000))
			default:
				w.WriteHeader(404)
			}
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		switch id {
		case "2601.00002v1":
			w.Write(pdf([]string{strings.Repeat("Memory consolidation improves retrieval across agents. ", 35), "", strings.Repeat("Experiments include long term retention and limitations. ", 45)}))
		case "2601.00004v1":
			w.Write(pdf([]string{"", ""}))
		case "2601.00011v1":
			w.Write(pdfDocument([]string{"Text"}, strings.Repeat("x", (1<<20)+1024)))
		case "2601.00005v1":
			fmt.Fprint(w, "%PDF-1.4\ncorrupt")
		case "2601.00006v1":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, htmlArticle())
		case "2601.00007v1":
			fmt.Fprint(w, strings.Repeat("x", 5000))
		default:
			w.WriteHeader(404)
		}
	}))
	defer fixture.Close()
	target, _ := url.Parse(fixture.URL)
	client := &http.Client{Transport: fixtureTransport{target: target, unexpected: &unexpectedTarget}}
	cfg := documents.Config{CacheDir: filepath.Join(runtime, "cache"), HTTPClient: client}
	s, err := documents.New(db.DB, cfg)
	if err != nil {
		return err
	}
	ingest := func(number int, abstract string) (documents.Document, error) {
		id := fmt.Sprintf("2601.%05dv1", number)
		if err := register(id); err != nil {
			return documents.Document{}, err
		}
		return s.Import(ctx, documents.Source{PaperVersionID: id, ArxivID: id, Abstract: abstract})
	}
	h, err := ingest(1, "")
	if err != nil {
		return err
	}
	if h.Format != "html" || h.ExtractionStatus != "full_text" || len(h.Segments) < 3 || strings.Contains(h.Text, "NAVIGATION_MARKER") || strings.Contains(h.Text, "SCRIPT_MARKER") || !strings.Contains(h.Text, "Память") {
		return errors.New("HTML structure/text extraction failed")
	}
	if err = validate(h); err != nil {
		return err
	}
	fmt.Println("PASS: HTTP HTML article extraction, Unicode offsets, sections, navigation/script exclusion, bounded overlapping chunks and stable evidence IDs")
	p, err := ingest(2, "")
	if err != nil {
		return err
	}
	if p.Format != "pdf" || p.ExtractionStatus != "full_text" || len(p.Segments) != 3 || p.Segments[1].Page != 2 || p.Segments[1].CharStart != p.Segments[1].CharEnd || !warning(p, "pdf_page_without_text") {
		return errors.New("PDF text/page boundary/blank-page warnings failed")
	}
	if err = validate(p); err != nil {
		return err
	}
	before := requests.Load()
	cached, err := ingest(2, "")
	if err != nil {
		return err
	}
	if !cached.Cached || cached.ID != p.ID || len(cached.Chunks) != len(p.Chunks) || cached.Chunks[0].ID != p.Chunks[0].ID || requests.Load() != before {
		return errors.New("repeat import cache/dedup failed")
	}
	var count int
	if err = db.DB.QueryRowContext(ctx, "SELECT count(*) FROM documents WHERE paper_version_id=?", p.PaperVersionID).Scan(&count); err != nil || count != 1 {
		return errors.New("duplicate document persisted")
	}
	fmt.Println("PASS: HTTP HTML-to-PDF fallback, real pdfinfo/pdftotext, three pages including empty page warning, source hash/cache, repeat import without HTTP or duplicate rows")
	historicalDocuments := []documents.Document{}
	for _, versions := range []struct{ name, extractor, chunking string }{
		{"extractor", "historical-extractor", documents.ChunkingVersion},
		{"chunking", documents.ExtractorVersion, "historical-chunking"},
		{"both", "historical-extractor", "historical-chunking"},
	} {
		// Seed historical storage from the actual HTTP-ingested article. A later
		// timestamp ensures import cannot accidentally select an obsolete version.
		prefix := "historical-" + versions.name + "-"
		historical := h
		historical.ID = prefix + h.ID
		historical.ExtractorVersion = versions.extractor
		historical.ChunkingVersion = versions.chunking
		historical.Chunks = append([]documents.Chunk(nil), h.Chunks...)
		for i := range historical.Chunks {
			historical.Chunks[i].ID = prefix + h.Chunks[i].ID
			historical.Chunks[i].DocumentID = historical.ID
		}
		_, err = db.DB.ExecContext(ctx, `INSERT INTO documents
 (id,paper_version_id,format,content_hash,extractor_version,chunking_version,extraction_status,source_url,local_path,text,segments_json,warnings_json,created_at)
 SELECT ?,paper_version_id,format,content_hash,?,?,extraction_status,source_url,local_path,text,segments_json,warnings_json,'2999-01-01T00:00:00Z' FROM documents WHERE id=?`, historical.ID, versions.extractor, versions.chunking, h.ID)
		if err != nil {
			return err
		}
		_, err = db.DB.ExecContext(ctx, `INSERT INTO chunks
 (id,document_id,ordinal,text,char_start,char_end,page_start,page_end,section)
 SELECT ?||id,?,ordinal,text,char_start,char_end,page_start,page_end,section FROM chunks WHERE document_id=?`, prefix, historical.ID, h.ID)
		if err != nil {
			return err
		}
		got, err := s.Get(ctx, historical.ID)
		if err != nil || !reflect.DeepEqual(got, historical) {
			return fmt.Errorf("historical %s version lost document data: %v", versions.name, err)
		}
		before := requests.Load()
		current, err := ingest(1, "")
		if err != nil || !current.Cached || current.ID != h.ID || current.ExtractorVersion != documents.ExtractorVersion || current.ChunkingVersion != documents.ChunkingVersion || requests.Load() != before {
			return errors.New("import cache selected a historical algorithm version")
		}
		historicalDocuments = append(historicalDocuments, historical)
	}
	for _, n := range []int{3, 4, 5, 6} {
		d, err := ingest(n, "Available abstract only. Память агентов.")
		if err != nil {
			return err
		}
		if d.Format != "abstract" || d.ExtractionStatus != "abstract_only" || len(d.Warnings) < 2 || len(d.Chunks) != 1 {
			return fmt.Errorf("honest abstract fallback failed for %d", n)
		}
		if err = validate(d); err != nil {
			return err
		}
	}
	largeOutput, err := ingest(11, "Fallback after output cap")
	if err != nil || largeOutput.ExtractionStatus != "abstract_only" || !warning(largeOutput, "pdfinfo_failed") {
		return errors.New("real Poppler stdout cap failed")
	}
	empty, err := ingest(10, "")
	if err != nil {
		return err
	}
	if empty.ExtractionStatus != "failed" || len(empty.Chunks) != 0 || !warning(empty, "abstract_unavailable") {
		return errors.New("empty extraction marked successful")
	}
	fmt.Println("PASS: metadata-only HTML, blank PDF, corrupt PDF, wrong MIME type and unavailable source retain explicit abstract_only/failed reasons; empty text never full_text")
	cfg.MaxDownloadBytes = 1024
	limited, err := documents.New(db.DB, cfg)
	if err != nil {
		return err
	}
	if err = register("2601.00007v1"); err != nil {
		return err
	}
	large, err := limited.Import(ctx, documents.Source{PaperVersionID: "2601.00007v1", ArxivID: "2601.00007v1", Abstract: "Fallback"})
	if err != nil || !warning(large, "html_source_too_large") || !warning(large, "pdf_source_too_large") {
		return errors.New("bounded download failed")
	}
	redirected, err := ingest(9, "Fallback")
	if err != nil {
		return err
	}
	if redirected.ExtractionStatus != "abstract_only" || !warning(redirected, "html_source_unavailable") || unexpectedTarget.Load() != 0 {
		return errors.New("redirect refused check failed")
	}
	if err = register("extraction-timeout"); err != nil {
		return err
	}
	cfg.MaxDownloadBytes = 0
	cfg.ExtractTimeout = time.Nanosecond
	timed, err := documents.New(db.DB, cfg)
	if err != nil {
		return err
	}
	timeoutDoc, err := timed.Import(ctx, documents.Source{PaperVersionID: "extraction-timeout", ArxivID: "2601.00002v1", Abstract: "Fallback after timeout"})
	if err != nil || timeoutDoc.ExtractionStatus != "abstract_only" || !warning(timeoutDoc, "pdfinfo_timeout") {
		return errors.New("Poppler deadline fallback failed")
	}
	cancelled, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()
	if err = register("2601.00008v1"); err != nil {
		return err
	}
	_, err = s.Import(cancelled, documents.Source{PaperVersionID: "2601.00008v1", ArxivID: "2601.00008v1", Abstract: "Fallback"})
	if !errors.Is(err, context.DeadlineExceeded) {
		return errors.New("HTTP cancellation not propagated")
	}
	before = requests.Load()
	_, err = s.Import(ctx, documents.Source{PaperVersionID: "unknown", ArxivID: "../../private"})
	if err == nil || requests.Load() != before {
		return errors.New("invalid source reached network")
	}
	if err = db.DB.QueryRowContext(ctx, "SELECT count(*) FROM documents WHERE paper_version_id='2601.00008v1'").Scan(&count); err != nil || count != 0 {
		return errors.New("cancelled ingestion persisted a document")
	}
	if err = restart(dbPath, &db, &s, runtime, p); err != nil {
		return err
	}
	for _, historical := range historicalDocuments {
		got, err := s.Get(ctx, historical.ID)
		if err != nil || !reflect.DeepEqual(got, historical) {
			return fmt.Errorf("restart changed historical document: %v", err)
		}
		if err = validate(got); err != nil {
			return err
		}
	}
	fmt.Println("PASS: historical extractor/chunking versions remain readable by ID with unchanged text, coordinates, warnings and evidence after SQLite restart; import cache selects only current algorithms")
	fmt.Println("PASS: download byte cap, redirect allowlist before transport, exact versioned ID validation, real Poppler stdout cap and pre-execution command deadline, in-flight HTTP deadline without persistence, SQLite restart with text/coordinates/evidence preserved")
	return nil
}
func warning(d documents.Document, code string) bool {
	for _, w := range d.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}
func validate(d documents.Document) error {
	text := []rune(d.Text)
	covered := make([]bool, len(text))
	end := 0
	for _, s := range d.Segments {
		if s.CharStart != end || s.CharEnd < s.CharStart || s.CharEnd > len(text) {
			return errors.New("segments do not partition text")
		}
		end = s.CharEnd
	}
	if end != len(text) {
		return errors.New("segments miss text tail")
	}
	for i, c := range d.Chunks {
		if c.Ordinal != i || c.DocumentID != d.ID || c.ID == "" || c.CharStart < 0 || c.CharEnd > len(text) || c.CharEnd <= c.CharStart || len([]rune(c.Text)) > 2000 || c.Text != string(text[c.CharStart:c.CharEnd]) {
			return errors.New("chunk text/offsets mismatch")
		}
		located := false
		for _, s := range d.Segments {
			if c.CharStart >= s.CharStart && c.CharEnd <= s.CharEnd && c.Section == s.Section && c.PageStart == s.Page && c.PageEnd == s.Page {
				located = true
			}
		}
		if !located {
			return errors.New("chunk crossed section/page boundary")
		}
		for j := c.CharStart; j < c.CharEnd; j++ {
			covered[j] = true
		}
	}
	for _, v := range covered {
		if !v {
			return errors.New("chunks leave a text gap")
		}
	}
	return nil
}
func restart(path string, db **store.Store, s **documents.Service, runtime string, want documents.Document) error {
	if err := (*db).Close(); err != nil {
		return err
	}
	reopened, err := store.Open(context.Background(), path)
	if err != nil {
		return err
	}
	*db = reopened
	*s, err = documents.New(reopened.DB, documents.Config{CacheDir: filepath.Join(runtime, "cache")})
	if err != nil {
		return err
	}
	got, err := (*s).Get(context.Background(), want.ID)
	if err != nil {
		return err
	}
	if got.Text != want.Text || len(got.Chunks) != len(want.Chunks) || got.Chunks[0].ID != want.Chunks[0].ID || len(got.Segments) != len(want.Segments) {
		return errors.New("restart changed document")
	}
	return validate(got)
}

// Only the fixture executable rewrites official URLs onto its ephemeral server.
// The production service still validates logical URLs and every redirect.
type fixtureTransport struct {
	target     *url.URL
	unexpected *atomic.Int32
}

func (t fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "arxiv.org" {
		t.unexpected.Add(1)
		return nil, errors.New("unexpected fixture target")
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.target.Scheme
	u.Host = t.target.Host
	clone.URL = &u
	response, err := http.DefaultTransport.RoundTrip(clone)
	if response != nil {
		response.Request = r
	}
	return response, err
}
func htmlArticle() string {
	paragraph := strings.Repeat("Память agents consolidate experiences into durable retrieval structures. ", 45)
	return `<html><nav>NAVIGATION_MARKER</nav><article class="ltx_document"><h1>Agent memory</h1><section><h2>Abstract</h2><p>` + paragraph + `</p></section><section><h2>1 Methods</h2><p>` + paragraph + `</p><p>` + paragraph + `</p></section><section><h2>2 Results</h2><p>` + paragraph + `</p><script>SCRIPT_MARKER</script></section></article></html>`
}

// Minimal text PDF fixture authored here, not a downloaded publication. Poppler
// parses real PDF objects/xref and emits page breaks, including blank pages.
func pdf(pages []string) []byte { return pdfDocument(pages, "") }
func pdfDocument(pages []string, title string) []byte {
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	kids := []string{}
	for _, text := range pages {
		number := len(objects) + 1
		kids = append(kids, fmt.Sprintf("%d 0 R", number))
		stream := "BT /F1 10 Tf 30 750 Td 12 TL\n"
		words := strings.Fields(text)
		for len(words) > 0 {
			n := min(10, len(words))
			line := strings.Join(words[:n], " ")
			words = words[n:]
			stream += "(" + line + ") Tj T*\n"
		}
		stream += "ET\n"
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", number+1), fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream))
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", len(pages), strings.Join(kids, " "))
	info := ""
	if title != "" {
		objects = append(objects, "<< /Title ("+title+") >>")
		info = fmt.Sprintf(" /Info %d 0 R", len(objects))
	}
	body := "%PDF-1.4\n"
	offsets := []int{0}
	for i, o := range objects {
		offsets = append(offsets, len(body))
		body += fmt.Sprintf("%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := len(body)
	body += fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, o := range offsets[1:] {
		body += fmt.Sprintf("%010d 00000 n \n", o)
	}
	body += fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R%s >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, info, xref)
	return []byte(body)
}

// PDF-specific live probe: it cannot return synthetic PDF bytes or rewrite a URL.
type pdfOnlyTransport struct{}

func (pdfOnlyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasPrefix(r.URL.Path, "/html/") {
		return nil, errors.New("HTML deliberately bypassed by live PDF probe")
	}
	return http.DefaultTransport.RoundTrip(r)
}

// Extraction/error fixtures seed complete metadata in the production T04 schema.
// catalogDocuments separately exercises registration through the actual adapter.
func registerFixture(ctx context.Context, db *store.Store, id string) error {
	_, err := db.DB.ExecContext(ctx, `INSERT OR IGNORE INTO paper_versions
 (id,arxiv_id,version,title,authors_json,abstract,abstract_url,html_url,pdf_url,published_at,updated_at,fetched_at)
 VALUES (?,?,1,'Extraction fixture','[]','','','','','','','')`, id, id)
	return err
}
