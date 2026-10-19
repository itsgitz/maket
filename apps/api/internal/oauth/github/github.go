// Package github signs users in with GitHub OAuth. It asks for no scopes:
// the public profile is all maket needs, and the access token is used once
// to read it and then dropped.
package github

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// Default endpoints.
const (
	defaultAuthURL  = "https://github.com/login/oauth/authorize"
	defaultTokenURL = "https://github.com/login/oauth/access_token"
	defaultAPIURL   = "https://api.github.com"
)

// maxUserBody bounds how much of the /user response is read.
const maxUserBody = 1 << 20

// User is the GitHub account that signed in.
type User struct {
	ID        int64
	Login     string
	Name      string // "" when unset on GitHub
	AvatarURL string
}

// Config configures a Provider.
type Config struct {
	ClientID, ClientSecret string
	RedirectURL            string // e.g. https://app.kotakserver.my.id/api/auth/github/callback

	// Overrides for tests; empty means github.com / api.github.com.
	AuthURL, TokenURL, APIURL string

	// HTTPClient is used for the token exchange and the API call. Nil means
	// a client with a 10s timeout.
	HTTPClient *http.Client
}

// Provider runs the GitHub OAuth flow. It is safe for concurrent use.
type Provider struct {
	oauth  oauth2.Config
	apiURL string
	client *http.Client
}

// New returns a Provider for cfg.
func New(cfg Config) *Provider {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Provider{
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint: oauth2.Endpoint{
				AuthURL:  cmp.Or(cfg.AuthURL, defaultAuthURL),
				TokenURL: cmp.Or(cfg.TokenURL, defaultTokenURL),
				// GitHub documents client_id/client_secret as form
				// parameters; fixing the style avoids auto-detect's retry.
				AuthStyle: oauth2.AuthStyleInParams,
			},
		},
		apiURL: strings.TrimSuffix(cmp.Or(cfg.APIURL, defaultAPIURL), "/"),
		client: client,
	}
}

// AuthCodeURL is where to send the browser to start login.
func (p *Provider) AuthCodeURL(state string) string {
	return p.oauth.AuthCodeURL(state)
}

// Exchange trades the callback's code for a token and fetches the user.
func (p *Provider) Exchange(ctx context.Context, code string) (User, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, p.client)
	tok, err := p.oauth.Exchange(ctx, code)
	if err != nil {
		return User{}, fmt.Errorf("github: exchange code: %w", err)
	}
	u, err := p.fetchUser(ctx, tok.AccessToken)
	if err != nil {
		return User{}, fmt.Errorf("github: fetch user: %w", err)
	}
	return u, nil
}

// fetchUser reads the token owner's profile from GET /user.
func (p *Provider) fetchUser(ctx context.Context, token string) (User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.apiURL+"/user", nil)
	if err != nil {
		return User{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := p.client.Do(req)
	if err != nil {
		return User{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUserBody))
	if err != nil {
		return User{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return User{}, fmt.Errorf("status %s: %.200s", resp.Status, body)
	}
	var v struct {
		ID        int64   `json:"id"`
		Login     string  `json:"login"`
		Name      *string `json:"name"`
		AvatarURL string  `json:"avatar_url"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return User{}, fmt.Errorf("decode: %w", err)
	}
	if v.ID == 0 || v.Login == "" {
		return User{}, errors.New("response has no id or login")
	}
	u := User{ID: v.ID, Login: v.Login, AvatarURL: v.AvatarURL}
	if v.Name != nil {
		u.Name = *v.Name
	}
	return u, nil
}
