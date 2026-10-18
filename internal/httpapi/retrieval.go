package httpapi

import (
	"errors"
	"net/http"

	"paper-scout/internal/auth"
	"paper-scout/internal/retrieval"
	"paper-scout/internal/store"
)

// RegisterRetrieval also supports composed servers used by downstream nodes.
func RegisterRetrieval(mux *http.ServeMux, db *store.Store, service *retrieval.Service) {
	protect := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, RequireAuth(auth.New(db), handler))
	}
	protect("POST /api/v1/library/search", func(w http.ResponseWriter, r *http.Request) {
		var input retrieval.SearchRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		user, _ := UserFromContext(r.Context())
		result, err := service.Search(r.Context(), user.ID, input)
		if err != nil {
			retrievalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	protect("GET /api/v1/evidence/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !noQuery(w, r) {
			return
		}
		user, _ := UserFromContext(r.Context())
		result, err := service.Evidence(r.Context(), user.ID, r.PathValue("id"))
		if err != nil {
			retrievalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
}

func retrievalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, retrieval.ErrNotFound):
		apiError(w, 404, "not_found", "Resource not found", false)
	case errors.Is(err, retrieval.ErrInvalid):
		apiError(w, 400, "invalid_input", "Use a nonempty question up to 4000 characters, at most 100 paper IDs and limit 1..8", false)
	case errors.Is(err, retrieval.ErrScopeLimit):
		apiError(w, 400, "scope_limit", "Narrow paper_version_ids to at most 20000 chunks", false)
	default:
		apiError(w, 500, "retrieval_error", "Cannot retrieve passages", false)
	}
}
