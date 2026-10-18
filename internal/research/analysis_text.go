package research

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"paper-scout/internal/documents"
)

type analysisPacket struct {
	Start    int        `json:"char_start"`
	End      int        `json:"char_end"`
	Passages []Evidence `json:"passages"`
}

func hashInput(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func newAnalysis(id, runID string, d documents.Document) Analysis {
	return Analysis{ID: id, RunID: runID, DocumentID: d.ID, PaperVersionID: d.PaperVersionID, Status: "none", ExtractionStatus: d.ExtractionStatus, Warnings: d.Warnings, TotalRunes: len([]rune(d.Text)), SuccessfulRanges: []Range{}, Observations: []Observations{}, Evidence: []Evidence{}}
}

func evidence(d documents.Document, c documents.Chunk, start, end int) Evidence {
	url := d.SourceURL
	if d.Format == "pdf" && c.PageStart > 0 {
		url = fmt.Sprintf("%s#page=%d", url, c.PageStart)
	}
	return Evidence{ID: c.ID, DocumentID: d.ID, Text: string([]rune(d.Text)[start:end]), Start: start, End: end, PageStart: c.PageStart, PageEnd: c.PageEnd, Section: c.Section, SourceURL: url}
}

// Parts partition the exact document text. Labels are existing evidence chunks;
// overlapping retrieval chunks are clipped, never omitted or counted twice.
func packets(d documents.Document, limit int) ([]analysisPacket, error) {
	total := len([]rune(d.Text))
	out := []analysisPacket{}
	for start := 0; start < total; {
		end := min(start+limit, total)
		if end < total {
			boundary := end
			best := start
			for _, s := range d.Segments {
				if s.CharEnd > best && s.CharEnd <= boundary {
					best = s.CharEnd
				}
			}
			if best > start {
				end = best
			}
		}
		p := analysisPacket{Start: start, End: end, Passages: []Evidence{}}
		cursor := start
		for _, c := range d.Chunks {
			if c.CharEnd <= cursor {
				continue
			}
			if c.CharStart >= end {
				break
			}
			if c.CharStart > cursor {
				return nil, errors.New("analysis: text has no evidence")
			}
			stop := min(c.CharEnd, end)
			p.Passages = append(p.Passages, evidence(d, c, cursor, stop))
			cursor = stop
			if cursor == end {
				break
			}
		}
		if cursor != end {
			return nil, errors.New("analysis: text has no evidence")
		}
		out = append(out, p)
		start = end
	}
	return out, nil
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
