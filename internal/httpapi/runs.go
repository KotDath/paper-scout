package httpapi

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"paper-scout/internal/auth"
	"paper-scout/internal/research"
	"paper-scout/internal/store"
)

func registerRuns(mux *http.ServeMux, db *store.Store, runs *research.Service) {
	protect := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, RequireAuth(auth.New(db), h)) }
	protect("POST /api/v1/conversations", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Title string `json:"title"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if !validText(input.Title, 200) {
			apiError(w, 400, "invalid_input", "Use a nonempty title up to 200 characters", false)
			return
		}
		user, _ := UserFromContext(r.Context())
		c, err := db.CreateConversation(r.Context(), user.ID, input.Title)
		if runError(w, err) {
			return
		}
		writeJSON(w, 201, c)
	})
	protect("GET /api/v1/conversations", func(w http.ResponseWriter, r *http.Request) {
		limit, ok := pageLimit(w, r, "cursor")
		if !ok {
			return
		}
		cursor := r.URL.Query().Get("cursor")
		if len(cursor) > 128 {
			apiError(w, 400, "invalid_input", "Invalid cursor", false)
			return
		}
		user, _ := UserFromContext(r.Context())
		items, err := db.Conversations(r.Context(), user.ID, cursor, limit+1)
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
	protect("GET /api/v1/conversations/{id}", func(w http.ResponseWriter, r *http.Request) {
		limit, after, ok := sequencePage(w, r)
		if !ok {
			return
		}
		user, _ := UserFromContext(r.Context())
		id := r.PathValue("id")
		c, err := db.Conversation(r.Context(), user.ID, id)
		if runError(w, err) {
			return
		}
		messages, err := db.PublicMessagesPage(r.Context(), user.ID, id, after, limit+1)
		if runError(w, err) {
			return
		}
		var next *int64
		if len(messages) > limit {
			messages = messages[:limit]
			seq := messages[len(messages)-1].Seq
			next = &seq
		}
		refs, err := db.ConversationRuns(r.Context(), user.ID, id)
		if runError(w, err) {
			return
		}
		writeJSON(w, 200, map[string]any{"conversation": c, "messages": messages, "runs": refs, "next_after_seq": next})
	})
	protect("POST /api/v1/conversations/{id}/runs", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Message         string `json:"message"`
			ClientRequestID string `json:"client_request_id"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if !validText(input.Message, 20000) || !validText(input.ClientRequestID, 128) {
			apiError(w, 400, "invalid_input", "Use a message up to 20000 characters and client_request_id up to 128 characters", false)
			return
		}
		user, _ := UserFromContext(r.Context())
		run, _, err := runs.Create(r.Context(), user.ID, r.PathValue("id"), input.ClientRequestID, input.Message)
		if runError(w, err) {
			return
		}
		writeJSON(w, 202, map[string]any{"run_id": run.ID, "status": run.Status, "progress_url": "/api/v1/runs/" + run.ID})
	})
	protect("GET /api/v1/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !noQuery(w, r) {
			return
		}
		user, _ := UserFromContext(r.Context())
		run, err := db.Run(r.Context(), user.ID, r.PathValue("id"))
		if runError(w, err) {
			return
		}
		sources, err := db.RunSources(r.Context(), user.ID, run.ID)
		if runError(w, err) {
			return
		}
		writeJSON(w, 200, struct {
			store.Run
			Sources []store.ReportSource `json:"sources"`
		}{run, sources})
	})
	protect("GET /api/v1/runs/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		limit, after, ok := sequencePage(w, r)
		if !ok {
			return
		}
		user, _ := UserFromContext(r.Context())
		items, err := db.RunEvents(r.Context(), user.ID, r.PathValue("id"), after, limit+1)
		if runError(w, err) {
			return
		}
		more := len(items) > limit
		if more {
			items = items[:limit]
		}
		next := after
		if len(items) > 0 {
			next = items[len(items)-1].Seq
		}
		writeJSON(w, 200, map[string]any{"items": items, "next_after_seq": next, "has_more": more})
	})
	protect("POST /api/v1/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !noQuery(w, r) {
			return
		}
		// No body fields can select another owner or alter run state.
		r.Body = http.MaxBytesReader(w, r.Body, 1)
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) != 0 {
			apiError(w, 400, "invalid_input", "Cancel takes no body", false)
			return
		}
		user, _ := UserFromContext(r.Context())
		run, err := runs.Cancel(r.Context(), user.ID, r.PathValue("id"))
		if runError(w, err) {
			return
		}
		writeJSON(w, 200, run)
	})
}

func validText(value string, max int) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= max
}

func pageLimit(w http.ResponseWriter, r *http.Request, extra string) (int, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		apiError(w, 400, "invalid_input", "Invalid pagination parameters", false)
		return 0, false
	}
	for key, values := range query {
		if (key != "limit" && key != extra) || len(values) != 1 {
			apiError(w, 400, "invalid_input", "Invalid pagination parameters", false)
			return 0, false
		}
	}
	limit := 50
	if values, ok := r.URL.Query()["limit"]; ok {
		n, err := strconv.Atoi(values[0])
		if err != nil || n < 1 || n > 100 {
			apiError(w, 400, "invalid_input", "Limit must be between 1 and 100", false)
			return 0, false
		}
		limit = n
	}
	return limit, true
}

func sequencePage(w http.ResponseWriter, r *http.Request) (int, int64, bool) {
	limit, ok := pageLimit(w, r, "after_seq")
	if !ok {
		return 0, 0, false
	}
	var after int64
	if values, ok := r.URL.Query()["after_seq"]; ok {
		n, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || n < 0 {
			apiError(w, 400, "invalid_input", "after_seq must be nonnegative", false)
			return 0, 0, false
		}
		after = n
	}
	return limit, after, true
}

func runError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		apiError(w, 404, "not_found", "Resource not found", false)
	case errors.Is(err, store.ErrActiveRun):
		apiError(w, 409, "active_run", "Conversation already has an active run", false)
	case errors.Is(err, store.ErrIdempotency):
		apiError(w, 409, "idempotency_conflict", "client_request_id was used with another message", false)
	case errors.Is(err, store.ErrRunCapacity):
		apiError(w, 429, "run_capacity", "Server already has two active runs", true)
	default:
		apiError(w, 500, "internal_error", "Cannot access run state", false)
	}
	return true
}
