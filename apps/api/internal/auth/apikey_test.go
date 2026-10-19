package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestAPIKeyFormat(t *testing.T) {
	seen := map[string]bool{}
	for range 2000 {
		k := newAPIKeySecret()
		if !strings.HasPrefix(k, "mk_") {
			t.Fatalf("key %q lacks mk_ prefix", k)
		}
		if len(k) != 46 {
			t.Fatalf("key %q has length %d, want 46", k, len(k))
		}
		if !wellFormedAPIKey(k) {
			t.Fatalf("key %q is not well formed", k)
		}
		if seen[k] {
			t.Fatalf("duplicate key %q", k)
		}
		seen[k] = true
	}
}

func TestEncodeBase62(t *testing.T) {
	zeros := make([]byte, 32)
	ones := bytes.Repeat([]byte{0xff}, 32)
	one := append(make([]byte, 31), 1)
	big := append(make([]byte, 31), 62)
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{"zero is all padding", zeros, strings.Repeat("0", 43)},
		{"one", one, strings.Repeat("0", 42) + "1"},
		{"62 carries", big, strings.Repeat("0", 41) + "10"},
	}
	for _, tt := range tests {
		if got := encodeBase62(tt.in); got != tt.want {
			t.Errorf("%s: encodeBase62 = %q, want %q", tt.name, got, tt.want)
		}
	}
	// The largest value still fits in 43 digits without overflowing.
	if got := encodeBase62(ones); len(got) != 43 || got[0] == '0' {
		t.Errorf("encodeBase62(max) = %q", got)
	}
}

func TestWellFormedAPIKey(t *testing.T) {
	good := "mk_" + strings.Repeat("aZ9", 14) + "x"
	tests := []struct {
		key  string
		want bool
	}{
		{good, true},
		{"", false},
		{"mk_", false},
		{good[:len(good)-1], false},
		{good + "x", false},
		{"MK_" + good[3:], false},
		{"mk-" + good[3:], false},
		{good[:10] + "-" + good[11:], false},
		{good[:10] + "_" + good[11:], false},
		{good[:10] + "é" + good[12:], false},
	}
	for _, tt := range tests {
		if got := wellFormedAPIKey(tt.key); got != tt.want {
			t.Errorf("wellFormedAPIKey(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}

func TestCreateAPIKeyStoresOnlyHash(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	u := mustLogin(t, s, 1, "alice")
	key, secret, err := s.CreateAPIKey(ctx, u.ID, "  laptop agent  ")
	if err != nil {
		t.Fatal(err)
	}
	want := APIKey{ID: key.ID, Name: "laptop agent", Prefix: secret[3:11], CreatedAt: t0}
	if key.ID == 0 || key.Name != want.Name || key.Prefix != want.Prefix || !key.CreatedAt.Equal(t0) || key.LastUsedAt != nil || key.RevokedAt != nil {
		t.Errorf("key = %+v, want %+v", key, want)
	}

	var name, prefix string
	var keyHash []byte
	var created time.Time
	if err := s.db.QueryRow(`SELECT name, prefix, key_hash, created_at FROM api_keys WHERE id = ?`, key.ID).
		Scan(&name, &prefix, &keyHash, &created); err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256([]byte(secret)); !bytes.Equal(keyHash, sum[:]) {
		t.Error("key_hash is not SHA-256 of the secret")
	}
	if prefix != secret[3:11] || name != "laptop agent" || !created.Equal(t0) {
		t.Errorf("row = %q %q %v", name, prefix, created)
	}

	// No column of the row holds the secret or its random part.
	rows, err := s.db.Query(`SELECT * FROM api_keys`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for i, v := range vals {
			var s string
			switch v := v.(type) {
			case []byte:
				s = string(v)
			default:
				s = fmt.Sprint(v)
			}
			if strings.Contains(s, secret[3:]) || strings.Contains(s, secret[11:]) {
				t.Errorf("column %s contains the secret", cols[i])
			}
		}
	}
}

func TestCreateAPIKeyName(t *testing.T) {
	s, _ := newTestService(t)
	u := mustLogin(t, s, 1, "alice")
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"x", false},
		{strings.Repeat("a", 100), false},
		{strings.Repeat("é", 100), false},
		{" " + strings.Repeat("a", 100) + " ", false},
		{"", true},
		{"   ", true},
		{strings.Repeat("a", 101), true},
		{"bad\xffutf8", true},
	}
	for _, tt := range tests {
		_, _, err := s.CreateAPIKey(context.Background(), u.ID, tt.name)
		if tt.wantErr != errors.Is(err, ErrInvalidName) || (!tt.wantErr && err != nil) {
			t.Errorf("CreateAPIKey(%q) err = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	alice := mustLogin(t, s, 1, "alice")
	bob := mustLogin(t, s, 2, "bob")

	k1, secret1, err := s.CreateAPIKey(ctx, alice.ID, "one")
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Second)
	k2, _, err := s.CreateAPIKey(ctx, alice.ID, "two")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateAPIKey(ctx, bob.ID, "bob's"); err != nil {
		t.Fatal(err)
	}

	p, err := s.ResolveAPIKey(ctx, secret1)
	if err != nil {
		t.Fatal(err)
	}
	if p.Method != MethodAPIKey || p.APIKeyID != k1.ID || p.SessionID != 0 || p.User != alice {
		t.Errorf("principal = %+v", p)
	}

	if err := s.RevokeAPIKey(ctx, bob.ID, k1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob revoking alice's key: err = %v, want ErrNotFound", err)
	}
	if err := s.RevokeAPIKey(ctx, alice.ID, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking unknown key: err = %v, want ErrNotFound", err)
	}
	revokedAt := clk.t
	if err := s.RevokeAPIKey(ctx, alice.ID, k1.ID); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Hour)
	if err := s.RevokeAPIKey(ctx, alice.ID, k1.ID); err != nil {
		t.Errorf("second revoke: %v", err)
	}
	if _, err := s.ResolveAPIKey(ctx, secret1); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("revoked key: err = %v, want ErrUnauthenticated", err)
	}

	keys, err := s.ListAPIKeys(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0].ID != k2.ID || keys[1].ID != k1.ID {
		t.Fatalf("ListAPIKeys = %+v, want [two, one]", keys)
	}
	if keys[0].RevokedAt != nil || keys[0].LastUsedAt != nil {
		t.Errorf("key two = %+v, want unused and unrevoked", keys[0])
	}
	if keys[1].RevokedAt == nil || !keys[1].RevokedAt.Equal(revokedAt) {
		t.Errorf("key one revoked_at = %v, want %v (first revoke kept)", keys[1].RevokedAt, revokedAt)
	}
	if want := t0.Add(time.Second); keys[1].LastUsedAt == nil || !keys[1].LastUsedAt.Equal(want) {
		t.Errorf("key one last_used_at = %v, want %v", keys[1].LastUsedAt, want)
	}

	none, err := s.ListAPIKeys(ctx, 9999)
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("ListAPIKeys(unknown) = %#v, %v; want empty non-nil", none, err)
	}
}

func TestResolveAPIKeyRejects(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	u := mustLogin(t, s, 1, "bob")
	_, secret, err := s.CreateAPIKey(ctx, u.ID, "k")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"", "mk_short", secret[:45], "mk_" + strings.Repeat("0", 43)} {
		if _, err := s.ResolveAPIKey(ctx, k); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("ResolveAPIKey(%q) err = %v, want ErrUnauthenticated", k, err)
		}
	}
	narrow := NewService(s.db, mustAllowlist(t, "alice"), clk.now)
	if _, err := narrow.ResolveAPIKey(ctx, secret); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("removed from allowlist: err = %v, want ErrNotAllowed", err)
	}
}

func TestAPIKeyLastUsedThrottle(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	u := mustLogin(t, s, 1, "alice")
	k, secret, err := s.CreateAPIKey(ctx, u.ID, "k")
	if err != nil {
		t.Fatal(err)
	}
	lastUsed := func() time.Time {
		t.Helper()
		var lu time.Time
		if err := s.db.QueryRow(`SELECT last_used_at FROM api_keys WHERE id = ?`, k.ID).Scan(&lu); err != nil {
			t.Fatal(err)
		}
		return lu
	}
	steps := []struct {
		advance time.Duration
		want    time.Time
	}{
		{0, t0},                            // first use is recorded
		{30 * time.Second, t0},             // within a minute: not written
		{29 * time.Second, t0},             // 59s: still not
		{time.Second, t0.Add(time.Minute)}, // exactly a minute: written
		{time.Minute + time.Second, t0.Add(2*time.Minute + time.Second)},
	}
	for i, st := range steps {
		clk.advance(st.advance)
		if _, err := s.ResolveAPIKey(ctx, secret); err != nil {
			t.Fatal(err)
		}
		if got := lastUsed(); !got.Equal(st.want) {
			t.Errorf("step %d: last_used_at = %v, want %v", i, got, st.want)
		}
	}
}
