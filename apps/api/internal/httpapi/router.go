// Package httpapi wires maket's HTTP surface: it routes each request by its
// Host header to the JSON API and dashboard, or to preview serving.
package httpapi

import (
	"net/http"
	"strings"

	"github.com/itsgitz/maket/apps/api/internal/auth"
	"github.com/itsgitz/maket/apps/api/internal/config"
	"github.com/itsgitz/maket/apps/api/internal/jsonapi"
	"github.com/itsgitz/maket/apps/api/internal/preview"
	"github.com/itsgitz/maket/apps/api/internal/prototype"
)

// PreviewServer serves the Preview for a Slug.
type PreviewServer interface {
	ServePreview(w http.ResponseWriter, r *http.Request, slug string)
}

// Deps are the handlers the router dispatches to.
type Deps struct {
	Dashboard http.Handler // embedded SPA, served on the app host outside /api
	Previews  PreviewServer
	Auth      *auth.Service
	GitHub    GitHubLogin

	Prototypes     *prototype.Service
	MaxUploadBytes int64  // largest accepted upload body
	TmpDir         string // where upload bodies are spooled
}

// New returns the top-level handler:
//
//   - the app host: /api/* is the JSON API, everything else the dashboard
//   - <slug>.<preview domain>: that Slug's Preview
//   - any other host: 404
//
// Hosts are matched exactly, port included, against the configured values.
func New(cfg config.Config, deps Deps) http.Handler {
	h := authHandlers{auth: deps.Auth, github: deps.GitHub, secure: cfg.Secure()}
	anyAuth := auth.Require()
	sessionOnly := auth.Require(auth.MethodSession)

	ph := prototypeHandlers{svc: deps.Prototypes, maxUploadBytes: deps.MaxUploadBytes, tmpDir: deps.TmpDir, previewURL: cfg.PreviewURL}

	api := http.NewServeMux()
	api.HandleFunc("GET /api/healthz", healthz)
	api.HandleFunc("GET /api/auth/github/login", h.login)
	api.HandleFunc("GET /api/auth/github/callback", h.callback)
	api.Handle("POST /api/auth/logout", sessionOnly(http.HandlerFunc(h.logout)))
	api.Handle("GET /api/me", anyAuth(http.HandlerFunc(h.me)))
	api.Handle("GET /api/keys", sessionOnly(http.HandlerFunc(h.listKeys)))
	api.Handle("POST /api/keys", sessionOnly(http.HandlerFunc(h.createKey)))
	api.Handle("DELETE /api/keys/{id}", sessionOnly(http.HandlerFunc(h.revokeKey)))
	api.Handle("GET /api/prototypes", anyAuth(http.HandlerFunc(ph.list)))
	api.Handle("POST /api/prototypes", anyAuth(http.HandlerFunc(ph.create)))
	api.Handle("GET /api/prototypes/{slug}", anyAuth(http.HandlerFunc(ph.get)))
	api.Handle("PUT /api/prototypes/{slug}", anyAuth(http.HandlerFunc(ph.replace)))
	api.Handle("DELETE /api/prototypes/{slug}", anyAuth(http.HandlerFunc(ph.delete)))
	api.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		jsonapi.Error(w, http.StatusNotFound, "not_found", "no such API endpoint")
	})

	app := http.NewServeMux()
	app.Handle("/api/", deps.Auth.Middleware(auth.MiddlewareConfig{AppOrigin: cfg.AppOrigin(), Secure: cfg.Secure()})(api))
	app.Handle("/", deps.Dashboard)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if host == cfg.AppHost {
			app.ServeHTTP(w, r)
			return
		}
		if slug, ok := preview.SlugFromHost(host, cfg.PreviewDomain); ok {
			deps.Previews.ServePreview(w, r, slug)
			return
		}
		http.NotFound(w, r)
	})
}

func healthz(w http.ResponseWriter, r *http.Request) {
	jsonapi.Write(w, http.StatusOK, map[string]string{"status": "ok"})
}
