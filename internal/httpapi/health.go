package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"time"

	"paper-scout/internal/research"
	"paper-scout/internal/researchmemory"
	"paper-scout/internal/retrieval"
	"paper-scout/internal/store"
)

// New returns health and authenticated API routes.
func New(db *store.Store, services ...*retrieval.Service) *http.ServeMux {
	return NewWithRuns(db, research.New(context.Background(), db), services...)
}

// NewWithRuns attaches the same lifecycle service used by background workers.
func NewWithRuns(db *store.Store, runs *research.Service, services ...*retrieval.Service) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		checks := map[string]bool{"database_fts5": db.Check(ctx) == nil}
		for _, command := range []string{"pdftotext", "pdfinfo"} {
			_, err := exec.LookPath(command)
			checks[command] = err == nil
		}
		status, code := "ready", http.StatusOK
		for _, ok := range checks {
			if !ok {
				status, code = "not_ready", http.StatusServiceUnavailable
			}
		}
		writeJSON(w, code, map[string]any{"status": status, "checks": checks})
	})
	registerAuth(mux, db)
	registerRuns(mux, db, runs)
	registerReports(mux, db)
	service := retrieval.New(db.DB, nil)
	if len(services) > 0 && services[0] != nil {
		service = services[0]
	}
	RegisterRetrieval(mux, db, service)
	RegisterResearchMemory(mux, db, researchmemory.New(db.DB, service.EmbeddingClient()))
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
