package preview

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/storage"
)

func TestSlugFromHost(t *testing.T) {
	const domain = "cloudshare.my.id"
	tests := []struct {
		host, want string
		ok         bool
	}{
		{"ab12cd34.cloudshare.my.id", "ab12cd34", true},
		{"AB12CD34.cloudshare.my.id", "ab12cd34", true},
		{"cloudshare.my.id", "", false},
		{"ab12cd3.cloudshare.my.id", "", false},   // too short
		{"ab12cd345.cloudshare.my.id", "", false}, // too long
		{"ab12-d34.cloudshare.my.id", "", false},
		{"x.ab12cd34.cloudshare.my.id", "", false},
		{"ab12cd34.cloudshare.my.id.evil.com", "", false},
		{"ab12cd34.notcloudshare.my.id", "", false},
	}
	for _, tt := range tests {
		got, ok := SlugFromHost(tt.host, domain)
		if got != tt.want || ok != tt.ok {
			t.Errorf("SlugFromHost(%q) = %q, %v; want %q, %v", tt.host, got, ok, tt.want, tt.ok)
		}
	}
}

func TestNewSlug(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		s := NewSlug()
		if !ValidSlug(s) {
			t.Fatalf("NewSlug() = %q, not a valid Slug", s)
		}
		if seen[s] {
			t.Fatalf("NewSlug() returned %q twice", s)
		}
		seen[s] = true
	}
}

func TestKeys(t *testing.T) {
	if got, want := VersionPrefix("ab12cd34", 3), "prototypes/ab12cd34/v3/"; got != want {
		t.Errorf("VersionPrefix = %q, want %q", got, want)
	}
	if got, want := FileKey("ab12cd34", 3, "assets/app.js"), "prototypes/ab12cd34/v3/assets/app.js"; got != want {
		t.Errorf("FileKey = %q, want %q", got, want)
	}
	if err := storage.ValidPrefix(VersionPrefix("ab12cd34", 1)); err != nil {
		t.Errorf("VersionPrefix is not a valid storage prefix: %v", err)
	}
}

func TestContentType(t *testing.T) {
	tests := map[string]string{
		"index.html":        "text/html; charset=utf-8",
		"a/b.HTM":           "text/html; charset=utf-8",
		"style.css":         "text/css; charset=utf-8",
		"app.js":            "text/javascript; charset=utf-8",
		"mod.mjs":           "text/javascript; charset=utf-8",
		"data.json":         "application/json; charset=utf-8",
		"app.js.map":        "application/json; charset=utf-8",
		"robots.txt":        "text/plain; charset=utf-8",
		"feed.xml":          "application/xml; charset=utf-8",
		"site.webmanifest":  "application/manifest+json; charset=utf-8",
		"logo.png":          "image/png",
		"LOGO.PNG":          "image/png",
		"p.jpg":             "image/jpeg",
		"p.JPEG":            "image/jpeg",
		"a.gif":             "image/gif",
		"a.webp":            "image/webp",
		"a.avif":            "image/avif",
		"icon.svg":          "image/svg+xml",
		"favicon.ico":       "image/x-icon",
		"f.woff":            "font/woff",
		"f.woff2":           "font/woff2",
		"f.ttf":             "font/ttf",
		"f.otf":             "font/otf",
		"clip.mp4":          "video/mp4",
		"clip.webm":         "video/webm",
		"song.mp3":          "audio/mpeg",
		"archive.zip":       "application/octet-stream",
		"README":            "application/octet-stream",
		"dir.html/noext":    "application/octet-stream",
		".hidden":           "application/octet-stream",
		"trailing.":         "application/octet-stream",
		"assets/v1.2/thing": "application/octet-stream",
	}
	for path, want := range tests {
		if got := ContentType(path); got != want {
			t.Errorf("ContentType(%q) = %q, want %q", path, got, want)
		}
	}
}

const slug = "ab12cd34"

type fakeLookup struct {
	sites map[string]Site
	err   error
}

func (f fakeLookup) LookupPreview(_ context.Context, s string) (Site, error) {
	if f.err != nil {
		return Site{}, f.err
	}
	site, ok := f.sites[s]
	if !ok {
		return Site{}, ErrNotFound
	}
	return site, nil
}

var modTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// newHandler returns a Handler over a LocalStore holding files as the
// current Version (2) of slug. Version 1 holds a stale file that must never
// be served.
func newHandler(t *testing.T, spa bool, files map[string]string) *Handler {
	t.Helper()
	store, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	if err := store.Put(ctx, FileKey(slug, 1, "old.html"), strings.NewReader("old")); err != nil {
		t.Fatal(err)
	}
	for path, body := range files {
		if err := store.Put(ctx, FileKey(slug, 2, path), strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	return &Handler{
		Lookup: fakeLookup{sites: map[string]Site{slug: {Slug: slug, Version: 2, SPA: spa}}},
		Store:  modTimeStore{store},
	}
}

// modTimeStore pins every object's ModTime so conditional requests are
// predictable.
type modTimeStore struct{ storage.Storage }

func (s modTimeStore) Open(ctx context.Context, key string) (storage.Object, error) {
	obj, err := s.Storage.Open(ctx, key)
	obj.ModTime = modTime
	return obj, err
}

var site = map[string]string{
	"index.html":           "<h1>home</h1>",
	"about/index.html":     "<h1>about</h1>",
	"docs/guide/page.html": "<h1>guide</h1>",
	"assets/app.js":        "console.log(1)",
	"media/clip.mp4":       "0123456789abcdef",
	"img/LOGO.PNG":         "png",
}

func do(h *Handler, method, path string, mod func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+slug+".cloudshare.my.id/", nil)
	r.URL.Path = path
	if mod != nil {
		mod(r)
	}
	rec := httptest.NewRecorder()
	h.ServePreview(rec, r, slug)
	return rec
}

func TestServePreview(t *testing.T) {
	plain := newHandler(t, false, site)
	spa := newHandler(t, true, site)
	with404 := newHandler(t, false, map[string]string{"index.html": "home", "404.html": "<h1>custom 404</h1>"})

	tests := []struct {
		name     string
		h        *Handler
		method   string
		path     string
		query    string
		code     int
		body     string
		ctype    string
		location string
	}{
		{"root", plain, "GET", "/", "", 200, "<h1>home</h1>", "text/html; charset=utf-8", ""},
		{"index direct", plain, "GET", "/index.html", "", 200, "<h1>home</h1>", "text/html; charset=utf-8", ""},
		{"nested file", plain, "GET", "/docs/guide/page.html", "", 200, "<h1>guide</h1>", "text/html; charset=utf-8", ""},
		{"js", plain, "GET", "/assets/app.js", "", 200, "console.log(1)", "text/javascript; charset=utf-8", ""},
		{"uppercase ext", plain, "GET", "/img/LOGO.PNG", "", 200, "png", "image/png", ""},
		{"case-sensitive path", plain, "GET", "/img/logo.png", "", 404, "404 page not found\n", "", ""},
		{"dir with slash", plain, "GET", "/about/", "", 200, "<h1>about</h1>", "text/html; charset=utf-8", ""},
		{"dir redirect", plain, "GET", "/about", "", 301, "", "", "/about/"},
		{"dir redirect query", plain, "GET", "/about", "a=1&b=2", 301, "", "", "/about/?a=1&b=2"},
		{"dir redirect HEAD", plain, "HEAD", "/about", "", 301, "", "", "/about/"},
		{"dir without index", plain, "GET", "/docs/", "", 404, "404 page not found\n", "", ""},
		{"non-SPA missing page", plain, "GET", "/dashboard/settings", "", 404, "404 page not found\n", "text/plain; charset=utf-8", ""},
		{"old version not served", plain, "GET", "/old.html", "", 404, "404 page not found\n", "", ""},
		{"SPA fallback", spa, "GET", "/dashboard/settings", "", 200, "<h1>home</h1>", "text/html; charset=utf-8", ""},
		{"SPA fallback dir slash", spa, "GET", "/dashboard/", "", 200, "<h1>home</h1>", "text/html; charset=utf-8", ""},
		{"SPA real file", spa, "GET", "/assets/app.js", "", 200, "console.log(1)", "text/javascript; charset=utf-8", ""},
		{"SPA still redirects dirs", spa, "GET", "/about", "", 301, "", "", "/about/"},
		{"SPA missing asset", spa, "GET", "/assets/missing.js", "", 404, "404 page not found\n", "", ""},
		{"SPA dotted segment", spa, "GET", "/v1.2", "", 404, "404 page not found\n", "", ""},
		{"custom 404", with404, "GET", "/nope", "", 404, "<h1>custom 404</h1>", "text/html; charset=utf-8", ""},
		{"custom 404 HEAD", with404, "HEAD", "/nope", "", 404, "", "text/html; charset=utf-8", ""},
		{"HEAD", plain, "HEAD", "/", "", 200, "", "text/html; charset=utf-8", ""},
		{"POST", plain, "POST", "/", "", 405, "405 method not allowed\n", "", ""},
		{"traversal", plain, "GET", "/a/../index.html", "", 404, "", "", ""},
		{"traversal SPA", spa, "GET", "/a/../index.html", "", 404, "", "", ""},
		{"leading traversal", spa, "GET", "/../index.html", "", 404, "", "", ""},
		{"dot segment", spa, "GET", "/./index.html", "", 404, "", "", ""},
		{"double slash", spa, "GET", "//index.html", "", 404, "", "", ""},
		{"inner empty segment", spa, "GET", "/about//index.html", "", 404, "", "", ""},
		{"backslash", spa, "GET", "/about\\index.html", "", 404, "", "", ""},
		{"NUL", spa, "GET", "/index.html\x00", "", 404, "", "", ""},
		{"control char", spa, "GET", "/a\nb", "", 404, "", "", ""},
		{"invalid UTF-8", spa, "GET", "/\xff", "", 200, "<h1>home</h1>", "", ""}, // SPA page path, never a file
		{"no leading slash", plain, "GET", "index.html", "", 404, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(tt.h, tt.method, tt.path, func(r *http.Request) { r.URL.RawQuery = tt.query })
			if rec.Code != tt.code {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.code, rec.Body)
			}
			if tt.body != "" || tt.method == "HEAD" {
				if got := rec.Body.String(); got != tt.body {
					t.Errorf("body = %q, want %q", got, tt.body)
				}
			}
			if tt.ctype != "" {
				if got := rec.Header().Get("Content-Type"); got != tt.ctype {
					t.Errorf("Content-Type = %q, want %q", got, tt.ctype)
				}
			}
			if got := rec.Header().Get("Location"); got != tt.location {
				t.Errorf("Location = %q, want %q", got, tt.location)
			}
			if tt.code == 405 {
				if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
					t.Errorf("Allow = %q", got)
				}
			}
			checkHeaders(t, rec)
		})
	}
}

func TestServePreviewHEADLength(t *testing.T) {
	rec := do(newHandler(t, false, site), "HEAD", "/", nil)
	if got := rec.Header().Get("Content-Length"); got != "13" {
		t.Errorf("Content-Length = %q, want 13", got)
	}
}

func TestServePreviewLookup(t *testing.T) {
	h := newHandler(t, false, site)
	rec := httptest.NewRecorder()
	h.ServePreview(rec, httptest.NewRequest("GET", "/", nil), "zzzzzzzz")
	if rec.Code != 404 || rec.Body.String() != "404 page not found\n" {
		t.Errorf("unknown slug: %d %q", rec.Code, rec.Body)
	}
	checkHeaders(t, rec)

	h.Lookup = fakeLookup{err: errors.New("db down")}
	rec = httptest.NewRecorder()
	h.ServePreview(rec, httptest.NewRequest("GET", "/", nil), slug)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "db down") {
		t.Errorf("lookup error: %d %q", rec.Code, rec.Body)
	}
	checkHeaders(t, rec)

	h.Lookup = fakeLookup{err: errors.Join(errors.New("gone"), ErrNotFound)}
	rec = do(h, "GET", "/", nil)
	if rec.Code != 404 {
		t.Errorf("wrapped ErrNotFound: %d", rec.Code)
	}
}

type failStore struct{ storage.Storage }

func (failStore) Open(context.Context, string) (storage.Object, error) {
	return storage.Object{}, errors.New("disk on fire")
}

func TestServePreviewStorageError(t *testing.T) {
	h := newHandler(t, false, site)
	h.Store = failStore{}
	rec := do(h, "GET", "/", nil)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "fire") {
		t.Errorf("storage error: %d %q", rec.Code, rec.Body)
	}
	checkHeaders(t, rec)
}

func TestServePreviewRange(t *testing.T) {
	rec := do(newHandler(t, false, site), "GET", "/media/clip.mp4", func(r *http.Request) {
		r.Header.Set("Range", "bytes=4-9")
	})
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got := rec.Body.String(); got != "456789" {
		t.Errorf("body = %q, want %q", got, "456789")
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 4-9/16" {
		t.Errorf("Content-Range = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Errorf("Content-Type = %q", got)
	}
	checkHeaders(t, rec)
}

func TestServePreviewNotModified(t *testing.T) {
	h := newHandler(t, false, site)
	rec := do(h, "GET", "/assets/app.js", nil)
	if got, want := rec.Header().Get("Last-Modified"), modTime.Format(http.TimeFormat); got != want {
		t.Errorf("Last-Modified = %q, want %q", got, want)
	}
	rec = do(h, "GET", "/assets/app.js", func(r *http.Request) {
		r.Header.Set("If-Modified-Since", modTime.Add(time.Hour).Format(http.TimeFormat))
	})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 has a body: %q", rec.Body)
	}
	checkHeaders(t, rec)
}

// checkHeaders asserts the headers every preview response carries, the
// Cache-Control for its status, and that no cookie is ever set.
func checkHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := rec.Header()
	for k, v := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Robots-Tag":           "noindex, nofollow",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	} {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	wantCache := "no-store"
	switch rec.Code {
	case 200, 206, 301, 304:
		wantCache = cacheControl
	}
	if got := h.Get("Cache-Control"); got != wantCache {
		t.Errorf("status %d: Cache-Control = %q, want %q", rec.Code, got, wantCache)
	}
	if len(h.Values("Set-Cookie")) != 0 || len(rec.Result().Cookies()) != 0 {
		t.Error("preview responses must never set cookies")
	}
	if h.Get("Content-Type") == "" && rec.Code != 301 && rec.Code != 304 {
		t.Errorf("status %d has no Content-Type", rec.Code)
	}
}
