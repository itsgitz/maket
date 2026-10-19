// Package cfpurge purges a Preview host from Cloudflare's cache, using only
// the standard library.
//
// Strategy: purge by hostname ({"hosts": [host]}), not by URL. Every plan,
// Free included, can purge by URL, hostname, tag, prefix or everything; Free
// allows 5 hostname/tag/prefix/everything requests per minute (bucket 25) and
// up to 100 hosts per request, shared across the account's Free zones.
// One hostname request drops every cached object of the Preview, including
// paths maket does not know about (query strings, SPA fallbacks, 404s), which
// URL purge would miss. See
// https://developers.cloudflare.com/cache/how-to/purge-cache/ (availability
// and limits, last updated Sep 29, 2026) and the API reference
// https://developers.cloudflare.com/api/resources/cache/methods/purge/
// (POST /zones/{zone_id}/purge_cache, token permission "Cache Purge").
package cfpurge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is Cloudflare's v4 API root.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// maxBody caps how much of a response is read.
const maxBody = 1 << 20

// ErrRateLimited is wrapped by Purge's error when Cloudflare answers 429.
var ErrRateLimited = errors.New("cfpurge: rate limited")

// Client purges cached content of one Cloudflare zone. The token needs the
// Zone → Cache Purge permission on that zone.
type Client struct {
	token   string
	zoneID  string
	baseURL string
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API root, for tests.
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient sets the HTTP client used for API calls.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.http = hc }
}

// New returns a Client for zoneID authenticated with apiToken.
func New(apiToken, zoneID string, opts ...Option) *Client {
	c := &Client{
		token:   apiToken,
		zoneID:  zoneID,
		baseURL: DefaultBaseURL,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Purge removes everything cached for one Preview host (e.g.
// "ab12cd34.cloudshare.my.id"), given the URL paths of the files that were
// served ("/", "/index.html", "/assets/app.js", "/docs/", ...).
//
// It purges the whole hostname in one request, so paths is ignored; it is
// kept so callers need not change if the strategy ever moves to URL purge.
// A 429 is returned as an error wrapping ErrRateLimited, without retrying.
func (c *Client) Purge(ctx context.Context, host string, paths []string) error {
	if host == "" || strings.ContainsAny(host, "/:?#@ ") {
		return fmt.Errorf("cfpurge: invalid host %q", host)
	}
	body, err := json.Marshal(map[string][]string{"hosts": {host}})
	if err != nil {
		return fmt.Errorf("cfpurge: %w", err)
	}
	endpoint := c.baseURL + "/zones/" + url.PathEscape(c.zoneID) + "/purge_cache"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cfpurge: purge %s: %w", host, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// *url.Error carries only the URL, never headers, so the token
		// cannot leak here.
		return fmt.Errorf("cfpurge: purge %s: %w", host, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("cfpurge: purge %s: read response: %w", host, err)
	}

	var env envelope
	jsonErr := json.Unmarshal(raw, &env)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: purge %s: %s", ErrRateLimited, host, env.describe(resp.Status))
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("cfpurge: purge %s: %s", host, env.describe(resp.Status))
	case jsonErr != nil:
		return fmt.Errorf("cfpurge: purge %s: decode response: %w", host, jsonErr)
	case !env.Success:
		return fmt.Errorf("cfpurge: purge %s: %s", host, env.describe("success=false"))
	}
	return nil
}

// envelope is Cloudflare's standard v4 response wrapper.
type envelope struct {
	Success bool       `json:"success"`
	Errors  []apiError `json:"errors"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// describe joins prefix with Cloudflare's error messages, if any.
func (e envelope) describe(prefix string) string {
	if len(e.Errors) == 0 {
		return prefix
	}
	msgs := make([]string, len(e.Errors))
	for i, ae := range e.Errors {
		msgs[i] = fmt.Sprintf("%d %s", ae.Code, ae.Message)
	}
	return prefix + ": " + strings.Join(msgs, "; ")
}
