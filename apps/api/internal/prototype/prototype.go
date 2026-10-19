// Package prototype manages Prototypes: Uploads create Versions in storage,
// the database records each Prototype's current Version, and old Versions
// are deleted once a new one is in place.
package prototype

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/itsgitz/maket/apps/api/internal/auth"
	"github.com/itsgitz/maket/apps/api/internal/preview"
	"github.com/itsgitz/maket/apps/api/internal/prototype/archive"
	"github.com/itsgitz/maket/apps/api/internal/storage"
)

// Prototype is one User's uploaded static frontend.
type Prototype struct {
	ID        int64
	UserID    int64
	Slug      string
	Name      string
	SPA       bool
	Version   int // current Version
	SizeBytes int64
	FileCount int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Upload is the input for creating a Prototype or a new Version of one.
type Upload struct {
	Filename string      // client file name; decides .html vs .zip
	File     io.ReaderAt // the uploaded bytes
	Size     int64
	Name     *string // nil: derive (new) or keep (existing)
	SPA      *bool   // nil: off (new) or keep (existing)
}

var (
	// ErrNotFound means no Prototype with that Slug belongs to the User.
	ErrNotFound = errors.New("prototype: not found")
	// ErrQuotaExceeded means the Upload would take the User over their Quota.
	ErrQuotaExceeded = errors.New("prototype: quota exceeded")
	// ErrInvalidName means an explicit name was empty or too long.
	ErrInvalidName = errors.New("prototype: name must be 1 to 100 characters")
)

// maxNameLen is the longest Prototype name, in characters.
const maxNameLen = 100

// Purger clears a Preview host's cached files from a CDN.
type Purger interface {
	Purge(ctx context.Context, host string, paths []string) error
}

// Config holds the Service's limits and environment.
type Config struct {
	Limits      archive.Limits
	QuotaBytes  int64
	TmpDir      string                   // scratch space for extracting Uploads
	PreviewHost func(slug string) string // e.g. "ab12cd34.cloudshare.my.id"
}

// Service creates, replaces, lists and deletes Prototypes.
type Service struct {
	db     *sql.DB
	store  storage.Storage
	allow  auth.Allowlist
	cfg    Config
	purger Purger // nil disables purging
	now    func() time.Time

	// userLocks serializes writes per User, so Quota checks and Version
	// numbers can't race. maket runs as a single process.
	mu        sync.Mutex
	userLocks map[int64]*sync.Mutex
}

// NewService returns a Service. purger may be nil.
func NewService(db *sql.DB, store storage.Storage, allow auth.Allowlist, cfg Config, purger Purger, now func() time.Time) *Service {
	return &Service{db: db, store: store, allow: allow, cfg: cfg, purger: purger, now: now, userLocks: map[int64]*sync.Mutex{}}
}

func (s *Service) lockUser(userID int64) func() {
	s.mu.Lock()
	l, ok := s.userLocks[userID]
	if !ok {
		l = &sync.Mutex{}
		s.userLocks[userID] = l
	}
	s.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Create stores up as Version 1 of a new Prototype.
func (s *Service) Create(ctx context.Context, userID int64, up Upload) (Prototype, error) {
	name, err := newName(up)
	if err != nil {
		return Prototype{}, err
	}
	res, err := archive.Extract(up.Filename, up.File, up.Size, s.cfg.Limits, s.cfg.TmpDir)
	if err != nil {
		return Prototype{}, err
	}
	defer res.Cleanup()

	defer s.lockUser(userID)()
	used, err := s.Usage(ctx, userID)
	if err != nil {
		return Prototype{}, err
	}
	if used+res.TotalSize > s.cfg.QuotaBytes {
		return Prototype{}, ErrQuotaExceeded
	}

	slug, err := s.freeSlug(ctx)
	if err != nil {
		return Prototype{}, err
	}
	if err := s.putVersion(ctx, slug, 1, res); err != nil {
		return Prototype{}, err
	}
	now := s.now().UTC()
	p := Prototype{
		UserID: userID, Slug: slug, Name: name, SPA: up.SPA != nil && *up.SPA, Version: 1,
		SizeBytes: res.TotalSize, FileCount: len(res.Files), CreatedAt: now, UpdatedAt: now,
	}
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO prototypes (user_id, slug, name, spa, current_version, size_bytes, file_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		p.UserID, p.Slug, p.Name, p.SPA, p.Version, p.SizeBytes, p.FileCount, p.CreatedAt, p.UpdatedAt,
	).Scan(&p.ID)
	if err != nil {
		s.store.DeletePrefix(context.WithoutCancel(ctx), slugPrefix(slug))
		return Prototype{}, fmt.Errorf("prototype: create: %w", err)
	}
	return p, nil
}

// Replace stores up as a new Version of the User's Prototype slug, then
// deletes the previous Version. Omitted name and SPA settings are kept.
func (s *Service) Replace(ctx context.Context, userID int64, slug string, up Upload) (Prototype, error) {
	if up.Name != nil {
		if _, err := cleanName(*up.Name); err != nil {
			return Prototype{}, err
		}
	}
	if _, err := s.Get(ctx, userID, slug); err != nil {
		return Prototype{}, err // don't extract for a Slug the User can't touch
	}
	res, err := archive.Extract(up.Filename, up.File, up.Size, s.cfg.Limits, s.cfg.TmpDir)
	if err != nil {
		return Prototype{}, err
	}
	defer res.Cleanup()

	defer s.lockUser(userID)()
	old, err := s.Get(ctx, userID, slug) // re-read under the lock
	if err != nil {
		return Prototype{}, err
	}
	used, err := s.Usage(ctx, userID)
	if err != nil {
		return Prototype{}, err
	}
	if used-old.SizeBytes+res.TotalSize > s.cfg.QuotaBytes {
		return Prototype{}, ErrQuotaExceeded
	}

	p := old
	p.Version = old.Version + 1
	p.SizeBytes = res.TotalSize
	p.FileCount = len(res.Files)
	p.UpdatedAt = s.now().UTC()
	if up.Name != nil {
		p.Name, _ = cleanName(*up.Name)
	}
	if up.SPA != nil {
		p.SPA = *up.SPA
	}

	// A crashed earlier attempt may have left files at this Version.
	if err := s.store.DeletePrefix(ctx, preview.VersionPrefix(slug, p.Version)); err != nil {
		return Prototype{}, err
	}
	if err := s.putVersion(ctx, slug, p.Version, res); err != nil {
		return Prototype{}, err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE prototypes SET name = ?, spa = ?, current_version = ?, size_bytes = ?, file_count = ?, updated_at = ?
		WHERE id = ? AND current_version = ?`,
		p.Name, p.SPA, p.Version, p.SizeBytes, p.FileCount, p.UpdatedAt, p.ID, old.Version)
	if err != nil {
		s.store.DeletePrefix(context.WithoutCancel(ctx), preview.VersionPrefix(slug, p.Version))
		return Prototype{}, fmt.Errorf("prototype: replace: %w", err)
	}

	// The new Version is live; the old one goes, best effort (the cleanup
	// job catches anything left behind).
	ctx = context.WithoutCancel(ctx)
	paths := s.servedPaths(ctx, preview.VersionPrefix(slug, old.Version))
	if err := s.store.DeletePrefix(ctx, preview.VersionPrefix(slug, old.Version)); err != nil {
		slog.Warn("prototype: delete old version", "slug", slug, "version", old.Version, "err", err)
	}
	s.purge(slug, paths)
	return p, nil
}

// Delete removes the User's Prototype slug and all its files.
func (s *Service) Delete(ctx context.Context, userID int64, slug string) error {
	defer s.lockUser(userID)()
	p, err := s.Get(ctx, userID, slug)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM prototypes WHERE id = ?`, p.ID); err != nil {
		return fmt.Errorf("prototype: delete: %w", err)
	}
	ctx = context.WithoutCancel(ctx)
	paths := s.servedPaths(ctx, preview.VersionPrefix(slug, p.Version))
	if err := s.store.DeletePrefix(ctx, slugPrefix(slug)); err != nil {
		slog.Warn("prototype: delete files", "slug", slug, "err", err)
	}
	s.purge(slug, paths)
	return nil
}

const selectPrototype = `SELECT id, user_id, slug, name, spa, current_version, size_bytes, file_count, created_at, updated_at FROM prototypes`

func scanPrototype(row interface{ Scan(...any) error }) (Prototype, error) {
	var p Prototype
	err := row.Scan(&p.ID, &p.UserID, &p.Slug, &p.Name, &p.SPA, &p.Version, &p.SizeBytes, &p.FileCount, &p.CreatedAt, &p.UpdatedAt)
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return p, err
}

// Get returns the User's Prototype slug.
func (s *Service) Get(ctx context.Context, userID int64, slug string) (Prototype, error) {
	if !preview.ValidSlug(slug) {
		return Prototype{}, ErrNotFound
	}
	p, err := scanPrototype(s.db.QueryRowContext(ctx, selectPrototype+` WHERE slug = ? AND user_id = ?`, slug, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return Prototype{}, ErrNotFound
	}
	if err != nil {
		return Prototype{}, fmt.Errorf("prototype: get: %w", err)
	}
	return p, nil
}

// List returns the User's Prototypes, most recently updated first.
func (s *Service) List(ctx context.Context, userID int64) ([]Prototype, error) {
	rows, err := s.db.QueryContext(ctx, selectPrototype+` WHERE user_id = ? ORDER BY updated_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("prototype: list: %w", err)
	}
	defer rows.Close()
	out := []Prototype{}
	for rows.Next() {
		p, err := scanPrototype(rows)
		if err != nil {
			return nil, fmt.Errorf("prototype: list: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Usage is the total size of the User's current Versions, which is what the
// Quota limits.
func (s *Service) Usage(ctx context.Context, userID int64) (int64, error) {
	var used int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(size_bytes), 0) FROM prototypes WHERE user_id = ?`, userID).Scan(&used)
	if err != nil {
		return 0, fmt.Errorf("prototype: usage: %w", err)
	}
	return used, nil
}

// QuotaBytes is each User's Quota.
func (s *Service) QuotaBytes() int64 { return s.cfg.QuotaBytes }

// LookupPreview implements preview.Lookup. Prototypes whose owner is no
// longer on the Allowlist are not served.
func (s *Service) LookupPreview(ctx context.Context, slug string) (preview.Site, error) {
	var site preview.Site
	var login string
	err := s.db.QueryRowContext(ctx, `
		SELECT p.current_version, p.spa, u.github_login
		FROM prototypes p JOIN users u ON u.id = p.user_id
		WHERE p.slug = ?`, slug).Scan(&site.Version, &site.SPA, &login)
	if errors.Is(err, sql.ErrNoRows) {
		return preview.Site{}, preview.ErrNotFound
	}
	if err != nil {
		return preview.Site{}, fmt.Errorf("prototype: lookup preview: %w", err)
	}
	if !s.allow.Allows(login) {
		return preview.Site{}, preview.ErrNotFound
	}
	site.Slug = slug
	return site, nil
}

// putVersion copies every extracted file into storage, several at a time
// (each Put fsyncs, so one-by-one is slow for large zips). On failure the
// partial Version is removed.
func (s *Service) putVersion(ctx context.Context, slug string, version int, res *archive.Result) error {
	const workers = 8
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	files := make(chan archive.File)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for f := range files {
				if err := s.putFile(ctx, slug, version, res, f); err != nil {
					cancel(err)
				}
			}
		})
	}
	for _, f := range res.Files {
		select {
		case files <- f:
		case <-ctx.Done():
		}
	}
	close(files)
	wg.Wait()

	if err := context.Cause(ctx); err != nil {
		s.store.DeletePrefix(context.WithoutCancel(ctx), preview.VersionPrefix(slug, version))
		return fmt.Errorf("prototype: store files: %w", err)
	}
	return nil
}

func (s *Service) putFile(ctx context.Context, slug string, version int, res *archive.Result, f archive.File) error {
	if ctx.Err() != nil {
		return nil // another file already failed
	}
	src, err := res.Open(f)
	if err != nil {
		return err
	}
	defer src.Close()
	return s.store.Put(ctx, preview.FileKey(slug, version, f.Path), src)
}

// servedPaths lists the URL paths a Version answers to: every file, plus
// the directory path for each index.html. Errors just shorten the list.
func (s *Service) servedPaths(ctx context.Context, prefix string) []string {
	var paths []string
	for obj, err := range s.store.List(ctx, prefix) {
		if err != nil {
			break
		}
		p := "/" + strings.TrimPrefix(obj.Key, prefix)
		paths = append(paths, p)
		if path.Base(p) == "index.html" {
			paths = append(paths, strings.TrimSuffix(p, "index.html"))
		}
	}
	return paths
}

// purge clears the Preview's CDN cache in the background, best effort.
func (s *Service) purge(slug string, paths []string) {
	if s.purger == nil {
		return
	}
	host := s.cfg.PreviewHost(slug)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.purger.Purge(ctx, host, paths); err != nil {
			slog.Warn("prototype: purge cache", "host", host, "err", err)
		}
	}()
}

// freeSlug picks a random Slug not used by any Prototype.
func (s *Service) freeSlug(ctx context.Context) (string, error) {
	for range 5 {
		slug := preview.NewSlug()
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM prototypes WHERE slug = ?`, slug).Scan(&n); err != nil {
			return "", fmt.Errorf("prototype: pick slug: %w", err)
		}
		if n == 0 {
			return slug, nil
		}
	}
	return "", errors.New("prototype: could not find a free slug")
}

func slugPrefix(slug string) string { return "prototypes/" + slug + "/" }

// newName is the name for a new Prototype: the explicit one, else the file
// name without its extension, else "Untitled".
func newName(up Upload) (string, error) {
	if up.Name != nil {
		return cleanName(*up.Name)
	}
	base := path.Base(strings.ReplaceAll(up.Filename, `\`, "/"))
	base = strings.TrimSpace(strings.TrimSuffix(base, path.Ext(base)))
	if base == "" || base == "." || base == "/" {
		return "Untitled", nil
	}
	if r := []rune(base); len(r) > maxNameLen {
		base = string(r[:maxNameLen])
	}
	return base, nil
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxNameLen {
		return "", ErrInvalidName
	}
	return name, nil
}
