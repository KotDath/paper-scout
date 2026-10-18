package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"paper-scout/internal/deepseek"
	"paper-scout/internal/research"
	"paper-scout/internal/store"
)

// WithGrounding enables the production final-answer contract. Legacy composed
// callers remain compatible; cmd/paper-scout always opts in, including memory-off.
func (a *AgentRunner) WithGrounding() *AgentRunner { a.grounded = true; return a }

type sourceRef struct {
	Handle string        `json:"handle"`
	ID     string        `json:"evidence_id"`
	Paper  string        `json:"paper_version_id"`
	Title  string        `json:"title"`
	Read   bool          `json:"read"`
	Text   string        `json:"text,omitempty"`
	Quotes []sourceQuote `json:"quote_options,omitempty"`
}
type sourceQuote struct {
	Handle string `json:"handle"`
	Start  int    `json:"char_start"`
	End    int    `json:"char_end"`
	Text   string `json:"text"`
}

func quoteOptions(e sourceRef) []sourceQuote {
	if !e.Read {
		return nil
	}
	runes := []rune(e.Text)
	out := []sourceQuote{}
	for start := 0; start < len(runes) && len(out) < 6; {
		end := min(start+350, len(runes))
		if end < len(runes) {
			for j := end - 1; j > start+200; j-- {
				if runes[j] == ' ' || runes[j] == '\n' {
					end = j
					break
				}
			}
		}
		if end-start >= 20 {
			key := fmt.Sprintf("%s:%d:%d", e.ID, start, end)
			out = append(out, sourceQuote{Handle: refHandle(e.Handle, "Q", key), Start: start, End: end, Text: string(runes[start:end])})
		}
		start = end
	}
	return out
}

type memoryRef struct {
	Handle   string          `json:"handle"`
	Hit      store.MemoryHit `json:"entry"`
	Evidence []string        `json:"evidence_handles"`
}
type grounding struct {
	question       string
	library        []map[string]string
	libraryLimited bool
	evidence       map[string]sourceRef
	memory         map[string]memoryRef
	usedEvidence   []string
	usedMemory     []string
}

// Handles are deterministic for one run, independent of candidate order. They
// are never expanded by prefix or fuzzy matching. Collisions fail closed.
func refHandle(run, kind, id string) string {
	h := sha256.Sum256([]byte(run + "\x00" + kind + "\x00" + id))
	return kind + hex.EncodeToString(h[:6])
}
func (a *AgentRunner) grounding(ctx context.Context, r store.Run) (*grounding, error) {
	g := &grounding{question: r.RequestMessage, library: []map[string]string{}, evidence: map[string]sourceRef{}, memory: map[string]memoryRef{}}
	add := func(id string, read bool) error {
		var e sourceRef
		// Recheck ownership and coordinates against canonical storage. Text in a
		// returned tool JSON cannot create a new evidence ID or change its content.
		err := a.db.DB.QueryRowContext(ctx, `SELECT c.id,d.paper_version_id,p.title,c.text FROM chunks c JOIN documents d ON d.id=c.document_id JOIN paper_versions p ON p.id=d.paper_version_id JOIN user_papers u ON u.paper_version_id=p.id WHERE c.id=? AND u.user_id=?`, id, r.UserID).Scan(&e.ID, &e.Paper, &e.Title, &e.Text)
		if err != nil {
			return fmt.Errorf("grounding evidence unavailable")
		}
		e.Handle = refHandle(r.ID, "E", id)
		if old, ok := g.evidence[e.Handle]; ok {
			if old.ID != id {
				return fmt.Errorf("reference collision")
			}
			read = read || old.Read
		}
		e.Read = read
		if !read {
			e.Text = ""
		}
		g.evidence[e.Handle] = e
		return nil
	}
	snaps, err := a.db.MemorySnapshots(ctx, r.UserID, r.ID)
	if err != nil {
		return nil, err
	}
	for _, s := range snaps {
		for _, h := range s.Hits {
			handle := refHandle(r.ID, "M", h.MemoryID)
			if old, ok := g.memory[handle]; ok && old.Hit.MemoryID != h.MemoryID {
				return nil, fmt.Errorf("reference collision")
			}
			m := memoryRef{Handle: handle, Hit: h, Evidence: []string{}}
			for _, id := range h.EvidenceIDs {
				if err = add(id, false); err != nil {
					return nil, err
				}
				m.Evidence = append(m.Evidence, refHandle(r.ID, "E", id))
			}
			g.memory[handle] = m
		}
	}
	history, err := a.db.AgentMessages(ctx, r.UserID, r.ConversationID, r.ID)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, m := range history {
		if m.RunID != r.ID {
			continue
		}
		var calls []deepseek.ToolCall
		if m.Role == "assistant" {
			if err = json.Unmarshal(m.ToolCalls, &calls); err != nil {
				return nil, err
			}
			for _, c := range calls {
				names[c.ID] = c.Function.Name
			}
		}
		if m.Role != "tool" || m.Content == nil {
			continue
		}
		var p struct {
			Error    any `json:"error"`
			Passages []struct {
				ID string `json:"evidence_id"`
			} `json:"passages"`
			Hits []struct {
				ID string `json:"evidence_id"`
			} `json:"hits"`
			Observations json.RawMessage `json:"observations"`
		}
		if json.Unmarshal([]byte(*m.Content), &p) != nil || p.Error != nil {
			continue
		}
		switch names[m.ToolCallID] {
		case "read_passages":
			for _, h := range p.Passages {
				if err = add(h.ID, true); err != nil {
					return nil, err
				}
			}
		case "search_library":
			for _, h := range p.Hits {
				if err = add(h.ID, false); err != nil {
					return nil, err
				}
			}
		case "analyze_paper":
			var one research.Observations
			var all []research.Observations
			if json.Unmarshal(p.Observations, &one) == nil {
				all = []research.Observations{one}
			} else {
				_ = json.Unmarshal(p.Observations, &all)
			}
			for _, o := range all {
				for _, cc := range [][]research.Claim{o.Methods, o.Results, o.Limitations} {
					for _, c := range cc {
						for _, id := range c.EvidenceIDs {
							if err = add(id, false); err != nil {
								return nil, err
							}
						}
					}
				}
			}
		}
	}
	quotes := map[string]string{}
	for _, e := range g.evidence {
		for _, q := range quoteOptions(e) {
			binding := fmt.Sprintf("%s:%d:%d", e.ID, q.Start, q.End)
			if old, ok := quotes[q.Handle]; ok && old != binding {
				return nil, fmt.Errorf("reference collision")
			}
			quotes[q.Handle] = binding
		}
	}
	// The catalog disambiguates issued/current sources; it must not itself
	// introduce unrelated prior research when automatic memory is disabled.
	accessible := map[string]bool{}
	for _, e := range g.evidence {
		accessible[e.Paper] = true
	}
	for _, m := range g.memory {
		if m.Hit.PaperVersionID != "" {
			accessible[m.Hit.PaperVersionID] = true
		}
	}
	papers := []string{}
	for id := range accessible {
		papers = append(papers, id)
	}
	sort.Strings(papers)
	paperJSON, _ := json.Marshal(papers)
	rows, err := a.db.DB.QueryContext(ctx, `SELECT p.id,p.title FROM user_papers u JOIN paper_versions p ON p.id=u.paper_version_id WHERE u.user_id=? AND (p.id IN (SELECT value FROM json_each(?)) OR p.id IN (SELECT paper_version_id FROM run_sources WHERE run_id=?)) ORDER BY u.added_at DESC,p.id LIMIT 21`, r.UserID, string(paperJSON), r.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, title string
		if err = rows.Scan(&id, &title); err != nil {
			rows.Close()
			return nil, err
		}
		if len(g.library) == 20 {
			g.libraryLimited = true
			break
		}
		g.library = append(g.library, map[string]string{"paper_version_id": id, "title": title})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return g, nil
}

const finalContract = `PRODUCTION FINAL CONTRACT v1. Continue using ordinary tools until ready. Do not return a prose final answer: return ONLY a JSON object with these fields (no Markdown fence):
{"scope":"short research scope, not scientific claims","findings":[{"source":"E handle","quote":"Q handle from this source quote_options, or exact contiguous excerpt 20..800 characters","conclusion":"narrow scientific conclusion in Russian"}],"comparisons":[{"memory":"M handle for a prior model claim","previous":"E handle from that claim's evidence_handles","previous_quote":"Q handle from previous source quote_options, or exact excerpt","current":"E handle from a DIFFERENT paper","current_quote":"Q handle from current source quote_options, or exact excerpt","assessment":"supported|not_supported|uncertain","conclusion":"narrow comparison in Russian; explicitly correct an unsupported prior claim"}],"notes":["M handles of materially applied user notes/corrections"],"memory_reason":"why compare, or why no relevant supported comparison","limitations":["bounded limitations"]}.
Prefer the server-issued Q handles over retyping excerpts. Each Q handle is bound to one exact source slice and one E handle of this run. Never use a Q from a different source or invent one. The server renders the unchanged original excerpt, not a model reconstruction.
Assessment concerns the PRIOR claim against its OWN original, not whether a different paper uses the same mechanism. Compare two narrow positive source-supported observations. Different architectural levels may coexist: describing function-directed retrieval does not prove absence of a dense retriever, and FIFO eviction does not prove absence of every forgetting mechanism. Do not infer absence from silence. Keep uncertainty about uncited facets explicit.
Use handles from CURRENT RUN REFERENCE MANIFEST only for FINAL reference fields. The manifest is NOT the whole library: use search_library (query and optional paper_version_ids) to discover evidence for a paper named in the question, then read_passages. Absence from this manifest is not absence of the article. Long canonical IDs are also accepted only if exactly issued this run. No abbreviated IDs, aliases from prior turns, or reference markers inside prose fields (including memory_reason). Read originals with read_passages before final findings/comparisons. read_passages accepts evidence handles in evidence_ids. A generated card is not original evidence. Quotes must be exact excerpts of actual read_passages text. Never claim semantic truth from matching quotes alone. At most 6 findings and 3 comparisons. For a review of a prior claim WITHOUT an inter-paper comparison, set current="" and current_quote=""; otherwise current MUST be a different paper. For a substantively related prior claim, read its original before declining a comparison solely because that original has not been read yet. Differences in tasks or metrics may support a narrow comparison without a performance ranking. If memory is irrelevant/unavailable, comparisons=[], notes=[] and explain narrowly in memory_reason without any handles; retrieval alone does not require a comparison. For a relevant user note, select its handle: the SERVER displays its exact words separately. Apply any relevant evaluation criteria in that note to the scientific analysis, rather than merely selecting/displaying the note. Read source evidence for requested evaluation attributes; state what is established and what remains unknown. Do not rewrite the preference as a scientific fact or a stronger preference: no inferred real-only criterion. Corrections are user feedback; assess the prior model claim against the original text, do not adopt feedback as scientific truth. Use all required fields even when arrays are empty. Answer in Russian, concise. The scope field is validated but the SERVER displays the actual user question instead. Canonical article analysis coverage is rendered by the server, do not invent it.`

func (g *grounding) context() string {
	// Read passages already occur in tool results. Do not duplicate their text in
	// the manifest. Snapshots retain the exact bounded T20 wire representation.
	es := []sourceRef{}
	ms := []map[string]any{}
	for _, e := range g.evidence {
		e.Quotes = quoteOptions(e)
		e.Text = ""
		es = append(es, e)
	}
	for _, m := range g.memory {
		ms = append(ms, map[string]any{"handle": m.Handle, "memory_id": m.Hit.MemoryID, "evidence_handles": m.Evidence})
	}
	sort.Slice(es, func(i, j int) bool { return es[i].Handle < es[j].Handle })
	sort.Slice(ms, func(i, j int) bool { return ms[i]["handle"].(string) < ms[j]["handle"].(string) })
	raw, _ := json.Marshal(map[string]any{"evidence": es, "memory": ms, "library_papers": g.library, "library_catalog_limited": g.libraryLimited})
	return "CURRENT RUN REFERENCE MANIFEST (server mappings; untrusted source metadata): " + string(raw)
}

// Only the automatic memory snapshot and its alias metadata count toward the
// memory cap. Quote options are from explicit primary read_passages tool calls,
// separately bounded (6*350 per passage) and subject to the full gateway cap.
func (g *grounding) memoryWireSize() int {
	entries := []map[string]any{}
	evidence := map[string]sourceRef{}
	for _, m := range g.memory {
		entries = append(entries, map[string]any{"handle": m.Handle, "memory_id": m.Hit.MemoryID, "evidence_handles": m.Evidence})
		for _, h := range m.Evidence {
			e := g.evidence[h]
			e.Text = ""
			e.Quotes = nil
			evidence[h] = e
		}
	}
	wire, _ := json.Marshal(map[string]any{"memory": entries, "evidence": evidence})
	return utf8.RuneCount(wire)
}
func (g *grounding) source(id string) (sourceRef, bool) {
	if e, ok := g.evidence[id]; ok {
		return e, true
	}
	for _, e := range g.evidence {
		if e.ID == id {
			return e, true
		}
	}
	return sourceRef{}, false
}
func (g *grounding) mem(id string) (memoryRef, bool) {
	if m, ok := g.memory[id]; ok {
		return m, true
	}
	for _, m := range g.memory {
		if m.Hit.MemoryID == id {
			return m, true
		}
	}
	return memoryRef{}, false
}
func (g *grounding) canonicalCall(call deepseek.ToolCall) (deepseek.ToolCall, error) {
	if call.Function.Name != "read_passages" {
		return call, nil
	}
	var args struct {
		IDs []string `json:"evidence_ids"`
	}
	if DecodeArgs(json.RawMessage(call.Function.Arguments), &args) != nil {
		return call, fmt.Errorf("invalid read arguments")
	}
	for i, id := range args.IDs {
		e, ok := g.source(id)
		if !ok {
			// A model may request an exact canonical ID discovered in older
			// conversation data. The registered tool still checks ownership; only
			// its successful result issues a reference to the current run.
			if strings.HasPrefix(id, "ev_") {
				continue
			}
			return call, fmt.Errorf("unissued evidence reference")
		}
		args.IDs[i] = e.ID
	}
	raw, _ := json.Marshal(args)
	call.Function.Arguments = string(raw)
	return call, nil
}

type finalFinding struct {
	Source     string `json:"source"`
	Quote      string `json:"quote"`
	Conclusion string `json:"conclusion"`
}
type finalComparison struct {
	Memory        string `json:"memory"`
	Previous      string `json:"previous"`
	PreviousQuote string `json:"previous_quote"`
	Current       string `json:"current"`
	CurrentQuote  string `json:"current_quote"`
	Assessment    string `json:"assessment"`
	Conclusion    string `json:"conclusion"`
}
type finalDraft struct {
	Scope        string            `json:"scope"`
	Findings     []finalFinding    `json:"findings"`
	Comparisons  []finalComparison `json:"comparisons"`
	Notes        []string          `json:"notes"`
	MemoryReason string            `json:"memory_reason"`
	Limitations  []string          `json:"limitations"`
}

var proseReference = regexp.MustCompile(`ev_[A-Za-z0-9_]+|mem_[A-Za-z0-9_]+|\b[EMQ][0-9a-f]{8,}\b`)

// A relevance explanation may name a delivered record without marking it used.
// Resolve exact issued aliases to plain canonical identifiers, not citation
// markers; only typed findings/comparisons/notes authorize report references.
func (g *grounding) canonicalProse(s string, max int) (string, bool) {
	if !utf8.ValidString(s) || strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > max || strings.Contains(s, "[memory:") {
		return s, false
	}
	ok := true
	s = proseReference.ReplaceAllStringFunc(s, func(id string) string {
		if m, found := g.mem(id); found {
			return m.Hit.MemoryID
		}
		if e, found := g.source(id); found {
			return e.ID
		}
		for _, e := range g.evidence {
			for _, q := range quoteOptions(e) {
				if q.Handle == id {
					return fmt.Sprintf("%s (%d–%d)", e.ID, q.Start, q.End)
				}
			}
		}
		ok = false
		return id
	})
	// The model's prose bound applies before exact server ID expansion. Keep a
	// separate bounded rendering allowance; an issued Q expands to an evidence
	// ID plus coordinates, rather than consuming the author's prose allowance.
	return s, ok && utf8.RuneCountInString(s) <= max*8
}

// validate returns canonical rendered text, precise diagnostics and a bounded
// list of missing primary reads. Quote addressability is NOT semantic entailment.
func (g *grounding) validate(raw string) (string, []string, []string) {
	g.usedEvidence = nil
	g.usedMemory = nil
	var d finalDraft
	if len(raw) > 48000 || DecodeArgs(json.RawMessage(raw), &d) != nil {
		return "", []string{"invalid_final_json: return only the declared JSON object, exact field names, no Markdown fence"}, nil
	}
	if d.Findings == nil || d.Comparisons == nil || d.Notes == nil || d.Limitations == nil || len(d.Findings) > 6 || len(d.Comparisons) > 3 || len(d.Notes) > 8 || len(d.Limitations) > 8 {
		return "", []string{fmt.Sprintf("invalid_final_arrays: findings=%d (max 6), comparisons=%d (max 3), notes=%d (max 8), limitations=%d (max 8); all four must be JSON arrays, never null", len(d.Findings), len(d.Comparisons), len(d.Notes), len(d.Limitations))}, nil
	}
	var reasonOK, scopeOK bool
	d.MemoryReason, reasonOK = g.canonicalProse(d.MemoryReason, 800)
	d.Scope, scopeOK = g.canonicalProse(d.Scope, 500)
	if !scopeOK || !reasonOK {
		return "", []string{"invalid_scope_or_memory_reason: bounded nonempty prose; all identifiers must resolve EXACTLY to this run, no citation markers"}, nil
	}
	issues := []string{}
	missing := []string{}
	seenMissing := map[string]bool{}
	check := func(id, quote string) (sourceRef, bool) {
		e, ok := g.source(id)
		if !ok {
			issues = append(issues, "unissued_source:"+safeRef(id))
			return e, false
		}
		if !e.Read {
			issues = append(issues, "primary_read_required:"+e.Handle)
			if !seenMissing[e.ID] {
				missing = append(missing, e.ID)
				seenMissing[e.ID] = true
			}
			return e, false
		}
		for _, q := range quoteOptions(e) {
			if quote == q.Handle {
				quote = q.Text
				break
			}
		}
		if utf8.RuneCountInString(quote) < 20 || utf8.RuneCountInString(quote) > 800 || !strings.Contains(e.Text, quote) {
			issues = append(issues, "excerpt_mismatch:"+e.Handle)
			return e, false
		}
		g.usedEvidence = appendUnique(g.usedEvidence, e.ID)
		// Bind the selected exact source slice to this validation result only.
		e.Quotes = []sourceQuote{{Text: quote}}
		return e, true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Вопрос пользователя\n\n%s\n\n", g.question)
	for _, f := range d.Findings {
		var ok bool
		f.Conclusion, ok = g.canonicalProse(f.Conclusion, 1200)
		if !ok {
			issues = append(issues, "invalid_finding_text")
		}
		e, ok := check(f.Source, f.Quote)
		if ok {
			fmt.Fprintf(&b, "### Вывод по первоисточнику: %s\n\n%s [ %s ]\n\n> %s\n\n", e.Title, f.Conclusion, e.ID, e.Quotes[0].Text)
		}
	}
	used := map[string]bool{}
	for _, c := range d.Comparisons {
		m, ok := g.mem(c.Memory)
		if !ok {
			issues = append(issues, "unissued_scientific_memory:"+safeRef(c.Memory))
			continue
		}
		if m.Hit.Kind != "claim" {
			issues = append(issues, "user_annotation_is_not_scientific_claim:"+m.Handle+"; preserve/select this valid annotation in notes, remove it only from comparisons; notes convey user criteria, not scientific proof")
			continue
		}
		var conclusionOK bool
		c.Conclusion, conclusionOK = g.canonicalProse(c.Conclusion, 1500)
		if !conclusionOK || (c.Assessment != "supported" && c.Assessment != "not_supported" && c.Assessment != "uncertain") {
			issues = append(issues, "invalid_comparison_assessment")
		}
		old, oldOK := check(c.Previous, c.PreviousQuote)
		reviewOnly := c.Current == "" && c.CurrentQuote == ""
		cur := sourceRef{}
		curOK := reviewOnly
		if !reviewOnly {
			cur, curOK = check(c.Current, c.CurrentQuote)
		}
		owns := false
		for _, id := range m.Hit.EvidenceIDs {
			owns = owns || id == old.ID
		}
		if !owns || (!reviewOnly && old.Paper == cur.Paper) {
			issues = append(issues, "comparison_requires_prior_origin_and_different_current_paper")
		}
		if oldOK && curOK && owns {
			title := "Сопоставление первоисточников"
			if reviewOnly {
				title = "Проверка прежнего вывода по первоисточнику — без межстатейного сравнения"
			}
			fmt.Fprintf(&b, "### %s\n\nПрежний вывод модели (%s): %s [memory:%s]\n\nОценка прежнего вывода: %s. %s\n\n%s [%s]:\n> %s\n\n", title, m.Hit.State, m.Hit.Text, m.Hit.MemoryID, c.Assessment, c.Conclusion, old.Title, old.ID, old.Quotes[0].Text)
			if !reviewOnly {
				fmt.Fprintf(&b, "%s [%s]:\n> %s\n\n", cur.Title, cur.ID, cur.Quotes[0].Text)
			}
			used[m.Hit.MemoryID] = true
			// A disputed claim's delivered correction is mandatory, not scientific proof.
			for _, annotation := range g.memory {
				if annotation.Hit.Kind == "user_correction" && annotation.Hit.TargetID == m.Hit.MemoryID {
					used[annotation.Hit.MemoryID] = true
				}
			}
		}
	}
	for _, id := range d.Notes {
		m, ok := g.mem(id)
		if !ok || (m.Hit.Kind != "user_note" && m.Hit.Kind != "user_correction") {
			issues = append(issues, "unissued_user_annotation:"+safeRef(id))
			continue
		}
		used[m.Hit.MemoryID] = true
	}
	// Annotation closure also applies to a materially used note/correction;
	// user feedback cannot be hidden by rendering only its parent annotation.
	for pass := 0; pass < len(g.memory); pass++ {
		changed := false
		for _, m := range g.memory {
			if m.Hit.Kind == "user_correction" && used[m.Hit.TargetID] && !used[m.Hit.MemoryID] {
				used[m.Hit.MemoryID] = true
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	fmt.Fprintf(&b, "## Отношение к прошлым исследованиям\n\n%s\n\n", d.MemoryReason)
	// Stable rendering, copied verbatim from the exact snapshot issued this run.
	keys := []string{}
	for k := range used {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	g.usedMemory = keys
	for _, id := range keys {
		m, _ := g.mem(id)
		if m.Hit.Kind == "user_note" || m.Hit.Kind == "user_correction" {
			fmt.Fprintf(&b, "### Личная аннотация пользователя — дословно, не научный факт\n\n%s\n\n[memory:%s]\n\n", m.Hit.Text, id)
		}
	}
	if len(d.Findings) == 0 && len(d.Comparisons) == 0 {
		b.WriteString("Подтверждённых научных выводов в этом ответе нет.\n\n")
	}
	for _, limit := range d.Limitations {
		var ok bool
		limit, ok = g.canonicalProse(limit, 1000)
		if !ok {
			issues = append(issues, "invalid_limitation")
		}
		fmt.Fprintf(&b, "Ограничение: %s\n\n", limit)
	}
	b.WriteString("Проверены адресуемость и точное совпадение выдержек с прочитанными фрагментами. Это не полная семантическая верификация выводов.\n")
	return b.String(), issues, missing
}
func safeRef(s string) string {
	if len(s) > 80 {
		return "invalid"
	}
	for _, r := range s {
		if r < 33 || r > 126 {
			return "invalid"
		}
	}
	return s
}

// Automatic repair reads use the same registered tool, admission checks,
// accounting, cancellation, journal and assistant/tool pairing as model calls.
func (a *AgentRunner) readForRepair(b *runBudget, s Scope, ids []string) error {
	if len(ids) > 8 {
		return &ToolError{Code: "passage_limit"}
	}
	if b.exhausted() || b.usage.ToolCalls >= b.limits.ToolCalls {
		return &ToolError{Code: "budget_exceeded"}
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}
	tool, ok := a.registry.tools["read_passages"]
	if !ok {
		return &ToolError{Code: "unknown_tool"}
	}
	raw, _ := json.Marshal(map[string]any{"evidence_ids": ids})
	if err := tool.Validate(raw); err != nil {
		return err
	}
	call := deepseek.ToolCall{ID: "server_grounding_read", Type: "function", Function: deepseek.FunctionCall{Name: "read_passages", Arguments: string(raw)}}
	calls, _ := json.Marshal([]deepseek.ToolCall{call})
	b.usage.ToolCalls++
	if err := b.save("tool_started", map[string]any{"tool": "read_passages", "call_id": call.ID, "reason": "answer_repair"}, nil, &store.Message{Role: "assistant", ToolCalls: calls}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(b.ctx, b.limits.ToolTimeout)
	defer cancel()
	output, err := tool.Execute(ctx, s, raw)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	code := failureCode(err)
	if err != nil {
		output = errorResult(code)
	}
	if saveErr := a.toolMessage(b, call, output, code); saveErr != nil {
		return saveErr
	}
	return err
}
