package httpapi

import (
	"archive/zip"
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/itsgitz/maket/apps/api/internal/oauth/github"
)

// multipartBody builds an upload body. fields are sent before the file.
func multipartBody(t *testing.T, filename string, content []byte, fields map[string]string) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	if filename != "" {
		fw, err := mw.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(content)
	}
	mw.Close()
	return buf.String(), mw.FormDataContentType()
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func (a *testApp) upload(method, path string, header map[string]string, filename string, content []byte, fields map[string]string) *http.Response {
	a.t.Helper()
	body, ctype := multipartBody(a.t, filename, content, fields)
	h := map[string]string{"Content-Type": ctype}
	for k, v := range header {
		h[k] = v
	}
	req, _ := http.NewRequest(method, a.origin+path, strings.NewReader(body))
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	a.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// getPreview fetches path from the Preview of slug.
func (a *testApp) getPreview(slug, path string) (int, string, http.Header) {
	a.t.Helper()
	req, _ := http.NewRequest("GET", a.origin+path, nil)
	req.Host = slug + ".preview.localhost:8080"
	resp, err := a.client.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Details struct {
			Paths []string `json:"paths"`
		} `json:"details"`
	} `json:"error"`
}

func TestPrototypesEndToEnd(t *testing.T) {
	app := newTestApp(t, github.User{ID: 42, Login: "anggit"}, "anggit")
	wantStatus(t, app.login(), http.StatusSeeOther)

	// Upload a zip from the browser session (needs Origin).
	zipBytes := makeZip(t, map[string]string{
		"dist/index.html": "<h1>v1</h1>", "dist/app.js": "1", "dist/docs/index.html": "docs", "__MACOSX/._x": "junk",
	})
	wantStatus(t, app.upload("POST", "/api/prototypes", nil, "site.zip", zipBytes, nil), http.StatusForbidden)
	resp := app.upload("POST", "/api/prototypes", app.withOrigin(), "site.zip", zipBytes, map[string]string{"spa": "true"})
	wantStatus(t, resp, http.StatusCreated)
	p := decode[prototypeResponse](t, resp)
	if p.Name != "site" || !p.SPA || p.Version != 1 || p.FileCount != 3 || p.URL != "http://"+p.Slug+".preview.localhost:8080" {
		t.Errorf("created %+v", p)
	}

	// The Preview serves it.
	if code, body, h := app.getPreview(p.Slug, "/"); code != 200 || body != "<h1>v1</h1>" || h.Get("Set-Cookie") != "" {
		t.Errorf("preview / = %d %q", code, body)
	}
	if code, _, _ := app.getPreview(p.Slug, "/docs"); code != http.StatusMovedPermanently {
		t.Errorf("/docs = %d, want 301", code)
	}
	if code, body, _ := app.getPreview(p.Slug, "/some/route"); code != 200 || body != "<h1>v1</h1>" {
		t.Errorf("SPA fallback = %d %q", code, body)
	}
	if code, _, _ := app.getPreview(p.Slug, "/missing.js"); code != 404 {
		t.Errorf("missing asset = %d", code)
	}

	// An agent with an API key re-uploads: name and SPA are kept, Version bumps.
	created := decode[apiKeyResponse](t, app.do("POST", "/api/keys", `{"name":"agent"}`, app.withOrigin()))
	resp = app.upload("PUT", "/api/prototypes/"+p.Slug, bearer(created.Key), "index.html", []byte("<h1>v2</h1>"), nil)
	wantStatus(t, resp, http.StatusOK)
	p2 := decode[prototypeResponse](t, resp)
	if p2.Version != 2 || p2.Name != "site" || !p2.SPA || p2.FileCount != 1 {
		t.Errorf("replaced %+v", p2)
	}
	if code, body, _ := app.getPreview(p.Slug, "/"); code != 200 || body != "<h1>v2</h1>" {
		t.Errorf("preview after replace = %d %q", code, body)
	}

	// List and get.
	list := decode[struct {
		Prototypes []prototypeResponse `json:"prototypes"`
		Usage      struct {
			Used  int64 `json:"used_bytes"`
			Quota int64 `json:"quota_bytes"`
		} `json:"usage"`
	}](t, app.do("GET", "/api/prototypes", "", bearer(created.Key)))
	if len(list.Prototypes) != 1 || list.Usage.Used != 11 || list.Usage.Quota != 100<<10 {
		t.Errorf("list = %+v", list)
	}
	wantStatus(t, app.do("GET", "/api/prototypes/"+p.Slug, "", nil), http.StatusOK)
	wantStatus(t, app.do("GET", "/api/prototypes/zzzzzzzz", "", nil), http.StatusNotFound)

	// Delete.
	wantStatus(t, app.do("DELETE", "/api/prototypes/"+p.Slug, "", bearer(created.Key)), http.StatusNoContent)
	if code, _, _ := app.getPreview(p.Slug, "/"); code != 404 {
		t.Errorf("preview after delete = %d", code)
	}
}

func TestUploadErrors(t *testing.T) {
	app := newTestApp(t, github.User{ID: 42, Login: "anggit"}, "anggit")
	wantStatus(t, app.login(), http.StatusSeeOther)
	o := app.withOrigin()

	tests := []struct {
		name     string
		filename string
		content  []byte
		status   int
		code     string
		paths    []string
	}{
		{"no file", "", nil, 400, "invalid_file", nil},
		{"wrong type", "notes.txt", []byte("x"), 400, "invalid_file", nil},
		{"missing index", "x.zip", makeZip(t, map[string]string{"about.html": "a"}), 400, "missing_index", nil},
		{"disallowed", "x.zip", makeZip(t, map[string]string{"index.html": "a", "run.php": "b", "README": "c"}), 400, "disallowed_files", []string{"README", "run.php"}},
		{"traversal", "x.zip", makeZip(t, map[string]string{"index.html": "a", "../evil.js": "b"}), 400, "unsafe_paths", []string{"../evil.js"}},
		{"upload too large", "big.html", bytes.Repeat([]byte("a"), 33<<10), 413, "too_large", nil},
		{"extracted too large", "x.zip", makeZip(t, map[string]string{"index.html": strings.Repeat("a", 70<<10)}), 413, "too_large", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := app.upload("POST", "/api/prototypes", o, tt.filename, tt.content, nil)
			if resp.StatusCode != tt.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, tt.status)
			}
			e := decode[errorBody](t, resp)
			if e.Error.Code != tt.code || strings.Join(e.Error.Details.Paths, ",") != strings.Join(tt.paths, ",") {
				t.Errorf("error %+v, want %s %v", e.Error, tt.code, tt.paths)
			}
		})
	}

	t.Run("bad spa value", func(t *testing.T) {
		wantStatus(t, app.upload("POST", "/api/prototypes", o, "a.html", []byte("a"), map[string]string{"spa": "maybe"}), 400)
	})
	t.Run("quota", func(t *testing.T) {
		chunk := []byte(strings.Repeat("a", 30<<10))
		for range 3 {
			wantStatus(t, app.upload("POST", "/api/prototypes", o, "a.html", chunk, nil), http.StatusCreated)
		}
		resp := app.upload("POST", "/api/prototypes", o, "a.html", chunk, nil)
		wantStatus(t, resp, http.StatusRequestEntityTooLarge)
		if e := decode[errorBody](t, resp); e.Error.Code != "quota_exceeded" {
			t.Errorf("code %q", e.Error.Code)
		}
	})
	t.Run("anonymous", func(t *testing.T) {
		anon := newTestApp(t, github.User{ID: 1, Login: "x"}, "x")
		wantStatus(t, anon.upload("POST", "/api/prototypes", anon.withOrigin(), "a.html", []byte("a"), nil), http.StatusUnauthorized)
	})
}
