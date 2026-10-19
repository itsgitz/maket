// Package githubtest is a fake GitHub for tests: the OAuth authorize and
// token endpoints plus GET /user.
package githubtest

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/itsgitz/maket/apps/api/internal/oauth/github"
)

// Server is a running fake GitHub. It is safe for concurrent use.
type Server struct {
	URL string // base URL; use for AuthURL/TokenURL/APIURL

	mu      sync.Mutex
	user    github.User
	fail    bool
	clients map[string]string      // client ID -> secret
	codes   map[string]grant       // unused codes
	tokens  map[string]github.User // access token -> user
}

// grant is what an authorization code was issued for.
type grant struct {
	clientID, redirectURI string
	user                  github.User
}

// NewServer starts a fake GitHub that authenticates as user. Closed via t.Cleanup.
func NewServer(t testing.TB, user github.User) *Server {
	s := &Server{
		user:    user,
		clients: map[string]string{},
		codes:   map[string]grant{},
		tokens:  map[string]github.User{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login/oauth/authorize", s.authorize)
	mux.HandleFunc("POST /login/oauth/access_token", s.accessToken)
	mux.HandleFunc("GET /user", s.getUser)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// Config registers clientID/clientSecret as a valid OAuth app and returns a
// github.Config pointed at the fake.
func (s *Server) Config(clientID, clientSecret, redirectURL string) github.Config {
	s.mu.Lock()
	s.clients[clientID] = clientSecret
	s.mu.Unlock()
	return github.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		AuthURL:      s.URL + "/login/oauth/authorize",
		TokenURL:     s.URL + "/login/oauth/access_token",
		APIURL:       s.URL,
	}
}

// SetUser changes who logs in next (codes issued from now on).
func (s *Server) SetUser(u github.User) {
	s.mu.Lock()
	s.user = u
	s.mu.Unlock()
}

// FailExchange makes the token endpoint answer every request with GitHub's
// 200-with-error response.
func (s *Server) FailExchange(on bool) {
	s.mu.Lock()
	s.fail = on
	s.mu.Unlock()
}

// authorize acts like GitHub after the user approves: redirect back with a code.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID, redirectURI := q.Get("client_id"), q.Get("redirect_uri")
	s.mu.Lock()
	_, ok := s.clients[clientID]
	s.mu.Unlock()
	if !ok {
		http.Error(w, "unknown client_id", http.StatusNotFound)
		return
	}
	dest, err := url.Parse(redirectURI)
	if err != nil || !dest.IsAbs() {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	code := rand.Text()
	s.mu.Lock()
	s.codes[code] = grant{clientID: clientID, redirectURI: redirectURI, user: s.user}
	s.mu.Unlock()
	v := dest.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	dest.RawQuery = v.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}

// accessToken exchanges a code. Like GitHub, failures are 200 with an error.
func (s *Server) accessToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	clientID, secret, basic := r.BasicAuth()
	if !basic {
		clientID, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	code := r.PostForm.Get("code")

	s.mu.Lock()
	defer s.mu.Unlock()
	if want, ok := s.clients[clientID]; !ok || secret == "" || secret != want {
		tokenError(w, "incorrect_client_credentials", "The client_id and/or client_secret passed are incorrect.")
		return
	}
	g, ok := s.codes[code]
	if s.fail || !ok || g.clientID != clientID {
		tokenError(w, "bad_verification_code", "The code passed is incorrect or expired.")
		return
	}
	if ru := r.PostForm.Get("redirect_uri"); ru != "" && ru != g.redirectURI {
		tokenError(w, "redirect_uri_mismatch", "The redirect_uri MUST match the registered callback URL for this application.")
		return
	}
	delete(s.codes, code)
	token := "gho_" + rand.Text()
	s.tokens[token] = g.user
	writeJSON(w, http.StatusOK, map[string]string{
		"access_token": token,
		"token_type":   "bearer",
		"scope":        "",
	})
}

func tokenError(w http.ResponseWriter, code, desc string) {
	writeJSON(w, http.StatusOK, map[string]string{
		"error":             code,
		"error_description": desc,
		"error_uri":         "https://docs.github.com/apps/managing-oauth-apps/troubleshooting-oauth-app-access-token-request-errors",
	})
}

// getUser returns the token owner's public profile.
func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok {
		token, ok = strings.CutPrefix(auth, "token ")
	}
	s.mu.Lock()
	u, found := s.tokens[token]
	s.mu.Unlock()
	if !ok || !found {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	var name *string
	if u.Name != "" {
		name = &u.Name
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         u.ID,
		"login":      u.Login,
		"name":       name,
		"avatar_url": u.AvatarURL,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
