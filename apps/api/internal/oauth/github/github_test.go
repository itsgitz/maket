package github_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/itsgitz/maket/apps/api/internal/oauth/github"
	"github.com/itsgitz/maket/apps/api/internal/oauth/github/githubtest"
)

const redirectURL = "https://app.example.com/api/auth/github/callback"

var octocat = github.User{ID: 583231, Login: "octocat", Name: "The Octocat", AvatarURL: "https://avatars.githubusercontent.com/u/583231"}

// authorize follows the provider's AuthCodeURL on the fake like a browser
// that approves, and returns the code from the callback redirect.
func authorize(t *testing.T, p *github.Provider, state string) string {
	t.Helper()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := noRedirect.Get(p.AuthCodeURL(state))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize status = %d, want 302", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.Scheme + "://" + loc.Host + loc.Path; got != redirectURL {
		t.Fatalf("redirected to %q, want %q", got, redirectURL)
	}
	if got := loc.Query().Get("state"); got != state {
		t.Fatalf("state = %q, want %q", got, state)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatal("no code in redirect")
	}
	return code
}

func TestAuthCodeURL(t *testing.T) {
	p := github.New(github.Config{ClientID: "cid", ClientSecret: "secret", RedirectURL: redirectURL})
	u, err := url.Parse(p.AuthCodeURL("st4te"))
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != "https://github.com/login/oauth/authorize" {
		t.Errorf("auth URL = %q", got)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id":     "cid",
		"redirect_uri":  redirectURL,
		"state":         "st4te",
		"response_type": "code",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if q.Has("scope") {
		t.Errorf("scope = %q, want none", q.Get("scope"))
	}
	if q.Has("client_secret") {
		t.Error("client_secret leaked into the auth URL")
	}
}

func TestExchange(t *testing.T) {
	tests := []struct {
		name string
		user github.User
	}{
		{"named", octocat},
		{"null name", github.User{ID: 7, Login: "noname", AvatarURL: "https://example.com/a.png"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := githubtest.NewServer(t, tt.user)
			p := github.New(fake.Config("cid", "secret", redirectURL))
			code := authorize(t, p, "xyz")
			got, err := p.Exchange(t.Context(), code)
			if err != nil {
				t.Fatalf("Exchange: %v", err)
			}
			if got != tt.user {
				t.Errorf("Exchange = %+v, want %+v", got, tt.user)
			}
		})
	}
}

func TestExchangeSetUser(t *testing.T) {
	fake := githubtest.NewServer(t, octocat)
	p := github.New(fake.Config("cid", "secret", redirectURL))
	other := github.User{ID: 2, Login: "hubot"}
	fake.SetUser(other)
	got, err := p.Exchange(t.Context(), authorize(t, p, "s"))
	if err != nil || got != other {
		t.Errorf("Exchange = %+v, %v; want %+v", got, err, other)
	}
}

func TestExchangeErrors(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, fake *githubtest.Server, cfg *github.Config) (code string)
		wantCode string // RFC 6749 error code from the token endpoint, if any
	}{
		{"code reused", func(t *testing.T, fake *githubtest.Server, cfg *github.Config) string {
			p := github.New(*cfg)
			code := authorize(t, p, "s")
			if _, err := p.Exchange(t.Context(), code); err != nil {
				t.Fatalf("first Exchange: %v", err)
			}
			return code
		}, "bad_verification_code"},
		{"unknown code", func(t *testing.T, fake *githubtest.Server, cfg *github.Config) string {
			return "nope"
		}, "bad_verification_code"},
		{"wrong client secret", func(t *testing.T, fake *githubtest.Server, cfg *github.Config) string {
			code := authorize(t, github.New(*cfg), "s")
			cfg.ClientSecret = "wrong"
			return code
		}, "incorrect_client_credentials"},
		{"fail exchange", func(t *testing.T, fake *githubtest.Server, cfg *github.Config) string {
			code := authorize(t, github.New(*cfg), "s")
			fake.FailExchange(true)
			return code
		}, "bad_verification_code"},
		{"empty login", func(t *testing.T, fake *githubtest.Server, cfg *github.Config) string {
			fake.SetUser(github.User{ID: 1})
			return authorize(t, github.New(*cfg), "s")
		}, ""},
		{"zero id", func(t *testing.T, fake *githubtest.Server, cfg *github.Config) string {
			fake.SetUser(github.User{Login: "ghost"})
			return authorize(t, github.New(*cfg), "s")
		}, ""},
		{"user endpoint 500", func(t *testing.T, fake *githubtest.Server, cfg *github.Config) string {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "boom", http.StatusInternalServerError)
			}))
			t.Cleanup(api.Close)
			cfg.APIURL = api.URL
			return authorize(t, github.New(*cfg), "s")
		}, ""},
		{"user endpoint bad JSON", func(t *testing.T, fake *githubtest.Server, cfg *github.Config) string {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("{"))
			}))
			t.Cleanup(api.Close)
			cfg.APIURL = api.URL
			return authorize(t, github.New(*cfg), "s")
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := githubtest.NewServer(t, octocat)
			cfg := fake.Config("cid", "secret", redirectURL)
			code := tt.setup(t, fake, &cfg)
			u, err := github.New(cfg).Exchange(t.Context(), code)
			if err == nil {
				t.Fatalf("Exchange = %+v, want error", u)
			}
			if u != (github.User{}) {
				t.Errorf("Exchange returned user %+v alongside error", u)
			}
			var re *oauth2.RetrieveError
			switch {
			case tt.wantCode == "" && errors.As(err, &re):
				t.Errorf("error = %v, want a /user failure", err)
			case tt.wantCode != "" && (!errors.As(err, &re) || re.ErrorCode != tt.wantCode):
				t.Errorf("error = %v, want token error %q", err, tt.wantCode)
			}
		})
	}
}

func TestExchangeHonorsContext(t *testing.T) {
	block := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-block:
		}
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(block) })

	tests := []struct {
		name string
		cfg  func(cfg *github.Config)
	}{
		{"slow token endpoint", func(cfg *github.Config) { cfg.TokenURL = slow.URL }},
		{"slow user endpoint", func(cfg *github.Config) { cfg.APIURL = slow.URL }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := githubtest.NewServer(t, octocat)
			cfg := fake.Config("cid", "secret", redirectURL)
			code := authorize(t, github.New(cfg), "s")
			tt.cfg(&cfg)
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err := github.New(cfg).Exchange(ctx, code)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Exchange error = %v, want context.DeadlineExceeded", err)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("Exchange took %v", d)
			}
		})
	}
}

func TestHTTPClientIsUsed(t *testing.T) {
	fake := githubtest.NewServer(t, octocat)
	cfg := fake.Config("cid", "secret", redirectURL)
	var paths []string
	cfg.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		return http.DefaultTransport.RoundTrip(r)
	})}
	p := github.New(cfg)
	if _, err := p.Exchange(t.Context(), authorize(t, p, "s")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(paths, " "); got != "/login/oauth/access_token /user" {
		t.Errorf("configured client saw %q, want token and user requests", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
