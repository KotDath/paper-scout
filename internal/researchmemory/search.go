package researchmemory

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"paper-scout/internal/embeddings"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type passage struct {
	id, title, text, inputHash string
	revision                   int
	hit                        Hit
	vector                     []float32
}
type IndexResult struct {
	Attempted int      `json:"attempted"`
	Published int      `json:"published"`
	Reasons   []string `json:"reasons"`
}

func bytesVector(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}
func readVector(b []byte) ([]float32, bool) {
	if len(b) != 4*embeddings.Dimensions {
		return nil, false
	}
	v := make([]float32, embeddings.Dimensions)
	norm := 0.
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
		x := float64(v[i])
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return nil, false
		}
		norm += x * x
	}
	return v, math.Abs(norm-1) < 1e-4
}
func embedCode(err error) string {
	var e *embeddings.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "embedding_unavailable"
}
func (s *Service) load(ctx context.Context, q querier, owner string, req SearchRequest) ([]passage, error) {
	where, args := scope(owner, req)
	rows, err := q.QueryContext(ctx, `SELECT p.id,p.text,p.input_hash,p.revision,p.char_start,p.char_end,m.id,m.kind,m.facet,m.paper_id,m.target_id,m.title,m.content_hash,m.revision,m.state FROM research_memory_passages p JOIN research_memories m ON m.id=p.memory_id WHERE `+where+` ORDER BY p.id LIMIT 20001`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []passage{}
	for rows.Next() {
		var p passage
		if err = rows.Scan(&p.id, &p.text, &p.inputHash, &p.revision, &p.hit.CharStart, &p.hit.CharEnd, &p.hit.MemoryID, &p.hit.Kind, &p.hit.Facet, &p.hit.PaperVersionID, &p.hit.TargetID, &p.title, &p.hit.ContentHash, &p.hit.Revision, &p.hit.State); err != nil {
			return nil, err
		}
		if p.hit.Kind != "claim" {
			p.hit.Facet = ""
		}
		p.hit.Title = p.title
		p.hit.Text = p.text
		p.hit.Authority = "model"
		if p.hit.Kind == "episode" {
			p.hit.Authority = "research_history"
		}
		if p.hit.Kind == "user_note" {
			p.hit.Authority = "user"
		}
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > MaxScopePassages {
		return nil, ErrScope
	}
	return out, nil
}
func (s *Service) vectors(ctx context.Context, q querier, items []passage) error {
	if s.embed == nil {
		return nil
	}
	id := s.embed.Identity()
	// Owner scope was already applied by load; never load vectors from another
	// owner even when two private strings happen to hash identically.
	for i := range items {
		p := &items[i]
		var blob []byte
		err := q.QueryRowContext(ctx, `SELECT vector_blob FROM research_memory_vectors WHERE passage_id=? AND model_id=? AND model_digest=? AND encoding_version=? AND dimensions=? AND input_hash=?`, p.id, id.Model, id.Digest, id.EncodingVersion, id.Dimensions, p.inputHash).Scan(&blob)
		if err == nil {
			if v, ok := readVector(blob); ok {
				p.vector = v
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

// Index encodes at most one 32-passage batch. Publication compares the current
// passage hash AND revision; synchronous note invalidation defeats stale writes.
func (s *Service) Index(ctx context.Context, owner string, req SearchRequest) (IndexResult, error) {
	out := IndexResult{Reasons: []string{}}
	if req.Before.IsZero() {
		req.Before = time.Now().UTC()
	}
	if s.embed == nil {
		out.Reasons = append(out.Reasons, "embedding_unavailable")
		return out, nil
	}
	items, err := s.load(ctx, s.db, owner, req)
	if err != nil {
		return out, err
	}
	if err = s.vectors(ctx, s.db, items); err != nil {
		return out, err
	}
	batch := []passage{}
	inputs := []string{}
	for _, p := range items {
		if p.vector == nil {
			batch = append(batch, p)
			inputs = append(inputs, embeddingsInput(p.title, p.text))
			if len(batch) == 32 {
				break
			}
		}
	}
	if len(batch) == 0 {
		return out, nil
	}
	out.Attempted = len(batch)
	result, err := s.embed.Embed(ctx, embeddings.Document, inputs)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		out.Reasons = append(out.Reasons, embedCode(err))
		return out, nil
	}
	if result.Identity != s.embed.Identity() {
		out.Reasons = append(out.Reasons, "embedding_incompatible")
		return out, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	for i, p := range batch {
		id := result.Identity
		res, e := tx.ExecContext(ctx, `INSERT INTO research_memory_vectors(passage_id,model_id,model_digest,encoding_version,dimensions,input_hash,vector_blob) SELECT p.id,?,?,?,?,?,? FROM research_memory_passages p JOIN research_memories m ON m.id=p.memory_id WHERE p.id=? AND p.input_hash=? AND p.revision=? AND m.revision=? AND m.owner=? ON CONFLICT(passage_id) DO UPDATE SET model_id=excluded.model_id,model_digest=excluded.model_digest,encoding_version=excluded.encoding_version,dimensions=excluded.dimensions,input_hash=excluded.input_hash,vector_blob=excluded.vector_blob`, id.Model, id.Digest, id.EncodingVersion, id.Dimensions, p.inputHash, bytesVector(result.Vectors[i]), p.id, p.inputHash, p.revision, p.revision, owner)
		if e != nil {
			return out, e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return out, e
		}
		out.Published += int(n)
	}
	if out.Published < out.Attempted {
		out.Reasons = append(out.Reasons, "source_changed")
	}
	return out, tx.Commit()
}
func matchQuery(text string) string {
	tokens := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	parts := []string{}
	for _, t := range unique(tokens) {
		parts = append(parts, `"`+strings.ToLower(t)+`"`)
		if len(parts) == 64 {
			break
		}
	}
	return strings.Join(parts, " OR ")
}
func (s *Service) Search(ctx context.Context, owner string, req SearchRequest) (SearchResult, error) {
	out := SearchResult{Query: req.Query, RetrievalMode: "lexical_only", Reasons: []string{}, Hits: []Hit{}, LexicalCandidates: []Hit{}, DenseCandidates: []Hit{}}
	if owner == "" || !utf8.ValidString(req.Query) || strings.TrimSpace(req.Query) == "" || utf8.RuneCountInString(req.Query) > 4000 || req.Limit < 0 || req.Limit > 5 {
		return out, ErrInvalid
	}
	if req.Limit == 0 {
		req.Limit = 5
	}
	refresh, err := s.Refresh(ctx, owner)
	out.Refresh = refresh
	if err != nil {
		return out, err
	}
	out.Reasons = append(out.Reasons, refresh.Reasons...)
	if req.Before.IsZero() {
		req.Before = time.Now().UTC()
	}
	index, err := s.Index(ctx, owner, req)
	if err != nil {
		return out, err
	}
	out.Reasons = append(out.Reasons, index.Reasons...)
	var query []float32
	if s.embed != nil {
		r, e := s.embed.Embed(ctx, embeddings.Query, []string{req.Query})
		if e != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			out.Reasons = append(out.Reasons, embedCode(e))
		} else if r.Identity != s.embed.Identity() {
			out.Reasons = append(out.Reasons, "embedding_incompatible")
		} else {
			query = r.Vectors[0]
		}
	}
	// Read a fresh consistent view AFTER inference. Concurrent edits/exclusions
	// cannot sneak into either public candidate list or final recalled context.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	items, err := s.load(ctx, tx, owner, req)
	if err != nil {
		return out, err
	}
	if err = s.vectors(ctx, tx, items); err != nil {
		return out, err
	}
	byID := map[string]passage{}
	indexed := 0
	for _, p := range items {
		byID[p.id] = p
		if p.vector != nil {
			indexed++
		}
	}
	if len(items) > 0 {
		out.IndexCoverage = float64(indexed) / float64(len(items))
	}
	where, args := scope(owner, req)
	if match := matchQuery(req.Query); match != "" {
		args = append(args, match)
		rows, e := tx.QueryContext(ctx, `SELECT p.id,bm25(research_memory_fts) FROM research_memory_passages p JOIN research_memories m ON m.id=p.memory_id JOIN research_memory_fts ON research_memory_fts.passage_id=p.id WHERE `+where+` AND research_memory_fts MATCH ? ORDER BY bm25(research_memory_fts),p.id LIMIT 20`, args...)
		if e != nil {
			return out, e
		}
		for rows.Next() {
			var id string
			var score float64
			if e = rows.Scan(&id, &score); e != nil {
				break
			}
			h := byID[id].hit
			h.BM25 = score
			h.LexicalRank = len(out.LexicalCandidates) + 1
			out.LexicalCandidates = append(out.LexicalCandidates, h)
		}
		re := rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
		if re != nil {
			return out, re
		}
	}
	if query != nil {
		out.RetrievalMode = "hybrid"
		if indexed < len(items) {
			out.RetrievalMode = "hybrid_partial"
			out.Reasons = append(out.Reasons, "index_incomplete_or_incompatible")
		}
		for _, p := range items {
			if p.vector == nil {
				continue
			}
			h := p.hit
			for i, v := range p.vector {
				h.Cosine += float64(v) * float64(query[i])
			}
			out.DenseCandidates = append(out.DenseCandidates, h)
		}
		sort.Slice(out.DenseCandidates, func(i, j int) bool {
			a, b := out.DenseCandidates[i], out.DenseCandidates[j]
			if a.Cosine != b.Cosine {
				return a.Cosine > b.Cosine
			}
			return hitKey(a) < hitKey(b)
		})
		if len(out.DenseCandidates) > 20 {
			out.DenseCandidates = out.DenseCandidates[:20]
		}
		for i := range out.DenseCandidates {
			out.DenseCandidates[i].DenseRank = i + 1
		}
	}
	fused := map[string]Hit{}
	for _, h := range out.LexicalCandidates {
		h.RRFScore = 1 / float64(60+h.LexicalRank)
		fused[hitKey(h)] = h
	}
	for _, h := range out.DenseCandidates {
		k := hitKey(h)
		v, ok := fused[k]
		if !ok {
			v = h
		}
		v.DenseRank = h.DenseRank
		v.Cosine = h.Cosine
		v.RRFScore += 1 / float64(60+h.DenseRank)
		fused[k] = v
	}
	ranked := []Hit{}
	for _, h := range fused {
		ranked = append(ranked, h)
	}
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.RRFScore != b.RRFScore {
			return a.RRFScore > b.RRFScore
		}
		return hitKey(a) < hitKey(b)
	})
	// Decorate candidate provenance too; BM25/dense never become scientific proof.
	decorateHit := func(h *Hit) error {
		oo, e := origins(ctx, tx, owner, h.MemoryID, req)
		if e != nil {
			return e
		}
		decorate(h, oo)
		return nil
	}
	for i := range out.LexicalCandidates {
		if err = decorateHit(&out.LexicalCandidates[i]); err != nil {
			return out, err
		}
	}
	for i := range out.DenseCandidates {
		if err = decorateHit(&out.DenseCandidates[i]); err != nil {
			return out, err
		}
	}
	for i := range out.LexicalCandidates {
		if len(out.LexicalCandidates[i].Origins) > 5 {
			out.LexicalCandidates[i].Origins = out.LexicalCandidates[i].Origins[:5]
			out.Reasons = append(out.Reasons, "origin_budget")
		}
	}
	for i := range out.DenseCandidates {
		if len(out.DenseCandidates[i].Origins) > 5 {
			out.DenseCandidates[i].Origins = out.DenseCandidates[i].Origins[:5]
			out.Reasons = append(out.Reasons, "origin_budget")
		}
	}
	used := 0
	selected := map[string]bool{}
	emitted := map[string]bool{}
	groups := 0
	usedOrigins := map[string]bool{}
	for _, h := range ranked {
		if emitted[h.MemoryID] {
			continue
		}
		if h.Kind == "user_note" {
			entry, e := loadEntry(ctx, tx, owner, h.MemoryID)
			if e != nil {
				return out, e
			}
			h.Text = entry.Text
			h.CharStart = 0
			h.CharEnd = utf8.RuneCountInString(h.Text)
		}
		group := h.MemoryID
		if h.PaperVersionID != "" {
			group = "paper:" + h.PaperVersionID
		}
		if selected[group] {
			continue
		}
		if err = decorateHit(&h); err != nil {
			return out, err
		}
		eligible := []Origin{}
		newOrigins := map[string]bool{}
		for _, o := range h.Origins {
			if usedOrigins[o.ReportID] || newOrigins[o.ReportID] || len(usedOrigins)+len(newOrigins) < 5 {
				eligible = append(eligible, o)
				if !usedOrigins[o.ReportID] {
					newOrigins[o.ReportID] = true
				}
			}
		}
		if len(h.Origins) > len(eligible) {
			out.Reasons = append(out.Reasons, "origin_budget")
		}
		if len(h.Origins) > 0 && len(eligible) == 0 {
			continue
		}
		h.ReportID = ""
		h.ConversationID = ""
		decorate(&h, eligible)
		// Episode and claim from the same report do not occupy multiple origins.
		originGroup := ""
		if len(h.Origins) > 0 {
			originGroup = h.Origins[0].ReportID
		}
		if originGroup != "" && selected["report:"+originGroup] {
			continue
		}
		ann, e := annotations(ctx, tx, owner, h, req, false)
		if e != nil {
			return out, e
		}
		cost := utf8.RuneCountInString(h.Text)
		for _, a := range ann {
			if !emitted[a.MemoryID] {
				cost += utf8.RuneCountInString(a.Text)
			}
		}
		if used+cost > ContextRunes {
			out.Reasons = append(out.Reasons, "context_budget")
			continue
		}
		used += cost
		out.Hits = append(out.Hits, h)
		emitted[h.MemoryID] = true
		for _, a := range ann {
			if !emitted[a.MemoryID] {
				out.Hits = append(out.Hits, a)
				emitted[a.MemoryID] = true
			}
		}
		groups++
		for k := range newOrigins {
			usedOrigins[k] = true
		}
		selected[group] = true
		if originGroup != "" {
			selected["report:"+originGroup] = true
		}
		if groups == req.Limit {
			break
		}
	}
	out.Reasons = unique(out.Reasons)
	return out, tx.Commit()
}
func hitKey(h Hit) string { return h.MemoryID + ":" + strconv.Itoa(h.CharStart) }
