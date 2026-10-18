package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"

	"paper-scout/internal/documents"
	"paper-scout/internal/retrieval"
	"paper-scout/internal/store"
)

const corpusTitle = "Synthetic multidisciplinary retrieval corpus"

var corpus = []struct{ section, text string }{
	{"Memory consolidation", "An autonomous agent stores experiences in episodic memory. During consolidation it groups related observations, extracts general lessons, and writes reusable semantic knowledge into long-term memory. The agent retrieves these lessons on future tasks. Summaries preserve provenance links to the original episodes. This mechanism reduces repeated mistakes while keeping a record of where knowledge came from. It is evaluated by comparing task completion across successive episodes, including a baseline without consolidation."},
	{"Stellar spectroscopy", "Astronomers measure absorption lines in stellar spectra to infer atmospheric temperature and chemical composition. A telescope records light through a spectrograph. Calibration lamps establish wavelength precision. The observations are corrected for instrumental response before fitting a physical model of the stellar atmosphere. Uncertainty in the line strengths affects estimated metallicity. Independent observations on multiple nights distinguish instrument drift from physical changes in the star."},
	{"Ocean circulation", "Oceanographers estimate large-scale circulation from temperature and salinity profiles recorded by floating instruments. Surface winds and density gradients drive water movement between ocean basins. Numerical simulations compare measured profiles with model predictions. The analysis accounts for seasonal variations in solar heating and sea ice. Sparse sampling of deep water introduces uncertainty in estimates of heat transport. The study examines how currents redistribute thermal energy."},
	{"Plant physiology", "Botanists examine photosynthesis in leaves under controlled light and water availability. Carbon dioxide uptake is measured in a sealed chamber. Chlorophyll fluorescence indicates the efficiency of the photosynthetic machinery. Drought conditions lead to stomatal closure, limiting gas exchange and plant growth. Experiments compare different cultivars and soil treatments. Measurements are repeated over the growing season to distinguish temporary stress from lasting physiological changes."},
	{"Battery chemistry", "Electrochemical cells store energy through reversible reactions at two electrodes. Lithium ions move through an electrolyte during charging and discharge. Researchers measure capacity retention over repeated cycles and characterize electrode morphology with microscopy. Temperature affects reaction rates and degradation. Solid electrolyte interfaces can protect the anode but also increase resistance. The experiments compare formulations under identical charge rates to isolate chemical effects on cell performance."},
	{"Graph algorithms", "A shortest path algorithm identifies the minimum total edge weight connecting two vertices in a graph. Priority queues make exploration efficient when all weights are nonnegative. Experiments compare execution time on sparse and dense graphs. The analysis separates preprocessing from query execution. Changes to graph topology can invalidate earlier path calculations. The discussion covers computational complexity and the tradeoff between storage requirements and faster route planning."},
	{"Protein folding", "A protein is a chain of amino acids whose three-dimensional structure influences biological function. Molecular simulations estimate conformational changes under different solvent conditions. Hydrogen bonds and hydrophobic interactions stabilize folded states. Experimental spectroscopy provides an independent comparison for predicted structures. High-dimensional energy landscapes make exhaustive exploration expensive. The study examines how sequence substitutions alter stability and the rate at which a chain reaches its native conformation."},
	{"Seismic waves", "Geophysicists use seismic waves to study the interior structure of the Earth. Travel times depend on material density and elastic properties. Networks of sensors record arrivals after an earthquake. Inversion methods reconstruct velocity profiles from measured waveforms. Uneven sensor coverage creates uncertainty in poorly sampled regions. Comparisons across several events improve estimates of subsurface structure and help distinguish local effects from large-scale geological features."},
	{"Economic inflation", "Economists track price changes across a representative basket of consumer goods. The resulting index summarizes changes in purchasing power over time. Supply constraints, demand shifts, and monetary conditions affect observed inflation. Statistical adjustments account for seasonal patterns and changes in product quality. Household spending patterns differ, so the same national index can imply different experiences for different groups. The analysis compares wage growth and living costs across regions."},
	{"Wireless communication", "A wireless communication system transmits information through a noisy radio channel. Modulation determines how symbols map to carrier amplitude and phase. Error correction adds redundancy so the receiver can recover damaged messages. Engineers evaluate bit error rates under varying signal-to-noise ratios. Multipath propagation and interference reduce reliability. Experiments compare antenna configurations and channel coding choices under identical bandwidth and transmit power constraints."},
	{"Fluid turbulence", "Turbulent flow contains irregular velocity fluctuations over many spatial scales. Numerical models approximate energy transfer from large eddies to smaller dissipative motions. Experiments measure velocity in a wind tunnel using optical sensors. Boundary conditions and measurement resolution affect the comparison with simulations. The analysis estimates energy spectra and correlations between neighboring flow regions. Results describe how viscosity and forcing determine the distribution of kinetic energy."},
	{"Number theory", "Number theorists study the distribution of prime integers and properties of arithmetic functions. A proof establishes a bound under clearly stated mathematical assumptions. Computational examples illustrate behavior for finite ranges but do not replace a general argument. The analysis compares asymptotic estimates with numerical counts. Different error terms control how quickly an approximation becomes useful. The discussion relates divisibility, modular arithmetic, and the structure of multiplicative functions."},
}

type reroute struct{ target *url.URL }

func (t reroute) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	u := *r.URL
	clone.URL = &u
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func importCorpus(ctx context.Context, db *store.Store, runtime string) ([]documents.Document, error) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if !strings.HasPrefix(r.URL.Path, "/html/") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "<article>")
		if strings.Contains(r.URL.Path, "00002") {
			fmt.Fprint(w, "<h2>Private-library fixture</h2><p>Account B owns a separate source about episodic consolidation in a robotic navigation agent. The content is synthetic and has no relation to account A.</p>")
			fmt.Fprint(w, "<p>"+strings.Repeat("Navigation experiments evaluate localization and motion planning in simulated corridors. ", 8)+"</p>")
		} else {
			for _, entry := range corpus {
				fmt.Fprintf(w, "<h2>%s</h2><p>%s</p><p>These synthetic passages are integration fixtures, not claims about real publications.</p>", entry.section, entry.text)
			}
		}
		fmt.Fprint(w, "<p>This additional paragraph ensures the extraction fixture contains a structured article body and is visibly labeled synthetic.</p></article>")
	}))
	defer fixture.Close()
	target, _ := url.Parse(fixture.URL)
	service, err := documents.New(db.DB, documents.Config{CacheDir: filepath.Join(runtime, "cache"), HTTPClient: &http.Client{Transport: reroute{target}}})
	if err != nil {
		return nil, err
	}
	result := []documents.Document{}
	for i := 1; i <= 2; i++ {
		id := fmt.Sprintf("fixture_pv_%d", i)
		arxivID := fmt.Sprintf("2601.0000%dv1", i)
		title := corpusTitle
		if i == 2 {
			title = "Synthetic separate library"
		}
		_, err = db.DB.ExecContext(ctx, `INSERT INTO paper_versions(id,arxiv_id,version,title,authors_json,abstract,abstract_url,html_url,pdf_url,published_at,updated_at,fetched_at) VALUES (?,?,1,?,'[]','Synthetic integration content',?,?,?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, id, strings.TrimSuffix(arxivID, "v1"), title, "https://arxiv.org/abs/"+arxivID, "https://arxiv.org/html/"+arxivID, "https://arxiv.org/pdf/"+arxivID)
		if err != nil {
			return nil, err
		}
		d, err := service.Import(ctx, documents.Source{PaperVersionID: id, ArxivID: arxivID, Abstract: "Synthetic integration content"})
		if err != nil {
			return nil, err
		}
		if d.ExtractionStatus != "full_text" || len(d.Chunks) == 0 {
			return nil, fmt.Errorf("fixture HTML extraction failed: %s %+v", d.ExtractionStatus, d.Warnings)
		}
		result = append(result, d)
	}
	return result, nil
}

func scopeLimit(ctx context.Context, db *store.Store, service *retrieval.Service, source documents.Document) (resultErr error) {
	// Remove this intentionally oversized SQL fixture afterwards, without touching the
	// live source or leaving artificial domain rows behind after this check.
	_, err := db.DB.ExecContext(ctx, `INSERT INTO documents SELECT 'large_document',paper_version_id,format,'large_hash',extractor_version,chunking_version,extraction_status,source_url,local_path,text,segments_json,warnings_json,created_at FROM documents WHERE id=?`, source.ID)
	if err != nil {
		return err
	}
	defer func() {
		// Remove the disposable fixture's FTS rows in one pass before cascade.
		// The production per-chunk trigger scans an UNINDEXED FTS column; leaving
		// 20k fixture rows there makes cleanup quadratic under the race detector.
		// Assertions before cleanup use the unmodified real index/triggers.
		_, ftsErr := db.DB.ExecContext(context.WithoutCancel(ctx), `DELETE FROM chunks_fts WHERE chunk_id IN (SELECT id FROM chunks WHERE document_id='large_document')`)
		_, deleteErr := db.DB.ExecContext(context.WithoutCancel(ctx), `DELETE FROM documents WHERE id='large_document'`)
		resultErr = errors.Join(resultErr, ftsErr, deleteErr)
	}()
	_, err = db.DB.ExecContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x<20000)
 INSERT INTO chunks(id,document_id,ordinal,text,char_start,char_end,section) SELECT 'large_chunk_'||x,'large_document',x,'scope fixture',0,13,'scope' FROM n`)
	if err != nil {
		return err
	}
	_, err = service.Search(ctx, "owner_a", retrieval.SearchRequest{Query: "scope"})
	if err != retrieval.ErrScopeLimit {
		return fmt.Errorf("scope not rejected at 20000 chunks: %v", err)
	}
	fmt.Println("PASS oversized exact-search scope rejected before inference; no silent truncation")
	return nil
}
