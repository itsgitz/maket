// Package db opens maket's SQLite database and applies schema migrations.
package db

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/itsgitz/maket/apps/api/migrations"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// connParams are applied by the driver to every new connection, not just the
// first, because they live in the DSN. busy_timeout comes first so the switch
// to WAL waits on a locked file instead of failing. _time_format=sqlite makes
// time.Time values be written in a format SQLite's date functions understand.
const connParams = "_pragma=busy_timeout(5000)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=foreign_keys(1)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_time_format=sqlite"

// Open opens (creating if needed) the SQLite database at path, checks it is
// reachable, and applies any pending migrations from the embedded set.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("db: empty database path")
	}
	// The driver splits a plain file name at '?', so such a path would be
	// silently truncated.
	if strings.ContainsRune(path, '?') {
		return nil, fmt.Errorf("db: database path %q must not contain '?'", path)
	}
	db, err := sql.Open("sqlite", path+"?"+connParams)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	if err := Migrate(ctx, db, migrations.FS); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrationName matches NNNN_description.sql; the number is the version.
var migrationName = regexp.MustCompile(`^([0-9]+)_([A-Za-z0-9_-]+)\.sql$`)

type migration struct {
	version int64
	name    string // file name
}

// Migrate applies every *.sql file in files that is not yet recorded in
// schema_migrations, in ascending version order. Each migration runs in its
// own transaction together with its schema_migrations row, so a failure
// leaves no trace of that migration. It refuses to run if the database has
// a version newer than any file, i.e. the binary is older than the database.
func Migrate(ctx context.Context, db *sql.DB, files fs.FS) error {
	all, err := readMigrations(files)
	if err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TIMESTAMP NOT NULL
	)`); err != nil {
		return fmt.Errorf("migrate: create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}
	var latestFile int64
	if len(all) > 0 {
		latestFile = all[len(all)-1].version
	}
	if len(applied) > 0 {
		if latestDB := slices.Max(applied); latestDB > latestFile {
			return fmt.Errorf("migrate: database is at version %d but the newest migration is %d; this binary is older than the database", latestDB, latestFile)
		}
	}

	for _, m := range all {
		if slices.Contains(applied, m.version) {
			continue
		}
		if err := apply(ctx, db, files, m); err != nil {
			return err
		}
	}
	return nil
}

// readMigrations lists and validates the migration files, sorted by version.
func readMigrations(files fs.FS) ([]migration, error) {
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("migrate: list files: %w", err)
	}
	var all []migration
	seen := map[int64]string{}
	for _, name := range names {
		match := migrationName.FindStringSubmatch(name)
		if match == nil {
			return nil, fmt.Errorf("migrate: malformed migration file name %q, want NNNN_description.sql", name)
		}
		v, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || v < 1 {
			return nil, fmt.Errorf("migrate: malformed migration file name %q: version must be a positive integer", name)
		}
		if prev, ok := seen[v]; ok {
			return nil, fmt.Errorf("migrate: duplicate migration version %d: %q and %q", v, prev, name)
		}
		seen[v] = name
		all = append(all, migration{version: v, name: name})
	}
	slices.SortFunc(all, func(a, b migration) int { return cmp.Compare(a.version, b.version) })
	return all, nil
}

func appliedVersions(ctx context.Context, db *sql.DB) ([]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("migrate: read schema_migrations: %w", err)
	}
	defer rows.Close()
	var vs []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("migrate: read schema_migrations: %w", err)
		}
		vs = append(vs, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrate: read schema_migrations: %w", err)
	}
	return vs, nil
}

func apply(ctx context.Context, db *sql.DB, files fs.FS, m migration) error {
	body, err := fs.ReadFile(files, m.name)
	if err != nil {
		return fmt.Errorf("migrate: read %s: %w", m.name, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate: %s: begin: %w", m.name, err)
	}
	defer tx.Rollback() // no-op after Commit

	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		return fmt.Errorf("migrate: %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, time.Now().UTC(),
	); err != nil {
		return fmt.Errorf("migrate: %s: record: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate: %s: commit: %w", m.name, err)
	}
	return nil
}
