package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	apiKeyPrefix = "mk_"
	base62       = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	// apiKeyBytes is the key's entropy.
	apiKeyBytes = 32
	// apiKeyBodyLen is the base62 length of any 32-byte value: 62^43 > 2^256.
	// Shorter encodings are left-padded with '0', so every key is
	// len("mk_") + 43 = 46 characters.
	apiKeyBodyLen = 43
	apiKeyLen     = len(apiKeyPrefix) + apiKeyBodyLen
	// apiKeyDisplayLen is how much of the key after "mk_" is kept as its
	// display prefix.
	apiKeyDisplayLen = 8
	// lastUsedEvery throttles writes of last_used_at.
	lastUsedEvery = time.Minute
	maxKeyName    = 100
)

// APIKey is an API Key's metadata; the secret itself is never stored.
type APIKey struct {
	ID         int64
	Name       string
	Prefix     string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// newAPIKeySecret returns a fresh "mk_" + 43 base62 characters key.
func newAPIKeySecret() string {
	b := make([]byte, apiKeyBytes)
	rand.Read(b)
	return apiKeyPrefix + encodeBase62(b)
}

// encodeBase62 encodes b as a big-endian number, left-padded to apiKeyBodyLen.
func encodeBase62(b []byte) string {
	n := new(big.Int).SetBytes(b)
	out := make([]byte, apiKeyBodyLen)
	base, rem := big.NewInt(62), new(big.Int)
	for i := len(out) - 1; i >= 0; i-- {
		n.QuoRem(n, base, rem)
		out[i] = base62[rem.Int64()]
	}
	return string(out)
}

// wellFormedAPIKey reports whether secret has the shape of a key we issue.
func wellFormedAPIKey(secret string) bool {
	body, ok := strings.CutPrefix(secret, apiKeyPrefix)
	if !ok || len(body) != apiKeyBodyLen {
		return false
	}
	for i := 0; i < len(body); i++ {
		if strings.IndexByte(base62, body[i]) < 0 {
			return false
		}
	}
	return true
}

// CreateAPIKey creates a key for userID and returns its metadata and the
// secret, which is shown once and stored only as a hash. The trimmed name
// must be 1-100 characters, else ErrInvalidName.
func (s *Service) CreateAPIKey(ctx context.Context, userID int64, name string) (key APIKey, secret string, err error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxKeyName || !utf8.ValidString(name) {
		return APIKey{}, "", ErrInvalidName
	}
	secret = newAPIKeySecret()
	key = APIKey{
		Name:      name,
		Prefix:    secret[len(apiKeyPrefix) : len(apiKeyPrefix)+apiKeyDisplayLen],
		CreatedAt: s.clock(),
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys (user_id, name, prefix, key_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, key.Name, key.Prefix, hash(secret), key.CreatedAt,
	)
	if err != nil {
		return APIKey{}, "", fmt.Errorf("auth: create api key: %w", err)
	}
	if key.ID, err = res.LastInsertId(); err != nil {
		return APIKey{}, "", fmt.Errorf("auth: create api key: %w", err)
	}
	return key, secret, nil
}

// ListAPIKeys returns userID's keys, newest first, revoked ones included.
func (s *Service) ListAPIKeys(ctx context.Context, userID int64) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, prefix, created_at, last_used_at, revoked_at
		FROM api_keys WHERE user_id = ?
		ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("auth: list api keys: %w", err)
	}
	defer rows.Close()
	keys := []APIKey{}
	for rows.Next() {
		var k APIKey
		var lastUsed, revoked sql.NullTime
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedAt, &lastUsed, &revoked); err != nil {
			return nil, fmt.Errorf("auth: list api keys: %w", err)
		}
		k.CreatedAt = k.CreatedAt.UTC()
		k.LastUsedAt, k.RevokedAt = timePtr(lastUsed), timePtr(revoked)
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list api keys: %w", err)
	}
	return keys, nil
}

// RevokeAPIKey revokes one of userID's keys. Revoking an already revoked key
// succeeds and keeps the original time; a key userID does not own is
// ErrNotFound.
func (s *Service) RevokeAPIKey(ctx context.Context, userID, keyID int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ? AND user_id = ?`,
		s.clock(), keyID, userID,
	)
	if err != nil {
		return fmt.Errorf("auth: revoke api key: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: revoke api key: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResolveAPIKey returns the Principal for a key secret: ErrUnauthenticated if
// it is malformed, unknown or revoked, ErrNotAllowed if its User is no longer
// on the Allowlist. It records last_used_at at most once a minute per key.
func (s *Service) ResolveAPIKey(ctx context.Context, secret string) (Principal, error) {
	if !wellFormedAPIKey(secret) {
		return Principal{}, ErrUnauthenticated
	}
	var keyID int64
	var lastUsed, revoked sql.NullTime
	u, err := scanUser(s.db.QueryRowContext(ctx, `
		SELECT `+userColumns+`, k.id, k.last_used_at, k.revoked_at
		FROM api_keys k JOIN users u ON u.id = k.user_id
		WHERE k.key_hash = ?`, hash(secret)), &keyID, &lastUsed, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, fmt.Errorf("auth: resolve api key: %w", err)
	}
	if revoked.Valid {
		return Principal{}, ErrUnauthenticated
	}
	if !s.allow.Allows(u.GitHubLogin) {
		return Principal{}, ErrNotAllowed
	}
	if now := s.clock(); !lastUsed.Valid || now.Sub(lastUsed.Time) >= lastUsedEvery {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE api_keys SET last_used_at = ? WHERE id = ?`, now, keyID,
		); err != nil {
			return Principal{}, fmt.Errorf("auth: record api key use: %w", err)
		}
	}
	return Principal{User: u, Method: MethodAPIKey, APIKeyID: keyID}, nil
}

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}
