package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
)

// fakeJWT builds an unsigned token with the given payload; Steam's real
// tokens are EdDSA-signed but we never check the signature.
func fakeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	p, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9." + base64.RawURLEncoding.EncodeToString(p) + ".c2ln"
}

func TestNewToken(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		raw     string
		wantID  uint64
		wantExp time.Time
		wantIat time.Time
		wantErr bool
	}{
		{
			name:    "steam token",
			raw:     fakeJWT(t, map[string]any{"sub": "76561198000000001", "aud": []string{"client", "web"}, "iat": 1_780_000_000, "exp": 1_800_000_000}),
			wantID:  76561198000000001,
			wantIat: time.Unix(1_780_000_000, 0).UTC(),
			wantExp: time.Unix(1_800_000_000, 0).UTC(),
		},
		{name: "no claims we use", raw: fakeJWT(t, map[string]any{"aud": "client"})},
		{name: "trailing newline", raw: fakeJWT(t, map[string]any{"sub": "7"}) + "\n", wantID: 7},
		{name: "two parts", raw: "abc.def", wantErr: true},
		{name: "bad base64", raw: "a.!!!.c", wantErr: true},
		{name: "payload not json", raw: "a." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".c", wantErr: true},
		{name: "sub not a steamid", raw: fakeJWT(t, map[string]any{"sub": "bob"}), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok, err := NewToken(tt.raw, now, SourceAuthenticate)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %v", tok)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tok.SteamID != tt.wantID || !tok.ExpiresAt.Equal(tt.wantExp) || !tok.IssuedAt.Equal(tt.wantIat) {
				t.Errorf("token = %+v", tok)
			}
			if tok.RefreshToken != strings.TrimSpace(tt.raw) || !tok.ObtainedAt.Equal(now) || tok.Source != SourceAuthenticate {
				t.Errorf("token fields not carried through: %+v", tok)
			}
		})
	}
}

func TestTokenExpiry(t *testing.T) {
	exp := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		tok     Token
		now     time.Time
		expired bool
		within  bool // within 14 days
	}{
		{"far from exp", Token{ExpiresAt: exp}, exp.Add(-30 * 24 * time.Hour), false, false},
		{"13 days left", Token{ExpiresAt: exp}, exp.Add(-13 * 24 * time.Hour), false, true},
		{"at exp", Token{ExpiresAt: exp}, exp, true, true},
		{"no exp", Token{}, exp, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tok.Expired(tt.now); got != tt.expired {
				t.Errorf("Expired = %v", got)
			}
			if got := tt.tok.ExpiresWithin(tt.now, 14*24*time.Hour); got != tt.within {
				t.Errorf("ExpiresWithin = %v", got)
			}
		})
	}
}

func TestTokenRedaction(t *testing.T) {
	const raw = "header.c2VjcmV0LXBheWxvYWQ.signature-value"
	tok := Token{RefreshToken: raw, SteamID: 7, Source: SourceSeed}

	var logged strings.Builder
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("t", "token", tok)
	outs := []string{
		fmt.Sprint(tok), fmt.Sprintf("%v", tok), fmt.Sprintf("%+v", tok), fmt.Sprintf("%#v", tok),
		fmt.Sprintf("%s", tok), fmt.Sprintf("%d", tok), fmt.Sprintf("%v", &tok), logged.String(),
		fmt.Sprintf("%v", struct{ T Token }{tok}),
	}
	for _, out := range outs {
		if strings.Contains(out, raw) || strings.Contains(out, "signature-value") {
			t.Errorf("token leaked: %s", out)
		}
	}
	if tok.Secret().Reveal() != raw {
		t.Error("Secret() lost the value")
	}

	// The persisted form must keep the real value (07 A14).
	b, err := json.Marshal(tok)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), raw) {
		t.Errorf("JSON lost the token: %s", b)
	}
}

// TestTokenHasNoSecretFields guards against someone "hardening" Token with
// config.Secret, which would persist the redaction marker.
func TestTokenHasNoSecretFields(t *testing.T) {
	secretType := reflect.TypeFor[config.Secret]()
	tt := reflect.TypeFor[Token]()
	for i := range tt.NumField() {
		if tt.Field(i).Type == secretType {
			t.Errorf("Token.%s is a config.Secret", tt.Field(i).Name)
		}
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bots", "steam_user")
	s := NewStore(dir)

	if _, err := s.Load(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("Load on empty store: %v", err)
	}
	tok, err := NewToken(fakeJWT(t, map[string]any{"sub": "76561198000000001", "exp": 1_800_000_000}), time.Now(), SourceAuthenticate)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(tok); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("token mode = %v, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", di.Mode().Perm())
	}

	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshToken != tok.RefreshToken || got.SteamID != tok.SteamID || !got.ExpiresAt.Equal(tok.ExpiresAt) || !got.ObtainedAt.Equal(tok.ObtainedAt) {
		t.Errorf("round trip: got %+v want %+v", got, tok)
	}

	// steam_id is a string on disk so JSON readers do not lose precision.
	b, _ := os.ReadFile(s.Path())
	if !strings.Contains(string(b), `"steam_id": "76561198000000001"`) {
		t.Errorf("unexpected file contents shape: %s", b)
	}

	if err := s.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); !errors.Is(err, ErrNoToken) {
		t.Errorf("Load after Delete: %v", err)
	}
	if err := s.Delete(); err != nil {
		t.Errorf("second Delete: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("leftover files: %v", entries)
	}
}

func TestStoreSaveRejectsEmpty(t *testing.T) {
	if err := NewStore(t.TempDir()).Save(Token{}); err == nil {
		t.Error("expected an error saving an empty token")
	}
}

func TestStoreLoadBadFile(t *testing.T) {
	tests := []struct {
		name, contents string
	}{
		{"not json", "eyJ-not-json"},
		{"no token", `{"steam_id":"7"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, TokenFile), []byte(tt.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := NewStore(dir).Load()
			if err == nil || errors.Is(err, ErrNoToken) {
				t.Fatalf("Load = %v, want a parse error", err)
			}
			if strings.Contains(err.Error(), tt.contents) {
				t.Errorf("error echoes the file: %v", err)
			}
		})
	}
}

// TestStoreCrashLeftovers simulates a crash between writing the temp file
// and the rename: the old token must still load and the next Save must win.
func TestStoreCrashLeftovers(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	old := Token{RefreshToken: fakeJWT(t, map[string]any{"sub": "1"}), SteamID: 1}
	if err := s.Save(old); err != nil {
		t.Fatal(err)
	}
	half := filepath.Join(dir, "."+TokenFile+".123456.tmp")
	if err := os.WriteFile(half, []byte(`{"refresh_token":"half-writ`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := s.Load()
	if err != nil || got.RefreshToken != old.RefreshToken {
		t.Fatalf("Load with a leftover temp file = %v, %v", got, err)
	}
	next := Token{RefreshToken: fakeJWT(t, map[string]any{"sub": "2"}), SteamID: 2}
	if err := s.Save(next); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Load(); got.SteamID != 2 {
		t.Errorf("Save after crash did not replace the token: %v", got)
	}
}

// TestStoreFailedSaveKeepsOld checks the temp file is cleaned up and the old
// token survives when the rename cannot happen.
func TestStoreFailedSaveKeepsOld(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	old := Token{RefreshToken: fakeJWT(t, map[string]any{"sub": "1"}), SteamID: 1}
	if err := s.Save(old); err != nil {
		t.Fatal(err)
	}
	// Put a non-empty directory where token.json goes, so the rename fails
	// after the temp file is written.
	target := filepath.Join(dir, "blocked")
	if err := os.MkdirAll(filepath.Join(target, TokenFile, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := NewStore(target).Save(old); err == nil {
		t.Fatal("expected Save onto a directory to fail")
	}
	entries, _ := os.ReadDir(target)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file %s left behind", e.Name())
		}
	}
	if got, err := s.Load(); err != nil || got.SteamID != 1 {
		t.Errorf("old token lost: %v, %v", got, err)
	}
}

func TestStoreLoadOrSeed(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	seed := fakeJWT(t, map[string]any{"sub": "76561198000000009", "exp": 1_800_000_000})
	stored := Token{RefreshToken: fakeJWT(t, map[string]any{"sub": "5"}), SteamID: 5, Source: SourceRenew}
	tokAt := func(sub string, exp time.Time) Token {
		tok, err := NewToken(fakeJWT(t, map[string]any{"sub": sub, "exp": exp.Unix()}), now.Add(-time.Hour), SourceAuthenticate)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	expired := tokAt("6", now.Add(-24*time.Hour))
	renewed := tokAt("7", time.Unix(1_900_000_000, 0)) // later than seed
	older := tokAt("8", now.Add(10*24*time.Hour))      // valid, but before seed's exp
	expiredSeed := fakeJWT(t, map[string]any{"sub": "76561198000000010", "exp": now.Add(-time.Hour).Unix()})

	tests := []struct {
		name       string
		existing   *Token
		raw        string // file contents instead of existing
		seed       config.Secret
		wantSeeded bool
		wantID     uint64
		wantErr    error // nil = no error
		anyErr     bool
	}{
		{name: "seeds an empty store", seed: config.Secret(seed), wantSeeded: true, wantID: 76561198000000009},
		{name: "stored token wins over seed", existing: &stored, seed: config.Secret(seed), wantID: 5},
		{name: "stored token without seed", existing: &stored, wantID: 5},
		{name: "nothing at all", wantErr: ErrNoToken},
		{name: "seed is a corrupt JWT", seed: "a.!!!.c", anyErr: true},
		// An operator shipped a fresh token through sops.
		{name: "seed replaces an expired token", existing: &expired, seed: config.Secret(seed), wantSeeded: true, wantID: 76561198000000009},
		{name: "seed replaces an unreadable file", raw: "{not json", seed: config.Secret(seed), wantSeeded: true, wantID: 76561198000000009},
		{name: "newer seed replaces a valid token", existing: &older, seed: config.Secret(seed), wantSeeded: true, wantID: 76561198000000009},
		{name: "renewed token beats the original seed", existing: &renewed, seed: config.Secret(seed), wantID: 7},
		{name: "expired seed does not replace an expired token", existing: &expired, seed: config.Secret(expiredSeed), wantID: 6},
		{name: "bad seed leaves a stored token alone", existing: &older, seed: "a.!!!.c", wantID: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewStore(filepath.Join(t.TempDir(), "bot"))
			if tt.existing != nil {
				if err := s.Save(*tt.existing); err != nil {
					t.Fatal(err)
				}
			}
			if tt.raw != "" {
				if err := os.MkdirAll(filepath.Dir(s.Path()), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(s.Path(), []byte(tt.raw), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			tok, seeded, err := s.LoadOrSeed(tt.seed, now)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			case tt.anyErr:
				if err == nil {
					t.Fatal("expected an error")
				}
				if strings.Contains(err.Error(), tt.seed.Reveal()) {
					t.Errorf("error echoes the seed: %v", err)
				}
				if _, lerr := s.Load(); !errors.Is(lerr, ErrNoToken) {
					t.Errorf("a bad seed was stored: %v", lerr)
				}
				return
			case err != nil:
				t.Fatal(err)
			}
			if seeded != tt.wantSeeded || tok.SteamID != tt.wantID {
				t.Errorf("LoadOrSeed = %v seeded=%v", tok, seeded)
			}
			if tt.wantSeeded {
				if tok.Source != SourceSeed || !tok.ObtainedAt.Equal(now) {
					t.Errorf("seeded token fields: %+v", tok)
				}
				// The seed is now the stored token; a second call loads it.
				again, seeded2, err := s.LoadOrSeed(tt.seed, now)
				if err != nil || seeded2 || again.RefreshToken != seed {
					t.Errorf("second LoadOrSeed = %v, %v, %v", again, seeded2, err)
				}
			}
		})
	}
}

// A configured STEAM_REFRESH_TOKEN that is not a JWT is accepted like one
// Authenticate minted: stored with the expiry unknown.
func TestStoreLoadOrSeedOpaqueToken(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	jwtStored := Token{RefreshToken: fakeJWT(t, map[string]any{"sub": "5", "exp": now.Add(24 * time.Hour).Unix()}), SteamID: 5}
	tests := []struct {
		name       string
		existing   *Token
		wantSeeded bool
		wantRaw    string
	}{
		{name: "empty store", wantSeeded: true, wantRaw: "plain-old-string"},
		{name: "valid stored token is kept", existing: &jwtStored, wantRaw: jwtStored.RefreshToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewStore(filepath.Join(t.TempDir(), "bot"))
			if tt.existing != nil {
				if err := s.Save(*tt.existing); err != nil {
					t.Fatal(err)
				}
			}
			tok, seeded, err := s.LoadOrSeed(" plain-old-string\n", now)
			if err != nil {
				t.Fatal(err)
			}
			if seeded != tt.wantSeeded || tok.RefreshToken != tt.wantRaw {
				t.Fatalf("LoadOrSeed = %v seeded=%v", tok, seeded)
			}
			if seeded && (!tok.ExpiresAt.IsZero() || tok.Source != SourceSeed || tok.Expired(now.Add(1000*time.Hour))) {
				t.Errorf("opaque seed fields: %+v", tok)
			}
		})
	}
}

func TestFromMinted(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	jwt := fakeJWT(t, map[string]any{"sub": "76561198000000009", "exp": 1_800_000_000})
	tests := []struct {
		name      string
		raw       string
		wantRaw   string
		wantID    uint64
		wantKnown bool
	}{
		{"jwt", jwt, jwt, 76561198000000009, true},
		{"jwt with padding whitespace", " " + jwt + "\n", jwt, 76561198000000009, true},
		{"opaque", " opaque-token\n", "opaque-token", 0, false},
		{"corrupt payload", "a.!!!.c", "a.!!!.c", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromMinted(tt.raw, now)
			if got.RefreshToken != tt.wantRaw || got.SteamID != tt.wantID || !got.ExpiresAt.IsZero() != tt.wantKnown ||
				got.Source != SourceAuthenticate || !got.ObtainedAt.Equal(now) {
				t.Errorf("FromMinted = %+v", got)
			}
		})
	}
}

func TestStoreConcurrent(t *testing.T) {
	s := NewStore(t.TempDir())
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			tok := Token{RefreshToken: fakeJWT(t, map[string]any{"sub": fmt.Sprint(i + 1)}), SteamID: uint64(i + 1)}
			if err := s.Save(tok); err != nil {
				t.Error(err)
			}
			if _, err := s.Load(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got, err := s.Load(); err != nil || got.SteamID == 0 {
		t.Errorf("final Load = %v, %v", got, err)
	}
}

// A token Steam refused is deleted, and the same configured seed must not
// bring it back, across a store re-open, until the seed itself changes.
func TestStoreRejectedSeedStaysOut(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mk := func(sub string) config.Secret {
		return config.Secret(fakeJWT(t, map[string]any{"sub": sub, "exp": now.Add(100 * 24 * time.Hour).Unix()}))
	}
	dead, fresh := mk("76561198000000001"), mk("76561198000000002")

	dir := t.TempDir()
	s := NewStore(dir)
	if _, seeded, err := s.LoadOrSeed(dead, now); err != nil || !seeded {
		t.Fatalf("first seed = %v, %v", seeded, err)
	}
	if err := s.DeleteRejected(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		reopen     bool
		seed       config.Secret
		wantSeeded bool
	}{
		{"same store, same seed", false, dead, false},
		{"re-opened store, same seed", true, dead, false},
		{"changed seed is accepted", true, fresh, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := s
			if tt.reopen {
				st = NewStore(dir)
			}
			_, seeded, err := st.LoadOrSeed(tt.seed, now)
			if seeded != tt.wantSeeded {
				t.Fatalf("seeded = %v, want %v (err %v)", seeded, tt.wantSeeded, err)
			}
			if !tt.wantSeeded && !errors.Is(err, ErrNoToken) {
				t.Errorf("err = %v, want ErrNoToken", err)
			}
		})
	}

	// Only a fingerprint is persisted, never the token.
	b, err := os.ReadFile(filepath.Join(dir, RejectedFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), dead.Reveal()) {
		t.Error("rejected file holds the token")
	}
}

func TestStoreRejectedSeedWithWhitespace(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	raw := fakeJWT(t, map[string]any{"sub": "76561198000000001", "exp": now.Add(100 * 24 * time.Hour).Unix()})
	// A YAML block scalar or a sops value can end in a newline; the stored
	// token is trimmed, so the fingerprint must match either way.
	seed := config.Secret(raw + "\n")

	s := NewStore(t.TempDir())
	if _, seeded, err := s.LoadOrSeed(seed, now); err != nil || !seeded {
		t.Fatalf("first seed = %v, %v", seeded, err)
	}
	if err := s.DeleteRejected(); err != nil {
		t.Fatal(err)
	}
	for _, sd := range []config.Secret{seed, config.Secret(raw), config.Secret("  " + raw)} {
		if _, seeded, _ := s.LoadOrSeed(sd, now); seeded {
			t.Fatalf("refused token re-seeded from a seed padded by %d bytes", len(sd.Reveal())-len(raw))
		}
	}
}

func TestStoreRejectedKeepsEarlierTokens(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mk := func(sub string) config.Secret {
		return config.Secret(fakeJWT(t, map[string]any{"sub": sub, "exp": now.Add(100 * 24 * time.Hour).Unix()}))
	}
	seed, minted := mk("76561198000000001"), mk("76561198000000002")

	s := NewStore(t.TempDir())
	if _, seeded, err := s.LoadOrSeed(seed, now); err != nil || !seeded {
		t.Fatalf("first seed = %v, %v", seeded, err)
	}
	if err := s.DeleteRejected(); err != nil {
		t.Fatal(err)
	}
	// The bot re-authenticates, and later that token is refused as well.
	if err := s.Save(Token{RefreshToken: minted.Reveal()}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRejected(); err != nil {
		t.Fatal(err)
	}
	if _, seeded, _ := s.LoadOrSeed(seed, now); seeded {
		t.Fatal("the first refused seed came back after a second token was refused")
	}
}
