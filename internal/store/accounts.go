package store

import (
	"context"
	"database/sql"
	"errors"
)

// ErrNotFound deliberately does not distinguish a missing or inaccessible row.
var ErrNotFound = errors.New("resource not found")

type User struct {
	ID        string `json:"id"`
	Login     string `json:"login"`
	CreatedAt string `json:"created_at"`
}

type SeedAccount struct {
	User
	PasswordHash string
}

// SeedAccounts creates missing accounts/profiles atomically. A repeated seed
// never changes IDs, password hashes, creation times or existing preferences.
func (s *Store) SeedAccounts(ctx context.Context, accounts []SeedAccount) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, account := range accounts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO users(id, login, password_hash, created_at)
			VALUES (?, ?, ?, ?) ON CONFLICT(login) DO NOTHING`,
			account.ID, account.Login, account.PasswordHash, account.CreatedAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO profiles(user_id, updated_at)
			SELECT id, created_at FROM users WHERE login = ?
			ON CONFLICT(user_id) DO NOTHING`, account.Login); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) LoginCredentials(ctx context.Context, login string) (User, string, error) {
	var user User
	var hash string
	err := s.DB.QueryRowContext(ctx, `SELECT id, login, created_at, password_hash
		FROM users WHERE login = ?`, login).Scan(&user.ID, &user.Login, &user.CreatedAt, &hash)
	return user, hash, notFound(err)
}

func (s *Store) CreateAuthSession(ctx context.Context, hash, userID, expiresAt string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO auth_sessions(token_hash, user_id, expires_at)
		VALUES (?, ?, ?)`, hash, userID, expiresAt)
	return err
}

func (s *Store) SessionUser(ctx context.Context, hash, now string) (User, error) {
	var user User
	err := s.DB.QueryRowContext(ctx, `SELECT u.id, u.login, u.created_at
		FROM auth_sessions AS a JOIN users AS u ON u.id = a.user_id
		WHERE a.token_hash = ? AND a.revoked_at IS NULL AND a.expires_at > ?`,
		hash, now).Scan(&user.ID, &user.Login, &user.CreatedAt)
	return user, notFound(err)
}

func (s *Store) RevokeAuthSession(ctx context.Context, hash, userID, now string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at = ?
		WHERE token_hash = ? AND user_id = ? AND revoked_at IS NULL`, now, hash, userID)
	return err
}

type Profile struct {
	UserID            string `json:"user_id"`
	Interests         string `json:"interests"`
	PreferredLanguage string `json:"preferred_language"`
	UpdatedAt         string `json:"updated_at"`
}

// Profile and UpdateProfile accept only the authenticated owner. Callers must
// never obtain userID from a body, query parameter or model-generated argument.
func (s *Store) Profile(ctx context.Context, userID string) (Profile, error) {
	var profile Profile
	err := s.DB.QueryRowContext(ctx, `SELECT user_id, interests, preferred_language, updated_at
		FROM profiles WHERE user_id = ?`, userID).Scan(&profile.UserID,
		&profile.Interests, &profile.PreferredLanguage, &profile.UpdatedAt)
	return profile, notFound(err)
}

func (s *Store) UpdateProfile(ctx context.Context, userID, interests, language, now string) (Profile, error) {
	var profile Profile
	err := s.DB.QueryRowContext(ctx, `UPDATE profiles
		SET interests = ?, preferred_language = ?, updated_at = ? WHERE user_id = ?
		RETURNING user_id, interests, preferred_language, updated_at`,
		interests, language, now, userID).Scan(&profile.UserID,
		&profile.Interests, &profile.PreferredLanguage, &profile.UpdatedAt)
	return profile, notFound(err)
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
