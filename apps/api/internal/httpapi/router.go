// Package httpapi wires maket's HTTP surface: it routes each request by its
// Host header to the JSON API and dashboard, or to preview serving.
package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/itsgitz/maket/apps/api/internal/config"
	"github.com/itsgitz/maket/apps/api/internal/preview"
)

// PreviewServer serves the Preview for a Slug.
type PreviewServer interface {
	ServePreview(w http.ResponseWriter, r *http.Request, slug string)
}

// Deps are the handlers the router dispatches to.
type Deps struct {
	Dashboard http.Handler // embedded SPA, served on the app host outside /api
	Previews  PreviewServer
}

// New returns the top-level handler:
//
//   - the app host: /api/* is the JSON API, everything else the dashboard
//   - <slug>.<preview domain>: that Slug's Preview
//   - any other host: 404
//
// Hosts are matched exactly, port included, against the configured values.
func New(cfg config.Config, deps Deps) http.Handler {
	app := http.NewServeMux()
	app.HandleFunc("GET /api/healthz", healthz)
	app.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, http.StatusNotFound, "not_found", "no such API endpoint")
	})
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
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// WriteError writes the API error shape: {"error": {"code", "message"}}.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	type body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	WriteJSON(w, status, map[string]body{"error": {Code: code, Message: message}})
}
