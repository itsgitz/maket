package auth

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/db"
)

// t0 is the fake clock's start; the nanoseconds check they survive a round trip.
var t0 = time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.UTC)

// fakeClock is an injectable now for Service.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// openTest opens a fresh, fully migrated database in a temp dir.
func openTest(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "maket.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func mustAllowlist(t *testing.T, logins ...string) Allowlist {
	t.Helper()
	a, err := NewAllowlist(logins)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// newTestService returns a Service allowing "alice" and "bob" over a fresh
// database, and its clock.
func newTestService(t *testing.T) (*Service, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: t0}
	return NewService(openTest(t), mustAllowlist(t, "alice", "bob"), clk.now), clk
}

func mustLogin(t *testing.T, s *Service, githubID int64, login string) User {
	t.Helper()
	u, err := s.LoginGitHub(context.Background(), Identity{GitHubID: githubID, Login: login, Name: "N " + login, AvatarURL: "https://a/" + login})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestAllowlist(t *testing.T) {
	a := mustAllowlist(t, " Alice ", "", "BOB", "  ")
	tests := []struct {
		login string
		want  bool
	}{
		{"alice", true},
		{"ALICE", true},
		{"Bob", true},
		{" bob ", true},
		{"carol", false},
		{"", false},
		{"alic", false},
	}
	for _, tt := range tests {
		if got := a.Allows(tt.login); got != tt.want {
			t.Errorf("Allows(%q) = %v, want %v", tt.login, got, tt.want)
		}
	}
}

func TestNewAllowlistEmpty(t *testing.T) {
	for _, logins := range [][]string{nil, {}, {""}, {" ", "\t"}} {
		if _, err := NewAllowlist(logins); err == nil {
			t.Errorf("NewAllowlist(%q) succeeded, want error", logins)
		}
	}
}

func TestZeroAllowlistAllowsNobody(t *testing.T) {
	if (Allowlist{}).Allows("alice") {
		t.Error("zero Allowlist allows alice")
	}
}

func TestLoginNotAllowedWritesNothing(t *testing.T) {
	s, _ := newTestService(t)
	_, err := s.LoginGitHub(context.Background(), Identity{GitHubID: 1, Login: "mallory"})
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("err = %v, want ErrNotAllowed", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("users has %d rows, want 0", n)
	}
}

func TestLoginUpserts(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	first, err := s.LoginGitHub(ctx, Identity{GitHubID: 42, Login: "Alice", Name: "Alice A", AvatarURL: "https://a/1"})
	if err != nil {
		t.Fatal(err)
	}
	want := User{ID: first.ID, GitHubID: 42, GitHubLogin: "Alice", Name: "Alice A", AvatarURL: "https://a/1", CreatedAt: t0}
	if first != want {
		t.Errorf("first login = %+v, want %+v", first, want)
	}

	// Same GitHub account, renamed and now without a display name.
	clk.advance(time.Hour)
	second, err := s.LoginGitHub(ctx, Identity{GitHubID: 42, Login: "bob", AvatarURL: "https://a/2"})
	if err != nil {
		t.Fatal(err)
	}
	want = User{ID: first.ID, GitHubID: 42, GitHubLogin: "bob", Name: "", AvatarURL: "https://a/2", CreatedAt: t0}
	if second != want {
		t.Errorf("second login = %+v, want %+v", second, want)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("users has %d rows, want 1", n)
	}
}

func TestPrincipalContext(t *testing.T) {
	ctx := context.Background()
	if _, ok := PrincipalFrom(ctx); ok {
		t.Error("PrincipalFrom(empty) ok")
	}
	p := Principal{User: User{ID: 7}, Method: MethodAPIKey, APIKeyID: 3}
	got, ok := PrincipalFrom(WithPrincipal(ctx, p))
	if !ok || got != p {
		t.Errorf("PrincipalFrom = %+v, %v; want %+v, true", got, ok, p)
	}
}
