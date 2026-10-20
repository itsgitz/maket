// Package apitypes holds the JSON bodies of maket's HTTP API that clients
// read: the server encodes them and the maket CLI decodes them, so the two
// cannot drift apart.
package apitypes

import "time"

// Me is the body of GET /api/me.
type Me struct {
	ID          int64  `json:"id"`
	GitHubLogin string `json:"github_login"`
	Name        string `json:"name"`
	AvatarURL   string `json:"avatar_url"`
	// AuthMethod is "session" or "api_key".
	AuthMethod string `json:"auth_method"`
}

// Prototype is the body of GET, POST and PUT on /api/prototypes, and one
// entry of PrototypeList.
type Prototype struct {
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	URL       string    `json:"url"` // Preview URL
	SPA       bool      `json:"spa"`
	Version   int       `json:"version"` // current Version, from 1
	SizeBytes int64     `json:"size_bytes"`
	FileCount int       `json:"file_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PrototypeList is the body of GET /api/prototypes: the User's Prototypes,
// most recently updated first, and their Quota usage.
type PrototypeList struct {
	Prototypes []Prototype `json:"prototypes"`
	Usage      Usage       `json:"usage"`
}

// Usage is a User's storage against their Quota.
type Usage struct {
	UsedBytes  int64 `json:"used_bytes"`
	QuotaBytes int64 `json:"quota_bytes"`
}

// ErrorResponse is the body of every API error.
type ErrorResponse struct {
	Error Error `json:"error"`
}

// Error describes a failed request. Code is stable and machine-readable;
// Message is for humans.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details is set for rejected uploads with offending files.
	Details *ErrorDetails `json:"details,omitempty"`
}

// ErrorDetails lists the files that made an Upload fail.
type ErrorDetails struct {
	Paths []string `json:"paths"`
	More  int      `json:"more"` // offending files not listed in Paths
}
