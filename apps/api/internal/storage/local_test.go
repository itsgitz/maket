package storage_test

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/storage"
	"github.com/itsgitz/maket/apps/api/internal/storage/storagetest"
)

func newLocal(t *testing.T, dir string) *storage.LocalStore {
	t.Helper()
	s, err := storage.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestLocalConformance(t *testing.T) {
	storagetest.Run(t, func(t *testing.T) storage.Storage {
		return newLocal(t, t.TempDir())
	})
}

func TestNewLocalCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	s := newLocal(t, dir)
	storagetest.Put(t, s, "k", "v")
	if b, err := os.ReadFile(filepath.Join(dir, "k")); err != nil || string(b) != "v" {
		t.Errorf("file on disk = %q, %v", b, err)
	}
}

// Symlinks planted in the store, by a bug or an attacker, must not let
// reads or writes reach outside it.
func TestLocalSymlinkEscape(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newLocal(t, dir)
	storagetest.Put(t, s, "prototypes/abc/v1/index.html", "x")
	for name, target := range map[string]string{
		"prototypes/dirlink":           outside, // directory link, absolute
		"prototypes/rellink":           "../../" + filepath.Base(outside),
		"prototypes/abc/v1/secret.txt": secret, // file link
	} {
		if err := os.Symlink(target, filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	// The relative link only escapes if dir and outside share a parent.
	if filepath.Dir(dir) != filepath.Dir(outside) {
		t.Logf("temp dirs differ in parent; relative link points elsewhere")
	}
	ctx := t.Context()

	for _, key := range []string{"prototypes/dirlink/secret", "prototypes/rellink/secret", "prototypes/abc/v1/secret.txt"} {
		obj, err := s.Open(ctx, key)
		if err == nil {
			b, _ := io.ReadAll(obj)
			obj.Close()
			t.Errorf("Open(%q) read %q through a symlink", key, b)
		}
	}
	for _, key := range []string{"prototypes/dirlink/secret", "prototypes/rellink/secret", "prototypes/dirlink/new"} {
		if err := s.Put(ctx, key, strings.NewReader("pwned")); err == nil {
			t.Errorf("Put(%q) wrote through a symlink", key)
		}
	}
	// Putting at the link itself replaces the link, not its target.
	storagetest.Put(t, s, "prototypes/abc/v1/secret.txt", "replaced")
	if got := storagetest.Read(t, s, "prototypes/abc/v1/secret.txt"); got != "replaced" {
		t.Errorf("Read after replacing link = %q", got)
	}

	// List skips links; DeletePrefix removes them without following.
	if got := storagetest.Keys(t, s, "prototypes/"); !slices.Equal(got, []string{
		"prototypes/abc/v1/index.html", "prototypes/abc/v1/secret.txt",
	}) {
		t.Errorf("List = %q", got)
	}
	if err := s.DeletePrefix(ctx, "prototypes/"); err != nil {
		t.Fatal(err)
	}

	if b, err := os.ReadFile(secret); err != nil || string(b) != "secret" {
		t.Errorf("file outside the store changed: %q, %v", b, err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 1 {
		t.Errorf("outside dir has %d entries, %v; want only secret", len(entries), err)
	}
}

// errReader yields some bytes, then fails.
type errReader struct{ n int }

var errBoom = errors.New("boom")

func (r *errReader) Read(p []byte) (int, error) {
	if r.n > 0 {
		n := min(r.n, len(p))
		for i := range n {
			p[i] = 'x'
		}
		r.n -= n
		return n, nil
	}
	return 0, errBoom
}

func TestLocalPutFailingReader(t *testing.T) {
	dir := t.TempDir()
	s := newLocal(t, dir)
	ctx := t.Context()

	const fresh, existing = "prototypes/abc/v1/new.js", "prototypes/abc/v1/old.js"
	storagetest.Put(t, s, existing, "old")
	for _, key := range []string{fresh, existing} {
		if err := s.Put(ctx, key, &errReader{n: 100_000}); !errors.Is(err, errBoom) {
			t.Errorf("Put(%q) = %v, want errBoom", key, err)
		}
	}
	if _, err := s.Open(ctx, fresh); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Open(%q) = %v, want ErrNotFound", fresh, err)
	}
	if got := storagetest.Read(t, s, existing); got != "old" {
		t.Errorf("failed Put changed %q to %q", existing, got)
	}
	// Nothing but the existing object is on disk, temp files included.
	var files []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return err
	})
	if !slices.Equal(files, []string{existing}) {
		t.Errorf("files on disk = %q, want only %q", files, existing)
	}
}

func TestLocalDeletePrefixPrunesEmptyDirs(t *testing.T) {
	dir := t.TempDir()
	s := newLocal(t, dir)
	ctx := t.Context()
	storagetest.Put(t, s, "prototypes/abc/v1/assets/deep/app.js", "x")
	storagetest.Put(t, s, "prototypes/abc/v1/index.html", "x")
	storagetest.Put(t, s, "prototypes/xyz/v3/index.html", "x")

	if err := s.DeletePrefix(ctx, "prototypes/abc/v1/"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "prototypes", "abc")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("empty prototypes/abc left behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "prototypes", "xyz", "v3", "index.html")); err != nil {
		t.Errorf("unrelated object removed: %v", err)
	}

	if err := s.DeletePrefix(ctx, "prototypes/xyz/v3/"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "prototypes")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("empty prototypes dir left behind: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("store root removed: %v", err)
	}
	storagetest.Put(t, s, "prototypes/abc/v2/index.html", "x") // still usable
}

func TestLocalCleanTemp(t *testing.T) {
	dir := t.TempDir()
	s := newLocal(t, dir)
	if err := s.CleanTemp(time.Hour); err != nil {
		t.Errorf("CleanTemp with no temp dir: %v", err)
	}

	tmp := filepath.Join(dir, ".tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	old, fresh := filepath.Join(tmp, "old"), filepath.Join(tmp, "fresh")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("partial"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	storagetest.Put(t, s, "prototypes/abc/v1/index.html", "kept")

	if err := s.CleanTemp(time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("old temp file survived: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh temp file removed: %v", err)
	}
	if got := storagetest.Read(t, s, "prototypes/abc/v1/index.html"); got != "kept" {
		t.Errorf("object changed by CleanTemp: %q", got)
	}
}
