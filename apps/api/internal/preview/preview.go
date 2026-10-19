// Package preview serves Prototypes on the preview domain.
package preview

import (
	"net/http"
	"strings"
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

// Handler serves Previews. File serving arrives with uploads (milestone 4);
// until then every Slug is unknown.
type Handler struct{}

// ServePreview answers a request for the Preview of slug.
func (Handler) ServePreview(w http.ResponseWriter, r *http.Request, slug string) {
	setHeaders(w.Header())
	http.Error(w, "404 page not found", http.StatusNotFound)
}

// setHeaders applies the headers every preview response carries.
func setHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("Cache-Control", "public, max-age=300")
}
