// Package config loads maket's settings from MAKET_* environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	DataDir       string // MAKET_DATA_DIR: holds the SQLite file and local storage
	StorageDriver string // MAKET_STORAGE_DRIVER: "local" ("s3" arrives with S3Store)

	GitHubClientID     string   // MAKET_GITHUB_CLIENT_ID
	GitHubClientSecret string   // MAKET_GITHUB_CLIENT_SECRET
	AllowedGitHubUsers []string // MAKET_ALLOWED_GITHUB_USERS, comma-separated
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
		DataDir:       required("MAKET_DATA_DIR"),
		StorageDriver: strings.ToLower(get("MAKET_STORAGE_DRIVER", "local")),

		GitHubClientID:     required("MAKET_GITHUB_CLIENT_ID"),
		GitHubClientSecret: required("MAKET_GITHUB_CLIENT_SECRET"),
	}
	for _, login := range strings.Split(get("MAKET_ALLOWED_GITHUB_USERS", ""), ",") {
		if login = strings.TrimSpace(login); login != "" {
			c.AllowedGitHubUsers = append(c.AllowedGitHubUsers, login)
		}
	}
	if len(c.AllowedGitHubUsers) == 0 {
		problems = append(problems, errors.New("MAKET_ALLOWED_GITHUB_USERS must list at least one GitHub username"))
	}

	if c.PublicScheme != "https" && c.PublicScheme != "http" {
		problems = append(problems, fmt.Errorf("MAKET_PUBLIC_SCHEME must be \"https\" or \"http\", got %q", c.PublicScheme))
	}
	if c.StorageDriver != "local" {
		problems = append(problems, fmt.Errorf("MAKET_STORAGE_DRIVER must be \"local\" (s3 is not implemented yet), got %q", c.StorageDriver))
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

// DBPath is the SQLite database file inside the data dir.
func (c Config) DBPath() string {
	return filepath.Join(c.DataDir, "maket.db")
}

// StorageDir is where LocalStore keeps Prototype files.
func (c Config) StorageDir() string {
	return filepath.Join(c.DataDir, "storage")
}

// Secure reports whether the public scheme is https, which decides the
// session cookie's name and Secure flag.
func (c Config) Secure() bool {
	return c.PublicScheme == "https"
}

// GitHubRedirectURL is the OAuth callback registered on the GitHub app.
func (c Config) GitHubRedirectURL() string {
	return c.AppOrigin() + "/api/auth/github/callback"
}

// AppOrigin is the dashboard origin, e.g. "https://app.kotakserver.my.id".
func (c Config) AppOrigin() string {
	return c.PublicScheme + "://" + c.AppHost
}

// PreviewURL is the public address of the Preview for slug.
func (c Config) PreviewURL(slug string) string {
	return c.PublicScheme + "://" + slug + "." + c.PreviewDomain
}
