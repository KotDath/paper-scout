// Package auth implements local accounts and revocable opaque bearer sessions.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"paper-scout/internal/store"
)

const SessionLifetime = 24 * time.Hour

var ErrUnauthorized = errors.New("invalid credentials or session")

type Service struct{ db *store.Store }

func New(db *store.Store) *Service { return &Service{db: db} }

// SeedFromEnvironment is explicit; normal server startup never creates users.
// Credentials are read by the process and are never included in diagnostics.
func (s *Service) SeedFromEnvironment(ctx context.Context) error {
	logins := []string{os.Getenv("SEED_USER_1_LOGIN"), os.Getenv("SEED_USER_2_LOGIN")}
	passwords := []string{os.Getenv("SEED_USER_1_PASSWORD"), os.Getenv("SEED_USER_2_PASSWORD")}
	for i := range logins {
		if !ValidLogin(logins[i]) || len(passwords[i]) < 12 || len(passwords[i]) > 72 || !utf8.ValidString(passwords[i]) {
			return errors.New("seed requires two valid logins and passwords of 12..72 UTF-8 bytes")
		}
	}
	if logins[0] == logins[1] {
		return errors.New("seed logins must be distinct")
	}
	accounts := make([]store.SeedAccount, 0, 2)
	for i, login := range logins {
		hash, err := bcrypt.GenerateFromPassword([]byte(passwords[i]), bcrypt.DefaultCost)
		if err != nil {
			return errors.New("cannot hash seed password")
		}
		id, err := randomToken()
		if err != nil {
			return err
		}
		accounts = append(accounts, store.SeedAccount{
			User:         store.User{ID: "usr_" + id, Login: login, CreatedAt: timestamp()},
			PasswordHash: string(hash),
		})
	}
	return s.db.SeedAccounts(ctx, accounts)
}

func ValidLogin(login string) bool {
	return utf8.ValidString(login) && len(login) >= 1 && len(login) <= 128 &&
		!strings.ContainsFunc(login, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

type LoginResult struct {
	SessionToken string     `json:"session_token"`
	ExpiresAt    string     `json:"expires_at"`
	User         store.User `json:"user"`
}

func (s *Service) Login(ctx context.Context, login, password string) (LoginResult, error) {
	user, hash, err := s.db.LoginCredentials(ctx, login)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return LoginResult{}, err
	}
	if errors.Is(err, store.ErrNotFound) {
		// Public dummy bcrypt hash, used only to perform equivalent password work
		// for unknown accounts. No account uses this hash.
		hash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
	}
	compareErr := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err != nil || compareErr != nil || len(password) > 72 {
		return LoginResult{}, ErrUnauthorized
	}
	token, err := randomToken()
	if err != nil {
		return LoginResult{}, err
	}
	expires := time.Now().UTC().Add(SessionLifetime).Format(time.RFC3339)
	if err := s.db.CreateAuthSession(ctx, tokenHash(token), user.ID, expires); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{SessionToken: token, ExpiresAt: expires, User: user}, nil
}

type Session struct {
	User      store.User
	TokenHash string
}

func (s *Service) Authenticate(ctx context.Context, token string) (Session, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(decoded) != 32 || len(token) != 43 {
		return Session{}, ErrUnauthorized
	}
	hash := tokenHash(token)
	user, err := s.db.SessionUser(ctx, hash, timestamp())
	if errors.Is(err, store.ErrNotFound) {
		return Session{}, ErrUnauthorized
	}
	return Session{User: user, TokenHash: hash}, err
}

func (s *Service) Logout(ctx context.Context, session Session) error {
	return s.db.RevokeAuthSession(ctx, session.TokenHash, session.User.ID, timestamp())
}

func tokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func randomToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", errors.New("cannot generate secure identifier")
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func timestamp() string { return time.Now().UTC().Format(time.RFC3339) }
