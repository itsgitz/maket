package prototype

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/preview"
)

// Cleanup removes leftovers older than olderThan:
//
//   - Version files that are not a Prototype's current Version (an old
//     Version whose delete failed, or a crash mid-Upload)
//   - files of Prototypes that no longer exist
//   - the store's stale temp files and stale Upload extraction directories
//
// The age check keeps it from touching an Upload that is still running.
func (s *Service) Cleanup(ctx context.Context, olderThan time.Duration) error {
	var errs []error
	if err := s.cleanVersions(ctx, olderThan); err != nil {
		errs = append(errs, err)
	}
	if c, ok := s.store.(interface{ CleanTemp(time.Duration) error }); ok {
		if err := c.CleanTemp(olderThan); err != nil {
			errs = append(errs, err)
		}
	}
	if err := cleanDir(s.cfg.TmpDir, s.now().Add(-olderThan)); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Service) cleanVersions(ctx context.Context, olderThan time.Duration) error {
	current := map[string]int{} // slug → current Version
	rows, err := s.db.QueryContext(ctx, `SELECT slug, current_version FROM prototypes`)
	if err != nil {
		return fmt.Errorf("prototype: cleanup: %w", err)
	}
	for rows.Next() {
		var slug string
		var v int
		if err := rows.Scan(&slug, &v); err != nil {
			rows.Close()
			return fmt.Errorf("prototype: cleanup: %w", err)
		}
		current[slug] = v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("prototype: cleanup: %w", err)
	}

	cutoff := s.now().Add(-olderThan)
	stale := map[string]bool{}  // version prefixes to delete
	recent := map[string]bool{} // version prefixes with a fresh file: maybe an Upload in progress
	for obj, err := range s.store.List(ctx, "prototypes/") {
		if err != nil {
			return fmt.Errorf("prototype: cleanup: %w", err)
		}
		slug, version, ok := parseVersionKey(obj.Key)
		if !ok {
			continue
		}
		if v, live := current[slug]; live && v == version {
			continue
		}
		prefix := preview.VersionPrefix(slug, version)
		if obj.ModTime.After(cutoff) {
			recent[prefix] = true
		} else {
			stale[prefix] = true
		}
	}

	var errs []error
	for prefix := range stale {
		if recent[prefix] {
			continue
		}
		if err := s.store.DeletePrefix(ctx, prefix); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// parseVersionKey splits "prototypes/<slug>/v<n>/<path>".
func parseVersionKey(key string) (slug string, version int, ok bool) {
	parts := strings.SplitN(key, "/", 4)
	if len(parts) != 4 || parts[0] != "prototypes" || !preview.ValidSlug(parts[1]) || !strings.HasPrefix(parts[2], "v") {
		return "", 0, false
	}
	v, err := strconv.Atoi(parts[2][1:])
	if err != nil || v < 1 {
		return "", 0, false
	}
	return parts[1], v, true
}

// cleanDir removes entries of dir last modified before cutoff.
func cleanDir(dir string, cutoff time.Time) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("prototype: cleanup %s: %w", dir, err)
	}
	var errs []error
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
