package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// ErrBackupFailed means the copy taken before a migration could not be
// written or did not verify. Open then applies no migration: the
// migrations cannot be undone, so the service does not run them without a
// way back.
var ErrBackupFailed = errors.New("refusing to migrate: pre-migration backup failed")

// backupTimeLayout names backup files. Every backup name ends in
// -<time>.db with this fixed-width time, which pruning sorts on.
const backupTimeLayout = "20060102T150405Z"

var (
	// autoBackupName matches the files the pre-migration backup writes:
	// dota_lobby-<from>-to-<to>-<time>.db.
	autoBackupName = regexp.MustCompile(`^dota_lobby-\d{4}-to-\d{4}-\d{8}T\d{6}Z\.db$`)
	// manualBackupName matches ManualBackupName.
	manualBackupName = regexp.MustCompile(`^dota_lobby-manual-\d{8}T\d{6}Z\.db$`)
)

// ManualBackupName is the file name an on-demand backup taken at t gets
// when it is told only a directory.
func ManualBackupName(t time.Time) string {
	return "dota_lobby-manual-" + t.UTC().Format(backupTimeLayout) + ".db"
}

func autoBackupFile(from, to int, t time.Time) string {
	return fmt.Sprintf("dota_lobby-%04d-to-%04d-%s.db", from, to, t.UTC().Format(backupTimeLayout))
}

// BackupInfo describes a written backup.
type BackupInfo struct {
	Path  string
	Bytes int64
}

// OpenForBackup opens an existing database read-only, applying no
// migration and taking no lock, so a backup can run next to the serving
// process. A missing file is an error rather than a new empty database.
func OpenForBackup(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("open store for backup: empty path")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("open store for backup: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("open store for backup: %s is not a regular file", path)
	}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	db, err := openDSN(path, q)
	if err != nil {
		return nil, fmt.Errorf("open store for backup: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open store for backup %s: %w", path, err)
	}
	return &Store{db: db, path: path}, nil
}

// Backup writes a consistent copy of the database to out and checks it.
// out must not exist. The copy is a snapshot of one read transaction, so
// the service may keep writing while it runs.
func (s *Store) Backup(ctx context.Context, out string) (BackupInfo, error) {
	if err := vacuumInto(ctx, s.db, out); err != nil {
		return BackupInfo{}, err
	}
	if err := verifyBackup(ctx, out, -1); err != nil {
		return BackupInfo{}, err
	}
	fi, err := os.Stat(out)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("backup: %w", err)
	}
	return BackupInfo{Path: out, Bytes: fi.Size()}, nil
}

// vacuumInto copies the database to out with VACUUM INTO. It reserves out
// first (refusing one that exists), writes to out.partial, syncs it and
// renames it into place, so out is either complete or absent. Both files
// are created 0600 before SQLite writes to them; SQLite accepts an empty
// existing file and would otherwise create it with the umask's mode.
func vacuumInto(ctx context.Context, db *sql.DB, out string) (err error) {
	reserve, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if err := reserve.Close(); err != nil {
		_ = os.Remove(out)
		return fmt.Errorf("backup: %w", err)
	}
	partial := out + ".partial"
	defer func() {
		if err != nil {
			_ = os.Remove(partial)
			_ = os.Remove(out)
		}
	}()
	pf, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if err := pf.Close(); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, partial); err != nil {
		return fmt.Errorf("backup: vacuum into %s: %w", partial, err)
	}
	if err := syncFile(partial); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if err := os.Rename(partial, out); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if err := syncFile(filepath.Dir(out)); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	return nil
}

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync %s: %w", path, err)
	}
	return f.Close()
}

// verifyBackup opens the copy read-only and runs PRAGMA quick_check. With
// wantVersion >= 0 it also checks the copy's schema version.
func verifyBackup(ctx context.Context, path string, wantVersion int) error {
	q := url.Values{}
	q.Set("mode", "ro")
	// The copy is a closed file nobody else opens: immutable skips locking
	// and the WAL, so the check leaves no -wal or -shm file behind.
	q.Set("immutable", "1")
	db, err := openDSN(path, q)
	if err != nil {
		return fmt.Errorf("verify backup: %w", err)
	}
	defer func() { _ = db.Close() }()
	var result string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&result); err != nil {
		return fmt.Errorf("verify backup %s: %w", path, err)
	}
	if result != "ok" {
		return fmt.Errorf("verify backup %s: quick_check: %s", path, result)
	}
	if wantVersion < 0 {
		return nil
	}
	var v int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return fmt.Errorf("verify backup %s: read schema version: %w", path, err)
	}
	if v != wantVersion {
		return fmt.Errorf("verify backup %s: schema version %d, want %d", path, v, wantVersion)
	}
	return nil
}

// preMigrateBackup copies the database at version from before migrations
// up to version to run and verifies the copy. It deletes nothing: old
// copies are pruned only once the migrations have succeeded
// (pruneAutoBackups), so a migration that fails on every restart cannot
// prune away the copy taken before the upgrade.
func (s *Store) preMigrateBackup(ctx context.Context, b backupOptions, from, to int) error {
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return fmt.Errorf("%w: %w", ErrBackupFailed, err)
	}
	out := filepath.Join(b.dir, autoBackupFile(from, to, time.Now()))
	if err := vacuumInto(ctx, s.db, out); err != nil {
		return fmt.Errorf("%w: %w", ErrBackupFailed, err)
	}
	if err := verifyBackup(ctx, out, from); err != nil {
		return fmt.Errorf("%w: %w", ErrBackupFailed, err)
	}
	var size int64
	if fi, err := os.Stat(out); err == nil {
		size = fi.Size()
	}
	s.log.Info("pre-migration backup written", "path", out, "from", from, "to", to, "bytes", size)
	return nil
}

// pruneAutoBackups keeps the newest b.keep automatic backups. A failure is
// only logged.
func (s *Store) pruneAutoBackups(b backupOptions) {
	if err := pruneBackups(b.dir, autoBackupName, b.keep); err != nil {
		s.log.Warn("could not prune old pre-migration backups", "dir", b.dir, "err", err)
	}
}

// PruneManualBackups deletes all but the newest keep on-demand backups
// (ManualBackupName) in dir. Other files are never touched.
func PruneManualBackups(dir string, keep int) error {
	return pruneBackups(dir, manualBackupName, keep)
}

// pruneBackups deletes all but the newest keep files in dir whose names
// match re. Age comes from the time at the end of the name, not the whole
// name: an automatic name starts with schema versions, so a copy just
// taken from an older schema sorts before older copies by name.
func pruneBackups(dir string, re *regexp.Regexp, keep int) error {
	if keep < 1 {
		return fmt.Errorf("prune backups: keep %d must be at least 1", keep)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("prune backups: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && re.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return nil
	}
	slices.SortFunc(names, func(a, b string) int {
		return cmp.Or(strings.Compare(backupTime(a), backupTime(b)), strings.Compare(a, b))
	})
	var errs []error
	for _, n := range names[:len(names)-keep] {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("prune backups: %w", err)
	}
	return nil
}

// backupTime is the fixed-width time at the end of a backup name
// (...-<time>.db), which sorts as text in time order.
func backupTime(name string) string {
	base := strings.TrimSuffix(name, ".db")
	return base[strings.LastIndexByte(base, '-')+1:]
}
