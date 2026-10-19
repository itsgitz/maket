package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/itsgitz/maket/apps/api/internal/jsonapi"
)

// MiddlewareConfig is what Middleware needs to know about the app.
type MiddlewareConfig struct {
	AppOrigin string // e.g. "https://app.kotakserver.my.id"
	Secure    bool   // true when the public scheme is https
}

// Middleware resolves the bearer key or session cookie, attaches the Principal
// to the request context, applies the Origin check, and renews the cookie
// when sliding. Requests with no credential pass through anonymously.
//
// An Authorization header, if present at all, is the only credential
// considered; anything but a well-formed "Bearer mk_..." is 401. An invalid or
// expired session cookie is cleared and the request treated as anonymous.
// Session-authenticated unsafe methods need Origin to equal cfg.AppOrigin.
func (s *Service) Middleware(cfg MiddlewareConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if authz, ok := r.Header["Authorization"]; ok {
				secret, ok := bearerKey(authz)
				if !ok {
					unauthorized(w)
					return
				}
				p, err := s.ResolveAPIKey(ctx, secret)
				if err != nil {
					authError(w, err)
					return
				}
				next.ServeHTTP(w, r.WithContext(WithPrincipal(ctx, p)))
				return
			}

			c, err := r.Cookie(SessionCookieName(cfg.Secure))
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			p, renewed, err := s.ResolveSession(ctx, c.Value)
			if errors.Is(err, ErrUnauthenticated) {
				ClearSessionCookie(w, cfg.Secure)
				next.ServeHTTP(w, r)
				return
			}
			if err != nil {
				authError(w, err)
				return
			}
			// Set before the Origin check: the database expiry has already
			// moved, and the cookie must follow it.
			if !renewed.IsZero() {
				setSessionCookie(w, c.Value, renewed, s.clock(), cfg.Secure)
			}
			if unsafeMethod(r.Method) && r.Header.Get("Origin") != cfg.AppOrigin {
				jsonapi.Error(w, http.StatusForbidden, "bad_origin", "request origin does not match the app")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(ctx, p)))
		})
	}
}

// Require rejects requests without a Principal (401 unauthorized) or whose
// Method is not in methods (403 session_required). With no methods given, any
// Method is accepted.
func Require(methods ...Method) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				unauthorized(w)
				return
			}
			if len(methods) > 0 && !slices.Contains(methods, p.Method) {
				jsonapi.Error(w, http.StatusForbidden, "session_required", "this endpoint needs a browser session, not an API key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bearerKey extracts the key from exactly one "Bearer mk_..." header value,
// checking its shape so malformed keys never reach the database.
func bearerKey(values []string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	key, ok := strings.CutPrefix(values[0], "Bearer ")
	if !ok || !wellFormedAPIKey(key) {
		return "", false
	}
	return key, true
}

func unsafeMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func unauthorized(w http.ResponseWriter) {
	jsonapi.Error(w, http.StatusUnauthorized, "unauthorized", "missing or invalid credentials")
}

// authError writes the response for a Resolve* error.
func authError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		unauthorized(w)
	case errors.Is(err, ErrNotAllowed):
		jsonapi.Error(w, http.StatusForbidden, "not_allowed", "this GitHub account is not allowed to use maket")
	default:
		slog.Error("auth: resolve credential", "err", err)
		jsonapi.Error(w, http.StatusInternalServerError, "internal", "internal error")
	}
}
