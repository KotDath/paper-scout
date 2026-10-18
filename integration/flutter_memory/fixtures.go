// Synthetic persisted research fixtures copied from the T19 integration contract.
// They are not real scientific findings or evidence of automatic agent recall.
package main

import (
	"context"
	"paper-scout/internal/research"
	"paper-scout/internal/store"
	"strings"
)

func seedResearch(ctx context.Context, db *store.Store, key string) error {
	exec := func(q string, args ...any) error { _, err := db.DB.ExecContext(ctx, q, args...); return err }
	stamp := "2026-01-02T00:00:00Z"
	paper := "paper_" + key
	run := "run_" + key
	conv := "conv_" + key
	doc := "doc_" + key
	ev := "ev_" + key
	source := "Synthetic episodic memory consolidation method uses forgetting and recurrent review."
	if key == "b" {
		source = "Synthetic episodic memory consolidation does not improve the measured result under this condition."
	}
	if err := exec(`INSERT INTO paper_versions VALUES(?,?,1,?,'[]',?,'https://arxiv.org/abs/2305.10250','https://arxiv.org/html/2305.10250v1','https://arxiv.org/pdf/2305.10250v1',?,?,?)`, paper, "synthetic-"+key, "Synthetic memory paper "+key, source, stamp, stamp, stamp); err != nil {
		return err
	}
	if err := exec(`INSERT INTO documents VALUES(?,?,'html',?,'fixture-v1','fixture-v1','full_text','https://arxiv.org/html/2305.10250v1','',?,'[]','[]',?)`, doc, paper, strings.Repeat(key, 64), source, stamp); err != nil {
		return err
	}
	if err := exec(`INSERT INTO chunks(id,document_id,ordinal,text,char_start,char_end,section) VALUES(?,?,0,?,0,?,'Methods')`, ev, doc, source, len([]rune(source))); err != nil {
		return err
	}
	if err := exec(`INSERT INTO user_papers(user_id,paper_version_id,added_at) VALUES('owner_a',?,?)`, paper, stamp); err != nil {
		return err
	}
	if err := exec(`INSERT INTO conversations VALUES(?,'owner_a',?,?)`, conv, "Synthetic memory research "+key, stamp); err != nil {
		return err
	}
	if err := exec(`INSERT INTO runs(id,user_id,conversation_id,client_request_id,request_message,status,created_at,budget_json) VALUES(?,'owner_a',?,?,?,'completed',?,'{}')`, run, conv, key, "Study episodic memory consolidation", stamp); err != nil {
		return err
	}
	if err := exec(`INSERT INTO run_sources VALUES(?,?,'synthetic integration source','complete')`, run, paper); err != nil {
		return err
	}
	obs := research.Observations{Methods: []research.Claim{{Text: "Episodic consolidation uses forgetting and recurrent review " + key, EvidenceIDs: []string{ev}}}, Results: []research.Claim{{Text: source, EvidenceIDs: []string{ev}}}, Limitations: []research.Claim{{Text: "Synthetic evaluation limitations require independent verification " + key, EvidenceIDs: []string{ev}}}}
	a := research.Analysis{ID: "analysis_" + key, RunID: run, DocumentID: doc, PaperVersionID: paper, Status: "complete", ExtractionStatus: "full_text", TotalRunes: len([]rune(source)), CoveredRunes: len([]rune(source)), SuccessfulRanges: []research.Range{{Start: 0, End: len([]rune(source))}}, Synthesis: &obs}
	if err := exec(`INSERT INTO paper_analyses(id,run_id,document_id,question,status,result_json,created_at,finished_at) VALUES(?,?,?,'synthetic','complete',?,?,?)`, a.ID, run, doc, mustJSON(a), stamp, stamp); err != nil {
		return err
	}
	sources := []store.ReportSource{{Paper: store.Paper{ID: paper, Title: "Synthetic memory paper " + key}, AnalysisStatus: "complete", Coverage: []store.Coverage{}}}
	if err := exec(`UPDATE runs SET report_id=? WHERE id=?`, "report_"+key, run); err != nil {
		return err
	}
	return exec(`INSERT INTO reports(id,user_id,run_id,markdown,citations_json,sources_json,coverage_json,limitations_json,created_at) VALUES(?,'owner_a',?,?,'[]',?,'[]','[]',?)`, "report_"+key, run, "Synthetic report: "+source, mustJSON(sources), stamp)
}
