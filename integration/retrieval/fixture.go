package main

// Only Ollama's HTTP boundary is controlled. Production EmbeddingClient,
// ingestion, FTS5, vector storage, cosine ranking, ownership and fusion run intact.
import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"

	"paper-scout/internal/embeddings"
	"paper-scout/internal/retrieval"
)

func embeddingFixture() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": embeddings.Model, "digest": strings.Repeat("a", 64)}}})
		case "/api/show":
			json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"embedding"}, "model_info": map[string]any{"general.architecture": "qwen3", "qwen3.embedding_length": 1024, "qwen3.context_length": 32768}})
		case "/api/embed":
			var body struct {
				Input []string `json:"input"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Input) == 0 {
				w.WriteHeader(400)
				return
			}
			vectors := make([][]float64, len(body.Input))
			for i, text := range body.Input {
				v := make([]float64, 1024)
				v[1] = 1
				switch {
				case strings.HasPrefix(text, embeddings.QueryInstruction), strings.Contains(text, "Private-library fixture"), strings.Contains(text, "Stellar spectroscopy"):
					v[0], v[1] = 1, 0
				case strings.Contains(text, "Ocean circulation"):
					v[0], v[1] = .9, math.Sqrt(.19)
				case strings.Contains(text, "Memory consolidation"):
					v[0], v[1] = .8, .6
				}
				vectors[i] = v
			}
			json.NewEncoder(w).Encode(map[string]any{"model": embeddings.Model, "embeddings": vectors, "prompt_eval_count": len(body.Input)})
		default:
			http.NotFound(w, r)
		}
	}))
}

func knownRanks(result retrieval.SearchResult) error {
	// A's memory passage has lexical rank 1 and dense rank 3. The dense-only
	// distractors rank 1 and 2, and B's rank-1 source must be filtered before top-k.
	if len(result.LexicalCandidates) != 1 || len(result.DenseCandidates) < 3 || len(result.Hits) == 0 {
		return errors.New("known-rank corpus branch counts changed")
	}
	for i, section := range []string{"Stellar spectroscopy", "Ocean circulation", "Memory consolidation"} {
		if result.DenseCandidates[i].Section != section {
			return fmt.Errorf("known dense rank %d: got %s", i+1, result.DenseCandidates[i].Section)
		}
	}
	hit := result.Hits[0]
	if hit.Section != "Memory consolidation" || hit.LexicalRank != 1 || hit.DenseRank != 3 || math.Abs(hit.RRFScore-(1.0/61+1.0/63)) > 1e-12 {
		return errors.New("known RRF winner/score differs from 1/61 + 1/63")
	}
	fmt.Println("PASS: predetermined HTTP/SQLite BM25 rank 1 + dense rank 3 wins RRF at 1/61 + 1/63; dense-only distractors rank 1/2")
	return nil
}
