// Package config loads maket's settings from MAKET_* environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Config holds the server settings. Hosts include the port when it is not
// the scheme's default (e.g. "app.localhost:5173"), because routing matches
// the request Host header exactly.
type Config struct {
	ListenAddr    string // MAKET_LISTEN_ADDR
	AppHost       string // MAKET_APP_HOST
	PreviewDomain string // MAKET_PREVIEW_DOMAIN; previews live at <slug>.<PreviewDomain>
	PublicScheme  string // MAKET_PUBLIC_SCHEME: "https" or "http"
}

// Load reads the config from the process environment.
func Load() (Config, error) {
	return FromLookup(os.LookupEnv)
}

// FromLookup reads the config through lookup, so tests can supply values.
// It reports every problem at once rather than stopping at the first.
func FromLookup(lookup func(string) (string, bool)) (Config, error) {
	var problems []error
	get := func(name, def string) string {
		if v, ok := lookup(name); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}
	required := func(name string) string {
		v := get(name, "")
		if v == "" {
			problems = append(problems, fmt.Errorf("%s is required", name))
		}
		return v
	}

	c := Config{
		ListenAddr:    get("MAKET_LISTEN_ADDR", ":8080"),
		AppHost:       strings.ToLower(required("MAKET_APP_HOST")),
		PreviewDomain: strings.ToLower(required("MAKET_PREVIEW_DOMAIN")),
		PublicScheme:  strings.ToLower(get("MAKET_PUBLIC_SCHEME", "https")),
	}

	if c.PublicScheme != "https" && c.PublicScheme != "http" {
		problems = append(problems, fmt.Errorf("MAKET_PUBLIC_SCHEME must be \"https\" or \"http\", got %q", c.PublicScheme))
	}
	if c.AppHost != "" && c.PreviewDomain != "" {
		// The app must never be reachable as a preview subdomain, or preview
		// pages would share an origin family with the dashboard.
		if c.AppHost == c.PreviewDomain || strings.HasSuffix(c.AppHost, "."+c.PreviewDomain) {
			problems = append(problems, fmt.Errorf("MAKET_APP_HOST (%s) must not be under MAKET_PREVIEW_DOMAIN (%s)", c.AppHost, c.PreviewDomain))
		}
	}

	if len(problems) > 0 {
		return Config{}, errors.Join(problems...)
	}
	return c, nil
}

// AppOrigin is the dashboard origin, e.g. "https://app.kotakserver.my.id".
func (c Config) AppOrigin() string {
	return c.PublicScheme + "://" + c.AppHost
}

// PreviewURL is the public address of the Preview for slug.
func (c Config) PreviewURL(slug string) string {
	return c.PublicScheme + "://" + slug + "." + c.PreviewDomain
}
