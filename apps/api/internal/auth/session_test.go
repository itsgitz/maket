package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func TestCreateAndResolveSession(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	u := mustLogin(t, s, 1, "alice")

	token, expires, err := s.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := base64.RawURLEncoding.DecodeString(token); err != nil || len(b) != 32 {
		t.Errorf("token %q is not 32 bytes of base64url: %v", token, err)
	}
	if want := t0.Add(30 * 24 * time.Hour); !expires.Equal(want) {
		t.Errorf("expires = %v, want %v", expires, want)
	}

	var stored []byte
	if err := s.db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256([]byte(token)); !bytes.Equal(stored, sum[:]) {
		t.Error("token_hash is not SHA-256 of the token")
	}

	p, renewed, err := s.ResolveSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if p.Method != MethodSession || p.SessionID == 0 || p.APIKeyID != 0 || p.User != u {
		t.Errorf("principal = %+v", p)
	}
	if !renewed.IsZero() {
		t.Errorf("fresh session renewed to %v", renewed)
	}
}

func TestResolveSessionRejects(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	u := mustLogin(t, s, 1, "alice")
	token, _, err := s.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	unknown := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, tok := range []string{"", "nope", token + "A", token[:len(token)-1], unknown, token + "="} {
		if _, _, err := s.ResolveSession(ctx, tok); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("ResolveSession(%q) err = %v, want ErrUnauthenticated", tok, err)
		}
	}
}

func TestSessionExpiry(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	u := mustLogin(t, s, 1, "alice")
	token, expires, err := s.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	clk.t = expires.Add(-time.Nanosecond)
	if _, _, err := s.ResolveSession(ctx, token); err != nil {
		t.Fatalf("just before expiry: %v", err)
	}
	// That resolve renewed it; jump past the new expiry.
	clk.advance(30*24*time.Hour + time.Second)
	if _, _, err := s.ResolveSession(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("expired: err = %v, want ErrUnauthenticated", err)
	}
}

func TestSessionSlidingRenewal(t *testing.T) {
	const day = 24 * time.Hour
	tests := []struct {
		name        string
		after       time.Duration // since creation
		wantRenewed bool
	}{
		{"fresh", 0, false},
		{"23 hours", 23 * time.Hour, false},
		{"exactly 29 days left", day, false},
		{"just under 29 days left", day + time.Second, true},
		{"10 days in", 10 * day, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, clk := newTestService(t)
			ctx := context.Background()
			u := mustLogin(t, s, 1, "alice")
			token, created, err := s.CreateSession(ctx, u.ID)
			if err != nil {
				t.Fatal(err)
			}
			clk.advance(tt.after)
			_, renewed, err := s.ResolveSession(ctx, token)
			if err != nil {
				t.Fatal(err)
			}
			want := created
			if tt.wantRenewed {
				want = clk.t.Add(30 * day)
				if !renewed.Equal(want) {
					t.Errorf("renewed = %v, want %v", renewed, want)
				}
			} else if !renewed.IsZero() {
				t.Errorf("renewed = %v, want zero", renewed)
			}
			var stored time.Time
			if err := s.db.QueryRow(`SELECT expires_at FROM sessions`).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if !stored.Equal(want) {
				t.Errorf("stored expiry = %v, want %v", stored, want)
			}
		})
	}
}

func TestDeleteSessions(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	u := mustLogin(t, s, 1, "alice")
	old, _, err := s.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(10 * 24 * time.Hour)
	young, _, err := s.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(25 * 24 * time.Hour) // old expired 5 days ago; young has 15 days left

	n, err := s.DeleteExpiredSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("deleted %d, want 1", n)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM sessions WHERE token_hash = ?`, hash(old)).Scan(&count); err != nil || count != 0 {
		t.Errorf("old session remains (count %d, err %v)", count, err)
	}
	p, _, err := s.ResolveSession(ctx, young)
	if err != nil {
		t.Fatalf("young session: %v", err)
	}

	if err := s.DeleteSession(ctx, p.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ResolveSession(ctx, young); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("after DeleteSession: err = %v, want ErrUnauthenticated", err)
	}
	if err := s.DeleteSession(ctx, p.SessionID); err != nil {
		t.Errorf("second DeleteSession: %v", err)
	}
}

func TestResolveSessionNotAllowed(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	u := mustLogin(t, s, 1, "bob")
	token, _, err := s.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	narrow := NewService(s.db, mustAllowlist(t, "alice"), clk.now)
	if _, _, err := narrow.ResolveSession(ctx, token); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("err = %v, want ErrNotAllowed", err)
	}
}
