package storage_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/storage"
	"github.com/itsgitz/maket/apps/api/internal/storage/storagetest"
)

// TestS3Conformance runs the storagetest suite against a real S3-compatible
// server. It is skipped unless MAKET_TEST_S3_ENDPOINT is set, along with
// MAKET_TEST_S3_BUCKET, MAKET_TEST_S3_ACCESS_KEY_ID and
// MAKET_TEST_S3_SECRET_ACCESS_KEY (MAKET_TEST_S3_REGION is optional).
//
// The bucket must already exist. Each subtest writes under its own random
// key prefix and deletes that prefix when it ends, so runs do not collide
// with each other or with other objects in the bucket.
func TestS3Conformance(t *testing.T) {
	cfg := liveS3Config(t)
	storagetest.Run(t, func(t *testing.T) storage.Storage {
		inner, err := storage.NewS3(cfg)
		if err != nil {
			t.Fatal(err)
		}
		s := &prefixedStore{Storage: inner, prefix: "maket-test-" + rand.Text() + "/"}
		t.Cleanup(func() {
			if err := inner.DeletePrefix(context.Background(), s.prefix); err != nil {
				t.Errorf("cleanup %s: %v", s.prefix, err)
			}
		})
		return s
	})
}

// liveS3Config reads the test server settings, skipping the test when the
// endpoint is not set.
func liveS3Config(t *testing.T) storage.S3Config {
	t.Helper()
	endpoint := os.Getenv("MAKET_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("MAKET_TEST_S3_ENDPOINT not set")
	}
	cfg := storage.S3Config{
		Endpoint:        endpoint,
		Bucket:          os.Getenv("MAKET_TEST_S3_BUCKET"),
		Region:          os.Getenv("MAKET_TEST_S3_REGION"),
		AccessKeyID:     os.Getenv("MAKET_TEST_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("MAKET_TEST_S3_SECRET_ACCESS_KEY"),
	}
	if cfg.Bucket == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		t.Fatal("MAKET_TEST_S3_BUCKET, MAKET_TEST_S3_ACCESS_KEY_ID and MAKET_TEST_S3_SECRET_ACCESS_KEY must be set with MAKET_TEST_S3_ENDPOINT")
	}
	return cfg
}

// TestS3Multipart checks an object larger than one part, which the suite
// does not reach, and that a failed upload leaves no object behind.
func TestS3Multipart(t *testing.T) {
	cfg := liveS3Config(t)
	inner, err := storage.NewS3(cfg)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "maket-test-" + rand.Text() + "/"
	t.Cleanup(func() { inner.DeletePrefix(context.Background(), prefix) })

	big := make([]byte, 12<<20+123) // three parts: 5 MiB, 5 MiB, the rest
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	if err := inner.Put(t.Context(), prefix+"big.bin", bytes.NewReader(big)); err != nil {
		t.Fatalf("multipart Put: %v", err)
	}
	obj, err := inner.Open(t.Context(), prefix+"big.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close()
	if obj.Size != int64(len(big)) {
		t.Fatalf("Size = %d, want %d", obj.Size, len(big))
	}
	// Seek into the second part and read through the end, as a Range request would.
	if _, err := obj.Seek(6<<20, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(obj)
	if err != nil || !bytes.Equal(got, big[6<<20:]) {
		t.Fatalf("read from 6 MiB: %d bytes, %v; content mismatch", len(got), err)
	}

	// A reader that fails after a full part must not leave an object.
	failing := io.MultiReader(bytes.NewReader(big[:6<<20]), iotestErr{})
	if err := inner.Put(t.Context(), prefix+"partial.bin", failing); err == nil {
		t.Fatal("Put with a failing reader succeeded")
	}
	if _, err := inner.Open(t.Context(), prefix+"partial.bin"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Open after failed Put = %v, want ErrNotFound", err)
	}
}

// iotestErr is a reader that always fails.
type iotestErr struct{}

func (iotestErr) Read([]byte) (int, error) { return 0, errors.New("boom") }

// prefixedStore puts every key under prefix in the wrapped store. It checks
// keys and prefixes itself first, so the suite's invalid-key cases still
// fail the same way they would on a bare store.
type prefixedStore struct {
	storage.Storage
	prefix string
}

func (p *prefixedStore) Put(ctx context.Context, key string, r io.Reader) error {
	if err := storage.ValidKey(key); err != nil {
		return err
	}
	return p.Storage.Put(ctx, p.prefix+key, r)
}

func (p *prefixedStore) Open(ctx context.Context, key string) (storage.Object, error) {
	if err := storage.ValidKey(key); err != nil {
		return storage.Object{}, err
	}
	return p.Storage.Open(ctx, p.prefix+key)
}

func (p *prefixedStore) List(ctx context.Context, prefix string) iter.Seq2[storage.ObjectInfo, error] {
	return func(yield func(storage.ObjectInfo, error) bool) {
		if err := storage.ValidPrefix(prefix); err != nil {
			yield(storage.ObjectInfo{}, err)
			return
		}
		for info, err := range p.Storage.List(ctx, p.prefix+prefix) {
			if err == nil {
				info.Key = strings.TrimPrefix(info.Key, p.prefix)
			}
			if !yield(info, err) {
				return
			}
		}
	}
}

func (p *prefixedStore) DeletePrefix(ctx context.Context, prefix string) error {
	if err := storage.ValidPrefix(prefix); err != nil {
		return err
	}
	return p.Storage.DeletePrefix(ctx, p.prefix+prefix)
}

// fakeS3 serves just enough of path-style S3 for the reader: HEAD and ranged
// or full GET on objects kept in memory. It records every GET's Range header.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string]string // "bucket/key" → content
	gets    []string          // Range header of each GET ("" for none)
	srv     *httptest.Server
}

func newFakeS3(t *testing.T, objects map[string]string) *fakeS3 {
	t.Helper()
	f := &fakeS3{objects: objects}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// store returns an S3Store pointed at the fake, with bucket "b".
func (f *fakeS3) store(t *testing.T) *storage.S3Store {
	t.Helper()
	s, err := storage.NewS3(storage.S3Config{
		Endpoint: f.srv.URL, Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fakeS3) getRanges() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.gets...)
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	content, ok := f.objects[strings.TrimPrefix(r.URL.Path, "/")]
	if !ok {
		// HEAD has no body. GET gets the S3 NoSuchKey error document.
		w.WriteHeader(http.StatusNotFound)
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
		}
		return
	}
	w.Header().Set("Last-Modified", time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).Format(http.TimeFormat))
	w.Header().Set("Content-Length", fmt.Sprint(len(content)))
	if r.Method == http.MethodHead {
		return
	}
	rng := r.Header.Get("Range")
	f.gets = append(f.gets, rng)
	var from int
	if _, err := fmt.Sscanf(rng, "bytes=%d-", &from); err == nil && rng != "" {
		if from >= len(content) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(content)-1, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		fmt.Fprint(w, content[from:])
		return
	}
	fmt.Fprint(w, content)
}

func TestS3OpenMissingIsNotFound(t *testing.T) {
	f := newFakeS3(t, map[string]string{"b/prototypes/abc/v1/index.html": "x"})
	s := f.store(t)
	for _, key := range []string{"prototypes/abc/v1/missing.html", "prototypes/abc/v1"} {
		obj, err := s.Open(t.Context(), key)
		if err == nil {
			obj.Close()
		}
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("Open(%q) = %v, want ErrNotFound", key, err)
		}
	}
}

func TestS3ReaderLazyRanges(t *testing.T) {
	f := newFakeS3(t, map[string]string{"b/k": "0123456789"})
	obj, err := f.store(t).Open(t.Context(), "k")
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close()
	if obj.Size != 10 {
		t.Fatalf("Size = %d, want 10", obj.Size)
	}
	if n := len(f.getRanges()); n != 0 {
		t.Fatalf("Open issued %d GETs, want none", n)
	}

	buf := make([]byte, 3)
	if _, err := io.ReadFull(obj, buf); err != nil || string(buf) != "012" {
		t.Fatalf("first read = %q, %v", buf, err)
	}
	// Seeking alone must not touch the network.
	if pos, err := obj.Seek(5, io.SeekStart); err != nil || pos != 5 {
		t.Fatalf("Seek = %d, %v", pos, err)
	}
	if n := len(f.getRanges()); n != 1 {
		t.Fatalf("after first read and seek: %d GETs, want 1", n)
	}
	if pos, err := obj.Seek(-2, io.SeekEnd); err != nil || pos != 8 {
		t.Fatalf("Seek end = %d, %v", pos, err)
	}
	rest, err := io.ReadAll(obj)
	if err != nil || string(rest) != "89" {
		t.Fatalf("rest = %q, %v", rest, err)
	}
	if n, err := obj.Read(make([]byte, 4)); n != 0 || err != io.EOF {
		t.Errorf("Read at end = %d, %v; want 0, EOF", n, err)
	}

	// The seek to 5 sent nothing: the next read happens after seeking to 8.
	want := []string{"bytes=0-", "bytes=8-"}
	if got := f.getRanges(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("GET ranges = %q, want %q", got, want)
	}
}

func TestS3ReaderSeekWithinOpenBodyKeepsIt(t *testing.T) {
	f := newFakeS3(t, map[string]string{"b/k": "abcdef"})
	obj, err := f.store(t).Open(t.Context(), "k")
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close()
	buf := make([]byte, 2)
	io.ReadFull(obj, buf)
	// Seeking to the current position is a no-op and keeps the body.
	if _, err := obj.Seek(0, io.SeekCurrent); err != nil {
		t.Fatal(err)
	}
	if rest, err := io.ReadAll(obj); err != nil || string(rest) != "cdef" {
		t.Fatalf("rest = %q, %v", rest, err)
	}
	if n := len(f.getRanges()); n != 1 {
		t.Errorf("GETs = %d, want 1", n)
	}
}

func TestS3ReaderEmptyObjectNoGet(t *testing.T) {
	f := newFakeS3(t, map[string]string{"b/empty": ""})
	obj, err := f.store(t).Open(t.Context(), "empty")
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close()
	if b, err := io.ReadAll(obj); err != nil || len(b) != 0 {
		t.Fatalf("ReadAll = %q, %v", b, err)
	}
	// "bytes=0-" on an empty object is an invalid range, so none is sent.
	if n := len(f.getRanges()); n != 0 {
		t.Errorf("GETs = %d, want 0", n)
	}
}

func TestS3ReaderObjectDeletedAfterOpen(t *testing.T) {
	f := newFakeS3(t, map[string]string{"b/k": "data"})
	obj, err := f.store(t).Open(t.Context(), "k")
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close()
	f.mu.Lock()
	delete(f.objects, "b/k")
	f.mu.Unlock()
	if _, err := io.ReadAll(obj); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("read after delete = %v, want ErrNotFound", err)
	}
}

func TestS3ReaderClosed(t *testing.T) {
	f := newFakeS3(t, map[string]string{"b/k": "data"})
	obj, err := f.store(t).Open(t.Context(), "k")
	if err != nil {
		t.Fatal(err)
	}
	obj.Close()
	if _, err := obj.Read(make([]byte, 1)); err == nil {
		t.Error("Read after Close succeeded")
	}
	if _, err := obj.Seek(0, io.SeekStart); err == nil {
		t.Error("Seek after Close succeeded")
	}
}

func TestS3ReaderNegativeSeek(t *testing.T) {
	f := newFakeS3(t, map[string]string{"b/k": "data"})
	obj, err := f.store(t).Open(t.Context(), "k")
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close()
	if _, err := obj.Seek(-1, io.SeekStart); err == nil {
		t.Error("Seek to -1 succeeded")
	}
}

// Range requests through http.ServeContent, as preview serving does.
func TestS3ServeContentRange(t *testing.T) {
	f := newFakeS3(t, map[string]string{"b/video.bin": "0123456789"})
	obj, err := f.store(t).Open(t.Context(), "video.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close()

	req := httptest.NewRequest(http.MethodGet, "/video.bin", nil)
	req.Header.Set("Range", "bytes=2-4")
	rec := httptest.NewRecorder()
	http.ServeContent(rec, req, "video.bin", obj.ModTime, obj)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "234" {
		t.Errorf("range response = %d %q, want 206 \"234\"", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/video.bin", nil)
	rec = httptest.NewRecorder()
	http.ServeContent(rec, req, "video.bin", obj.ModTime, obj)
	if rec.Code != http.StatusOK || rec.Body.String() != "0123456789" {
		t.Errorf("full response = %d %q, want 200 full body", rec.Code, rec.Body.String())
	}
}

func TestS3NewRequiresFields(t *testing.T) {
	if _, err := storage.NewS3(storage.S3Config{Endpoint: "http://x", Bucket: "b", AccessKeyID: "k"}); err == nil {
		t.Error("NewS3 without a secret succeeded")
	}
}

// bucketServer answers HeadBucket for bucket "b" with status, and any other
// bucket with 404.
func bucketServer(t *testing.T, status int) *storage.S3Store {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && r.URL.Path == "/b" && status != http.StatusNotFound {
			w.WriteHeader(status)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	s, err := storage.NewS3(storage.S3Config{
		Endpoint: srv.URL, Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestS3CheckBucket(t *testing.T) {
	if err := bucketServer(t, http.StatusOK).Check(t.Context()); err != nil {
		t.Errorf("Check on an existing bucket: %v", err)
	}
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		err := bucketServer(t, status).Check(t.Context())
		if err == nil {
			t.Errorf("Check with status %d succeeded", status)
			continue
		}
		if !strings.Contains(err.Error(), `cannot access bucket "b"`) {
			t.Errorf("Check error = %q, want the bucket named", err)
		}
	}
}
