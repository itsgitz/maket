package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/apitypes"
)

// connectTimeout bounds dialing, the TLS handshake and the wait for response
// headers. There is no overall timeout: a large upload may take long.
const connectTimeout = 30 * time.Second

// maxResponseBytes caps how much of a response body is read.
const maxResponseBytes = 8 << 20

// NewHTTPClient returns the HTTP client the CLI uses.
func NewHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   connectTimeout,
		ResponseHeaderTimeout: connectTimeout,
		ForceAttemptHTTP2:     true,
	}}
}

// request describes one API call.
type request struct {
	method      string
	path        string // e.g. "/api/me"
	body        io.Reader
	contentType string
	want        int // expected status; the body is decoded into out
	out         any // nil: ignore the body
}

// do sends req to the API at s.URL with s.Key and decodes the response.
// Failures are *exitError: server errors keep their code and message
// (401 and 404 get their own exit codes), anything unreadable is
// invalid_response, and transport failures are network.
func (r *runner) do(ctx context.Context, s settings, req request) error {
	r.warnIfInsecure(s.URL)
	hreq, err := http.NewRequestWithContext(ctx, req.method, s.URL+req.path, req.body)
	if err != nil {
		return usageError("invalid API URL %q: %v", s.URL, err)
	}
	hreq.Header.Set("Authorization", "Bearer "+s.Key)
	hreq.Header.Set("User-Agent", "maket-cli/"+r.env.Version)
	hreq.Header.Set("Accept", "application/json")
	if req.contentType != "" {
		hreq.Header.Set("Content-Type", req.contentType)
	}
	resp, err := r.env.HTTPClient.Do(hreq)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err // drop the "Get <url>:" prefix; the message names the server
		}
		return newError(exitFailure, codeNetwork, "cannot reach %s: %v", s.URL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return newError(exitFailure, codeNetwork, "reading the response from %s: %v", s.URL, err)
	}

	if resp.StatusCode == req.want {
		if req.out == nil {
			return nil
		}
		if err := json.Unmarshal(body, req.out); err != nil {
			return invalidResponse(s, resp.StatusCode)
		}
		return nil
	}

	var er apitypes.ErrorResponse
	if err := json.Unmarshal(body, &er); err != nil || er.Error.Code == "" {
		return invalidResponse(s, resp.StatusCode)
	}
	e := &exitError{exit: exitFailure, body: er.Error}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		// Under --json the server's error is still passed through as is.
		server := er.Error
		e.exit, e.json = exitAuth, &server
		e.body.Message = "the API Key was rejected (revoked or wrong): run `maket login`"
	case http.StatusForbidden:
		// The key's owner was removed from the Allowlist: as good as revoked.
		if er.Error.Code == "not_allowed" {
			e.exit = exitAuth
		}
	case http.StatusNotFound:
		e.exit = exitNotFound
	}
	return e
}

func invalidResponse(s settings, status int) error {
	return newError(exitFailure, codeInvalidResponse, "unexpected response from %s (HTTP %d): is this a maket server?", s.URL, status)
}
