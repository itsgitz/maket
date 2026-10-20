package httpapi

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/apitypes"
	"github.com/itsgitz/maket/apps/api/internal/auth"
	"github.com/itsgitz/maket/apps/api/internal/config"
	"github.com/itsgitz/maket/apps/api/internal/oauth/github"
	"github.com/itsgitz/maket/apps/api/internal/oauth/github/githubtest"
	"github.com/itsgitz/maket/apps/api/internal/preview"
	"github.com/itsgitz/maket/apps/api/internal/prototype"
	"github.com/itsgitz/maket/apps/api/internal/prototype/archive"
	"github.com/itsgitz/maket/apps/api/internal/storage"
)

// testApp is a running maket API wired to a fake GitHub.
type testApp struct {
	t      *testing.T
	origin string // app origin, e.g. http://127.0.0.1:41234
	github *githubtest.Server
	client *http.Client // keeps cookies, never follows redirects
}

func newTestApp(t *testing.T, ghUser github.User, allowed ...string) *testApp {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		AppHost:       ln.Addr().String(),
		PreviewDomain: "preview.localhost:8080",
		PublicScheme:  "http",
	}
	gh := githubtest.NewServer(t, ghUser)
	authSvc, database := newAuthServiceDB(t, allowed...)
	store, err := storage.NewLocal(filepath.Join(t.TempDir(), "storage"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	tmp := t.TempDir()
	allow, _ := auth.NewAllowlist(allowed)
	protos := prototype.NewService(database, store, allow, prototype.Config{
		Limits:      archive.Limits{MaxFiles: 20, MaxExtractedBytes: 64 << 10},
		QuotaBytes:  100 << 10,
		TmpDir:      tmp,
		PreviewHost: func(slug string) string { return slug + "." + cfg.PreviewDomain },
	}, nil, time.Now)
	srv := httptest.NewUnstartedServer(New(cfg, Deps{
		Dashboard:      http.NotFoundHandler(),
		Previews:       &preview.Handler{Lookup: protos, Store: store},
		Auth:           authSvc,
		GitHub:         github.New(gh.Config("client-id", "client-secret", cfg.GitHubRedirectURL())),
		Prototypes:     protos,
		MaxUploadBytes: 32 << 10,
		TmpDir:         tmp,
	}))
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	return &testApp{
		t:      t,
		origin: cfg.AppOrigin(),
		github: gh,
		client: &http.Client{
			Jar:           jar,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// do sends a request; a non-empty body is sent as JSON.
func (a *testApp) do(method, url, body string, header map[string]string) *http.Response {
	a.t.Helper()
	if strings.HasPrefix(url, "/") {
		url = a.origin + url
	}
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	a.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// login runs the browser OAuth round trip and returns the callback response.
func (a *testApp) login() *http.Response {
	a.t.Helper()
	start := a.do("GET", "/api/auth/github/login", "", nil)
	if start.StatusCode != http.StatusFound {
		a.t.Fatalf("login: status %d", start.StatusCode)
	}
	approve := a.do("GET", start.Header.Get("Location"), "", nil) // the fake GitHub "approves"
	if approve.StatusCode != http.StatusFound {
		a.t.Fatalf("authorize: status %d", approve.StatusCode)
	}
	return a.do("GET", approve.Header.Get("Location"), "", nil)
}

func (a *testApp) withOrigin() map[string]string { return map[string]string{"Origin": a.origin} }

func bearer(key string) map[string]string { return map[string]string{"Authorization": "Bearer " + key} }

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

func wantStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s: status %d, want %d; body %s", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, want, body)
	}
}

func TestAuthFlowEndToEnd(t *testing.T) {
	app := newTestApp(t, github.User{ID: 42, Login: "Anggit", Name: "Anggit G", AvatarURL: "https://avatars.example/42"}, "anggit")

	// Anonymous.
	wantStatus(t, app.do("GET", "/api/me", "", nil), http.StatusUnauthorized)

	// Browser login.
	cb := app.login()
	wantStatus(t, cb, http.StatusSeeOther)
	if loc := cb.Header.Get("Location"); loc != "/" {
		t.Errorf("callback redirects to %q, want /", loc)
	}
	me := decode[apitypes.Me](t, app.do("GET", "/api/me", "", nil))
	if me.GitHubLogin != "Anggit" || me.Name != "Anggit G" || me.AuthMethod != "session" {
		t.Errorf("me = %+v", me)
	}

	// Creating a key needs the app Origin.
	wantStatus(t, app.do("POST", "/api/keys", `{"name":"agent"}`, nil), http.StatusForbidden)
	created := app.do("POST", "/api/keys", `{"name":"  agent  "}`, app.withOrigin())
	wantStatus(t, created, http.StatusCreated)
	key := decode[apiKeyResponse](t, created)
	if !strings.HasPrefix(key.Key, "mk_") || key.Name != "agent" || key.Prefix != key.Key[3:11] {
		t.Errorf("created key = %+v", key)
	}

	// The key works for /me but cannot manage keys, and is never listed in full.
	viaKey := decode[apitypes.Me](t, app.do("GET", "/api/me", "", bearer(key.Key)))
	if viaKey.AuthMethod != "api_key" || viaKey.ID != me.ID {
		t.Errorf("me via key = %+v", viaKey)
	}
	wantStatus(t, app.do("GET", "/api/keys", "", bearer(key.Key)), http.StatusForbidden)
	wantStatus(t, app.do("POST", "/api/keys", `{"name":"x"}`, bearer(key.Key)), http.StatusForbidden)
	list := app.do("GET", "/api/keys", "", nil)
	wantStatus(t, list, http.StatusOK)
	raw, _ := io.ReadAll(list.Body)
	if strings.Contains(string(raw), key.Key) || !strings.Contains(string(raw), key.Prefix) {
		t.Errorf("key list leaks the secret or lacks the prefix: %s", raw)
	}

	// Revoking.
	id := strconv.FormatInt(key.ID, 10)
	wantStatus(t, app.do("DELETE", "/api/keys/"+id, "", app.withOrigin()), http.StatusNoContent)
	wantStatus(t, app.do("GET", "/api/me", "", bearer(key.Key)), http.StatusUnauthorized)
	wantStatus(t, app.do("DELETE", "/api/keys/999999", "", app.withOrigin()), http.StatusNotFound)
	wantStatus(t, app.do("DELETE", "/api/keys/abc", "", app.withOrigin()), http.StatusNotFound)

	// Logout needs the Origin too, then the session is gone.
	wantStatus(t, app.do("POST", "/api/auth/logout", "", nil), http.StatusForbidden)
	wantStatus(t, app.do("POST", "/api/auth/logout", "", app.withOrigin()), http.StatusNoContent)
	wantStatus(t, app.do("GET", "/api/me", "", nil), http.StatusUnauthorized)
}

func TestLoginNotAllowlisted(t *testing.T) {
	app := newTestApp(t, github.User{ID: 7, Login: "stranger"}, "anggit")
	cb := app.login()
	wantStatus(t, cb, http.StatusForbidden)
	body, _ := io.ReadAll(cb.Body)
	if !strings.Contains(string(body), "stranger is not allowed") {
		t.Errorf("page = %s", body)
	}
	for _, c := range cb.Cookies() {
		if c.Name == "maket_session" && c.MaxAge >= 0 {
			t.Error("a session cookie was set for a non-allowlisted user")
		}
	}
	wantStatus(t, app.do("GET", "/api/me", "", nil), http.StatusUnauthorized)
}

func TestCallbackRejections(t *testing.T) {
	user := github.User{ID: 42, Login: "anggit"}

	t.Run("state mismatch", func(t *testing.T) {
		app := newTestApp(t, user, "anggit")
		app.do("GET", "/api/auth/github/login", "", nil) // sets the state cookie
		wantStatus(t, app.do("GET", "/api/auth/github/callback?code=x&state=forged", "", nil), http.StatusBadRequest)
	})
	t.Run("no state cookie", func(t *testing.T) {
		app := newTestApp(t, user, "anggit")
		wantStatus(t, app.do("GET", "/api/auth/github/callback?code=x&state=abc", "", nil), http.StatusBadRequest)
	})
	t.Run("user cancelled", func(t *testing.T) {
		app := newTestApp(t, user, "anggit")
		wantStatus(t, app.do("GET", "/api/auth/github/callback?error=access_denied", "", nil), http.StatusBadRequest)
	})
	t.Run("exchange fails", func(t *testing.T) {
		app := newTestApp(t, user, "anggit")
		app.github.FailExchange(true)
		wantStatus(t, app.login(), http.StatusBadGateway)
		wantStatus(t, app.do("GET", "/api/me", "", nil), http.StatusUnauthorized)
	})
	t.Run("state is single use", func(t *testing.T) {
		app := newTestApp(t, user, "anggit")
		start := app.do("GET", "/api/auth/github/login", "", nil)
		approve := app.do("GET", start.Header.Get("Location"), "", nil)
		cb := approve.Header.Get("Location")
		wantStatus(t, app.do("GET", cb, "", nil), http.StatusSeeOther)
		wantStatus(t, app.do("GET", cb, "", nil), http.StatusBadRequest) // state cookie was cleared
	})
}

func TestCreateKeyValidation(t *testing.T) {
	app := newTestApp(t, github.User{ID: 42, Login: "anggit"}, "anggit")
	wantStatus(t, app.login(), http.StatusSeeOther)
	for _, body := range []string{`{"name":"   "}`, `{"name":"` + strings.Repeat("x", 101) + `"}`} {
		wantStatus(t, app.do("POST", "/api/keys", body, app.withOrigin()), http.StatusBadRequest)
	}
	wantStatus(t, app.do("POST", "/api/keys", `not json`, app.withOrigin()), http.StatusBadRequest)
}
