package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"
	"time"
)

func openTemp(t *testing.T, opts ...Option) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "dota_lobby.db")
	s, err := Open(context.Background(), path, opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func TestOpenMigratesAndReopens(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	v, err := s.SchemaVersion(ctx)
	if err != nil || v != 1 {
		t.Fatalf("schema version = %d, %v; want 1", v, err)
	}
	if err := s.Leagues().ReplaceBotLeagues(ctx, "bot1", "gc", []uint32{7}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// A second open applies nothing and keeps the data.
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	var n int
	if err := s2.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("schema_migrations rows = %d, %v", n, err)
	}
	sets, err := s2.Leagues().BotLeagues(ctx)
	if err != nil || len(sets["bot1"].Sources["gc"].IDs) != 1 {
		t.Fatalf("data lost on reopen: %+v, %v", sets, err)
	}
}

func TestOpenPragmas(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	tests := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"busy_timeout", "5000"},
		{"foreign_keys", "1"},
	}
	for _, tt := range tests {
		var got string
		if err := s.DB().QueryRowContext(ctx, "PRAGMA "+tt.pragma).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tt.pragma, err)
		}
		if got != tt.want {
			t.Errorf("PRAGMA %s = %q, want %q", tt.pragma, got, tt.want)
		}
	}
}

func TestOpenPermissions(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		prepare func(t *testing.T, path string)
	}{
		{"new file", func(*testing.T, string) {}},
		{"existing world-readable file", func(t *testing.T, path string) {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "db", "dota_lobby.db")
			tt.prepare(t, path)
			s, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			// Force a WAL write so the side files exist.
			if err := s.Leagues().ReplaceBotLeagues(ctx, "b", "gc", []uint32{1}, time.Now()); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{path, path + "-wal", path + "-shm"} {
				fi, err := os.Stat(p)
				if err != nil {
					t.Fatalf("stat %s: %v", p, err)
				}
				if mode := fi.Mode().Perm(); mode&0o077 != 0 {
					t.Errorf("%s mode %o, want no group/other access", filepath.Base(p), mode)
				}
			}
			fi, err := os.Stat(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			if mode := fi.Mode().Perm(); mode&0o077 != 0 && tt.name == "new file" {
				t.Errorf("created dir mode %o", mode)
			}
		})
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (99, 'x')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := Open(ctx, path)
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("Open on a newer schema = %v, want ErrSchemaTooNew", err)
	}
}

func TestMigrationsApplyInOrder(t *testing.T) {
	ctx := context.Background()
	v1 := fstest.MapFS{"0001_a.sql": {Data: []byte("CREATE TABLE a (x INTEGER);")}}
	v2 := fstest.MapFS{
		"0001_a.sql": v1["0001_a.sql"],
		"0002_b.sql": {Data: []byte("CREATE TABLE b (y INTEGER); INSERT INTO a (x) VALUES (1);")},
	}
	path := filepath.Join(t.TempDir(), "m.db")
	s, err := Open(ctx, path, withMigrations(v1))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(ctx, path, withMigrations(v2))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var n int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM a`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("second migration did not run once: %d, %v", n, err)
	}
	if v, _ := s.SchemaVersion(ctx); v != 2 {
		t.Errorf("version = %d", v)
	}
}

func TestMigrationFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	bad := fstest.MapFS{
		"0001_a.sql": {Data: []byte("CREATE TABLE a (x INTEGER);")},
		"0002_b.sql": {Data: []byte("CREATE TABLE b (y INTEGER); NOT SQL;")},
	}
	path := filepath.Join(t.TempDir(), "m.db")
	if _, err := Open(ctx, path, withMigrations(bad)); err == nil {
		t.Fatal("broken migration accepted")
	}
	s, err := Open(ctx, path, withMigrations(fstest.MapFS{"0001_a.sql": bad["0001_a.sql"]}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var n int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'b'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("table b left behind by a failed migration: %d, %v", n, err)
	}
}

func TestLoadMigrationsRejectsBadNames(t *testing.T) {
	tests := []struct {
		name string
		fsys fstest.MapFS
	}{
		{"no underscore", fstest.MapFS{"0001.sql": {}}},
		{"not a number", fstest.MapFS{"abcd_x.sql": {}}},
		{"zero", fstest.MapFS{"0000_x.sql": {}}},
		{"gap", fstest.MapFS{"0001_a.sql": {}, "0003_c.sql": {}}},
		{"duplicate", fstest.MapFS{"0001_a.sql": {}, "0001_b.sql": {}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := loadMigrations(tt.fsys); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestEmbeddedMigrationsLoad(t *testing.T) {
	sub, err := loadMigrations(mustSub(t))
	if err != nil || len(sub) == 0 {
		t.Fatalf("embedded migrations: %v, %v", sub, err)
	}
}

func mustSub(t *testing.T) fstest.MapFS {
	t.Helper()
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, e := range entries {
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = &fstest.MapFile{Data: b}
	}
	return out
}

func TestLeagues(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	l := s.Leagues()
	t0 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

	if err := l.ReplaceBotLeagues(ctx, "bot1", "gc", []uint32{30, 10, 20, 10}, t0); err != nil {
		t.Fatal(err)
	}
	if err := l.ReplaceBotLeagues(ctx, "bot2", "gc", nil, t0); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordRefreshError(ctx, "bot3", errors.New("gc timeout"), t0); err != nil {
		t.Fatal(err)
	}
	// A failure after a success keeps the list and the success time.
	if err := l.RecordRefreshError(ctx, "bot1", errors.New("not ready"), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	got, err := l.BotLeagues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]BotLeagueSet{
		"bot1": {Bot: "bot1", Sources: map[string]LeagueSource{"gc": {IDs: []uint32{10, 20, 30}, RefreshedAt: t0}},
			OKAt: t0, Error: "not ready", ErrorAt: t0.Add(time.Minute)},
		"bot2": {Bot: "bot2", Sources: map[string]LeagueSource{}, OKAt: t0},
		"bot3": {Bot: "bot3", Sources: map[string]LeagueSource{}, Error: "gc timeout", ErrorAt: t0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BotLeagues =\n%+v\nwant\n%+v", got, want)
	}

	// A new success replaces the list and clears the error.
	t1 := t0.Add(time.Hour)
	if err := l.ReplaceBotLeagues(ctx, "bot1", "gc", []uint32{40}, t1); err != nil {
		t.Fatal(err)
	}
	got, err = l.BotLeagues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b1 := got["bot1"]
	if !reflect.DeepEqual(b1.Sources["gc"].IDs, []uint32{40}) || !b1.OKAt.Equal(t1) || b1.Error != "" || !b1.ErrorAt.IsZero() {
		t.Errorf("after second refresh: %+v", b1)
	}
	// Other sources of the same bot are untouched.
	if err := l.ReplaceBotLeagues(ctx, "bot1", "config", []uint32{5}, t1); err != nil {
		t.Fatal(err)
	}
	got, _ = l.BotLeagues(ctx)
	if len(got["bot1"].Sources) != 2 || got["bot1"].Sources["gc"].IDs[0] != 40 {
		t.Errorf("sources = %+v", got["bot1"].Sources)
	}
}
