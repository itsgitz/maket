package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandler(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":       {Data: []byte("<html>spa</html>")},
		"assets/app-1.js":  {Data: []byte("console.log(1)")},
		"favicon.svg":      {Data: []byte("<svg/>")},
		"assets/sub/x.css": {Data: []byte("body{}")},
	}
	h := NewHandler(dist)

	tests := []struct {
		path, wantBody, wantCache string
	}{
		{"/", "spa", "no-cache"},
		{"/prototypes", "spa", "no-cache"}, // client-side route
		{"/keys/new", "spa", "no-cache"},   // nested client-side route
		{"/assets/", "spa", "no-cache"},    // directory: no listing
		{"/assets/app-1.js", "console.log", "public, max-age=31536000, immutable"},
		{"/favicon.svg", "<svg/>", ""},
		{"/index.html", "spa", "no-cache"},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", tt.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), tt.wantBody) {
			t.Errorf("%s: body %q, want %q", tt.path, rec.Body, tt.wantBody)
		}
		if got := rec.Header().Get("Cache-Control"); got != tt.wantCache {
			t.Errorf("%s: Cache-Control %q, want %q", tt.path, got, tt.wantCache)
		}
	}
}

func TestHandlerPlaceholderWithoutBuild(t *testing.T) {
	rec := httptest.NewRecorder()
	NewHandler(fstest.MapFS{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "build:web") {
		t.Errorf("got %d %q", rec.Code, rec.Body)
	}
}

func TestHandlerRejectsWrites(t *testing.T) {
	rec := httptest.NewRecorder()
	NewHandler(fstest.MapFS{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("got %d", rec.Code)
	}
}
