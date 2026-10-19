// Package oauth is the home of maket's browser login providers, one
// subpackage each (github today; google may follow).
//
// The login handlers rely on every provider having the same shape:
//
//	// AuthCodeURL is where to send the browser to start login; state is
//	// echoed back on the callback.
//	AuthCodeURL(state string) string
//	// Exchange trades the callback's code for the signed-in account.
//	Exchange(ctx context.Context, code string) (User, error)
//
// User is provider-specific (it carries the provider's own account ID), so
// there is no shared interface yet; add one here when a second provider
// lands and the handlers need to treat them alike. Providers request no more
// than the public profile and never return or keep the access token.
package oauth
