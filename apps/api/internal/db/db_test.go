package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/itsgitz/maket/apps/api/migrations"
)

// openTest opens a fresh, fully migrated database in a temp dir.
func openTest(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "maket.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// openRaw opens a fresh database with the production pragmas but no
// migrations, for driving Migrate directly.
func openRaw(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "raw.db")+"?"+connParams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func tables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	return names
}

func versions(t *testing.T, db *sql.DB) []int64 {
	t.Helper()
	vs, err := appliedVersions(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(vs)
	return vs
}

func TestOpenMigratesFreshDB(t *testing.T) {
	db := openTest(t)
	got := tables(t, db)
	for _, want := range []string{"users", "sessions", "api_keys", "prototypes", "schema_migrations"} {
		if !slices.Contains(got, want) {
			t.Errorf("table %s missing; have %v", want, got)
		}
	}
	if vs := versions(t, db); !slices.Equal(vs, []int64{1}) {
		t.Errorf("applied versions = %v, want [1]", vs)
	}

	// Running again is a no-op: no error, no new rows, applied_at unchanged.
	var before string
	if err := db.QueryRow(`SELECT applied_at FROM schema_migrations WHERE version = 1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	var after string
	if err := db.QueryRow(`SELECT applied_at FROM schema_migrations WHERE version = 1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if vs := versions(t, db); !slices.Equal(vs, []int64{1}) || before != after {
		t.Errorf("second Migrate changed state: versions %v, applied_at %q -> %q", vs, before, after)
	}
}

func TestReopenExistingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maket.db")
	for i := range 2 {
		db, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}
		db.Close()
	}
}

func TestOpenRejectsQuestionMarkPath(t *testing.T) {
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "a?b.db")); err == nil {
		t.Fatal("expected error for path containing '?'")
	}
}

func TestPragmasOnEveryConnection(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	// Holding the first conn forces the pool to open a second, distinct one.
	var conns []*sql.Conn
	for range 2 {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		conns = append(conns, c)
	}

	tests := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"busy_timeout", "5000"},
		{"synchronous", "1"}, // NORMAL
	}
	for i, c := range conns {
		for _, tt := range tests {
			var got string
			if err := c.QueryRowContext(ctx, "PRAGMA "+tt.pragma).Scan(&got); err != nil {
				t.Fatalf("conn %d: PRAGMA %s: %v", i, tt.pragma, err)
			}
			if !strings.EqualFold(got, tt.want) {
				t.Errorf("conn %d: PRAGMA %s = %q, want %q", i, tt.pragma, got, tt.want)
			}
		}
	}
}

func TestForeignKeys(t *testing.T) {
	db := openTest(t)
	now := time.Now().UTC()

	if _, err := db.Exec(`INSERT INTO sessions (user_id, token_hash, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		999, []byte("orphan"), now, now); err == nil {
		t.Fatal("session for nonexistent user was accepted")
	}

	res, err := db.Exec(`INSERT INTO users (github_id, github_login, created_at) VALUES (?, ?, ?)`, 42, "octocat", now)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := res.LastInsertId()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO sessions (user_id, token_hash, expires_at, created_at) VALUES (?, ?, ?, ?)`,
			[]any{uid, []byte("tok"), now, now}},
		{`INSERT INTO api_keys (user_id, name, prefix, key_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
			[]any{uid, "ci", "mk_abc", []byte("key"), now}},
		{`INSERT INTO prototypes (user_id, slug, name, current_version, size_bytes, file_count, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{uid, "ab12cd34", "demo", 1, 10, 1, now, now}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}

	if _, err := db.Exec(`DELETE FROM users WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"sessions", "api_keys", "prototypes"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s: %d rows left after deleting user, want 0 (cascade)", table, n)
		}
	}
}

func TestMigrateAppliesInNumericOrder(t *testing.T) {
	db := openRaw(t)
	// 10 sorts before 2 lexically; 0010 depends on table created by 0002.
	files := fstest.MapFS{
		"10_c.sql":    {Data: []byte(`INSERT INTO log (step) VALUES ('ten');`)},
		"0002_b.sql":  {Data: []byte(`INSERT INTO log (step) VALUES ('two');`)},
		"0001_a.sql":  {Data: []byte(`CREATE TABLE log (n INTEGER PRIMARY KEY, step TEXT NOT NULL);`)},
		"README.md":   {Data: []byte("ignored")},
		"0003_d.sql~": {Data: []byte("ignored")},
		"sub/9_x.sql": {Data: []byte("ignored: not top level")},
	}
	if err := Migrate(context.Background(), db, files); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT step FROM log ORDER BY n`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var steps []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		steps = append(steps, s)
	}
	if want := []string{"two", "ten"}; !slices.Equal(steps, want) {
		t.Errorf("steps = %v, want %v", steps, want)
	}
	if vs := versions(t, db); !slices.Equal(vs, []int64{1, 2, 10}) {
		t.Errorf("versions = %v", vs)
	}

	// A later file is picked up on the next run; earlier ones are not re-run.
	files["0011_e.sql"] = &fstest.MapFile{Data: []byte(`INSERT INTO log (step) VALUES ('eleven');`)}
	if err := Migrate(context.Background(), db, files); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM log`).Scan(&n)
	if n != 3 {
		t.Errorf("log has %d rows after second run, want 3", n)
	}
}

func TestMigrateFailureRollsBack(t *testing.T) {
	db := openRaw(t)
	files := fstest.MapFS{
		"0001_ok.sql":  {Data: []byte(`CREATE TABLE a (x INTEGER);`)},
		"0002_bad.sql": {Data: []byte(`CREATE TABLE b (x INTEGER); INSERT INTO missing VALUES (1);`)},
	}
	err := Migrate(context.Background(), db, files)
	if err == nil || !strings.Contains(err.Error(), "0002_bad.sql") {
		t.Fatalf("err = %v, want failure naming 0002_bad.sql", err)
	}
	if vs := versions(t, db); !slices.Equal(vs, []int64{1}) {
		t.Errorf("versions = %v, want [1]", vs)
	}
	if got := tables(t, db); slices.Contains(got, "b") || !slices.Contains(got, "a") {
		t.Errorf("tables = %v, want a but not b", got)
	}
}

func TestMigrateErrors(t *testing.T) {
	tests := []struct {
		name    string
		applied fstest.MapFS // run first, must succeed
		files   fstest.MapFS
		want    string
	}{
		{
			name: "duplicate version",
			files: fstest.MapFS{
				"0001_a.sql": {Data: []byte(`SELECT 1;`)},
				"1_b.sql":    {Data: []byte(`SELECT 1;`)},
			},
			want: "duplicate migration version 1",
		},
		{
			name:  "no number",
			files: fstest.MapFS{"init.sql": {Data: []byte(`SELECT 1;`)}},
			want:  "malformed",
		},
		{
			name:  "no description",
			files: fstest.MapFS{"0001.sql": {Data: []byte(`SELECT 1;`)}},
			want:  "malformed",
		},
		{
			name:  "zero version",
			files: fstest.MapFS{"0000_a.sql": {Data: []byte(`SELECT 1;`)}},
			want:  "malformed",
		},
		{
			name: "database ahead of files",
			applied: fstest.MapFS{
				"0001_a.sql": {Data: []byte(`SELECT 1;`)},
				"0002_b.sql": {Data: []byte(`SELECT 1;`)},
			},
			files: fstest.MapFS{"0001_a.sql": {Data: []byte(`SELECT 1;`)}},
			want:  "older than the database",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openRaw(t)
			if tt.applied != nil {
				if err := Migrate(context.Background(), db, tt.applied); err != nil {
					t.Fatal(err)
				}
			}
			err := Migrate(context.Background(), db, tt.files)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
