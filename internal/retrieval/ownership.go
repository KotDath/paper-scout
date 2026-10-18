package retrieval

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AddPaper is for the trusted importer after it has resolved public metadata.
// It grants library access without marking the article saved/read or changing notes.
// API/tools must get userID from authentication, never from model arguments.
func (s *Service) AddPaper(ctx context.Context, userID, paperID string) error {
	if userID == "" || paperID == "" {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_papers(user_id,paper_version_id,added_at)
 SELECT u.id,p.id,? FROM users u CROSS JOIN paper_versions p WHERE u.id=? AND p.id=?
 ON CONFLICT(user_id,paper_version_id) DO NOTHING`, time.Now().UTC().Format(time.RFC3339Nano), userID, paperID)
	if err != nil {
		return err
	}
	var exists int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM user_papers WHERE user_id=? AND paper_version_id=?`, userID, paperID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

const hitColumns = `c.id,c.document_id,d.paper_version_id,p.title,c.text,c.char_start,c.char_end,COALESCE(c.page_start,0),COALESCE(c.page_end,0),c.section,d.source_url,d.extraction_status`
const hitJoins = ` FROM chunks c JOIN documents d ON d.id=c.document_id JOIN paper_versions p ON p.id=d.paper_version_id `
const ownerWhere = ` EXISTS (SELECT 1 FROM user_papers up WHERE up.user_id=? AND up.paper_version_id=d.paper_version_id) `

// Evidence always rechecks membership, including direct guessed evidence IDs.
func (s *Service) Evidence(ctx context.Context, userID, evidenceID string) (Hit, error) {
	var h Hit
	if userID == "" || evidenceID == "" {
		return h, ErrNotFound
	}
	err := scanHit(s.db.QueryRowContext(ctx, `SELECT `+hitColumns+hitJoins+`WHERE `+ownerWhere+` AND c.id=?`, userID, evidenceID), &h)
	if errors.Is(err, sql.ErrNoRows) {
		return Hit{}, ErrNotFound
	}
	return h, err
}

type scanner interface{ Scan(...any) error }

func scanHit(row scanner, h *Hit, extra ...any) error {
	args := []any{&h.EvidenceID, &h.DocumentID, &h.PaperVersionID, &h.Title, &h.Text, &h.CharStart, &h.CharEnd, &h.PageStart, &h.PageEnd, &h.Section, &h.SourceURL, &h.ExtractionStatus}
	return row.Scan(append(args, extra...)...)
}
