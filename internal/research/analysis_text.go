package research

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"paper-scout/internal/documents"
)

type analysisPacket struct {
	Start    int        `json:"char_start"`
	End      int        `json:"char_end"`
	Passages []Evidence `json:"passages"`
}

func hashInput(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func newAnalysis(id, runID string, d documents.Document) Analysis {
	return Analysis{ID: id, RunID: runID, DocumentID: d.ID, PaperVersionID: d.PaperVersionID, Status: "none", ExtractionStatus: d.ExtractionStatus, Warnings: d.Warnings, TotalRunes: utf8.RuneCountInString(d.Text), SuccessfulRanges: []Range{}, Observations: []Observations{}, Evidence: []Evidence{}}
}

func evidence(d documents.Document, c documents.Chunk, start, end int) Evidence {
	url := d.SourceURL
	if d.Format == "pdf" && c.PageStart > 0 {
		url = fmt.Sprintf("%s#page=%d", url, c.PageStart)
	}
	// Full evidence reuses the canonical chunk string. Only boundary clips
	// decode runes, bounded by the chunk size rather than document size.
	text := c.Text
	if start != c.CharStart || end != c.CharEnd {
		text = string([]rune(c.Text)[start-c.CharStart : end-c.CharStart])
	}
	return Evidence{ID: c.ID, DocumentID: d.ID, Text: text, Start: start, End: end, PageStart: c.PageStart, PageEnd: c.PageEnd, Section: c.Section, SourceURL: url}
}

// packetReader materializes only the next requested range. Both metadata
// cursors move forward, so untouched document tails incur no preparation work.
// Overlapping retrieval chunks are clipped, never omitted or counted twice.
type packetReader struct {
	document              documents.Document
	total, limit          int
	start, segment, chunk int
}

func (r *packetReader) next(ctx context.Context) (analysisPacket, error) {
	if err := ctx.Err(); err != nil {
		return analysisPacket{}, err
	}
	end := min(r.start+r.limit, r.total)
	if end < r.total {
		best := r.start
		for r.segment < len(r.document.Segments) {
			if err := ctx.Err(); err != nil {
				return analysisPacket{}, err
			}
			boundary := r.document.Segments[r.segment].CharEnd
			if boundary > end {
				break
			}
			if boundary > best {
				best = boundary
			}
			r.segment++
		}
		if best > r.start {
			end = best
		}
	}
	p := analysisPacket{Start: r.start, End: end, Passages: []Evidence{}}
	cursor := r.start
	for cursor < end && r.chunk < len(r.document.Chunks) {
		if err := ctx.Err(); err != nil {
			return analysisPacket{}, err
		}
		c := r.document.Chunks[r.chunk]
		if c.CharEnd <= cursor {
			r.chunk++
			continue
		}
		if c.CharStart > cursor {
			return analysisPacket{}, errors.New("analysis: text has no evidence")
		}
		stop := min(c.CharEnd, end)
		p.Passages = append(p.Passages, evidence(r.document, c, cursor, stop))
		cursor = stop
		if cursor == c.CharEnd {
			r.chunk++
		}
	}
	if cursor != end {
		return analysisPacket{}, errors.New("analysis: text has no evidence")
	}
	if err := ctx.Err(); err != nil {
		return analysisPacket{}, err
	}
	r.start = end
	return p, nil
}

func parseObservations(content *string, allowed map[string]bool) (*Observations, error) {
	if content == nil {
		return nil, errors.New("invalid_analysis")
	}
	text := strings.TrimSpace(*content)
	if strings.HasPrefix(text, "```json\n") && strings.HasSuffix(text, "```") {
		text = strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "```")
	}
	var obs Observations
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&obs); err != nil {
		return nil, errors.New("invalid_analysis")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || obs.Methods == nil || obs.Results == nil || obs.Limitations == nil {
		return nil, errors.New("invalid_analysis")
	}
	claims := append(append(append([]Claim{}, obs.Methods...), obs.Results...), obs.Limitations...)
	if len(claims) > 24 {
		return nil, errors.New("invalid_analysis")
	}
	for _, c := range claims {
		if strings.TrimSpace(c.Text) == "" || len([]rune(c.Text)) > 2000 || len(c.EvidenceIDs) == 0 || len(c.EvidenceIDs) > 8 {
			return nil, errors.New("invalid_analysis")
		}
		for _, id := range c.EvidenceIDs {
			if !allowed[id] {
				return nil, errors.New("invalid_evidence")
			}
		}
	}
	return &obs, nil
}

func cited(obs Observations) []string {
	seen := map[string]bool{}
	ids := []string{}
	for _, group := range [][]Claim{obs.Methods, obs.Results, obs.Limitations} {
		for _, claim := range group {
			for _, id := range claim.EvidenceIDs {
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

func finalizeCoverage(out *Analysis, d documents.Document, finished bool) {
	ranges := append([]Range{}, out.SuccessfulRanges...)
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
	out.CoveredRunes = 0
	end := 0
	for _, r := range ranges {
		if r.End > end {
			out.CoveredRunes += r.End - max(r.Start, end)
			end = r.End
		}
	}
	if finished && out.ErrorCode == "" && out.TotalRunes > 0 && out.CoveredRunes == out.TotalRunes {
		if d.ExtractionStatus == "full_text" {
			out.Status = "complete"
		} else if d.ExtractionStatus == "abstract_only" {
			out.Status = "abstract_only"
		}
	}
	ids := map[string]bool{}
	for _, obs := range out.Observations {
		for _, id := range cited(obs) {
			ids[id] = true
		}
	}
	if out.Synthesis != nil {
		for _, id := range cited(*out.Synthesis) {
			ids[id] = true
		}
	}
	out.Evidence = []Evidence{}
	for _, c := range d.Chunks {
		if ids[c.ID] {
			out.Evidence = append(out.Evidence, evidence(d, c, c.CharStart, c.CharEnd))
		}
	}
}
