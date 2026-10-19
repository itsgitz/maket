package storage

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"
	"time"
)

// tmpDir holds LocalStore's in-progress writes. ValidKey rejects keys whose
// first segment starts with ".tmp", so no key can reach it.
const tmpDir = ".tmp"

// LocalStore is a Storage on the local disk. Each key is a file path below
// one directory, and all access goes through an os.Root, so symlinks and
// ".." can never reach outside that directory.
type LocalStore struct {
	root *os.Root
}

var _ Storage = (*LocalStore)(nil)

// NewLocal returns a LocalStore keeping its files in dir, creating dir if
// it does not exist.
func NewLocal(dir string) (*LocalStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("storage: create %s: %w", dir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", dir, err)
	}
	return &LocalStore{root: root}, nil
}

// Close releases the store's directory.
func (s *LocalStore) Close() error {
	return s.root.Close()
}

// Put writes r to a temp file inside the store, syncs it, and renames it
// over key, so readers never see a partial file.
func (s *LocalStore) Put(ctx context.Context, key string, r io.Reader) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidKey(key); err != nil {
		return err
	}
	if err := s.root.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("storage: put %s: %w", key, err)
	}
	tmp := path.Join(tmpDir, rand.Text())
	f, err := s.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("storage: put %s: %w", key, err)
	}
	defer func() {
		if err != nil {
			f.Close()
			s.root.Remove(tmp)
		}
	}()
	if _, err := io.Copy(f, ctxReader{ctx, r}); err != nil {
		return fmt.Errorf("storage: put %s: %w", key, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("storage: put %s: %w", key, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("storage: put %s: %w", key, err)
	}
	dir := path.Dir(key)
	// A concurrent DeletePrefix may prune the parent directory between
	// MkdirAll and Rename, so retry once if it vanished.
	for attempt := 0; ; attempt++ {
		if err = s.root.MkdirAll(dir, 0o755); err == nil {
			err = s.root.Rename(tmp, key)
		}
		if err == nil || attempt == 1 || !errors.Is(err, fs.ErrNotExist) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("storage: put %s: %w", key, err)
	}
	s.syncDir(dir)
	return nil
}

// syncDir makes a rename into dir durable. Failure is not reported: the
// file itself is already complete and in place.
func (s *LocalStore) syncDir(dir string) {
	if d, err := s.root.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// Open opens the file at key.
func (s *LocalStore) Open(ctx context.Context, key string) (Object, error) {
	if err := ctx.Err(); err != nil {
		return Object{}, err
	}
	if err := ValidKey(key); err != nil {
		return Object{}, err
	}
	f, err := s.root.Open(key)
	if err != nil {
		return Object{}, notFound(key, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return Object{}, fmt.Errorf("storage: open %s: %w", key, err)
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return Object{}, fmt.Errorf("storage: open %s: %w", key, ErrNotFound)
	}
	return Object{ReadSeekCloser: f, Size: info.Size(), ModTime: info.ModTime()}, nil
}

// notFound wraps err for key, adding ErrNotFound when the file is missing
// or a parent path is a file rather than a directory.
func notFound(key string, err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return fmt.Errorf("storage: open %s: %w (%w)", key, ErrNotFound, err)
	}
	return fmt.Errorf("storage: open %s: %w", key, err)
}

// List walks the directory for prefix. Symlinks and other non-regular files
// are skipped. Objects are collected and sorted before yielding, because
// directory order is not key order ("a.txt" sorts before "a/b").
func (s *LocalStore) List(ctx context.Context, prefix string) iter.Seq2[ObjectInfo, error] {
	return func(yield func(ObjectInfo, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(ObjectInfo{}, err)
			return
		}
		if err := ValidPrefix(prefix); err != nil {
			yield(ObjectInfo{}, err)
			return
		}
		var objs []ObjectInfo
		if err := s.walk(ctx, strings.TrimSuffix(prefix, "/"), &objs); err != nil {
			yield(ObjectInfo{}, fmt.Errorf("storage: list %s: %w", prefix, err))
			return
		}
		slices.SortFunc(objs, func(a, b ObjectInfo) int { return strings.Compare(a.Key, b.Key) })
		for _, o := range objs {
			if !yield(o, nil) {
				return
			}
		}
	}
}

// walk appends every regular file below dir to objs. A missing dir, or a
// dir that is a file, holds nothing.
func (s *LocalStore) walk(ctx context.Context, dir string, objs *[]ObjectInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d, err := s.root.Open(dir)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := d.ReadDir(-1)
	d.Close()
	if errors.Is(err, syscall.ENOTDIR) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := dir + "/" + e.Name()
		switch {
		case e.IsDir():
			if err := s.walk(ctx, name, objs); err != nil {
				return err
			}
		case e.Type().IsRegular():
			info, err := e.Info()
			if errors.Is(err, fs.ErrNotExist) {
				continue // deleted while listing
			}
			if err != nil {
				return err
			}
			*objs = append(*objs, ObjectInfo{Key: name, Size: info.Size(), ModTime: info.ModTime()})
		}
	}
	return nil
}

// DeletePrefix removes the directory for prefix, then removes parent
// directories left empty, up to but not including the store's root.
func (s *LocalStore) DeletePrefix(ctx context.Context, prefix string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidPrefix(prefix); err != nil {
		return err
	}
	dir := strings.TrimSuffix(prefix, "/")
	// Only a real directory holds objects under prefix: if dir is the file
	// for key "a", prefix "a/" matches nothing and "a" must survive.
	info, err := s.root.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("storage: delete %s: %w", prefix, err)
	}
	if !info.IsDir() {
		return nil
	}
	if err := s.root.RemoveAll(dir); err != nil {
		return fmt.Errorf("storage: delete %s: %w", prefix, err)
	}
	// Remove only deletes empty directories, so stop at the first one that
	// still holds something (or is already gone).
	for parent := path.Dir(dir); parent != "."; parent = path.Dir(parent) {
		if s.root.Remove(parent) != nil {
			break
		}
	}
	return nil
}

// CleanTemp removes temp files last modified more than olderThan ago. They
// are left behind only when the process dies during a Put.
func (s *LocalStore) CleanTemp(olderThan time.Duration) error {
	d, err := s.root.Open(tmpDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("storage: clean temp: %w", err)
	}
	entries, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return fmt.Errorf("storage: clean temp: %w", err)
	}
	cutoff := time.Now().Add(-olderThan)
	var errs []error
	for _, e := range entries {
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue // a Put finished or failed meanwhile
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := s.root.RemoveAll(path.Join(tmpDir, e.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("storage: clean temp: %w", err)
	}
	return nil
}

// ctxReader stops a copy once ctx is canceled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
