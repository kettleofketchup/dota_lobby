package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
)

// TokenFile is the token's file name inside a bot's state dir.
const TokenFile = "token.json"

// ErrNoToken is returned by Load when the bot has no stored token.
var ErrNoToken = errors.New("auth: no stored token")

// Store persists one bot's token at <dir>/token.json, where dir is
// config.Config.BotStateDir. Writes go to a temp file that is fsynced and
// renamed, so a crash leaves either the old token or the new one (04 §6.2).
// A Store is safe for concurrent use; across processes the state dir LOCK
// keeps a single writer.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore returns the store for the bot state dir dir. Nothing is created
// until the first Save.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Path is the token file's location.
func (s *Store) Path() string { return filepath.Join(s.dir, TokenFile) }

// Load returns the stored token, or ErrNoToken when there is none.
func (s *Store) Load() (Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) load() (Token, error) {
	b, err := os.ReadFile(s.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return Token{}, ErrNoToken
	}
	if err != nil {
		return Token{}, fmt.Errorf("auth: read token: %w", err)
	}
	var t Token
	if err := json.Unmarshal(b, &t); err != nil {
		// Never wrap the decoder error with the file contents.
		return Token{}, fmt.Errorf("auth: parse %s: %w", s.Path(), err)
	}
	if t.RefreshToken == "" {
		return Token{}, fmt.Errorf("auth: %s has no refresh_token", s.Path())
	}
	return t, nil
}

// Save writes t atomically with mode 0600, creating the dir with 0700.
func (s *Store) Save(t Token) error {
	if !t.IsSet() {
		return errors.New("auth: refusing to save an empty token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(t)
}

func (s *Store) save(t Token) error {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("auth: encode token: %w", err)
	}
	b = append(b, '\n')
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("auth: create state dir: %w", err)
	}
	return writeFileAtomic(s.Path(), b)
}

// Delete removes the stored token. A missing token is not an error.
func (s *Store) Delete() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.Path()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("auth: delete token: %w", err)
	}
	return syncDir(s.dir)
}

// LoadOrSeed returns the stored token, replacing it with seed (the
// STEAM_REFRESH_TOKEN from the bot's credentials) when the seed is the
// better token: nothing usable is stored (missing, unreadable or expired),
// or the seed is a different token that expires later, which means an
// operator minted a new one and shipped it through sops. A seed
// that is older than the stored token, the usual case once the service has
// renewed, is ignored; renewals are never written back to sops. seeded
// reports that the seed was saved. Without a seed or token it returns
// ErrNoToken.
func (s *Store) LoadOrSeed(seed config.Secret, now time.Time) (t Token, seeded bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err = s.load()
	if !seed.IsSet() {
		return t, false, err
	}
	st, serr := NewToken(seed.Reveal(), now, SourceSeed)
	if errors.Is(serr, ErrNotJWT) {
		// Same as a token Authenticate minted: usable at logon, expiry unknown.
		st, serr = opaqueToken(seed.Reveal(), now, SourceSeed), nil
	}
	if serr != nil {
		if errors.Is(err, ErrNoToken) {
			return Token{}, false, fmt.Errorf("auth: seed refresh token: %w", serr)
		}
		return t, false, err // keep what is stored; the seed is unusable
	}
	if !seedWins(t, err, st, now) {
		return t, false, err
	}
	if err := s.save(st); err != nil {
		return Token{}, false, err
	}
	return st, true, nil
}

func seedWins(stored Token, loadErr error, seed Token, now time.Time) bool {
	switch {
	case loadErr != nil:
		return true // missing or unreadable
	case seed.RefreshToken == stored.RefreshToken:
		return false
	case stored.Expired(now):
		return !seed.Expired(now)
	}
	// Both usable: only a newer seed replaces a stored token, and only
	// when both expiries are known.
	return !stored.ExpiresAt.IsZero() && !seed.ExpiresAt.IsZero() && seed.ExpiresAt.After(stored.ExpiresAt)
}

// writeFileAtomic writes b next to path, fsyncs it, renames it over path and
// fsyncs the directory so the rename survives a power cut.
func writeFileAtomic(path string, b []byte) (err error) {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("auth: create temp file: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	// CreateTemp already uses 0600; Chmod makes that independent of it.
	if err = f.Chmod(0o600); err != nil {
		return fmt.Errorf("auth: chmod temp file: %w", err)
	}
	if _, err = f.Write(b); err != nil {
		return fmt.Errorf("auth: write temp file: %w", err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("auth: fsync temp file: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("auth: close temp file: %w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("auth: rename token: %w", err)
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("auth: open state dir: %w", err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("auth: fsync state dir: %w", err)
	}
	return nil
}
