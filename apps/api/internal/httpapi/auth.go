package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/apitypes"
	"github.com/itsgitz/maket/apps/api/internal/auth"
	"github.com/itsgitz/maket/apps/api/internal/jsonapi"
	"github.com/itsgitz/maket/apps/api/internal/oauth/github"
)

// GitHubLogin starts and completes a GitHub OAuth login.
type GitHubLogin interface {
	AuthCodeURL(state string) string
	Exchange(ctx context.Context, code string) (github.User, error)
}

// stateTTL bounds how long a login may take between leaving for GitHub and
// coming back.
const stateTTL = 10 * time.Minute

func stateCookieName(secure bool) string {
	if secure {
		return "__Host-maket_oauth_state"
	}
	return "maket_oauth_state"
}

type authHandlers struct {
	auth   *auth.Service
	github GitHubLogin
	secure bool
}

// login sends the browser to GitHub with a random state, remembered in a
// short-lived cookie so the callback can check the round trip.
func (h authHandlers) login(w http.ResponseWriter, r *http.Request) {
	state := rand.Text()
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName(h.secure),
		Value:    state,
		Path:     "/",
		MaxAge:   int(stateTTL.Seconds()),
		Secure:   h.secure,
		HttpOnly: true,
		// Lax, because GitHub's redirect back is a cross-site top-level GET.
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, h.github.AuthCodeURL(state), http.StatusFound)
}

func (h authHandlers) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, err := r.Cookie(stateCookieName(h.secure))
	http.SetCookie(w, &http.Cookie{Name: stateCookieName(h.secure), Path: "/", MaxAge: -1, Secure: h.secure, HttpOnly: true, SameSite: http.SameSiteLaxMode})

	if q.Get("error") != "" {
		loginPage(w, http.StatusBadRequest, "Sign-in was cancelled.")
		return
	}
	if err != nil || q.Get("state") == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(q.Get("state"))) != 1 {
		loginPage(w, http.StatusBadRequest, "Your sign-in expired or was started in another tab. Please try again.")
		return
	}

	ghUser, err := h.github.Exchange(r.Context(), q.Get("code"))
	if err != nil {
		slog.Warn("github login: exchange", "err", err)
		loginPage(w, http.StatusBadGateway, "We couldn't confirm your sign-in with GitHub. Please try again.")
		return
	}
	user, err := h.auth.LoginGitHub(r.Context(), auth.Identity{
		GitHubID:  ghUser.ID,
		Login:     ghUser.Login,
		Name:      ghUser.Name,
		AvatarURL: ghUser.AvatarURL,
	})
	if errors.Is(err, auth.ErrNotAllowed) {
		slog.Info("github login: not on allowlist", "login", ghUser.Login)
		loginPage(w, http.StatusForbidden, "The GitHub account "+ghUser.Login+" is not allowed to use maket.")
		return
	}
	if err != nil {
		slog.Error("github login: save user", "err", err)
		loginPage(w, http.StatusInternalServerError, "Something went wrong while signing you in. Please try again.")
		return
	}

	token, expires, err := h.auth.CreateSession(r.Context(), user.ID)
	if err != nil {
		slog.Error("github login: create session", "err", err)
		loginPage(w, http.StatusInternalServerError, "Something went wrong while signing you in. Please try again.")
		return
	}
	auth.SetSessionCookie(w, token, expires, h.secure)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h authHandlers) logout(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	if err := h.auth.DeleteSession(r.Context(), p.SessionID); err != nil {
		internalError(w, "logout", err)
		return
	}
	auth.ClearSessionCookie(w, h.secure)
	w.WriteHeader(http.StatusNoContent)
}

func (h authHandlers) me(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	jsonapi.Write(w, http.StatusOK, apitypes.Me{
		ID:          p.User.ID,
		GitHubLogin: p.User.GitHubLogin,
		Name:        p.User.Name,
		AvatarURL:   p.User.AvatarURL,
		AuthMethod:  string(p.Method),
	})
}

type apiKeyResponse struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	// Key is the full secret, present only in the response that creates it.
	Key string `json:"key,omitempty"`
}

func toAPIKeyResponse(k auth.APIKey) apiKeyResponse {
	return apiKeyResponse{ID: k.ID, Name: k.Name, Prefix: k.Prefix, CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt, RevokedAt: k.RevokedAt}
}

func (h authHandlers) listKeys(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	keys, err := h.auth.ListAPIKeys(r.Context(), p.User.ID)
	if err != nil {
		internalError(w, "list keys", err)
		return
	}
	out := make([]apiKeyResponse, len(keys))
	for i, k := range keys {
		out[i] = toAPIKeyResponse(k)
	}
	jsonapi.Write(w, http.StatusOK, map[string]any{"keys": out})
}

func (h authHandlers) createKey(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		jsonapi.Error(w, http.StatusBadRequest, "invalid_body", `expected a JSON body like {"name": "my agent"}`)
		return
	}
	key, secret, err := h.auth.CreateAPIKey(r.Context(), p.User.ID, req.Name)
	if errors.Is(err, auth.ErrInvalidName) {
		jsonapi.Error(w, http.StatusBadRequest, "invalid_name", "key name must be 1 to 100 characters")
		return
	}
	if err != nil {
		internalError(w, "create key", err)
		return
	}
	resp := toAPIKeyResponse(key)
	resp.Key = secret
	jsonapi.Write(w, http.StatusCreated, resp)
}

func (h authHandlers) revokeKey(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonapi.Error(w, http.StatusNotFound, "not_found", "no such API key")
		return
	}
	err = h.auth.RevokeAPIKey(r.Context(), p.User.ID, id)
	if errors.Is(err, auth.ErrNotFound) {
		jsonapi.Error(w, http.StatusNotFound, "not_found", "no such API key")
		return
	}
	if err != nil {
		internalError(w, "revoke key", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func internalError(w http.ResponseWriter, what string, err error) {
	slog.Error(what, "err", err)
	jsonapi.Error(w, http.StatusInternalServerError, "internal", "internal error")
}

var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>maket sign-in</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem}</style>
<h1>maket</h1>
<p>{{.}}</p>
<p><a href="/">Back to maket</a></p>
`))

// loginPage renders a plain page for a failed browser login, where a JSON
// error would be a dead end.
func loginPage(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	loginTmpl.Execute(w, message)
}
