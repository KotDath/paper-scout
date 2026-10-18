package retrieval

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"paper-scout/internal/embeddings"
)

var errSourceChanged = errors.New("source_changed")

type chunk struct {
	hit      Hit
	chunking string
	hash     string
	vector   []float32
}

// Index keeps network calls outside transactions, persists each successful batch,
// and resumes from compatible public cache entries after interruption.
func (s *Service) Index(ctx context.Context, userID, documentID string) (IndexStatus, error) {
	status := IndexStatus{DocumentID: documentID, Status: "pending"}
	select {
	case s.indexLock <- struct{}{}:
		defer func() { <-s.indexLock }()
	case <-ctx.Done():
		return status, ctx.Err()
	}
	var chunking string
	err := s.db.QueryRowContext(ctx, `SELECT d.chunking_version FROM documents d WHERE d.id=? AND `+ownerWhere, documentID, userID).Scan(&chunking)
	if errors.Is(err, sql.ErrNoRows) {
		return status, ErrNotFound
	}
	if err != nil {
		return status, err
	}
	items, err := s.loadChunks(ctx, userID, nil, documentID)
	if err != nil {
		return status, err
	}
	status.TotalChunks = len(items)
	identity := embeddings.Identity{}
	if s.embed != nil {
		identity = s.embed.Identity()
	}
	finish := func(cause error) (IndexStatus, error) {
		if cause != nil {
			status.LastError = errorCode(cause)
			status.Status = "failed"
			if status.IndexedChunks > 0 {
				status.Status = "partial"
			}
		}
		// Preserve progress even when the caller cancels. This bounded write does not
		// execute more inference and never turns cancellation into successful indexing.
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := s.saveStatus(saveCtx, &status, identity, chunking, items); err != nil {
			return status, err
		}
		return status, cause
	}
	if err = s.saveStatus(ctx, &status, identity, chunking, items); err != nil {
		return status, err
	}
	if len(items) == 0 {
		return finish(errors.New("text_unusable"))
	}
	if s.embed == nil {
		return finish(&embeddings.Error{Code: "embedding_unavailable"})
	}
	pending := []chunk{}
	for _, c := range items {
		var blob []byte
		err = s.db.QueryRowContext(ctx, `SELECT vector_blob FROM chunk_embeddings WHERE model_id=? AND model_digest=? AND encoding_version=? AND dimensions=? AND chunking_version=? AND input_hash=? LIMIT 1`, identity.Model, identity.Digest, identity.EncodingVersion, identity.Dimensions, c.chunking, c.hash).Scan(&blob)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return finish(err)
		}
		if v, ok := readVector(blob); err == nil && ok {
			c.vector = v
			if err = s.saveVectors(ctx, []chunk{c}, identity); err != nil {
				return finish(err)
			}
			status.CacheHits++
			status.IndexedChunks++
		} else {
			pending = append(pending, c)
		}
	}
	for start := 0; start < len(pending); start += embeddings.MaxBatchSize {
		batch := pending[start:min(start+embeddings.MaxBatchSize, len(pending))]
		inputs := make([]string, len(batch))
		for i, c := range batch {
			inputs[i] = embeddings.DocumentText(c.hit.Title, c.hit.Section, c.hit.Text)
		}
		result, err := s.embed.Embed(ctx, embeddings.Document, inputs)
		if err != nil {
			return finish(err)
		}
		if result.Identity != identity {
			return finish(&embeddings.Error{Code: "embedding_incompatible"})
		}
		for i := range batch {
			if result.InputHashes[i] != batch[i].hash {
				return finish(&embeddings.Error{Code: "embedding_incompatible"})
			}
			batch[i].vector = result.Vectors[i]
		}
		if err = s.saveVectors(ctx, batch, identity); err != nil {
			return finish(err)
		}
		status.IndexedChunks += len(batch)
		if err = s.saveStatus(ctx, &status, identity, chunking, items); err != nil {
			return finish(err)
		}
	}
	status.Status = "ready"
	if err = s.saveStatus(ctx, &status, identity, chunking, items); err != nil {
		return finish(err)
	}
	return status, nil
}

func errorCode(err error) string {
	var e *embeddings.Error
	if errors.As(err, &e) {
		return e.Code
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "embedding_timeout"
	}
	if errors.Is(err, errSourceChanged) {
		return "source_changed"
	}
	if err.Error() == "text_unusable" {
		return "text_unusable"
	}
	return "index_error"
}

func (s *Service) saveVectors(ctx context.Context, items []chunk, id embeddings.Identity) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range items {
		_, err = tx.ExecContext(ctx, `INSERT INTO chunk_embeddings(chunk_id,model_id,model_digest,encoding_version,dimensions,chunking_version,input_hash,vector_blob) VALUES (?,?,?,?,?,?,?,?)
 ON CONFLICT(chunk_id,model_id,model_digest,encoding_version,dimensions,chunking_version,input_hash) DO UPDATE SET vector_blob=excluded.vector_blob`, c.hit.EvidenceID, id.Model, id.Digest, id.EncodingVersion, id.Dimensions, c.chunking, c.hash, vectorBytes(c.vector))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Every progress write checks the source snapshot in the same transaction.
// On mutation, replace counters from the old inference inputs with counts of
// currently compatible vectors, and return that same status to the caller.
func (s *Service) saveStatus(ctx context.Context, status *IndexStatus, id embeddings.Identity, chunking string, items []chunk) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var title, currentChunking string
	err = tx.QueryRowContext(ctx, `SELECT p.title,d.chunking_version FROM documents d JOIN paper_versions p ON p.id=d.paper_version_id WHERE d.id=?`, status.DocumentID).Scan(&title, &currentChunking)
	if err != nil {
		return err
	}
	changed := currentChunking != chunking
	expected := make(map[string]string, len(items))
	for _, c := range items {
		expected[c.hit.EvidenceID] = c.hash
		if c.chunking != currentChunking {
			changed = true
		}
	}
	current := make(map[string]string, len(items))
	rows, err := tx.QueryContext(ctx, `SELECT id,section,text FROM chunks WHERE document_id=?`, status.DocumentID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var evidence, section, text string
		if err = rows.Scan(&evidence, &section, &text); err != nil {
			rows.Close()
			return err
		}
		hash := inputHash(embeddings.DocumentText(title, section, text))
		current[evidence] = hash
		if expected[evidence] != hash {
			changed = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(current) != len(expected) {
		changed = true
	}
	updated := *status
	if changed {
		updated.Status = "partial"
		updated.LastError = "source_changed"
		updated.TotalChunks = len(current)
		updated.IndexedChunks = 0
		rows, err = tx.QueryContext(ctx, `SELECT e.chunk_id,e.input_hash,e.vector_blob FROM chunk_embeddings e JOIN chunks c ON c.id=e.chunk_id WHERE c.document_id=? AND e.model_id=? AND e.model_digest=? AND e.encoding_version=? AND e.dimensions=? AND e.chunking_version=?`, status.DocumentID, id.Model, id.Digest, id.EncodingVersion, id.Dimensions, currentChunking)
		if err != nil {
			return err
		}
		counted := make(map[string]bool, len(current))
		for rows.Next() {
			var evidence, hash string
			var blob []byte
			if err = rows.Scan(&evidence, &hash, &blob); err != nil {
				rows.Close()
				return err
			}
			if current[evidence] == hash && !counted[evidence] {
				if _, ok := readVector(blob); ok {
					updated.IndexedChunks++
					counted[evidence] = true
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO document_indexes(document_id,model_id,model_digest,encoding_version,dimensions,chunking_version,indexed_chunks,total_chunks,status,last_error,updated_at)
 VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(document_id) DO UPDATE SET model_id=excluded.model_id,model_digest=excluded.model_digest,encoding_version=excluded.encoding_version,dimensions=excluded.dimensions,chunking_version=excluded.chunking_version,indexed_chunks=excluded.indexed_chunks,total_chunks=excluded.total_chunks,status=excluded.status,last_error=excluded.last_error,updated_at=excluded.updated_at`, updated.DocumentID, id.Model, id.Digest, id.EncodingVersion, id.Dimensions, currentChunking, updated.IndexedChunks, updated.TotalChunks, updated.Status, updated.LastError, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	*status = updated
	if changed {
		return errSourceChanged
	}
	return nil
}
