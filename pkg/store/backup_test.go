package store

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

var (
	migV1 = fstest.MapFS{"0001_a.sql": {Data: []byte("CREATE TABLE a (x INTEGER);")}}
	migV2 = fstest.MapFS{
		"0001_a.sql": migV1["0001_a.sql"],
		"0002_b.sql": {Data: []byte("CREATE TABLE b (y INTEGER);")},
	}
)

// openAtV1 creates a database at schema version 1 holding one row and
// closes it.
func openAtV1(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "dota_lobby.db")
	s, err := Open(ctx, path, withMigrations(migV1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO a (x) VALUES (42)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func backupFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func schemaVersionOf(t *testing.T, path string) int {
	t.Helper()
	s, err := OpenForBackup(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	v, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPreMigrateBackup(t *testing.T) {
	ctx := context.Background()
	path := openAtV1(t)
	dir := filepath.Join(filepath.Dir(path), "backups")
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	s, err := Open(ctx, path, withMigrations(migV2), WithPreMigrateBackup(dir, 5), WithLogger(log))
	if err != nil {
		t.Fatalf("Open with a pending migration: %v", err)
	}
	if v, err := s.SchemaVersion(ctx); err != nil || v != 2 {
		t.Fatalf("schema version after migrate = %d, %v", v, err)
	}
	_ = s.Close()

	files := backupFiles(t, dir)
	if len(files) != 1 || !autoBackupName.MatchString(files[0]) || !strings.HasPrefix(files[0], "dota_lobby-0001-to-0002-") {
		t.Fatalf("backups = %v", files)
	}
	backup := filepath.Join(dir, files[0])
	fi, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("backup mode %o, want 600", mode)
	}
	if di, err := os.Stat(dir); err != nil || di.Mode().Perm() != 0o700 {
		t.Errorf("backup dir: %v, %v", di, err)
	}
	if v := schemaVersionOf(t, backup); v != 1 {
		t.Errorf("backup schema version = %d, want 1", v)
	}
	b, err := OpenForBackup(ctx, backup)
	if err != nil {
		t.Fatal(err)
	}
	var x int
	if err := b.DB().QueryRowContext(ctx, `SELECT x FROM a`).Scan(&x); err != nil || x != 42 {
		t.Errorf("backup data: %d, %v", x, err)
	}
	_ = b.Close()
	for _, want := range []string{"pre-migration backup written", "from=1", "to=2", "bytes="} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs.String())
		}
	}
}

func TestPreMigrateBackupSkipped(t *testing.T) {
	ctx := context.Background()
	t.Run("fresh database", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "dota_lobby.db")
		dir := filepath.Join(filepath.Dir(path), "backups")
		s, err := Open(ctx, path, withMigrations(migV2), WithPreMigrateBackup(dir, 5))
		if err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
		if files := backupFiles(t, dir); len(files) != 0 {
			t.Errorf("backups of a fresh database: %v", files)
		}
	})
	t.Run("nothing to migrate", func(t *testing.T) {
		path := openAtV1(t)
		dir := filepath.Join(filepath.Dir(path), "backups")
		s, err := Open(ctx, path, withMigrations(migV1), WithPreMigrateBackup(dir, 5))
		if err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
		if files := backupFiles(t, dir); len(files) != 0 {
			t.Errorf("backups with nothing pending: %v", files)
		}
	})
}

func TestPreMigrateBackupFailureBlocksMigration(t *testing.T) {
	ctx := context.Background()
	path := openAtV1(t)
	// A file where the backup directory should be.
	dir := filepath.Join(filepath.Dir(path), "backups")
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(ctx, path, withMigrations(migV2), WithPreMigrateBackup(dir, 5))
	if !errors.Is(err, ErrBackupFailed) {
		t.Fatalf("Open = %v, want ErrBackupFailed", err)
	}
	if !strings.Contains(err.Error(), "refusing to migrate: pre-migration backup failed") {
		t.Errorf("error text: %v", err)
	}
	if v := schemaVersionOf(t, path); v != 1 {
		t.Errorf("schema version after a failed backup = %d, want 1", v)
	}
}

func TestOpenRejectsBadBackupOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dota_lobby.db")
	for _, opt := range []Option{WithPreMigrateBackup("", 5), WithPreMigrateBackup(t.TempDir(), 0)} {
		if _, err := Open(context.Background(), path, opt); err == nil {
			t.Error("Open accepted a backup without a directory or keep")
		}
	}
}

func TestPruneBackups(t *testing.T) {
	dir := t.TempDir()
	var auto []string
	for i := range 7 {
		ts := time.Date(2026, 11, 9, 12, 0, i, 0, time.UTC)
		auto = append(auto, autoBackupFile(1, 2, ts))
	}
	manual := []string{ManualBackupName(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), "manual-keep.db", "notes.txt"}
	for _, n := range slices.Concat(auto, manual) {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneBackups(dir, autoBackupName, 3); err != nil {
		t.Fatal(err)
	}
	got := backupFiles(t, dir)
	want := slices.Concat(auto[4:], manual)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("after prune:\n got %v\nwant %v", got, want)
	}

	// Manual pruning touches only manual backups.
	for _, h := range []int{1, 2} {
		n := ManualBackupName(time.Date(2026, 1, 1, h, 0, 0, 0, time.UTC))
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := PruneManualBackups(dir, 1); err != nil {
		t.Fatal(err)
	}
	got = backupFiles(t, dir)
	want = slices.Concat(auto[4:], []string{ManualBackupName(time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)), "manual-keep.db", "notes.txt"})
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("after manual prune:\n got %v\nwant %v", got, want)
	}
	if err := pruneBackups(dir, autoBackupName, 0); err == nil {
		t.Error("keep 0 accepted")
	}
}

func TestBackupRefusesExistingFile(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	out := filepath.Join(filepath.Dir(path), "copy.db")
	if err := os.WriteFile(out, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Backup(ctx, out); !errors.Is(err, os.ErrExist) {
		t.Fatalf("Backup over an existing file = %v", err)
	}
	if b, _ := os.ReadFile(out); string(b) != "keep me" {
		t.Errorf("existing file changed: %q", b)
	}
	if _, err := os.Stat(out + ".partial"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("partial file left behind: %v", err)
	}
}

// A backup through a read-only handle while the serving store writes.
func TestBackupWhileWriting(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := s.Leagues().ReplaceBotLeagues(ctx, "bot", "gc", []uint32{uint32(i%50 + 1)}, time.Now()); err != nil {
				t.Errorf("write during backup: %v", err)
				return
			}
		}
	})
	b, err := OpenForBackup(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "copy.db")
	info, err := b.Backup(ctx, out)
	close(stop)
	wg.Wait()
	_ = b.Close()
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if info.Path != out || info.Bytes == 0 {
		t.Errorf("info = %+v", info)
	}
	if v := schemaVersionOf(t, out); v != len(mustMigrations(t)) {
		t.Errorf("backup schema version = %d", v)
	}
	for _, side := range []string{"-wal", "-shm", ".partial"} {
		if _, err := os.Stat(out + side); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left next to the backup: %v", side, err)
		}
	}
}

func TestOpenForBackupMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.db")
	if _, err := OpenForBackup(context.Background(), path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenForBackup = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("OpenForBackup created the file")
	}
}
