package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

const (
	// sessionTTL is how long a session lives after it was last extended.
	sessionTTL = 30 * 24 * time.Hour
	// sessionRenewBelow is the remaining lifetime under which a resolved
	// session is extended, so the expiry is written at most about daily.
	sessionRenewBelow = 29 * 24 * time.Hour
	// sessionTokenBytes is the token's entropy; it is sent base64url without
	// padding, 43 characters.
	sessionTokenBytes = 32
)

// CreateSession starts a session for userID and returns its token, to be
// sent only in the session cookie, and its expiry.
func (s *Service) CreateSession(ctx context.Context, userID int64) (token string, expires time.Time, err error) {
	b := make([]byte, sessionTokenBytes)
	rand.Read(b)
	token = base64.RawURLEncoding.EncodeToString(b)
	now := s.clock()
	expires = now.Add(sessionTTL)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (user_id, token_hash, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		userID, hash(token), expires, now,
	); err != nil {
		return "", time.Time{}, fmt.Errorf("auth: create session: %w", err)
	}
	return token, expires, nil
}

// ResolveSession returns the Principal for a session token: ErrUnauthenticated
// if the token is malformed, unknown or expired, ErrNotAllowed if its User is
// no longer on the Allowlist. renewed is non-zero only when the expiry was
// extended, and is then the new expiry.
func (s *Service) ResolveSession(ctx context.Context, token string) (p Principal, renewed time.Time, err error) {
	if b, err := base64.RawURLEncoding.Strict().DecodeString(token); err != nil || len(b) != sessionTokenBytes {
		return Principal{}, time.Time{}, ErrUnauthenticated
	}
	var sessionID int64
	var expires time.Time
	u, err := scanUser(s.db.QueryRowContext(ctx, `
		SELECT `+userColumns+`, s.id, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ?`, hash(token)), &sessionID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, time.Time{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, time.Time{}, fmt.Errorf("auth: resolve session: %w", err)
	}
	now := s.clock()
	if !now.Before(expires) {
		return Principal{}, time.Time{}, ErrUnauthenticated
	}
	if !s.allow.Allows(u.GitHubLogin) {
		return Principal{}, time.Time{}, ErrNotAllowed
	}
	if expires.Sub(now) < sessionRenewBelow {
		renewed = now.Add(sessionTTL)
		if _, err := s.db.ExecContext(ctx,
			`UPDATE sessions SET expires_at = ? WHERE id = ?`, renewed, sessionID,
		); err != nil {
			return Principal{}, time.Time{}, fmt.Errorf("auth: renew session: %w", err)
		}
	}
	return Principal{User: u, Method: MethodSession, SessionID: sessionID}, renewed, nil
}

// DeleteSession ends a session. Deleting an unknown session is not an error.
func (s *Service) DeleteSession(ctx context.Context, sessionID int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sessionID); err != nil {
		return fmt.Errorf("auth: delete session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions removes sessions past their expiry and reports how
// many there were.
func (s *Service) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	// All times are stored UTC in one format, so they compare as text.
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, s.clock())
	if err != nil {
		return 0, fmt.Errorf("auth: delete expired sessions: %w", err)
	}
	return res.RowsAffected()
}
