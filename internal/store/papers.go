package store

import (
	"context"
	"encoding/json"
)

// Paper is an owner-scoped public projection. Cache paths and source text are
// deliberately absent; evidence is read through the existing retrieval service.
type Paper struct {
	ID          string          `json:"id"`
	ArxivID     string          `json:"arxiv_id"`
	Version     int             `json:"version"`
	Title       string          `json:"title"`
	Authors     json.RawMessage `json:"authors"`
	Abstract    string          `json:"abstract"`
	AbstractURL string          `json:"abstract_url"`
	HTMLURL     string          `json:"html_url"`
	PDFURL      string          `json:"pdf_url"`
	PublishedAt string          `json:"published_at"`
	UpdatedAt   string          `json:"updated_at"`
	Saved       bool            `json:"saved"`
	Read        bool            `json:"read"`
	Note        string          `json:"note"`
	AddedAt     string          `json:"added_at"`
	Documents   []PaperDocument `json:"documents"`
}
type PaperDocument struct {
	ID               string          `json:"id"`
	Format           string          `json:"format"`
	ExtractionStatus string          `json:"extraction_status"`
	Warnings         json.RawMessage `json:"warnings"`
	SourceURL        string          `json:"source_url"`
	IndexStatus      string          `json:"index_status"`
	IndexedChunks    int             `json:"indexed_chunks"`
	TotalChunks      int             `json:"total_chunks"`
	IndexError       string          `json:"index_error,omitempty"`
}

func (s *Store) Paper(ctx context.Context, owner, id string) (Paper, error) {
	var p Paper
	var authors string
	err := s.DB.QueryRowContext(ctx, `SELECT p.id,p.arxiv_id,p.version,p.title,p.authors_json,p.abstract,p.abstract_url,p.html_url,p.pdf_url,p.published_at,p.updated_at,COALESCE(u.saved,0),COALESCE(u.read,0),COALESCE(u.note,''),COALESCE(u.added_at,'') FROM paper_versions p LEFT JOIN user_papers u ON u.paper_version_id=p.id AND u.user_id=? WHERE p.id=? AND (u.user_id IS NOT NULL OR EXISTS(SELECT 1 FROM run_sources rs JOIN runs r ON r.id=rs.run_id WHERE rs.paper_version_id=p.id AND r.user_id=?))`, owner, id, owner).Scan(&p.ID, &p.ArxivID, &p.Version, &p.Title, &authors, &p.Abstract, &p.AbstractURL, &p.HTMLURL, &p.PDFURL, &p.PublishedAt, &p.UpdatedAt, &p.Saved, &p.Read, &p.Note, &p.AddedAt)
	if err != nil {
		return p, notFound(err)
	}
	p.Authors = json.RawMessage(authors)
	p.Documents = []PaperDocument{}
	rows, err := s.DB.QueryContext(ctx, `SELECT d.id,d.format,d.extraction_status,d.warnings_json,d.source_url,COALESCE(i.status,'pending'),COALESCE(i.indexed_chunks,0),(SELECT count(*) FROM chunks c WHERE c.document_id=d.id),COALESCE(i.last_error,'') FROM documents d LEFT JOIN document_indexes i ON i.document_id=d.id WHERE d.paper_version_id=? ORDER BY d.created_at,d.id`, id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var d PaperDocument
		var warnings string
		if err = rows.Scan(&d.ID, &d.Format, &d.ExtractionStatus, &warnings, &d.SourceURL, &d.IndexStatus, &d.IndexedChunks, &d.TotalChunks, &d.IndexError); err != nil {
			return p, err
		}
		d.Warnings = json.RawMessage(warnings)
		p.Documents = append(p.Documents, d)
	}
	return p, rows.Err()
}

func (s *Store) Library(ctx context.Context, owner, cursor string, limit int) ([]Paper, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT paper_version_id FROM user_papers WHERE user_id=? AND paper_version_id>? ORDER BY paper_version_id LIMIT ?`, owner, cursor, limit)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []Paper{}
	for _, id := range ids {
		p, err := s.Paper(ctx, owner, id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

type PaperPatch struct {
	Saved *bool   `json:"saved"`
	Read  *bool   `json:"read"`
	Note  *string `json:"note"`
}

func (s *Store) UpdatePaper(ctx context.Context, owner, id string, p PaperPatch) (Paper, error) {
	result, err := s.DB.ExecContext(ctx, `UPDATE user_papers SET saved=COALESCE(?,saved),read=COALESCE(?,read),note=COALESCE(?,note) WHERE user_id=? AND paper_version_id=?`, p.Saved, p.Read, p.Note, owner, id)
	if err != nil {
		return Paper{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Paper{}, err
	}
	if n != 1 {
		return Paper{}, ErrNotFound
	}
	return s.Paper(ctx, owner, id)
}
