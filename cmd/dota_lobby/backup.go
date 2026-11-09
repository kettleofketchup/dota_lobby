package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/store"
)

// backup copies the database to --out and checks the copy. It opens the
// database read-only and takes no state-dir lock, so it runs next to a
// serving process, and with a newer image before an upgrade: it applies no
// migration. It finds the database the way healthcheck finds the port,
// reading only state_dir and the store section, so neither the secrets nor
// the bot list need to be valid. --secrets is accepted for the same
// command line as serve and ignored.
//
// stdout gets only the path written, for scripts; stderr gets the size.
func (c cli) backup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	var cf configFlags
	cf.register(fs)
	out := fs.String("out", "", "file to write, or a directory (existing, or ending in /) to write dota_lobby-manual-<time>.db into (required)")
	dbPath := fs.String("db", "", "database file (default: store.path from the config)")
	keepManual := fs.Int("keep-manual", 0, "with a directory --out, keep only the newest N dota_lobby-manual-*.db there (0 keeps all)")
	timeout := fs.Duration("timeout", 2*time.Minute, "give up after this long")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("%w: --out is required", errUsage)
	}
	if *keepManual < 0 {
		return fmt.Errorf("%w: --keep-manual must not be negative", errUsage)
	}

	dir, target, err := backupTarget(*out, time.Now())
	if err != nil {
		return err
	}
	if dir == "" && *keepManual > 0 {
		return fmt.Errorf("%w: --keep-manual needs a directory --out", errUsage)
	}

	path := *dbPath
	if path == "" {
		sc, err := config.LoadStore(config.Options{ConfigFile: cf.configFile})
		if err != nil {
			return fmt.Errorf("find database: %w", err)
		}
		path = sc.Path
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	db, err := store.OpenForBackup(ctx, path)
	if err != nil {
		return err
	}
	info, err := db.Backup(ctx, target)
	if cerr := db.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(c.stdout, info.Path)
	_, _ = fmt.Fprintf(c.stderr, "backup of %s verified: %d bytes\n", path, info.Bytes)
	if dir != "" && *keepManual > 0 {
		if err := store.PruneManualBackups(dir, *keepManual); err != nil {
			return fmt.Errorf("backup written, but: %w", err)
		}
	}
	return nil
}

// backupTarget resolves --out. An existing directory, or a path ending in
// a separator (created 0700 if missing), gets a timestamped file name and
// is returned as dir; anything else is the file itself.
func backupTarget(out string, now time.Time) (dir, target string, err error) {
	fi, statErr := os.Stat(out)
	isDir := statErr == nil && fi.IsDir()
	if !isDir && strings.HasSuffix(out, string(filepath.Separator)) {
		if err := os.MkdirAll(out, 0o700); err != nil {
			return "", "", fmt.Errorf("backup: %w", err)
		}
		isDir = true
	}
	if isDir {
		dir = filepath.Clean(out)
		return dir, filepath.Join(dir, store.ManualBackupName(now)), nil
	}
	if statErr == nil {
		return "", "", fmt.Errorf("backup: %s already exists", out)
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return "", "", fmt.Errorf("backup: %w", statErr)
	}
	return "", out, nil
}
