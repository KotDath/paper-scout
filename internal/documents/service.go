package documents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var versionedID = regexp.MustCompile(`^(?:[0-9]{4}\.[0-9]{4,5}|[a-z-]+(?:\.[A-Z]{2})?/[0-9]{7})v[1-9][0-9]*$`)

func New(db *sql.DB, cfg Config) (*Service, error) {
	if db == nil || cfg.CacheDir == "" {
		return nil, errors.New("documents requires database and cache directory")
	}
	if cfg.DownloadTimeout == 0 {
		cfg.DownloadTimeout = 60 * time.Second
	}
	if cfg.ExtractTimeout == 0 {
		cfg.ExtractTimeout = 30 * time.Second
	}
	if cfg.MaxDownloadBytes == 0 {
		cfg.MaxDownloadBytes = 30 << 20
	}
	if cfg.DownloadTimeout <= 0 || cfg.DownloadTimeout > 60*time.Second || cfg.ExtractTimeout <= 0 || cfg.ExtractTimeout > 30*time.Second || cfg.MaxDownloadBytes < 1 || cfg.MaxDownloadBytes > 30<<20 {
		return nil, errors.New("invalid document resource limits")
	}
	abs, err := filepath.Abs(cfg.CacheDir)
	if err != nil {
		return nil, err
	}
	cfg.CacheDir = abs
	if err := os.MkdirAll(cfg.CacheDir, 0700); err != nil {
		return nil, err
	}
	client := http.Client{}
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
	}
	client.Timeout = cfg.DownloadTimeout
	// Check the redirect before Go can contact its target, even with an injected client.
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !allowedURL(req.URL) {
			return errors.New("source_redirect_refused")
		}
		if len(via) > 0 && strings.TrimSuffix(req.URL.Path, ".pdf") != strings.TrimSuffix(via[0].URL.Path, ".pdf") {
			return errors.New("source_redirect_refused")
		}
		return nil
	}
	return &Service{db: db, cfg: cfg, client: &client}, nil
}
func allowedURL(u *url.URL) bool {
	if u.Scheme != "https" || u.Host != "arxiv.org" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	for _, prefix := range []string{"/html/", "/pdf/"} {
		if strings.HasPrefix(u.Path, prefix) {
			return versionedID.MatchString(strings.TrimSuffix(strings.TrimPrefix(u.Path, prefix), ".pdf"))
		}
	}
	return false
}

// Import attempts HTML, then a text PDF, then the explicitly labeled abstract.
// The caller must register Source.PaperVersionID in the catalog beforehand.
// Import is a public technical-cache operation, not an ownership grant.
func (s *Service) Import(ctx context.Context, source Source) (Document, error) {
	if source.PaperVersionID == "" || len(source.PaperVersionID) > 200 || len(source.ArxivID) > 100 || !versionedID.MatchString(source.ArxivID) {
		return Document{}, errors.New("source requires paper version ID and exact versioned arXiv ID")
	}
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	existing, err := s.load(ctx, "paper_version_id", source.PaperVersionID)
	if err == nil && existing.ExtractionStatus == "full_text" {
		existing.Cached = true
		return existing, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Document{}, err
	}
	warnings := []Warning{}
	for _, format := range []string{"html", "pdf"} {
		if err := ctx.Err(); err != nil {
			return Document{}, err
		}
		sourceURL := "https://arxiv.org/" + format + "/" + source.ArxivID
		raw, err := s.download(ctx, sourceURL, format)
		if err != nil {
			if ctx.Err() != nil {
				return Document{}, ctx.Err()
			}
			warnings = append(warnings, Warning{Code: format + "_" + err.Error()})
			continue
		}
		var text string
		var segments []Segment
		var extractedWarnings []Warning
		var path string
		if format == "html" {
			text, segments, extractedWarnings, err = extractHTML(raw)
		} else {
			// Poppler receives a private file written atomically, never a URL or shell command.
			path, err = s.cache(raw, format)
			if err != nil {
				return Document{}, err
			}
			text, segments, extractedWarnings, err = s.extractPDF(ctx, path)
		}
		if err != nil {
			if ctx.Err() != nil {
				return Document{}, ctx.Err()
			}
			warnings = append(warnings, Warning{Code: err.Error()})
			continue
		}
		if path == "" {
			path, err = s.cache(raw, format)
			if err != nil {
				return Document{}, err
			}
		}
		d := Document{PaperVersionID: source.PaperVersionID, Format: format, ContentHash: hash(raw), ExtractionStatus: "full_text", SourceURL: sourceURL, LocalPath: path, Text: text, Segments: segments, Warnings: append(warnings, extractedWarnings...)}
		return s.save(ctx, d)
	}
	text := strings.TrimSpace(source.Abstract)
	status := "abstract_only"
	if text == "" {
		status = "failed"
		warnings = append(warnings, Warning{Code: "abstract_unavailable"})
	}
	segments := []Segment{}
	if text != "" {
		segments = append(segments, Segment{CharEnd: len([]rune(text)), Section: "Abstract"})
	}
	return s.save(ctx, Document{PaperVersionID: source.PaperVersionID, Format: "abstract", ContentHash: hash([]byte(text)), ExtractionStatus: status, SourceURL: "https://arxiv.org/abs/" + source.ArxivID, Text: text, Segments: segments, Warnings: warnings})
}
func (s *Service) download(ctx context.Context, sourceURL, format string) ([]byte, error) {
	child, cancel := context.WithTimeout(ctx, s.cfg.DownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(child, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, errors.New("invalid_url")
	}
	if !allowedURL(req.URL) {
		return nil, errors.New("invalid_url")
	}
	req.Header.Set("User-Agent", "PaperScout/0.1 (local research prototype)")
	if format == "pdf" {
		req.Header.Set("Accept", "application/pdf")
	} else {
		req.Header.Set("Accept", "text/html, application/xhtml+xml")
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, errors.New("source_unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http_%d", res.StatusCode)
	}
	if res.ContentLength > s.cfg.MaxDownloadBytes {
		return nil, errors.New("source_too_large")
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, s.cfg.MaxDownloadBytes+1))
	if err != nil {
		return nil, errors.New("source_unavailable")
	}
	if int64(len(raw)) > s.cfg.MaxDownloadBytes {
		return nil, errors.New("source_too_large")
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(res.Header.Get("Content-Type"), ";")[0]))
	if format == "pdf" {
		if contentType != "application/pdf" && contentType != "application/octet-stream" {
			return nil, errors.New("invalid_content_type")
		}
		if !strings.HasPrefix(string(raw), "%PDF-") {
			return nil, errors.New("invalid_pdf_signature")
		}
	} else if contentType != "text/html" && contentType != "application/xhtml+xml" {
		return nil, errors.New("invalid_content_type")
	}
	return raw, nil
}
func (s *Service) cache(raw []byte, format string) (string, error) {
	target := filepath.Join(s.cfg.CacheDir, hash(raw)+"."+format)
	tmp, err := os.CreateTemp(s.cfg.CacheDir, ".source-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(raw); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(tmp.Name(), target); err != nil {
		return "", err
	}
	return target, nil
}
