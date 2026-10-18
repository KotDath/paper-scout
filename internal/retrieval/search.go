package retrieval

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"paper-scout/internal/embeddings"
)

// scopeSQL is shared by both branches. Membership is checked before top-k.
func scopeSQL(userID string, papers []string, documentID string) (string, []any) {
	where := ownerWhere
	args := []any{userID}
	if len(papers) > 0 {
		where += ` AND d.paper_version_id IN (` + strings.TrimRight(strings.Repeat("?,", len(papers)), ",") + `)`
		for _, p := range papers {
			args = append(args, p)
		}
	}
	if documentID != "" {
		where += ` AND d.id=?`
		args = append(args, documentID)
	}
	return where, args
}

func (s *Service) loadChunks(ctx context.Context, userID string, papers []string, documentID string) ([]chunk, error) {
	where, args := scopeSQL(userID, papers, documentID)
	rows, err := s.db.QueryContext(ctx, `SELECT `+hitColumns+`,d.chunking_version`+hitJoins+`WHERE `+where+` ORDER BY c.id LIMIT 20001`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []chunk{}
	for rows.Next() {
		var c chunk
		if err = scanHit(rows, &c.hit, &c.chunking); err != nil {
			return nil, err
		}
		c.hash = inputHash(embeddings.DocumentText(c.hit.Title, c.hit.Section, c.hit.Text))
		items = append(items, c)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(items) > MaxScopeChunks {
		return nil, ErrScopeLimit
	}
	return items, nil
}

// Tokens are quoted literals joined with OR. Operators, wildcards, column
// selectors and arbitrary FTS syntax never pass through from user text.
func matchQuery(text string) string {
	tokens := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	parts := []string{}
	seen := map[string]bool{}
	for _, token := range tokens {
		token = strings.ToLower(token)
		if !seen[token] {
			parts = append(parts, `"`+token+`"`)
			seen[token] = true
		}
		if len(parts) == 64 {
			break
		}
	}
	return strings.Join(parts, " OR ")
}

func (s *Service) Search(ctx context.Context, userID string, req SearchRequest) (SearchResult, error) {
	result := SearchResult{Query: req.Query, LexicalTerms: req.LexicalTerms, RetrievalMode: "lexical_only", Reasons: []string{}, Hits: []Hit{}, LexicalCandidates: []Hit{}, DenseCandidates: []Hit{}}
	if userID == "" || !utf8.ValidString(req.Query) || strings.TrimSpace(req.Query) == "" || utf8.RuneCountInString(req.Query) > 4000 || !utf8.ValidString(req.LexicalTerms) || utf8.RuneCountInString(req.LexicalTerms) > 4000 || len(req.PaperVersionIDs) > 100 || req.Limit < 0 || req.Limit > 8 {
		return result, ErrInvalid
	}
	if req.Limit == 0 {
		req.Limit = 8
	}
	items, err := s.loadChunks(ctx, userID, req.PaperVersionIDs, "")
	if err != nil {
		return result, err
	}
	result.TotalChunks = len(items)
	terms := req.LexicalTerms
	if strings.TrimSpace(terms) == "" {
		terms = req.Query
	}
	result.LexicalTerms = terms
	if match := matchQuery(terms); match != "" {
		where, args := scopeSQL(userID, req.PaperVersionIDs, "")
		args = append(args, match)
		rows, err := s.db.QueryContext(ctx, `SELECT `+hitColumns+`,bm25(chunks_fts)`+hitJoins+`JOIN chunks_fts ON chunks_fts.chunk_id=c.id WHERE `+where+` AND chunks_fts MATCH ? ORDER BY bm25(chunks_fts),c.id LIMIT 20`, args...)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var h Hit
			if err = scanHit(rows, &h, &h.BM25); err != nil {
				rows.Close()
				return result, err
			}
			h.LexicalRank = len(result.LexicalCandidates) + 1
			result.LexicalCandidates = append(result.LexicalCandidates, h)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return result, err
		}
	}
	if s.embed == nil {
		result.Reasons = append(result.Reasons, "embedding_unavailable")
	} else {
		identity := s.embed.Identity()
		if err = s.loadVectors(ctx, userID, req.PaperVersionIDs, items, identity); err != nil {
			return result, err
		}
		for _, c := range items {
			if c.vector != nil {
				result.IndexedChunks++
			}
		}
		if result.TotalChunks > 0 {
			result.IndexCoverage = float64(result.IndexedChunks) / float64(result.TotalChunks)
		}
		if result.IndexedChunks < result.TotalChunks {
			result.Reasons = append(result.Reasons, "index_incomplete_or_incompatible")
		}
		// Always perform query inference, even on an empty scope, to report an actual
		// backend failure rather than claiming a healthy hybrid search without it.
		query, err := s.embed.Embed(ctx, embeddings.Query, []string{req.Query})
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			result.Reasons = append(result.Reasons, errorCode(err))
		} else if query.Identity != identity {
			result.Reasons = append(result.Reasons, "embedding_incompatible")
		} else {
			for _, c := range items {
				if c.vector == nil {
					continue
				}
				h := c.hit
				for i, v := range c.vector {
					h.Cosine += float64(v) * float64(query.Vectors[0][i])
				}
				result.DenseCandidates = append(result.DenseCandidates, h)
			}
			sort.Slice(result.DenseCandidates, func(i, j int) bool {
				a, b := result.DenseCandidates[i], result.DenseCandidates[j]
				if a.Cosine != b.Cosine {
					return a.Cosine > b.Cosine
				}
				return a.EvidenceID < b.EvidenceID
			})
			if len(result.DenseCandidates) > 20 {
				result.DenseCandidates = result.DenseCandidates[:20]
			}
			for i := range result.DenseCandidates {
				result.DenseCandidates[i].DenseRank = i + 1
			}
			result.RetrievalMode = "hybrid"
			if result.IndexedChunks < result.TotalChunks {
				result.RetrievalMode = "hybrid_partial"
			}
		}
	}
	candidates := map[string]Hit{}
	for _, h := range result.LexicalCandidates {
		h.RRFScore = 1 / float64(60+h.LexicalRank)
		candidates[h.EvidenceID] = h
	}
	for _, h := range result.DenseCandidates {
		existing, ok := candidates[h.EvidenceID]
		if !ok {
			existing = h
		}
		existing.DenseRank = h.DenseRank
		existing.Cosine = h.Cosine
		existing.RRFScore += 1 / float64(60+h.DenseRank)
		candidates[h.EvidenceID] = existing
	}
	fused := make([]Hit, 0, len(candidates))
	for _, h := range candidates {
		fused = append(fused, h)
	}
	sort.Slice(fused, func(i, j int) bool {
		if fused[i].RRFScore != fused[j].RRFScore {
			return fused[i].RRFScore > fused[j].RRFScore
		}
		return fused[i].EvidenceID < fused[j].EvidenceID
	})
	used := 0
	for _, h := range fused {
		duplicate := false
		for _, chosen := range result.Hits {
			if h.PaperVersionID == chosen.PaperVersionID && h.Text == chosen.Text {
				duplicate = true
				break
			}
			if h.DocumentID == chosen.DocumentID {
				overlap := min(h.CharEnd, chosen.CharEnd) - max(h.CharStart, chosen.CharStart)
				shorter := min(h.CharEnd-h.CharStart, chosen.CharEnd-chosen.CharStart)
				if overlap > 0 && overlap*2 >= shorter {
					duplicate = true
					break
				}
			}
		}
		n := utf8.RuneCountInString(h.Text)
		if duplicate || used+n > ContextRunes {
			continue
		}
		used += n
		result.Hits = append(result.Hits, h)
		if len(result.Hits) == req.Limit {
			break
		}
	}
	return result, nil
}

func (s *Service) loadVectors(ctx context.Context, userID string, papers []string, items []chunk, id embeddings.Identity) error {
	positions := map[string]int{}
	for i, c := range items {
		positions[c.hit.EvidenceID] = i
	}
	where, args := scopeSQL(userID, papers, "")
	args = append(args, id.Model, id.Digest, id.EncodingVersion, id.Dimensions)
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,e.chunking_version,e.input_hash,e.vector_blob`+hitJoins+`JOIN chunk_embeddings e ON e.chunk_id=c.id WHERE `+where+` AND e.model_id=? AND e.model_digest=? AND e.encoding_version=? AND e.dimensions=?`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var evidence, chunking, hash string
		var blob []byte
		if err = rows.Scan(&evidence, &chunking, &hash, &blob); err != nil {
			return err
		}
		pos, ok := positions[evidence]
		if !ok {
			return fmt.Errorf("retrieval scope changed during search")
		}
		c := &items[pos]
		if c.chunking == chunking && c.hash == hash {
			if v, ok := readVector(blob); ok {
				c.vector = v
			}
		}
	}
	return rows.Err()
}
