// Package storage keeps Prototype files behind one interface, so the local
// disk and S3-compatible services are interchangeable.
//
// Keys are relative, "/"-separated paths such as
// "prototypes/<slug>/v<version>/<path>". There are no directories: a prefix
// is just the leading part of a key, and it always ends in "/".
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"
	"time"
	"unicode/utf8"
)

// Storage stores objects by key. Implementations are safe for concurrent use.
type Storage interface {
	// Put creates or replaces the object at key with the contents of r.
	// Readers see either the old object or the complete new one, never a
	// partial write. If r fails, no object is created and an existing one is
	// left unchanged.
	Put(ctx context.Context, key string, r io.Reader) error

	// Open returns the object at key, or an error wrapping ErrNotFound.
	// The caller must Close it.
	Open(ctx context.Context, key string) (Object, error)

	// List yields every object whose key starts with prefix, at any depth,
	// in lexical key order. An error ends the sequence.
	List(ctx context.Context, prefix string) iter.Seq2[ObjectInfo, error]

	// DeletePrefix deletes every object whose key starts with prefix.
	// Deleting nothing is not an error.
	DeletePrefix(ctx context.Context, prefix string) error
}

// Object is an open object. It is seekable so it can be passed to
// http.ServeContent, which needs that for range requests.
type Object struct {
	io.ReadSeekCloser
	Size    int64
	ModTime time.Time
}

// ObjectInfo describes an object returned by List.
type ObjectInfo struct {
	Key     string
	Size    int64
	ModTime time.Time
}

var (
	// ErrNotFound reports that no object exists at a key.
	ErrNotFound = errors.New("storage: object not found")
	// ErrInvalidKey reports a malformed key or prefix.
	ErrInvalidKey = errors.New("storage: invalid key")
)

// reservedPrefix starts the first segment of names an implementation may use
// for its own bookkeeping, such as LocalStore's ".tmp" directory. Keys may
// not begin with it.
const reservedPrefix = ".tmp"

// ValidKey reports whether key is a well-formed key: non-empty, relative,
// "/"-separated and already clean (no empty, "." or ".." segments, no
// leading or trailing "/"), valid UTF-8 without backslashes or control
// characters, and with a first segment that does not start with ".tmp".
// The error wraps ErrInvalidKey.
func ValidKey(key string) error {
	if reason := keyProblem(key); reason != "" {
		return fmt.Errorf("%w %q: %s", ErrInvalidKey, key, reason)
	}
	return nil
}

// ValidPrefix reports whether prefix is a valid key followed by "/". So a
// prefix is never empty, and "prototypes/ab/" cannot match keys under
// "prototypes/abc/". The error wraps ErrInvalidKey.
func ValidPrefix(prefix string) error {
	key, ok := strings.CutSuffix(prefix, "/")
	if !ok {
		return fmt.Errorf("%w: prefix %q must end in /", ErrInvalidKey, prefix)
	}
	if reason := keyProblem(key); reason != "" {
		return fmt.Errorf("%w: prefix %q: %s", ErrInvalidKey, prefix, reason)
	}
	return nil
}

// keyProblem describes what is wrong with key, or returns "" if nothing is.
func keyProblem(key string) string {
	if key == "" {
		return "empty"
	}
	if !utf8.ValidString(key) {
		return "not valid UTF-8"
	}
	for _, c := range key {
		if c < 0x20 || c == 0x7f {
			return "contains a control character"
		}
		if c == '\\' {
			return "contains a backslash"
		}
	}
	for i, seg := range strings.Split(key, "/") {
		switch {
		case seg == "":
			return "empty segment (leading, trailing or double /)"
		case seg == "." || seg == "..":
			return "contains a . or .. segment"
		case i == 0 && strings.HasPrefix(seg, reservedPrefix):
			return "first segment starts with the reserved " + reservedPrefix
		}
	}
	return ""
}
