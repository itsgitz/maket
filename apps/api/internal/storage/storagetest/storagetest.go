// Package storagetest is a conformance suite every storage.Storage
// implementation must pass.
package storagetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/itsgitz/maket/apps/api/internal/storage"
)

// Run runs the suite. newStore must return an empty store; it is called
// once per subtest and is responsible for its own cleanup. Subtests run in
// parallel, each on its own store.
func Run(t *testing.T, newStore func(t *testing.T) storage.Storage) {
	tests := []struct {
		name string
		fn   func(*testing.T, storage.Storage)
	}{
		{"PutOpen", testPutOpen},
		{"Overwrite", testOverwrite},
		{"Seek", testSeek},
		{"OpenMissing", testOpenMissing},
		{"List", testList},
		{"ListEmpty", testListEmpty},
		{"ListStopEarly", testListStopEarly},
		{"DeletePrefix", testDeletePrefix},
		{"InvalidKeys", testInvalidKeys},
		{"CanceledContext", testCanceledContext},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.fn(t, newStore(t))
		})
	}
}

// Put stores content at key or fails the test.
func Put(t *testing.T, s storage.Storage, key, content string) {
	t.Helper()
	if err := s.Put(t.Context(), key, strings.NewReader(content)); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
}

// Read returns the content at key or fails the test.
func Read(t *testing.T, s storage.Storage, key string) string {
	t.Helper()
	obj, err := s.Open(t.Context(), key)
	if err != nil {
		t.Fatalf("Open(%q): %v", key, err)
	}
	defer obj.Close()
	b, err := io.ReadAll(obj)
	if err != nil {
		t.Fatalf("read %q: %v", key, err)
	}
	if int64(len(b)) != obj.Size {
		t.Errorf("Open(%q).Size = %d, read %d bytes", key, obj.Size, len(b))
	}
	return string(b)
}

// Keys lists prefix and returns the keys, failing the test on error.
func Keys(t *testing.T, s storage.Storage, prefix string) []string {
	t.Helper()
	var keys []string
	for info, err := range s.List(t.Context(), prefix) {
		if err != nil {
			t.Fatalf("List(%q): %v", prefix, err)
		}
		keys = append(keys, info.Key)
	}
	return keys
}

func testPutOpen(t *testing.T, s storage.Storage) {
	const key, content = "prototypes/ab12cd34/v1/index.html", "<h1>hello</h1>"
	Put(t, s, key, content)
	obj, err := s.Open(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close()
	if obj.Size != int64(len(content)) {
		t.Errorf("Size = %d, want %d", obj.Size, len(content))
	}
	if obj.ModTime.IsZero() {
		t.Error("ModTime is zero")
	}
	if got, err := io.ReadAll(obj); err != nil || string(got) != content {
		t.Errorf("content = %q, %v; want %q", got, err, content)
	}

	// Empty and binary objects round-trip too.
	Put(t, s, "prototypes/ab12cd34/v1/empty.txt", "")
	if got := Read(t, s, "prototypes/ab12cd34/v1/empty.txt"); got != "" {
		t.Errorf("empty object = %q", got)
	}
	bin := string(bytes.Repeat([]byte{0, 1, 2, 0xff}, 50_000))
	Put(t, s, "prototypes/ab12cd34/v1/a.bin", bin)
	if got := Read(t, s, "prototypes/ab12cd34/v1/a.bin"); got != bin {
		t.Errorf("binary object differs (len %d, want %d)", len(got), len(bin))
	}
}

func testOverwrite(t *testing.T, s storage.Storage) {
	const key = "prototypes/ab12cd34/v1/index.html"
	Put(t, s, key, "a much longer first version")
	Put(t, s, key, "second")
	if got := Read(t, s, key); got != "second" {
		t.Errorf("after overwrite = %q, want %q", got, "second")
	}
	if got := Keys(t, s, "prototypes/"); !slices.Equal(got, []string{key}) {
		t.Errorf("List after overwrite = %q", got)
	}
}

func testSeek(t *testing.T, s storage.Storage) {
	const key = "prototypes/ab12cd34/v1/video.mp4"
	Put(t, s, key, "0123456789")
	obj, err := s.Open(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close()

	if pos, err := obj.Seek(3, io.SeekStart); err != nil || pos != 3 {
		t.Fatalf("Seek(3, start) = %d, %v", pos, err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(obj, buf); err != nil || string(buf) != "3456" {
		t.Errorf("read after seek = %q, %v; want %q", buf, err, "3456")
	}
	if pos, err := obj.Seek(-2, io.SeekEnd); err != nil || pos != 8 {
		t.Fatalf("Seek(-2, end) = %d, %v", pos, err)
	}
	if rest, err := io.ReadAll(obj); err != nil || string(rest) != "89" {
		t.Errorf("read after seek from end = %q, %v; want %q", rest, err, "89")
	}
	if pos, err := obj.Seek(0, io.SeekStart); err != nil || pos != 0 {
		t.Fatalf("Seek(0, start) = %d, %v", pos, err)
	}
	if all, err := io.ReadAll(obj); err != nil || string(all) != "0123456789" {
		t.Errorf("read after rewind = %q, %v", all, err)
	}
}

func testOpenMissing(t *testing.T, s storage.Storage) {
	Put(t, s, "prototypes/ab12cd34/v1/index.html", "x")
	for _, key := range []string{
		"prototypes/ab12cd34/v1/missing.html",
		"prototypes/zz99zz99/v1/index.html",
		"prototypes/ab12cd34/v1",                  // a prefix, not an object
		"prototypes/ab12cd34/v1/index.html/inner", // below an object
	} {
		obj, err := s.Open(t.Context(), key)
		if err == nil {
			obj.Close()
		}
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("Open(%q) error = %v, want ErrNotFound", key, err)
		}
	}
}

func testList(t *testing.T, s storage.Storage) {
	content := map[string]string{
		"prototypes/abc/v1/index.html":      "index",
		"prototypes/abc/v1/a.txt":           "a",
		"prototypes/abc/v1/a/b.css":         "bb",
		"prototypes/abc/v1/assets/x/y.js":   "yyy",
		"prototypes/abc/v2/index.html":      "v2",
		"prototypes/abcd/v1/index.html":     "sibling",
		"prototypes/ab/v1/index.html":       "shorter sibling",
		"prototypes/abc.html":               "not under prototypes/abc/",
		"other/prototypes/abc/v1/index.htm": "elsewhere",
	}
	for k, v := range content {
		Put(t, s, k, v)
	}
	tests := []struct {
		prefix string
		want   []string
	}{
		{"prototypes/abc/v1/", []string{
			"prototypes/abc/v1/a.txt",
			"prototypes/abc/v1/a/b.css",
			"prototypes/abc/v1/assets/x/y.js",
			"prototypes/abc/v1/index.html",
		}},
		{"prototypes/abc/", []string{
			"prototypes/abc/v1/a.txt",
			"prototypes/abc/v1/a/b.css",
			"prototypes/abc/v1/assets/x/y.js",
			"prototypes/abc/v1/index.html",
			"prototypes/abc/v2/index.html",
		}},
		{"prototypes/abcd/", []string{"prototypes/abcd/v1/index.html"}},
		{"prototypes/abc/v1/assets/x/", []string{"prototypes/abc/v1/assets/x/y.js"}},
	}
	for _, tt := range tests {
		var got []string
		for info, err := range s.List(t.Context(), tt.prefix) {
			if err != nil {
				t.Fatalf("List(%q): %v", tt.prefix, err)
			}
			got = append(got, info.Key)
			if want := int64(len(content[info.Key])); info.Size != want {
				t.Errorf("List(%q): %s Size = %d, want %d", tt.prefix, info.Key, info.Size, want)
			}
			if info.ModTime.IsZero() {
				t.Errorf("List(%q): %s ModTime is zero", tt.prefix, info.Key)
			}
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("List(%q) = %q\nwant %q", tt.prefix, got, tt.want)
		}
	}
}

func testListEmpty(t *testing.T, s storage.Storage) {
	if got := Keys(t, s, "prototypes/"); len(got) != 0 {
		t.Errorf("List on empty store = %q", got)
	}
	Put(t, s, "prototypes/abc/v1/index.html", "x")
	for _, prefix := range []string{
		"prototypes/zzz/",
		"prototypes/ab/",
		"prototypes/abc/v1/index.html/", // an object, not a prefix
	} {
		if got := Keys(t, s, prefix); len(got) != 0 {
			t.Errorf("List(%q) = %q, want nothing", prefix, got)
		}
	}
}

func testListStopEarly(t *testing.T, s storage.Storage) {
	for _, k := range []string{"p/a", "p/b", "p/c"} {
		Put(t, s, k, k)
	}
	var got []string
	for info, err := range s.List(t.Context(), "p/") {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, info.Key)
		break
	}
	if !slices.Equal(got, []string{"p/a"}) {
		t.Errorf("first listed = %q, want [p/a]", got)
	}
}

func testDeletePrefix(t *testing.T, s storage.Storage) {
	keys := []string{
		"prototypes/abc/v1/index.html",
		"prototypes/abc/v1/assets/app.js",
		"prototypes/abc/v2/index.html",
		"prototypes/abcd/v1/index.html",
		"prototypes/abc.html",
	}
	for _, k := range keys {
		Put(t, s, k, k)
	}
	for range 2 { // the second delete finds nothing and must not fail
		if err := s.DeletePrefix(t.Context(), "prototypes/abc/v1/"); err != nil {
			t.Fatalf("DeletePrefix: %v", err)
		}
	}
	want := []string{
		"prototypes/abc.html",
		"prototypes/abc/v2/index.html",
		"prototypes/abcd/v1/index.html",
	}
	if got := Keys(t, s, "prototypes/"); !slices.Equal(got, want) {
		t.Errorf("after DeletePrefix = %q\nwant %q", got, want)
	}
	if _, err := s.Open(t.Context(), keys[0]); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Open deleted key: %v, want ErrNotFound", err)
	}

	// A prefix naming an object deletes nothing: "x/" does not match "x".
	if err := s.DeletePrefix(t.Context(), "prototypes/abc.html/"); err != nil {
		t.Fatalf("DeletePrefix on object path: %v", err)
	}
	if got := Read(t, s, "prototypes/abc.html"); got != "prototypes/abc.html" {
		t.Errorf("object deleted through prefix %q", "prototypes/abc.html/")
	}

	// The store stays usable for the deleted prefix.
	Put(t, s, keys[0], "again")
	if got := Read(t, s, keys[0]); got != "again" {
		t.Errorf("re-put after delete = %q", got)
	}
}

// badKeys are rejected as keys; badPrefixes as prefixes.
var (
	badKeys = []string{
		"", "/", "/a", "a/", "a//b", "a/./b", "a/../b", ".", "..", "./a", "a/..",
		`a\b`, "a\x00b", "a\nb", "a\x7fb", "a\xffb",
		".tmp", ".tmp/x", ".tmpfile/x",
	}
	badPrefixes = []string{
		"", "/", "a", "prototypes/abc", "a//", "/a/", "a/../", "../", ".tmp/", `a\b/`,
	}
)

func testInvalidKeys(t *testing.T, s storage.Storage) {
	ctx := t.Context()
	for _, key := range badKeys {
		if err := storage.ValidKey(key); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("ValidKey(%q) = %v, want ErrInvalidKey", key, err)
		}
		if err := s.Put(ctx, key, strings.NewReader("x")); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("Put(%q) = %v, want ErrInvalidKey", key, err)
		}
		obj, err := s.Open(ctx, key)
		if err == nil {
			obj.Close()
		}
		if !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("Open(%q) = %v, want ErrInvalidKey", key, err)
		}
	}
	for _, prefix := range append(badPrefixes, badKeys...) {
		if strings.HasSuffix(prefix, "/") && !slices.Contains(badPrefixes, prefix) {
			continue // a bad key plus "/" may be a good prefix, e.g. "a/"
		}
		if err := storage.ValidPrefix(prefix); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("ValidPrefix(%q) = %v, want ErrInvalidKey", prefix, err)
		}
		if err := s.DeletePrefix(ctx, prefix); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("DeletePrefix(%q) = %v, want ErrInvalidKey", prefix, err)
		}
		if err := listErr(ctx, s, prefix); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("List(%q) error = %v, want ErrInvalidKey", prefix, err)
		}
	}
	for _, key := range []string{"a", "index.html", "prototypes/ab12cd34/v1/assets/app.js", "a/.tmp", ".hidden/x", "a b/ü.txt", "..a/b..", "a/.../b"} {
		if err := storage.ValidKey(key); err != nil {
			t.Errorf("ValidKey(%q) = %v, want nil", key, err)
		}
	}
	for _, prefix := range []string{"a/", "prototypes/", "prototypes/ab12cd34/v1/"} {
		if err := storage.ValidPrefix(prefix); err != nil {
			t.Errorf("ValidPrefix(%q) = %v, want nil", prefix, err)
		}
	}
}

// listErr returns the first error List yields, or nil.
func listErr(ctx context.Context, s storage.Storage, prefix string) error {
	for _, err := range s.List(ctx, prefix) {
		if err != nil {
			return err
		}
	}
	return nil
}

func testCanceledContext(t *testing.T, s storage.Storage) {
	const key = "prototypes/abc/v1/index.html"
	Put(t, s, key, "x")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := s.Put(ctx, "prototypes/abc/v1/new.html", strings.NewReader("y")); !errors.Is(err, context.Canceled) {
		t.Errorf("Put = %v, want context.Canceled", err)
	}
	obj, err := s.Open(ctx, key)
	if err == nil {
		obj.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Open = %v, want context.Canceled", err)
	}
	if err := listErr(ctx, s, "prototypes/"); !errors.Is(err, context.Canceled) {
		t.Errorf("List = %v, want context.Canceled", err)
	}
	if err := s.DeletePrefix(ctx, "prototypes/"); !errors.Is(err, context.Canceled) {
		t.Errorf("DeletePrefix = %v, want context.Canceled", err)
	}
	if got := Keys(t, s, "prototypes/"); !slices.Equal(got, []string{key}) {
		t.Errorf("canceled calls changed the store: %q", got)
	}
}
