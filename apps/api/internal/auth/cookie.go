package auth

import (
	"net/http"
	"time"
)

// SessionCookieName is "__Host-maket_session" over https. Browsers reject the
// __Host- prefix without Secure, so plain-http development uses
// "maket_session".
func SessionCookieName(secure bool) string {
	if secure {
		return "__Host-maket_session"
	}
	return "maket_session"
}

// SetSessionCookie sets the host-only session cookie, expiring with the
// session.
func SetSessionCookie(w http.ResponseWriter, token string, expires time.Time, secure bool) {
	setSessionCookie(w, token, expires, time.Now(), secure)
}

// setSessionCookie is SetSessionCookie with MaxAge measured from now.
func setSessionCookie(w http.ResponseWriter, token string, expires, now time.Time, secure bool) {
	maxAge := int(expires.Sub(now).Seconds())
	if maxAge <= 0 {
		maxAge = -1 // already expired: delete rather than make it a browser-session cookie
	}
	http.SetCookie(w, sessionCookie(token, expires, maxAge, secure))
}

// ClearSessionCookie tells the browser to delete the session cookie.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, sessionCookie("", time.Unix(0, 0), -1, secure))
}

func sessionCookie(value string, expires time.Time, maxAge int, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookieName(secure),
		Value:    value,
		Path:     "/",
		Expires:  expires.UTC(),
		MaxAge:   maxAge,
		Secure:   secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}
