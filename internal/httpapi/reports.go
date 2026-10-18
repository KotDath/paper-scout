package httpapi

import (
	"net/http"
	"unicode/utf8"

	"paper-scout/internal/auth"
	"paper-scout/internal/store"
)

func registerReports(mux *http.ServeMux, db *store.Store) {
	protect := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, RequireAuth(auth.New(db), h)) }
	protect("GET /api/v1/reports/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !noQuery(w, r) {
			return
		}
		user, _ := UserFromContext(r.Context())
		report, err := db.Report(r.Context(), user.ID, r.PathValue("id"))
		if runError(w, err) {
			return
		}
		writeJSON(w, 200, report)
	})
	protect("GET /api/v1/papers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !noQuery(w, r) {
			return
		}
		user, _ := UserFromContext(r.Context())
		p, err := db.Paper(r.Context(), user.ID, r.PathValue("id"))
		if runError(w, err) {
			return
		}
		writeJSON(w, 200, p)
	})
	protect("GET /api/v1/library", func(w http.ResponseWriter, r *http.Request) {
		limit, ok := pageLimit(w, r, "cursor")
		if !ok {
			return
		}
		cursor := r.URL.Query().Get("cursor")
		if len(cursor) > 200 {
			apiError(w, 400, "invalid_input", "Invalid cursor", false)
			return
		}
		user, _ := UserFromContext(r.Context())
		items, err := db.Library(r.Context(), user.ID, cursor, limit+1)
		if runError(w, err) {
			return
		}
		next := ""
		if len(items) > limit {
			items = items[:limit]
			next = items[len(items)-1].ID
		}
		writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
	})
	protect("PATCH /api/v1/library/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !noQuery(w, r) {
			return
		}
		var input store.PaperPatch
		if !decodeJSON(w, r, &input) {
			return
		}
		if (input.Saved == nil && input.Read == nil && input.Note == nil) || (input.Note != nil && (!utf8.ValidString(*input.Note) || utf8.RuneCountInString(*input.Note) > 10000)) {
			apiError(w, 400, "invalid_input", "Supply saved, read or a note up to 10000 characters", false)
			return
		}
		user, _ := UserFromContext(r.Context())
		p, err := db.UpdatePaper(r.Context(), user.ID, r.PathValue("id"), input)
		if runError(w, err) {
			return
		}
		writeJSON(w, 200, p)
	})
}
