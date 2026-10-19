package prototype

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/auth"
	"github.com/itsgitz/maket/apps/api/internal/db"
	"github.com/itsgitz/maket/apps/api/internal/preview"
	"github.com/itsgitz/maket/apps/api/internal/prototype/archive"
	"github.com/itsgitz/maket/apps/api/internal/storage"
	"github.com/itsgitz/maket/apps/api/internal/storage/storagetest"
)

type fakePurger struct {
	calls chan purgeCall
}

type purgeCall struct {
	host  string
	paths []string
}

func (f *fakePurger) Purge(ctx context.Context, host string, paths []string) error {
	f.calls <- purgeCall{host, slices.Sorted(slices.Values(paths))}
	return nil
}

type env struct {
	svc    *Service
	db     *sql.DB
	store  *storage.LocalStore
	purger *fakePurger
	tmp    string
	alice  int64
	bob    int64
}

func newEnv(t *testing.T, quota int64) *env {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(t.Context(), filepath.Join(dir, "maket.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	store, err := storage.NewLocal(filepath.Join(dir, "storage"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	tmp := filepath.Join(dir, "tmp")
	os.MkdirAll(tmp, 0o755)
	allow, _ := auth.NewAllowlist([]string{"alice", "bob"})
	e := &env{db: database, store: store, purger: &fakePurger{calls: make(chan purgeCall, 10)}, tmp: tmp}
	e.svc = NewService(database, store, allow, Config{
		Limits:      archive.Limits{MaxFiles: 50, MaxExtractedBytes: 1 << 20},
		QuotaBytes:  quota,
		TmpDir:      tmp,
		PreviewHost: func(slug string) string { return slug + ".cloudshare.my.id" },
	}, e.purger, time.Now)
	e.alice = e.user(t, 1, "alice")
	e.bob = e.user(t, 2, "bob")
	return e
}

func (e *env) user(t *testing.T, githubID int64, login string) int64 {
	t.Helper()
	var id int64
	err := e.db.QueryRow(`INSERT INTO users (github_id, github_login, created_at) VALUES (?, ?, ?) RETURNING id`,
		githubID, login, time.Now().UTC()).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func htmlUpload(name, body string) Upload {
	return Upload{Filename: name, File: bytes.NewReader([]byte(body)), Size: int64(len(body))}
}

func zipUpload(t *testing.T, name string, files map[string]string) Upload {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range slices.Sorted(func(yield func(string) bool) {
		for k := range files {
			if !yield(k) {
				return
			}
		}
	}) {
		w, err := zw.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(files[p]))
	}
	zw.Close()
	return Upload{Filename: name, File: bytes.NewReader(buf.Bytes()), Size: int64(buf.Len())}
}

func ptr[T any](v T) *T { return &v }

func TestCreate(t *testing.T) {
	e := newEnv(t, 1<<20)
	ctx := t.Context()

	p, err := e.svc.Create(ctx, e.alice, htmlUpload("landing.html", "<h1>hi</h1>"))
	if err != nil {
		t.Fatal(err)
	}
	if !preview.ValidSlug(p.Slug) || p.Name != "landing" || p.Version != 1 || p.FileCount != 1 || p.SizeBytes != 11 || p.SPA {
		t.Errorf("created %+v", p)
	}
	if got := storagetest.Keys(t, e.store, "prototypes/"+p.Slug+"/"); !slices.Equal(got, []string{"prototypes/" + p.Slug + "/v1/index.html"}) {
		t.Errorf("stored keys %v", got)
	}

	site, err := e.svc.LookupPreview(ctx, p.Slug)
	if err != nil || site != (preview.Site{Slug: p.Slug, Version: 1}) {
		t.Errorf("LookupPreview = %+v, %v", site, err)
	}

	z, err := e.svc.Create(ctx, e.alice, zipUpload(t, "build.zip", map[string]string{
		"dist/index.html": "<html>", "dist/assets/app.js": "x",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if z.Name != "build" || z.FileCount != 2 {
		t.Errorf("zip created %+v", z)
	}

	named, err := e.svc.Create(ctx, e.alice, Upload{Filename: "x.html", File: bytes.NewReader([]byte("a")), Size: 1, Name: ptr("  Demo  "), SPA: ptr(true)})
	if err != nil || named.Name != "Demo" || !named.SPA {
		t.Errorf("named create = %+v, %v", named, err)
	}

	list, err := e.svc.List(ctx, e.alice)
	if err != nil || len(list) != 3 || list[0].Slug != named.Slug {
		t.Errorf("list = %v, %v", list, err)
	}
	if other, _ := e.svc.List(ctx, e.bob); len(other) != 0 {
		t.Errorf("bob sees %v", other)
	}
}

func TestCreateRejects(t *testing.T) {
	e := newEnv(t, 1<<20)
	ctx := t.Context()

	_, err := e.svc.Create(ctx, e.alice, zipUpload(t, "x.zip", map[string]string{"about.html": "a"}))
	var ae *archive.Error
	if !errors.As(err, &ae) || ae.Code != "missing_index" {
		t.Fatalf("got %v, want missing_index", err)
	}
	if _, err := e.svc.Create(ctx, e.alice, Upload{Filename: "x.html", File: bytes.NewReader([]byte("a")), Size: 1, Name: ptr(" ")}); !errors.Is(err, ErrInvalidName) {
		t.Errorf("blank name: %v", err)
	}
	if keys := storagetest.Keys(t, e.store, "prototypes/"); len(keys) != 0 {
		t.Errorf("rejected uploads left files: %v", keys)
	}
	if entries, _ := os.ReadDir(e.tmp); len(entries) != 0 {
		t.Errorf("tmp not cleaned: %v", entries)
	}
}

func TestReplace(t *testing.T) {
	e := newEnv(t, 1<<20)
	ctx := t.Context()
	p, _ := e.svc.Create(ctx, e.alice, Upload{Filename: "a.html", File: bytes.NewReader([]byte("v1")), Size: 2, Name: ptr("Mine"), SPA: ptr(true)})

	r, err := e.svc.Replace(ctx, e.alice, p.Slug, zipUpload(t, "other.zip", map[string]string{"index.html": "v2", "docs/index.html": "d"}))
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != 2 || r.Name != "Mine" || !r.SPA || r.FileCount != 2 || r.CreatedAt != p.CreatedAt {
		t.Errorf("replaced %+v (keeps name and spa when omitted)", r)
	}
	want := []string{"prototypes/" + p.Slug + "/v2/docs/index.html", "prototypes/" + p.Slug + "/v2/index.html"}
	if got := storagetest.Keys(t, e.store, "prototypes/"+p.Slug+"/"); !slices.Equal(got, want) {
		t.Errorf("after replace keys %v, want only v2", got)
	}
	select {
	case c := <-e.purger.calls:
		if c.host != p.Slug+".cloudshare.my.id" || !slices.Equal(c.paths, []string{"/", "/index.html"}) {
			t.Errorf("purge %+v", c)
		}
	case <-time.After(5 * time.Second):
		t.Error("no purge after replace")
	}

	r3, err := e.svc.Replace(ctx, e.alice, p.Slug, Upload{Filename: "b.html", File: bytes.NewReader([]byte("v3")), Size: 2, Name: ptr("Renamed"), SPA: ptr(false)})
	if err != nil || r3.Version != 3 || r3.Name != "Renamed" || r3.SPA {
		t.Errorf("replace with settings = %+v, %v", r3, err)
	}

	if _, err := e.svc.Replace(ctx, e.bob, p.Slug, htmlUpload("x.html", "x")); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob replacing alice's prototype: %v", err)
	}
	if _, err := e.svc.Replace(ctx, e.alice, "nope", htmlUpload("x.html", "x")); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad slug: %v", err)
	}
}

func TestQuota(t *testing.T) {
	e := newEnv(t, 100)
	ctx := t.Context()
	big := string(bytes.Repeat([]byte("a"), 60))

	p, err := e.svc.Create(ctx, e.alice, htmlUpload("a.html", big))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Create(ctx, e.alice, htmlUpload("b.html", big)); !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("120/100: %v", err)
	}
	// Net accounting: replacing 60 with 90 makes 90, under 100.
	if _, err := e.svc.Replace(ctx, e.alice, p.Slug, htmlUpload("a.html", string(bytes.Repeat([]byte("b"), 90)))); err != nil {
		t.Errorf("net replace: %v", err)
	}
	// Quota is per User.
	if _, err := e.svc.Create(ctx, e.bob, htmlUpload("b.html", big)); err != nil {
		t.Errorf("bob: %v", err)
	}
	if used, _ := e.svc.Usage(ctx, e.alice); used != 90 {
		t.Errorf("alice usage %d", used)
	}
}

func TestDelete(t *testing.T) {
	e := newEnv(t, 1<<20)
	ctx := t.Context()
	p, _ := e.svc.Create(ctx, e.alice, htmlUpload("a.html", "x"))

	if err := e.svc.Delete(ctx, e.bob, p.Slug); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob deleting: %v", err)
	}
	if err := e.svc.Delete(ctx, e.alice, p.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.LookupPreview(ctx, p.Slug); !errors.Is(err, preview.ErrNotFound) {
		t.Errorf("lookup after delete: %v", err)
	}
	if keys := storagetest.Keys(t, e.store, "prototypes/"); len(keys) != 0 {
		t.Errorf("files left: %v", keys)
	}
	select {
	case c := <-e.purger.calls:
		if !slices.Equal(c.paths, []string{"/", "/index.html"}) {
			t.Errorf("purge %+v", c)
		}
	case <-time.After(5 * time.Second):
		t.Error("no purge after delete")
	}
}

func TestPreviewHiddenWhenOwnerLeavesAllowlist(t *testing.T) {
	e := newEnv(t, 1<<20)
	p, _ := e.svc.Create(t.Context(), e.bob, htmlUpload("a.html", "x"))
	onlyAlice, _ := auth.NewAllowlist([]string{"alice"})
	e.svc.allow = onlyAlice
	if _, err := e.svc.LookupPreview(t.Context(), p.Slug); !errors.Is(err, preview.ErrNotFound) {
		t.Errorf("got %v, want not found", err)
	}
}

func TestCleanup(t *testing.T) {
	e := newEnv(t, 1<<20)
	ctx := t.Context()
	p, _ := e.svc.Create(ctx, e.alice, htmlUpload("a.html", "x"))

	put := func(key string) { storagetest.Put(t, e.store, key, "x") }
	put(preview.FileKey(p.Slug, 7, "index.html"))     // orphaned Version of a live Prototype
	put(preview.FileKey("zzzzzzzz", 1, "index.html")) // Prototype that no longer exists
	os.MkdirAll(filepath.Join(e.tmp, "upload-old"), 0o755)

	// Nothing is old enough yet.
	if err := e.svc.Cleanup(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if n := len(storagetest.Keys(t, e.store, "prototypes/")); n != 3 {
		t.Fatalf("recent leftovers removed too early: %d keys", n)
	}

	// An hour later they go; the current Version stays.
	e.svc.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if err := e.svc.Cleanup(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	want := []string{preview.FileKey(p.Slug, 1, "index.html")}
	if got := storagetest.Keys(t, e.store, "prototypes/"); !slices.Equal(got, want) {
		t.Errorf("after cleanup %v, want %v", got, want)
	}
	if entries, _ := os.ReadDir(e.tmp); len(entries) != 0 {
		t.Errorf("tmp not cleaned: %v", entries)
	}
}

func TestNewName(t *testing.T) {
	long := string(bytes.Repeat([]byte("é"), 120))
	tests := map[string]string{
		"landing.html":  "landing",
		"My Site.zip":   "My Site",
		`C:\x\demo.htm`: "demo",
		".zip":          "Untitled",
		"":              "Untitled",
		long + ".zip":   string([]rune(long)[:100]),
	}
	for in, want := range tests {
		if got, _ := newName(Upload{Filename: in}); got != want {
			t.Errorf("newName(%q) = %q, want %q", in, got, want)
		}
	}
}
