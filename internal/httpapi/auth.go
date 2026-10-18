package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"paper-scout/internal/auth"
	"paper-scout/internal/store"
)

type sessionContextKey struct{}

// UserFromContext is the sole HTTP owner source for downstream handlers. Never
// replace it with a client-supplied user_id. RequireAuth installs this value.
func UserFromContext(ctx context.Context) (store.User, bool) {
	session, ok := ctx.Value(sessionContextKey{}).(auth.Session)
	return session.User, ok
}

func RequireAuth(service *auth.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if len(values) != 1 {
			unauthorized(w)
			return
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			unauthorized(w)
			return
		}
		session, err := service.Authenticate(r.Context(), parts[1])
		if err != nil {
			if errors.Is(err, auth.ErrUnauthorized) {
				unauthorized(w)
			} else {
				apiError(w, 500, "internal_error", "Cannot validate session", false)
			}
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session)))
	})
}

func registerAuth(mux *http.ServeMux, db *store.Store) {
	service := auth.New(db)
	protect := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, RequireAuth(service, handler))
	}
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Login    string `json:"login"`
			Password string `json:"password"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if !auth.ValidLogin(input.Login) || len(input.Password) == 0 || len(input.Password) > 72 {
			unauthorized(w)
			return
		}
		result, err := service.Login(r.Context(), input.Login, input.Password)
		if err != nil {
			if errors.Is(err, auth.ErrUnauthorized) {
				unauthorized(w)
			} else {
				apiError(w, 500, "internal_error", "Cannot create session", false)
			}
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	protect("POST /api/v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(sessionContextKey{}).(auth.Session)
		if err := service.Logout(r.Context(), session); err != nil {
			apiError(w, 500, "internal_error", "Cannot revoke session", false)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	})
	protect("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		if !noQuery(w, r) {
			return
		}
		user, _ := UserFromContext(r.Context())
		writeJSON(w, http.StatusOK, user)
	})
	protect("GET /api/v1/profile", func(w http.ResponseWriter, r *http.Request) {
		if !noQuery(w, r) {
			return
		}
		user, _ := UserFromContext(r.Context())
		profile, err := db.Profile(r.Context(), user.ID)
		profileResult(w, profile, err)
	})
	protect("PUT /api/v1/profile", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Interests         string `json:"interests"`
			PreferredLanguage string `json:"preferred_language"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if !utf8.ValidString(input.Interests) || utf8.RuneCountInString(input.Interests) > 10000 ||
			(input.PreferredLanguage != "ru" && input.PreferredLanguage != "en") {
			apiError(w, 400, "invalid_input", "Use at most 10000 interest characters and language ru or en", false)
			return
		}
		user, _ := UserFromContext(r.Context())
		profile, err := db.UpdateProfile(r.Context(), user.ID, input.Interests,
			input.PreferredLanguage, time.Now().UTC().Format(time.RFC3339))
		profileResult(w, profile, err)
	})
	// Unknown API resources are still behind authentication. Downstream nodes
	// register their concrete routes with RequireAuth and owner-scoped SQL.
	protect("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
		apiError(w, 404, "not_found", "Resource not found", false)
	})
}

func profileResult(w http.ResponseWriter, profile store.Profile, err error) {
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, 404, "not_found", "Resource not found", false)
	} else if err != nil {
		apiError(w, 500, "internal_error", "Cannot access profile", false)
	} else {
		writeJSON(w, http.StatusOK, profile)
	}
}

func noQuery(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery != "" {
		apiError(w, 400, "invalid_input", "Query parameters are not supported", false)
		return false
	}
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if !noQuery(w, r) {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		apiError(w, 400, "invalid_input", "Content-Type must be application/json", false)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		apiError(w, 400, "invalid_input", "Invalid JSON request", false)
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		apiError(w, 400, "invalid_input", "Expected one JSON object", false)
		return false
	}
	return true
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	apiError(w, http.StatusUnauthorized, "unauthorized", "Invalid credentials or session", false)
}

func apiError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	var raw [16]byte
	requestID := "unavailable"
	if _, err := rand.Read(raw[:]); err == nil {
		requestID = hex.EncodeToString(raw[:])
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"code": code, "message": message, "retryable": retryable}, "request_id": requestID})
}
