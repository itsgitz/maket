package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/auth"
	"github.com/itsgitz/maket/apps/api/internal/config"
	"github.com/itsgitz/maket/apps/api/internal/db"
)

type fakePreviews struct{ slug string }

func (f *fakePreviews) ServePreview(w http.ResponseWriter, r *http.Request, slug string) {
	f.slug = slug
	w.Write([]byte("preview:" + slug))
}

// newAuthService returns an auth.Service over a fresh database.
func newAuthService(t *testing.T, allowed ...string) *auth.Service {
	t.Helper()
	database, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "maket.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	allow, err := auth.NewAllowlist(allowed)
	if err != nil {
		t.Fatal(err)
	}
	return auth.NewService(database, allow, time.Now)
}

func newTestRouter(t *testing.T) (http.Handler, *fakePreviews) {
	cfg := config.Config{
		AppHost:       "app.localhost:5173",
		PreviewDomain: "preview.localhost:8080",
		PublicScheme:  "http",
	}
	previews := &fakePreviews{}
	dashboard := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("dashboard"))
	})
	return New(cfg, Deps{Dashboard: dashboard, Previews: previews, Auth: newAuthService(t, "someone")}), previews
}

func TestHostRouting(t *testing.T) {
	h, _ := newTestRouter(t)
	tests := []struct {
		name, host, path string
		wantStatus       int
		wantBody         string
	}{
		{"healthz", "app.localhost:5173", "/api/healthz", 200, `"status":"ok"`},
		{"app host is case-insensitive", "APP.localhost:5173", "/api/healthz", 200, `"status":"ok"`},
		{"unknown api path is JSON 404", "app.localhost:5173", "/api/nope", 404, `"code":"not_found"`},
		{"dashboard root", "app.localhost:5173", "/", 200, "dashboard"},
		{"dashboard client route", "app.localhost:5173", "/keys", 200, "dashboard"},
		{"preview", "ab12cd34.preview.localhost:8080", "/", 200, "preview:ab12cd34"},
		{"preview uppercase host", "AB12CD34.preview.localhost:8080", "/x.css", 200, "preview:ab12cd34"},
		{"app host without port", "app.localhost", "/api/healthz", 404, ""},
		{"preview host wrong port", "ab12cd34.preview.localhost:9999", "/", 404, ""},
		{"preview domain itself", "preview.localhost:8080", "/", 404, ""},
		{"malformed slug", "ab-cd.preview.localhost:8080", "/", 404, ""},
		{"nested subdomain", "x.ab12cd34.preview.localhost:8080", "/", 404, ""},
		{"api is not served on preview hosts", "ab12cd34.preview.localhost:8080", "/api/healthz", 200, "preview:ab12cd34"},
		{"unknown host", "example.com", "/", 404, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Host = tt.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Errorf("status %d, want %d", rec.Code, tt.wantStatus)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body %q, want it to contain %q", rec.Body, tt.wantBody)
			}
		})
	}
}

func TestUnmatchedAPIMethodIsJSONError(t *testing.T) {
	h, _ := newTestRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/api/healthz", nil)
	req.Host = "app.localhost:5173"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("got %d %q, want JSON 404", rec.Code, rec.Header().Get("Content-Type"))
	}
}
