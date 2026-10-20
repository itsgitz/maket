// Package config loads maket's settings from MAKET_* environment variables.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
	StorageDriver string // MAKET_STORAGE_DRIVER: "local" or "s3"

	// Used when StorageDriver is "s3". Region defaults to "auto", which is
	// what Cloudflare R2 expects.
	S3Endpoint        string // MAKET_S3_ENDPOINT: http or https base URL
	S3Bucket          string // MAKET_S3_BUCKET
	S3Region          string // MAKET_S3_REGION
	S3AccessKeyID     string // MAKET_S3_ACCESS_KEY_ID
	S3SecretAccessKey string // MAKET_S3_SECRET_ACCESS_KEY

	GitHubClientID     string   // MAKET_GITHUB_CLIENT_ID
	GitHubClientSecret string   // MAKET_GITHUB_CLIENT_SECRET
	AllowedGitHubUsers []string // MAKET_ALLOWED_GITHUB_USERS, comma-separated

	MaxUploadBytes    int64 // MAKET_MAX_UPLOAD_MB
	MaxExtractedBytes int64 // MAKET_MAX_EXTRACTED_MB
	MaxFiles          int   // MAKET_MAX_FILES
	UserQuotaBytes    int64 // MAKET_USER_QUOTA_MB

	// Optional: purge a Prototype's Cloudflare cache after re-upload or
	// delete. Both or neither.
	CloudflareAPIToken string // MAKET_CF_API_TOKEN
	CloudflareZoneID   string // MAKET_CF_ZONE_ID
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
	positive := func(name string, def int) int {
		v := get(name, strconv.Itoa(def))
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			problems = append(problems, fmt.Errorf("%s must be a positive whole number, got %q", name, v))
		}
		return n
	}
	const mb = 1 << 20
	c.MaxUploadBytes = int64(positive("MAKET_MAX_UPLOAD_MB", 20)) * mb
	c.MaxExtractedBytes = int64(positive("MAKET_MAX_EXTRACTED_MB", 100)) * mb
	c.MaxFiles = positive("MAKET_MAX_FILES", 500)
	c.UserQuotaBytes = int64(positive("MAKET_USER_QUOTA_MB", 1024)) * mb

	c.CloudflareAPIToken = get("MAKET_CF_API_TOKEN", "")
	c.CloudflareZoneID = get("MAKET_CF_ZONE_ID", "")
	if (c.CloudflareAPIToken == "") != (c.CloudflareZoneID == "") {
		problems = append(problems, errors.New("MAKET_CF_API_TOKEN and MAKET_CF_ZONE_ID must be set together (or both left empty)"))
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
	switch c.StorageDriver {
	case "local":
	case "s3":
		c.S3Endpoint = required("MAKET_S3_ENDPOINT")
		if c.S3Endpoint != "" {
			u, err := url.Parse(c.S3Endpoint)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				problems = append(problems, fmt.Errorf("MAKET_S3_ENDPOINT must be an http or https URL, got %q", c.S3Endpoint))
			}
		}
		c.S3Bucket = required("MAKET_S3_BUCKET")
		c.S3Region = get("MAKET_S3_REGION", "auto")
		c.S3AccessKeyID = required("MAKET_S3_ACCESS_KEY_ID")
		c.S3SecretAccessKey = required("MAKET_S3_SECRET_ACCESS_KEY")
	default:
		problems = append(problems, fmt.Errorf("MAKET_STORAGE_DRIVER must be \"local\" or \"s3\", got %q", c.StorageDriver))
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

// CloudflarePurge reports whether cache purging is configured.
func (c Config) CloudflarePurge() bool {
	return c.CloudflareAPIToken != ""
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
