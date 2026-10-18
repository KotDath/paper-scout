package httpapi

import (
	"errors"
	"net/http"
	"paper-scout/internal/auth"
	"paper-scout/internal/researchmemory"
	"paper-scout/internal/store"
)

// RegisterResearchMemory supports production and composed integration servers.
func RegisterResearchMemory(mux *http.ServeMux, db *store.Store, s *researchmemory.Service) {
	protect := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, RequireAuth(auth.New(db), h)) }
	protect("POST /api/v1/research-memory/search", func(w http.ResponseWriter, r *http.Request) {
		if !noQuery(w, r) {
			return
		}
		var input researchmemory.SearchRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		u, _ := UserFromContext(r.Context())
		out, err := s.Search(r.Context(), u.ID, input)
		if memoryError(w, err) {
			return
		}
		writeJSON(w, 200, out)
	})
	protect("GET /api/v1/reports/{id}/research-memory", func(w http.ResponseWriter, r *http.Request) {
		if !noQuery(w, r) {
			return
		}
		u, _ := UserFromContext(r.Context())
		out, err := s.AnnotationsForReport(r.Context(), u.ID, r.PathValue("id"))
		if memoryError(w, err) {
			return
		}
		writeJSON(w, 200, out)
	})
	protect("GET /api/v1/research-memory/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !noQuery(w, r) {
			return
		}
		u, _ := UserFromContext(r.Context())
		out, err := s.Get(r.Context(), u.ID, r.PathValue("id"))
		if memoryError(w, err) {
			return
		}
		writeJSON(w, 200, out)
	})
	protect("PATCH /api/v1/research-memory/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !noQuery(w, r) {
			return
		}
		var input researchmemory.Patch
		if !decodeJSON(w, r, &input) {
			return
		}
		u, _ := UserFromContext(r.Context())
		out, err := s.Patch(r.Context(), u.ID, r.PathValue("id"), input)
		if memoryError(w, err) {
			return
		}
		writeJSON(w, 200, out)
	})
}
func memoryError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		apiError(w, 404, "not_found", "Resource not found", false)
	case errors.Is(err, researchmemory.ErrPending):
		apiError(w, 503, "memory_pending", "Current research annotations are pending; retry", true)
	case errors.Is(err, researchmemory.ErrConflict):
		apiError(w, 409, "revision_conflict", "Reload the current revision", false)
	case errors.Is(err, researchmemory.ErrInvalid):
		apiError(w, 400, "invalid_input", "Use query/correction 1..4000 characters, limit 1..5, or exactly one patch action and expected_revision", false)
	case errors.Is(err, researchmemory.ErrScope):
		apiError(w, 400, "memory_scope_limit", "Memory exceeds bounded local search scope", false)
	default:
		apiError(w, 503, "memory_unavailable", "Research memory is unavailable", true)
	}
	return true
}
