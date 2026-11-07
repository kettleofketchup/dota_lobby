package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/auth"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

// authenticator is the part of gc.Client the auth command needs.
type authenticator interface {
	Authenticate(ctx context.Context, creds gc.Credentials) (string, error)
}

// newAuthenticator is swapped in tests; Authenticate opens no CM
// connection, only Steam's Web API auth session.
var newAuthenticator = func(log *slog.Logger) authenticator {
	return gc.New(gc.Options{Logger: log})
}

// auth runs the password flow once for one bot and stores the refresh
// token in the service's token store (state_dir), where serve picks it up
// at the bot's next logon. With a shared_secret it needs no human; without
// one it takes --code or asks on the terminal.
func (c cli) auth(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("auth", flag.ContinueOnError)
	var cf configFlags
	cf.register(fs)
	name := fs.String("bot", "", "bot name from the config (required)")
	code := fs.String("code", "", "Steam Guard code (authenticator or email) for a bot without shared_secret")
	force := fs.Bool("force", false, "run even though a serve process holds the state dir")
	timeout := fs.Duration("timeout", 2*time.Minute, "give up after this long")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("%w: --bot is required", errUsage)
	}

	log := newLogger(c.stderr, config.LogConfig{Level: "warn", Format: "text"})
	cfg, err := cf.load(log)
	if err != nil {
		return err
	}
	b, ok := cfg.Bot(*name)
	if !ok {
		names := make([]string, len(cfg.Bots))
		for i, bc := range cfg.Bots {
			names[i] = bc.Name
		}
		return fmt.Errorf("no bot %q in the config (have: %s)", *name, strings.Join(names, ", "))
	}
	if !b.Credentials.CanAuthenticate() {
		return fmt.Errorf("bot %s has no password configured", b.Name)
	}

	// The token store is shared with serve; refuse to race a running
	// service unless told to. The store's writes are atomic either way.
	lock, err := auth.AcquireLock(cfg.StateDir)
	switch {
	case errors.Is(err, auth.ErrLocked) && *force:
		_, _ = fmt.Fprintf(c.stderr, "warning: a serve process holds the state dir; it uses the new token at the bot's next logon\n"+
			"  (a bot in needs_attention waits for: POST /v1/bots/%s/retry)\n", b.Name)
	case errors.Is(err, auth.ErrLocked):
		return fmt.Errorf("%w (stop the service or pass --force)", err)
	case err != nil:
		return err
	}
	defer func() { _ = lock.Release() }()

	creds := gc.Credentials{
		Username:   b.Username,
		Password:   b.Credentials.Password,
		DeviceName: b.Credentials.DeviceName,
		SteamGuard: c.guardPrompt(*code),
	}
	if b.Credentials.SharedSecret.IsSet() {
		totp, err := auth.TOTP{SharedSecret: b.Credentials.SharedSecret, Skew: cfg.Steam.TOTPSkew}.Code()
		if err != nil {
			return fmt.Errorf("shared_secret: %w", err)
		}
		creds.TwoFactorCode = totp
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	raw, err := newAuthenticator(log).Authenticate(ctx, creds)
	if err != nil {
		return fmt.Errorf("authenticate %s: %w", b.Name, err)
	}

	now := time.Now()
	tok, err := auth.NewToken(raw, now, auth.SourceAuthenticate)
	if errors.Is(err, auth.ErrNotJWT) {
		// Still usable for logon; only the expiry is unknown.
		tok = auth.Token{RefreshToken: strings.TrimSpace(raw), ObtainedAt: now.UTC(), Source: auth.SourceAuthenticate}
	} else if err != nil {
		return err
	}
	store := auth.NewStore(cfg.BotStateDir(b))
	if err := store.Save(tok); err != nil {
		return fmt.Errorf("save token: %w", err)
	}

	_, _ = fmt.Fprintf(c.stdout, "bot %s: refresh token saved to %s\n", b.Name, store.Path())
	if tok.SteamID != 0 {
		_, _ = fmt.Fprintf(c.stdout, "  steam_id:   %d\n", tok.SteamID)
	}
	if !tok.ExpiresAt.IsZero() {
		_, _ = fmt.Fprintf(c.stdout, "  expires_at: %s\n", tok.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if !b.Credentials.Headless() {
		_, _ = fmt.Fprintln(c.stdout, "  note: no shared_secret, so this bot cannot re-authenticate on its own when the token dies")
	}
	return nil
}

// guardPrompt answers Steam Guard inside the auth session: --code first,
// then the terminal. With neither, it returns nil so the adapter fails
// fast instead of waiting for a phone approval.
func (c cli) guardPrompt(code string) func(ctx context.Context, kinds []gc.GuardKind) (string, error) {
	interactive := isTerminal(c.stdin)
	if code == "" && !interactive {
		return nil
	}
	used := false
	in := bufio.NewReader(c.stdin)
	return func(ctx context.Context, kinds []gc.GuardKind) (string, error) {
		if code != "" && !used {
			used = true
			return code, nil
		}
		if !interactive {
			return "", fmt.Errorf("steam rejected --code and asked again (%v): %w", kinds, gc.ErrSteamGuardRequired)
		}
		_, _ = fmt.Fprintf(c.stderr, "Steam Guard code required (%s): ", guardNames(kinds))
		line, err := readLine(ctx, in)
		if err != nil {
			return "", fmt.Errorf("read guard code: %w", err)
		}
		if line == "" {
			return "", gc.ErrSteamGuardRequired
		}
		return line, nil
	}
}

func guardNames(kinds []gc.GuardKind) string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = string(k)
	}
	return strings.Join(out, ", ")
}

// readLine reads one trimmed line, giving up when ctx ends.
func readLine(ctx context.Context, r *bufio.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := r.ReadString('\n')
		if errors.Is(err, io.EOF) && line != "" {
			err = nil
		}
		ch <- result{strings.TrimSpace(line), err}
	}()
	select {
	case res := <-ch:
		return res.line, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
