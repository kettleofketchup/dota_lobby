// Package store is the service's SQLite database: one file, opened once by
// the process that holds the state-dir lock, with embedded migrations
// applied on Open. Repositories for each table group hang off *Store.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	// Pure-Go SQLite driver, registered as "sqlite"; the build stays CGO-free.
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrSchemaTooNew means the database was migrated by a newer binary than
// this one. Running an older binary against it could corrupt data, so Open
// refuses.
var ErrSchemaTooNew = errors.New("store: database schema is newer than this binary")

// timeLayout is how timestamps are stored: RFC 3339 in UTC with all nine
// fraction digits. The fixed width makes text order time order, which the
// queries rely on; time.RFC3339Nano drops trailing zeros, so "10.1Z" would
// sort after "10.12Z". Reads accept any RFC 3339 time, so rows written
// before the fixed width still parse.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// Store is an open database. It is safe for concurrent use.
type Store struct {
	db   *sql.DB
	path string
	log  *slog.Logger
}

// Option configures Open.
type Option func(*options)

type options struct {
	migrations fs.FS
	backup     *backupOptions
	log        *slog.Logger
}

type backupOptions struct {
	dir  string
	keep int
}

// withMigrations replaces the embedded migrations, for tests.
func withMigrations(fsys fs.FS) Option {
	return func(o *options) { o.migrations = fsys }
}

// WithPreMigrateBackup makes Open copy an existing database into dir
// before it applies any pending migration. Once every migration has been
// applied, only the newest keep of those copies are kept; after a failed
// migration none is deleted. A fresh database, or one with nothing to
// migrate, is not copied. When the copy fails, Open fails with
// ErrBackupFailed and applies nothing.
func WithPreMigrateBackup(dir string, keep int) Option {
	return func(o *options) { o.backup = &backupOptions{dir: dir, keep: keep} }
}

// WithLogger sets the logger for what Open does on its own, such as a
// pre-migration backup. Nil discards.
func WithLogger(log *slog.Logger) Option {
	return func(o *options) { o.log = log }
}

// Open opens or creates the database at path, owner-only (0600), and
// applies any pending migrations. The parent directory is created 0700 if
// it is missing.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	o := options{}
	if sub, err := fs.Sub(migrationFS, "migrations"); err == nil {
		o.migrations = sub
	}
	for _, opt := range opts {
		opt(&o)
	}
	if o.log == nil {
		o.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.backup != nil && (o.backup.dir == "" || o.backup.keep < 1) {
		return nil, fmt.Errorf("open store: pre-migration backup needs a directory and keep >= 1 (got %q, %d)", o.backup.dir, o.backup.keep)
	}
	if path == "" {
		return nil, errors.New("open store: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	// Create the file ourselves so it is 0600 from the start; SQLite gives
	// its -wal and -shm files the database file's mode.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	db, err := openDSN(path, q)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	// One connection serialises every statement. The state-dir lock already
	// limits the database to one process, and the load is a handful of
	// writes a minute.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path, log: o.log}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open store %s: %w", path, err)
	}
	if err := s.migrate(ctx, o.migrations, o.backup); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open store %s: %w", path, err)
	}
	return s, nil
}

// openDSN opens path with the given URI query. SQLite reads the DSN as a
// URI: the path is escaped so '?', '#' and '%' in it name that file, not a
// different one. A URI path must be absolute.
func openDSN(path string, q url.Values) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: q.Encode()}).String()
	return sql.Open("sqlite", dsn)
}

// Close closes the database.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close store: %w", err)
	}
	return nil
}

// DB is the underlying handle, for the repositories in this package and
// their tests.
func (s *Store) DB() *sql.DB { return s.db }

// Path is the database file.
func (s *Store) Path() string { return s.path }

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads NNNN_name.sql files in version order. Versions must
// be unique and start at 1 without gaps.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		num, _, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("migration %s: name must be NNNN_name.sql", name)
		}
		v, err := strconv.Atoi(num)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %s: bad version %q", name, num)
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		out = append(out, migration{version: v, name: name, sql: string(body)})
	}
	slices.SortFunc(out, func(a, b migration) int { return a.version - b.version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration %s: expected version %d", m.name, i+1)
		}
	}
	return out, nil
}

// migrate applies every migration newer than the database, each in its own
// transaction together with its schema_migrations row. With backup set, an
// existing database is copied first, and old copies are pruned only after
// every migration has been applied.
func (s *Store) migrate(ctx context.Context, fsys fs.FS, backup *backupOptions) error {
	ms, err := loadMigrations(fsys)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current > len(ms) {
		return fmt.Errorf("%w: database at version %d, binary knows %d", ErrSchemaTooNew, current, len(ms))
	}
	backedUp := backup != nil && current > 0 && current < len(ms)
	if backedUp {
		if err := s.preMigrateBackup(ctx, *backup, current, len(ms)); err != nil {
			return err
		}
	}
	for _, m := range ms[current:] {
		if err := s.apply(ctx, m); err != nil {
			return err
		}
	}
	if backedUp {
		s.pruneAutoBackups(*backup)
	}
	return nil
}

func (s *Store) apply(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		m.version, formatTime(time.Now())); err != nil {
		return fmt.Errorf("migration %s: record version: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration %s: commit: %w", m.name, err)
	}
	return nil
}

// SchemaVersion is the highest applied migration.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return v, nil
}

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s sql.NullString) (time.Time, error) {
	if !s.Valid || s.String == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s.String, err)
	}
	return t, nil
}
