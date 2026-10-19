// Package auth signs Users in with GitHub, keeps their sessions and API Keys,
// and resolves either credential into a Principal for HTTP handlers.
package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Method is how a request was authenticated.
type Method string

const (
	MethodSession Method = "session"
	MethodAPIKey  Method = "api_key"
)

// User is a person who signed in with GitHub.
type User struct {
	ID          int64
	GitHubID    int64
	GitHubLogin string
	Name        string // "" when GitHub has none
	AvatarURL   string
	CreatedAt   time.Time
}

// Identity is what a login provider reports about the person signing in.
type Identity struct {
	GitHubID  int64
	Login     string
	Name      string
	AvatarURL string
}

// Principal is the authenticated User behind a request and how they got in.
type Principal struct {
	User      User
	Method    Method
	SessionID int64 // set when Method == MethodSession
	APIKeyID  int64 // set when Method == MethodAPIKey
}

type principalKey struct{}

// WithPrincipal returns a copy of ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the Principal attached by WithPrincipal, if any.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

var (
	ErrUnauthenticated = errors.New("auth: invalid, expired or revoked credential")
	ErrNotAllowed      = errors.New("auth: user is not on the allowlist")
	ErrNotFound        = errors.New("auth: not found")
	ErrInvalidName     = errors.New("auth: name must be 1-100 characters")
)

// Service stores Users, sessions and API Keys.
type Service struct {
	db    *sql.DB
	allow Allowlist
	now   func() time.Time
}

// NewService returns a Service over db. now is injectable for tests; pass
// time.Now in production.
func NewService(db *sql.DB, allow Allowlist, now func() time.Time) *Service {
	return &Service{db: db, allow: allow, now: now}
}

// clock returns the current time in UTC, the only zone stored.
func (s *Service) clock() time.Time { return s.now().UTC() }

// LoginGitHub returns ErrNotAllowed, writing nothing, if id.Login is not on the
// Allowlist. Otherwise it upserts the User by GitHub ID, refreshing login,
// name and avatar, and returns it.
func (s *Service) LoginGitHub(ctx context.Context, id Identity) (User, error) {
	if !s.allow.Allows(id.Login) {
		return User{}, ErrNotAllowed
	}
	if id.GitHubID <= 0 {
		return User{}, fmt.Errorf("auth: login: invalid GitHub ID %d", id.GitHubID)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO users (github_id, github_login, name, avatar_url, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (github_id) DO UPDATE SET
			github_login = excluded.github_login,
			name = excluded.name,
			avatar_url = excluded.avatar_url`,
		id.GitHubID, id.Login, nullString(id.Name), nullString(id.AvatarURL), s.clock(),
	); err != nil {
		return User{}, fmt.Errorf("auth: login: upsert user: %w", err)
	}
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users u WHERE u.github_id = ?`, id.GitHubID))
	if err != nil {
		return User{}, fmt.Errorf("auth: login: read user: %w", err)
	}
	return u, nil
}

// userColumns are the users columns scanUser reads, from a table aliased u.
const userColumns = `u.id, u.github_id, u.github_login, u.name, u.avatar_url, u.created_at`

// scanUser scans userColumns, optionally followed by extra destinations.
func scanUser(row interface{ Scan(...any) error }, extra ...any) (User, error) {
	var u User
	var name, avatar sql.NullString
	dest := append([]any{&u.ID, &u.GitHubID, &u.GitHubLogin, &name, &avatar, &u.CreatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return User{}, err
	}
	u.Name, u.AvatarURL = name.String, avatar.String
	u.CreatedAt = u.CreatedAt.UTC() // the driver returns a fixed +00:00 zone
	return u, nil
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// hash is the digest stored in place of a session token or API Key.
func hash(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}
