package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testOrigin = "http://app.localhost:5173"

// recorder is a final handler that remembers the Principal it saw.
type recorder struct {
	called bool
	p      Principal
	ok     bool
}

func (rec *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec.called = true
	rec.p, rec.ok = PrincipalFrom(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// fixture is a Service with alice (session + key) and helpers to send
// requests through its Middleware.
type fixture struct {
	s       *Service
	clk     *fakeClock
	alice   User
	token   string
	keyID   int64
	secret  string
	cfg     MiddlewareConfig
	cookies string // cookie name in use
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s, clk := newTestService(t)
	ctx := context.Background()
	alice := mustLogin(t, s, 1, "alice")
	token, _, err := s.CreateSession(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	key, secret, err := s.CreateAPIKey(ctx, alice.ID, "agent")
	if err != nil {
		t.Fatal(err)
	}
	cfg := MiddlewareConfig{AppOrigin: testOrigin, Secure: false}
	return &fixture{s: s, clk: clk, alice: alice, token: token, keyID: key.ID, secret: secret, cfg: cfg, cookies: SessionCookieName(false)}
}

type reqOpt func(*http.Request)

func withCookie(name, value string) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: name, Value: value}) }
}
func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Add(k, v) } }

// serve sends a request through s's Middleware (and any extra wrappers,
// innermost last) to a recorder.
func serve(s *Service, cfg MiddlewareConfig, method string, opts []reqOpt, wrap ...func(http.Handler) http.Handler) (*httptest.ResponseRecorder, *recorder) {
	rec := &recorder{}
	var h http.Handler = rec
	for i := len(wrap) - 1; i >= 0; i-- {
		h = wrap[i](h)
	}
	h = s.Middleware(cfg)(h)
	r := httptest.NewRequest(method, "http://app.localhost:5173/api/x", nil)
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w, rec
}

func errorCode(w *httptest.ResponseRecorder) string {
	body := w.Body.String()
	_, after, ok := strings.Cut(body, `"code":"`)
	if !ok {
		return ""
	}
	code, _, _ := strings.Cut(after, `"`)
	return code
}

func TestMiddleware(t *testing.T) {
	f := newFixture(t)
	cookie := withCookie(f.cookies, f.token)
	bearer := withHeader("Authorization", "Bearer "+f.secret)
	origin := withHeader("Origin", testOrigin)
	tests := []struct {
		name       string
		method     string
		opts       []reqOpt
		wantStatus int
		wantCode   string
		wantMethod Method // "" means anonymous
	}{
		{"anonymous", "GET", nil, 204, "", ""},
		{"anonymous POST", "POST", nil, 204, "", ""},
		{"session", "GET", []reqOpt{cookie}, 204, "", MethodSession},
		{"bearer", "GET", []reqOpt{bearer}, 204, "", MethodAPIKey},
		{"bearer wins over cookie", "GET", []reqOpt{cookie, bearer}, 204, "", MethodAPIKey},
		{"bad bearer ignores good cookie", "GET", []reqOpt{cookie, withHeader("Authorization", "Bearer nope")}, 401, "unauthorized", ""},
		{"empty authorization", "GET", []reqOpt{withHeader("Authorization", "")}, 401, "unauthorized", ""},
		{"lowercase bearer", "GET", []reqOpt{withHeader("Authorization", "bearer "+f.secret)}, 401, "unauthorized", ""},
		{"double space", "GET", []reqOpt{withHeader("Authorization", "Bearer  "+f.secret)}, 401, "unauthorized", ""},
		{"basic", "GET", []reqOpt{withHeader("Authorization", "Basic Zm9vOmJhcg==")}, 401, "unauthorized", ""},
		{"two authorization headers", "GET", []reqOpt{bearer, bearer}, 401, "unauthorized", ""},
		{"unknown key", "GET", []reqOpt{withHeader("Authorization", "Bearer mk_"+strings.Repeat("0", 43))}, 401, "unauthorized", ""},
		{"cookie under other name", "GET", []reqOpt{withCookie(SessionCookieName(true), f.token)}, 204, "", ""},
		{"session POST without Origin", "POST", []reqOpt{cookie}, 403, "bad_origin", ""},
		{"session DELETE wrong Origin", "DELETE", []reqOpt{cookie, withHeader("Origin", "http://evil.example")}, 403, "bad_origin", ""},
		{"session PUT Origin with path", "PUT", []reqOpt{cookie, withHeader("Origin", testOrigin+"/")}, 403, "bad_origin", ""},
		{"session PATCH null Origin", "PATCH", []reqOpt{cookie, withHeader("Origin", "null")}, 403, "bad_origin", ""},
		{"session POST right Origin", "POST", []reqOpt{cookie, origin}, 204, "", MethodSession},
		{"session GET wrong Origin", "GET", []reqOpt{cookie, withHeader("Origin", "http://evil.example")}, 204, "", MethodSession},
		{"bearer POST without Origin", "POST", []reqOpt{bearer}, 204, "", MethodAPIKey},
		{"bearer DELETE wrong Origin", "DELETE", []reqOpt{bearer, withHeader("Origin", "http://evil.example")}, 204, "", MethodAPIKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, rec := serve(f.s, f.cfg, tt.method, tt.opts)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", w.Code, tt.wantStatus, w.Body)
			}
			if code := errorCode(w); code != tt.wantCode {
				t.Errorf("error code = %q, want %q", code, tt.wantCode)
			}
			if tt.wantStatus != 204 {
				if rec.called {
					t.Error("next handler called on rejection")
				}
				return
			}
			if tt.wantMethod == "" {
				if rec.ok {
					t.Errorf("principal = %+v, want anonymous", rec.p)
				}
				return
			}
			if !rec.ok || rec.p.Method != tt.wantMethod || rec.p.User != f.alice {
				t.Errorf("principal = %+v (ok %v), want %s for alice", rec.p, rec.ok, tt.wantMethod)
			}
			if tt.wantMethod == MethodAPIKey && rec.p.APIKeyID != f.keyID {
				t.Errorf("APIKeyID = %d, want %d", rec.p.APIKeyID, f.keyID)
			}
			if tt.wantMethod == MethodSession && rec.p.SessionID == 0 {
				t.Error("SessionID not set")
			}
		})
	}
}

func TestMiddlewareMalformedBearerSkipsDB(t *testing.T) {
	f := newFixture(t)
	f.s.db.Close() // any query would now fail with a 500
	for _, h := range []string{"Bearer nope", "Bearer " + f.secret[:45], "Token " + f.secret} {
		w, _ := serve(f.s, f.cfg, "GET", []reqOpt{withHeader("Authorization", h)})
		if w.Code != 401 {
			t.Errorf("%q: status = %d, want 401", h, w.Code)
		}
	}
	// Control: a well-formed key does reach the (closed) database.
	w, _ := serve(f.s, f.cfg, "GET", []reqOpt{withHeader("Authorization", "Bearer "+f.secret)})
	if w.Code != 500 {
		t.Errorf("well-formed key on closed db: status = %d, want 500", w.Code)
	}
}

func TestMiddlewareInvalidCookieClears(t *testing.T) {
	for _, secure := range []bool{false, true} {
		f := newFixture(t)
		f.cfg.Secure = secure
		name := SessionCookieName(secure)
		expired, _, err := f.s.CreateSession(context.Background(), f.alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{"garbage", f.token + "x"} {
			w, rec := serve(f.s, f.cfg, "GET", []reqOpt{withCookie(name, value)})
			assertCleared(t, w, rec, name)
		}
		f.clk.advance(31 * 24 * time.Hour)
		w, rec := serve(f.s, f.cfg, "GET", []reqOpt{withCookie(name, expired)})
		assertCleared(t, w, rec, name)
	}
}

func assertCleared(t *testing.T, w *httptest.ResponseRecorder, rec *recorder, name string) {
	t.Helper()
	if w.Code != 204 || rec.ok {
		t.Errorf("status %d, principal %v; want anonymous 204", w.Code, rec.ok)
	}
	c := onlyCookie(t, w)
	if c.Name != name || c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("cookie = %+v, want %s cleared", c, name)
	}
}

func onlyCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	cs := w.Result().Cookies()
	if len(cs) != 1 {
		t.Fatalf("got %d Set-Cookie, want 1: %v", len(cs), w.Header().Values("Set-Cookie"))
	}
	return cs[0]
}

func TestMiddlewareRemovedFromAllowlist(t *testing.T) {
	f := newFixture(t)
	narrow := NewService(f.s.db, mustAllowlist(t, "bob"), f.clk.now)
	for name, opt := range map[string]reqOpt{
		"session": withCookie(f.cookies, f.token),
		"key":     withHeader("Authorization", "Bearer "+f.secret),
	} {
		w, rec := serve(narrow, f.cfg, "GET", []reqOpt{opt})
		if w.Code != 403 || errorCode(w) != "not_allowed" || rec.called {
			t.Errorf("%s: status %d code %q called %v; want 403 not_allowed", name, w.Code, errorCode(w), rec.called)
		}
	}
}

func TestMiddlewareRenewsCookie(t *testing.T) {
	f := newFixture(t)
	cookie := withCookie(f.cookies, f.token)

	w, _ := serve(f.s, f.cfg, "GET", []reqOpt{cookie})
	if got := w.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("fresh session: Set-Cookie %v, want none", got)
	}

	f.clk.advance(2 * 24 * time.Hour)
	// Renewal is sent even when the request is then refused for its Origin,
	// since the stored expiry has already moved.
	for _, method := range []string{"GET", "POST"} {
		w, _ := serve(f.s, f.cfg, method, []reqOpt{cookie})
		c := onlyCookie(t, w)
		want := f.clk.t.Add(30 * 24 * time.Hour)
		if c.Name != f.cookies || c.Value != f.token || !c.Expires.Equal(want.Truncate(time.Second)) || c.MaxAge != 30*24*60*60 {
			t.Errorf("%s: renewed cookie = %+v, want expiry %v", method, c, want)
		}
		f.clk.advance(2 * 24 * time.Hour)
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	expires := time.Now().Add(30 * 24 * time.Hour)
	tests := []struct {
		secure   bool
		wantName string
	}{
		{true, "__Host-maket_session"},
		{false, "maket_session"},
	}
	for _, tt := range tests {
		if got := SessionCookieName(tt.secure); got != tt.wantName {
			t.Errorf("SessionCookieName(%v) = %q, want %q", tt.secure, got, tt.wantName)
		}
		w := httptest.NewRecorder()
		SetSessionCookie(w, "tok", expires, tt.secure)
		raw := w.Header().Get("Set-Cookie")
		c, err := http.ParseSetCookie(raw)
		if err != nil {
			t.Fatal(err)
		}
		if c.Name != tt.wantName || c.Value != "tok" || c.Path != "/" || c.Domain != "" ||
			!c.HttpOnly || c.Secure != tt.secure || c.SameSite != http.SameSiteLaxMode ||
			!c.Expires.Equal(expires.UTC().Truncate(time.Second)) ||
			c.MaxAge < 30*24*60*60-5 || c.MaxAge > 30*24*60*60 {
			t.Errorf("secure=%v: Set-Cookie %q", tt.secure, raw)
		}

		w = httptest.NewRecorder()
		ClearSessionCookie(w, tt.secure)
		raw = w.Header().Get("Set-Cookie")
		c, err = http.ParseSetCookie(raw)
		if err != nil {
			t.Fatal(err)
		}
		if c.Name != tt.wantName || c.Value != "" || c.MaxAge >= 0 || c.Path != "/" ||
			!c.HttpOnly || c.Secure != tt.secure || c.SameSite != http.SameSiteLaxMode {
			t.Errorf("secure=%v: clear Set-Cookie %q", tt.secure, raw)
		}
	}
}

func TestSecureMiddlewareUsesHostCookie(t *testing.T) {
	f := newFixture(t)
	f.cfg = MiddlewareConfig{AppOrigin: "https://app.kotakserver.my.id", Secure: true}
	_, rec := serve(f.s, f.cfg, "GET", []reqOpt{withCookie("__Host-maket_session", f.token)})
	if !rec.ok || rec.p.Method != MethodSession {
		t.Error("__Host- cookie not accepted when Secure")
	}
	_, rec = serve(f.s, f.cfg, "GET", []reqOpt{withCookie("maket_session", f.token)})
	if rec.ok {
		t.Error("plain cookie accepted when Secure")
	}
	w, rec := serve(f.s, f.cfg, "POST", []reqOpt{withCookie("__Host-maket_session", f.token), withHeader("Origin", "https://app.kotakserver.my.id")})
	if w.Code != 204 || !rec.ok {
		t.Errorf("secure POST with right Origin: status %d", w.Code)
	}
}

func TestRequire(t *testing.T) {
	f := newFixture(t)
	cookie := withCookie(f.cookies, f.token)
	bearer := withHeader("Authorization", "Bearer "+f.secret)
	tests := []struct {
		name       string
		methods    []Method
		opts       []reqOpt
		wantStatus int
		wantCode   string
	}{
		{"any: anonymous", nil, nil, 401, "unauthorized"},
		{"any: session", nil, []reqOpt{cookie}, 204, ""},
		{"any: key", nil, []reqOpt{bearer}, 204, ""},
		{"session: anonymous", []Method{MethodSession}, nil, 401, "unauthorized"},
		{"session: session", []Method{MethodSession}, []reqOpt{cookie}, 204, ""},
		{"session: key", []Method{MethodSession}, []reqOpt{bearer}, 403, "session_required"},
		{"both: key", []Method{MethodSession, MethodAPIKey}, []reqOpt{bearer}, 204, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, rec := serve(f.s, f.cfg, "GET", tt.opts, Require(tt.methods...))
			if w.Code != tt.wantStatus || errorCode(w) != tt.wantCode {
				t.Errorf("status %d code %q, want %d %q", w.Code, errorCode(w), tt.wantStatus, tt.wantCode)
			}
			if rec.called != (tt.wantStatus == 204) {
				t.Errorf("next called = %v", rec.called)
			}
		})
	}
}
