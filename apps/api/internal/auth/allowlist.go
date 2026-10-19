package auth

import (
	"errors"
	"strings"
)

// Allowlist is the set of GitHub usernames permitted to use maket, compared
// case-insensitively.
type Allowlist struct {
	logins map[string]struct{}
}

// NewAllowlist trims and lowercases logins and drops empty ones. It is an
// error if none remain.
func NewAllowlist(logins []string) (Allowlist, error) {
	set := map[string]struct{}{}
	for _, l := range logins {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" {
			set[l] = struct{}{}
		}
	}
	if len(set) == 0 {
		return Allowlist{}, errors.New("auth: allowlist is empty")
	}
	return Allowlist{logins: set}, nil
}

// Allows reports whether login is on the list.
func (a Allowlist) Allows(login string) bool {
	_, ok := a.logins[strings.ToLower(strings.TrimSpace(login))]
	return ok
}
