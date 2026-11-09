package main

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kettleofketchup/dota_lobby/pkg/store"
)

// newDB creates a fully migrated database in a fresh state dir and returns
// the open store and the backup command's config flags.
func newDB(t *testing.T) (*store.Store, []string) {
	t.Helper()
	flags, stateDir := writeConfig(t, "8080", "bots: []\n", "{}\n")
	s, err := store.Open(context.Background(), filepath.Join(stateDir, "dota_lobby.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, flags
}

var manualName = regexp.MustCompile(`^dota_lobby-manual-\d{8}T\d{6}Z\.db$`)

func TestBackupCommand(t *testing.T) {
	ctx := context.Background()
	t.Run("while the service writes", func(t *testing.T) {
		s, flags := newDB(t)
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if err := s.Leagues().ReplaceBotLeagues(ctx, "bot", "gc", []uint32{uint32(i%9 + 1)}, time.Now()); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		})
		out := filepath.Join(t.TempDir(), "copy.db")
		c, stdout, stderr := newCLI()
		code := c.run(ctx, append([]string{"backup", "--out", out}, flags...))
		close(stop)
		wg.Wait()
		if code != exitOK {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		if got := strings.TrimSpace(stdout.String()); got != out {
			t.Errorf("stdout %q, want only the path", got)
		}
		if !strings.Contains(stderr.String(), "verified") {
			t.Errorf("stderr: %s", stderr)
		}
		fi, err := os.Stat(out)
		if err != nil || fi.Mode().Perm() != 0o600 || fi.Size() == 0 {
			t.Fatalf("backup file: %v, %v", fi, err)
		}
	})

	t.Run("into a directory, pruning old manual backups", func(t *testing.T) {
		_, flags := newDB(t)
		dir := filepath.Join(t.TempDir(), "backups") + string(filepath.Separator)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		old := []string{
			store.ManualBackupName(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)),
			store.ManualBackupName(time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)),
			"dota_lobby-0001-to-0004-20200101T000000Z.db",
		}
		for _, n := range old {
			if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		c, stdout, stderr := newCLI()
		if code := c.run(ctx, append([]string{"backup", "--out", dir, "--keep-manual", "2"}, flags...)); code != exitOK {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		got := strings.TrimSpace(stdout.String())
		if filepath.Dir(got) != filepath.Clean(dir) || !manualName.MatchString(filepath.Base(got)) {
			t.Fatalf("wrote %q", got)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		want := []string{old[2], old[1], filepath.Base(got)}
		slices.Sort(want)
		if !slices.Equal(names, want) {
			t.Errorf("directory holds %v, want %v", names, want)
		}
	})

	t.Run("missing directory with a trailing slash is created", func(t *testing.T) {
		_, flags := newDB(t)
		dir := filepath.Join(t.TempDir(), "new") + "/"
		c, stdout, stderr := newCLI()
		if code := c.run(ctx, append([]string{"backup", "--out", dir}, flags...)); code != exitOK {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		if fi, err := os.Stat(filepath.Clean(dir)); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("dir: %v, %v", fi, err)
		}
		if _, err := os.Stat(strings.TrimSpace(stdout.String())); err != nil {
			t.Error(err)
		}
	})

	t.Run("existing --out", func(t *testing.T) {
		_, flags := newDB(t)
		out := filepath.Join(t.TempDir(), "copy.db")
		if err := os.WriteFile(out, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		c, _, stderr := newCLI()
		if code := c.run(ctx, append([]string{"backup", "--out", out}, flags...)); code != exitError {
			t.Fatalf("exit %d, want %d\n%s", code, exitError, stderr)
		}
		if b, _ := os.ReadFile(out); string(b) != "x" {
			t.Error("existing file overwritten")
		}
	})

	t.Run("no database", func(t *testing.T) {
		flags, _ := writeConfig(t, "8080", "bots: []\n", "{}\n")
		c, _, stderr := newCLI()
		out := filepath.Join(t.TempDir(), "copy.db")
		if code := c.run(ctx, append([]string{"backup", "--out", out}, flags...)); code != exitError {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		if _, err := os.Stat(out); err == nil {
			t.Error("a backup was written without a database")
		}
	})

	t.Run("--db needs no config", func(t *testing.T) {
		s, _ := newDB(t)
		t.Setenv("DOTA_LOBBY_CONFIG_FILE", "/nonexistent/config.yaml")
		out := filepath.Join(t.TempDir(), "copy.db")
		c, _, stderr := newCLI()
		if code := c.run(ctx, []string{"backup", "--db", s.Path(), "--out", out}); code != exitOK {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
	})

	for _, tt := range []struct {
		name string
		args []string
	}{
		{"missing --out", nil},
		{"negative --keep-manual", []string{"--out", "x/", "--keep-manual", "-1"}},
		{"--keep-manual with a file", []string{"--out", "/nonexistent/x.db", "--keep-manual", "3"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, _, _ := newCLI()
			if code := c.run(ctx, append([]string{"backup"}, tt.args...)); code != exitUsage {
				t.Errorf("exit %d, want %d", code, exitUsage)
			}
		})
	}
}

// serve backs the database up before it migrates an older schema, and
// refuses to start when the backup cannot be written.
func TestServeBacksUpBeforeMigrating(t *testing.T) {
	tests := []struct {
		name     string
		blockDir bool
		want     int
	}{
		{"backup written", false, exitOK},
		{"backup fails", true, exitError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := freeAddr(t)
			_, port, _ := net.SplitHostPort(addr)
			flags, stateDir := writeConfig(t, port, "bots: []\n", "{}\n")
			dbPath := filepath.Join(stateDir, "dota_lobby.db")
			seedVersion1(t, dbPath)
			backups := filepath.Join(stateDir, "backups")
			if tt.blockDir {
				if err := os.WriteFile(backups, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c, _, stderr := newCLI()
			done := make(chan int, 1)
			go func() { done <- c.run(ctx, append([]string{"serve"}, flags...)) }()

			if tt.blockDir {
				select {
				case code := <-done:
					if code != tt.want {
						t.Fatalf("exit %d\n%s", code, stderr)
					}
				case <-time.After(30 * time.Second):
					t.Fatal("serve did not exit")
				}
				if !strings.Contains(stderr.String(), "refusing to migrate: pre-migration backup failed") {
					t.Errorf("stderr: %s", stderr)
				}
				if v := versionOf(t, dbPath); v != 1 {
					t.Errorf("schema version %d after a failed backup, want 1", v)
				}
				return
			}

			waitLive(t, addr, stderr)
			cancel()
			if code := <-done; code != tt.want {
				t.Fatalf("exit %d\n%s", code, stderr)
			}
			entries, err := os.ReadDir(backups)
			if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "dota_lobby-0001-to-") {
				t.Fatalf("backups: %v, %v", entries, err)
			}
			if !strings.Contains(stderr.String(), "pre-migration backup written") {
				t.Errorf("no backup log:\n%s", stderr)
			}
		})
	}
}

// seedVersion1 creates a database with only the first embedded migration
// applied, as an old release would have left it.
func seedVersion1(t *testing.T, path string) {
	t.Helper()
	m, err := filepath.Glob(filepath.Join("..", "..", "pkg", "store", "migrations", "0001_*.sql"))
	if err != nil || len(m) != 1 {
		t.Fatalf("migration 0001: %v %v", m, err)
	}
	body, err := os.ReadFile(m[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, q := range []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		string(body),
		`INSERT INTO schema_migrations (version, applied_at) VALUES (1, '2026-01-01T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func versionOf(t *testing.T, path string) int {
	t.Helper()
	s, err := store.OpenForBackup(context.Background(), path)
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

func waitLive(t *testing.T, addr string, stderr *syncBuffer) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/livez")
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve never answered: %v\n%s", err, stderr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
