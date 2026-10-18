package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"time"

	"paper-scout/internal/store"
)

// New returns the foundation routes. Later nodes add authenticated API routes.
func New(db *store.Store) *http.ServeMux {
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
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
