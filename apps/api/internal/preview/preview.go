// Package preview serves Prototypes on the preview domain.
package preview

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/itsgitz/maket/apps/api/internal/storage"
)

// SlugFromHost returns the Slug when host is exactly "<slug>.<domain>" and
// the label is a well-formed Slug. Hosts are compared case-insensitively.
func SlugFromHost(host, domain string) (string, bool) {
	host = strings.ToLower(host)
	label, ok := strings.CutSuffix(host, "."+domain)
	if !ok || !ValidSlug(label) {
		return "", false
	}
	return label, true
}

// SlugLength is the number of characters in every Slug.
const SlugLength = 8

// slugAlphabet holds the characters a Slug is made of.
const slugAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// ValidSlug reports whether s has the Slug shape: 8 lowercase letters or
// digits, which is always a valid DNS label.
func ValidSlug(s string) bool {
	if len(s) != SlugLength {
		return false
	}
	for _, c := range []byte(s) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// NewSlug returns a random Slug. Each character is drawn uniformly from
// a-z0-9: random bytes at or above the largest multiple of 36 are discarded,
// so there is no modulo bias.
func NewSlug() string {
	const limit = 256 - 256%len(slugAlphabet) // 252
	out := make([]byte, 0, SlugLength)
	var buf [2 * SlugLength]byte
	for len(out) < SlugLength {
		rand.Read(buf[:]) // never returns an error; crashes the program instead
		for _, b := range buf {
			if int(b) < limit && len(out) < SlugLength {
				out = append(out, slugAlphabet[int(b)%len(slugAlphabet)])
			}
		}
	}
	return string(out)
}

// VersionPrefix returns the storage prefix holding every file of one Version:
// "prototypes/<slug>/v<version>/".
func VersionPrefix(slug string, version int) string {
	return "prototypes/" + slug + "/v" + strconv.Itoa(version) + "/"
}

// FileKey returns the storage key of the file at path (relative, no leading
// "/") in one Version.
func FileKey(slug string, version int, path string) string {
	return VersionPrefix(slug, version) + path
}

// Site is what preview serving needs to know about a Prototype.
type Site struct {
	Slug    string
	Version int  // current Version
	SPA     bool // SPA mode
}

// ErrNotFound means there is no Preview for the slug (unknown, deleted, or
// its owner is no longer on the Allowlist).
var ErrNotFound = errors.New("preview: not found")

// Lookup finds the Prototype behind a Slug.
type Lookup interface {
	// LookupPreview returns the Site for slug, or an error wrapping
	// ErrNotFound.
	LookupPreview(ctx context.Context, slug string) (Site, error)
}

// Handler serves Previews: the files of a Prototype's current Version, and
// nothing else.
type Handler struct {
	Lookup Lookup
	Store  storage.Storage
}

// ServePreview answers a request for the Preview of slug.
//
// Paths resolve as follows: "/" and paths ending in "/" serve that
// directory's index.html; an existing file is served as is; a directory
// without its trailing "/" is redirected to it; in SPA mode a missing page
// (last segment without a ".") gets the root index.html; anything else is a
// 404, using the Version's root 404.html when there is one.
func (h *Handler) ServePreview(w http.ResponseWriter, r *http.Request, slug string) {
	setHeaders(w.Header())
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		plainError(w, http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	site, err := h.Lookup.LookupPreview(ctx, slug)
	if errors.Is(err, ErrNotFound) {
		plainError(w, http.StatusNotFound)
		return
	}
	if err != nil {
		h.fail(w, r, slug, "lookup", err)
		return
	}

	rel, ok := cleanPath(r.URL.Path)
	if !ok {
		plainError(w, http.StatusNotFound)
		return
	}
	file := rel
	if file == "" || strings.HasSuffix(file, "/") {
		file += "index.html"
	}

	obj, found, err := h.open(ctx, site, file)
	if err != nil {
		h.fail(w, r, slug, "open", err)
		return
	}
	if found {
		h.serve(w, r, file, obj)
		return
	}

	if rel != "" && !strings.HasSuffix(rel, "/") {
		obj, found, err := h.open(ctx, site, rel+"/index.html")
		if err != nil {
			h.fail(w, r, slug, "open", err)
			return
		}
		if found {
			obj.Close()
			redirectToDir(w, r)
			return
		}
	}

	last := strings.TrimSuffix(rel, "/")
	last = last[strings.LastIndexByte(last, '/')+1:]
	if site.SPA && !strings.Contains(last, ".") {
		obj, found, err := h.open(ctx, site, "index.html")
		if err != nil {
			h.fail(w, r, slug, "open", err)
			return
		}
		if found {
			h.serve(w, r, "index.html", obj)
			return
		}
	}

	h.notFound(w, r, site)
}

// open opens a file of site's current Version. found is false, with a nil
// error, when the file does not exist.
func (h *Handler) open(ctx context.Context, site Site, path string) (obj storage.Object, found bool, err error) {
	obj, err = h.Store.Open(ctx, FileKey(site.Slug, site.Version, path))
	switch {
	case err == nil:
		return obj, true, nil
	case errors.Is(err, storage.ErrNotFound), errors.Is(err, storage.ErrInvalidKey):
		// An invalid key (say, invalid UTF-8) can never have been uploaded.
		return storage.Object{}, false, nil
	default:
		return storage.Object{}, false, err
	}
}

// serve writes obj, the file at path, with status 200 (or 206/304 as the
// request asks).
func (h *Handler) serve(w http.ResponseWriter, r *http.Request, path string, obj storage.Object) {
	defer obj.Close()
	w.Header().Set("Content-Type", ContentType(path))
	http.ServeContent(w, r, "", obj.ModTime, obj)
}

// notFound writes a 404, with the body of the Version's root 404.html when
// there is one.
func (h *Handler) notFound(w http.ResponseWriter, r *http.Request, site Site) {
	obj, found, err := h.open(r.Context(), site, "404.html")
	if err != nil {
		h.fail(w, r, site.Slug, "open 404.html", err)
		return
	}
	if !found {
		plainError(w, http.StatusNotFound)
		return
	}
	defer obj.Close()
	setNoStore(w.Header())
	w.Header().Set("Content-Type", ContentType("404.html"))
	w.Header().Set("Content-Length", strconv.FormatInt(obj.Size, 10))
	w.WriteHeader(http.StatusNotFound)
	if r.Method != http.MethodHead {
		if _, err := io.Copy(w, obj); err != nil {
			slog.Warn("preview: write 404.html", "slug", site.Slug, "err", err)
		}
	}
}

// fail logs err and writes a plain 500.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, slug, what string, err error) {
	slog.Error("preview: "+what, "slug", slug, "path", r.URL.Path, "err", err)
	plainError(w, http.StatusInternalServerError)
}

// redirectToDir answers 301 with the request path plus "/", keeping the
// query. cleanPath has already ruled out empty segments, so the Location
// starts with exactly one "/" and cannot be read as another host.
func redirectToDir(w http.ResponseWriter, r *http.Request) {
	loc := r.URL.EscapedPath() + "/"
	if r.URL.RawQuery != "" {
		loc += "?" + r.URL.RawQuery
	}
	w.Header().Set("Location", loc)
	w.WriteHeader(http.StatusMovedPermanently)
}

// cleanPath turns a request path into a path relative to the Version root,
// keeping a trailing "/". It rejects, rather than cleans, anything that is
// not already a plain path: no leading "/", empty segments (other than the
// trailing one), "." or ".." segments, backslashes or control characters.
func cleanPath(p string) (string, bool) {
	rel, ok := strings.CutPrefix(p, "/")
	if !ok {
		return "", false
	}
	if rel == "" {
		return "", true
	}
	for _, c := range []byte(rel) {
		if c < 0x20 || c == 0x7f || c == '\\' {
			return "", false
		}
	}
	for seg := range strings.SplitSeq(strings.TrimSuffix(rel, "/"), "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	return rel, true
}

// plainError writes a plain-text error for code, with Cache-Control:
// no-store.
func plainError(w http.ResponseWriter, code int) {
	setNoStore(w.Header())
	msg := strings.ToLower(http.StatusText(code))
	if code == http.StatusNotFound {
		msg = "page not found" // matches net/http's own 404 text
	}
	http.Error(w, fmt.Sprintf("%d %s", code, msg), code)
}

// setHeaders applies the headers every preview response carries. Error
// responses replace the Cache-Control with setNoStore.
func setHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("Cache-Control", "public, max-age=300")
}

// setNoStore marks an error response uncacheable. This deliberately departs
// from the brief's blanket "public, max-age=300": Cloudflare would otherwise
// keep serving a 404 for a path for minutes after an Upload adds it.
func setNoStore(h http.Header) {
	h.Set("Cache-Control", "no-store")
}

// contentTypes maps every extension an Upload may contain to its
// Content-Type. It is explicit because the production image has no MIME
// database for mime.TypeByExtension to read.
var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".htm":         "text/html; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".json":        "application/json; charset=utf-8",
	".map":         "application/json; charset=utf-8",
	".txt":         "text/plain; charset=utf-8",
	".xml":         "application/xml; charset=utf-8",
	".webmanifest": "application/manifest+json; charset=utf-8",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".gif":         "image/gif",
	".webp":        "image/webp",
	".avif":        "image/avif",
	".svg":         "image/svg+xml",
	".ico":         "image/x-icon",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".ttf":         "font/ttf",
	".otf":         "font/otf",
	".mp4":         "video/mp4",
	".webm":        "video/webm",
	".mp3":         "audio/mpeg",
}

// ContentType returns the Content-Type for a file path by its extension,
// case-insensitively, or application/octet-stream for unknown extensions.
func ContentType(path string) string {
	name := path[strings.LastIndexByte(path, '/')+1:]
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		if ct, ok := contentTypes[strings.ToLower(name[i:])]; ok {
			return ct
		}
	}
	return "application/octet-stream"
}
