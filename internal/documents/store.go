package documents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func (s *Service) save(ctx context.Context, d Document) (Document, error) {
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	d.ExtractorVersion = ExtractorVersion
	d.ChunkingVersion = ChunkingVersion
	d.ID = "doc_" + hash([]byte(d.PaperVersionID+":"+d.Format+":"+d.ContentHash+":"+ExtractorVersion+":"+ChunkingVersion))
	d.Chunks = makeChunks(d)
	segments, err := json.Marshal(d.Segments)
	if err != nil {
		return Document{}, err
	}
	warnings, err := json.Marshal(d.Warnings)
	if err != nil {
		return Document{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO documents
 (id,paper_version_id,format,content_hash,extractor_version,chunking_version,extraction_status,source_url,local_path,text,segments_json,warnings_json,created_at)
 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT (id) DO NOTHING`, d.ID, d.PaperVersionID, d.Format, d.ContentHash, d.ExtractorVersion, d.ChunkingVersion, d.ExtractionStatus, d.SourceURL, d.LocalPath, d.Text, string(segments), string(warnings), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Document{}, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Document{}, err
	}
	if inserted > 0 {
		for _, c := range d.Chunks {
			_, err = tx.ExecContext(ctx, `INSERT INTO chunks (id,document_id,ordinal,text,char_start,char_end,page_start,page_end,section) VALUES (?,?,?,?,?,?,?,?,?)`, c.ID, c.DocumentID, c.Ordinal, c.Text, c.CharStart, c.CharEnd, pageNumber(c.PageStart), pageNumber(c.PageEnd), c.Section)
			if err != nil {
				return Document{}, err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return Document{}, err
	}
	if inserted == 0 {
		return s.Get(ctx, d.ID)
	}
	return d, nil
}
func pageNumber(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
func (s *Service) load(ctx context.Context, column, value string) (Document, error) {
	// column is selected only by the two internal call sites, never user input.
	if column != "id" && column != "paper_version_id" {
		return Document{}, errors.New("invalid document lookup")
	}
	where := column + "=?"
	args := []any{value}
	// Cache selection uses current algorithms; immutable IDs must also resolve
	// historical versions referenced by existing analysis and reports.
	if column == "paper_version_id" {
		where += " AND extractor_version=? AND chunking_version=?"
		args = append(args, ExtractorVersion, ChunkingVersion)
	}
	var d Document
	var segments, warnings string
	err := s.db.QueryRowContext(ctx, `SELECT id,paper_version_id,format,content_hash,extractor_version,chunking_version,extraction_status,source_url,local_path,text,segments_json,warnings_json FROM documents WHERE `+where+` ORDER BY (extraction_status='full_text') DESC,created_at DESC LIMIT 1`, args...).Scan(&d.ID, &d.PaperVersionID, &d.Format, &d.ContentHash, &d.ExtractorVersion, &d.ChunkingVersion, &d.ExtractionStatus, &d.SourceURL, &d.LocalPath, &d.Text, &segments, &warnings)
	if err != nil {
		return Document{}, err
	}
	if err = json.Unmarshal([]byte(segments), &d.Segments); err != nil {
		return Document{}, err
	}
	if err = json.Unmarshal([]byte(warnings), &d.Warnings); err != nil {
		return Document{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,document_id,ordinal,text,char_start,char_end,page_start,page_end,section FROM chunks WHERE document_id=? ORDER BY ordinal`, d.ID)
	if err != nil {
		return Document{}, err
	}
	defer rows.Close()
	d.Chunks = []Chunk{}
	for rows.Next() {
		var c Chunk
		var start, end sql.NullInt64
		if err = rows.Scan(&c.ID, &c.DocumentID, &c.Ordinal, &c.Text, &c.CharStart, &c.CharEnd, &start, &end, &c.Section); err != nil {
			return Document{}, err
		}
		c.PageStart = int(start.Int64)
		c.PageEnd = int(end.Int64)
		d.Chunks = append(d.Chunks, c)
	}
	return d, rows.Err()
}
