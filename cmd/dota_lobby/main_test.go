package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/auth"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
	"github.com/kettleofketchup/dota_lobby/pkg/gc/fake"
)

// syncBuffer is a bytes.Buffer safe for the serve goroutine and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newCLI() (cli, *syncBuffer, *syncBuffer) {
	out, errb := &syncBuffer{}, &syncBuffer{}
	return cli{stdin: strings.NewReader(""), stdout: out, stderr: errb}, out, errb
}

func TestRunDispatch(t *testing.T) {
	tests := []struct {
		args []string
		want int
	}{
		{[]string{"version"}, exitOK},
		{[]string{"help"}, exitOK},
		{[]string{"bogus"}, exitUsage},
		{[]string{"serve", "extra"}, exitUsage},
		{[]string{"serve", "--nope"}, exitUsage},
		{[]string{"auth"}, exitUsage},
		{[]string{"healthcheck", "-h"}, exitOK},
	}
	for _, tt := range tests {
		c, _, _ := newCLI()
		if got := c.run(context.Background(), tt.args); got != tt.want {
			t.Errorf("run(%q) = %d, want %d", tt.args, got, tt.want)
		}
	}
}

func TestHealthcheck(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   int
	}{
		{"alive", http.StatusOK, exitOK},
		{"stalled", http.StatusServiceUnavailable, exitError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/livez" {
					t.Errorf("probed %s", r.URL.Path)
				}
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()
			c, _, _ := newCLI()
			if got := c.run(context.Background(), []string{"healthcheck", "--url", srv.URL + "/livez"}); got != tt.want {
				t.Errorf("exit %d, want %d", got, tt.want)
			}
		})
	}
	t.Run("nothing listening", func(t *testing.T) {
		c, _, _ := newCLI()
		if got := c.run(context.Background(), []string{"healthcheck", "--url", "http://" + freeAddr(t) + "/livez", "--timeout", "2s"}); got != exitError {
			t.Errorf("exit %d, want %d", got, exitError)
		}
	})
}

func TestDefaultHealthURL(t *testing.T) {
	tests := []struct {
		name   string
		config string // config.yaml contents; empty means no file
		env    map[string]string
		want   string
	}{
		{"defaults", "", nil, "http://127.0.0.1:8080/livez"},
		{"port from config.yaml", "server:\n  port: 9321\n", nil, "http://127.0.0.1:9321/livez"},
		{"env beats config.yaml", "server:\n  port: 9321\n", map[string]string{"DOTA_LOBBY_SERVER_PORT": "9123"}, "http://127.0.0.1:9123/livez"},
		{"wildcard host becomes loopback", "server:\n  host: 0.0.0.0\n  port: 9321\n", nil, "http://127.0.0.1:9321/livez"},
		{"empty host becomes loopback", "server:\n  host: \"\"\n  port: 9321\n", nil, "http://127.0.0.1:9321/livez"},
		{"ipv6 wildcard", "server:\n  host: \"::\"\n", nil, "http://[::1]:8080/livez"},
		{"specific host kept", "server:\n  host: 10.1.2.3\n  port: 9321\n", nil, "http://10.1.2.3:9321/livez"},
		// The probe must not need the secrets or the bots to be valid.
		{"unreadable secrets are not needed", "server:\n  port: 9321\n", map[string]string{"DOTA_LOBBY_SECRETS_FILE": "/nonexistent/secrets.yaml"}, "http://127.0.0.1:9321/livez"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{"CONFIG_FILE", "CONFIG_DIR", "SECRETS_FILE", "SECRETS_DIR", "SERVER_PORT", "SERVER_HOST"} {
				t.Setenv("DOTA_LOBBY_"+k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			var cf configFlags
			if tt.config != "" {
				cf.configFile = filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(cf.configFile, []byte(tt.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := defaultHealthURL(cf)
			if err != nil || got != tt.want {
				t.Errorf("defaultHealthURL() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	t.Run("bad port is an error", func(t *testing.T) {
		t.Setenv("DOTA_LOBBY_SERVER_PORT", "70000")
		t.Setenv("DOTA_LOBBY_CONFIG_FILE", "")
		if _, err := defaultHealthURL(configFlags{}); err == nil {
			t.Error("want an error for port 70000")
		}
	})
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// writeConfig writes a config and secrets file into a temp dir and returns
// the serve/auth flags for them.
func writeConfig(t *testing.T, port string, bots string, secrets string) (flags []string, stateDir string) {
	t.Helper()
	dir := t.TempDir()
	stateDir = filepath.Join(dir, "state")
	cfg := fmt.Sprintf("server: { host: 127.0.0.1, port: %s }\nstate_dir: %s\nlog: { level: debug, format: text }\nreadiness: { min_ready_bots: 0 }\n%s", port, stateDir, bots)
	cfgPath := filepath.Join(dir, "config.yaml")
	secPath := filepath.Join(dir, "secrets.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secPath, []byte(secrets), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"--config", cfgPath, "--secrets", secPath}, stateDir
}

func TestServeZeroBotsStartsAndStops(t *testing.T) {
	addr := freeAddr(t)
	_, port, _ := net.SplitHostPort(addr)
	flags, stateDir := writeConfig(t, port, "bots: []\n", "secrets: { api_key: k }\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, _, stderr := newCLI()
	done := make(chan int, 1)
	go func() { done <- c.run(ctx, append([]string{"serve"}, flags...)) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/livez")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("livez = %d", resp.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve never answered: %v\n%s", err, stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	// While serving, the state dir is locked against a second process.
	if _, err := auth.AcquireLock(stateDir); err == nil {
		t.Error("state dir not locked while serving")
	}

	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("serve exit %d\n%s", code, stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not stop")
	}
	if !strings.Contains(stderr.String(), "stopped cleanly") {
		t.Errorf("no clean-stop log:\n%s", stderr.String())
	}
	lock, err := auth.AcquireLock(stateDir)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	_ = lock.Release()
}

func TestServeFailsWhenStateDirLocked(t *testing.T) {
	_, port, _ := net.SplitHostPort(freeAddr(t))
	flags, stateDir := writeConfig(t, port, "bots: []\n", "{}\n")
	lock, err := auth.AcquireLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	c, _, stderr := newCLI()
	if got := c.run(context.Background(), append([]string{"serve"}, flags...)); got != exitError {
		t.Fatalf("exit %d, want %d", got, exitError)
	}
	if !strings.Contains(stderr.String(), "locked") {
		t.Errorf("stderr does not mention the lock: %s", stderr.String())
	}
}

// testJWT builds an unsigned refresh-token-shaped JWT.
func testJWT(t *testing.T, sub string, exp time.Time) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"sub": sub, "iat": exp.Add(-200 * 24 * time.Hour).Unix(), "exp": exp.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"EdDSA","typ":"JWT"}`)) + "." + enc(b) + ".c2ln"
}

// A made-up but valid base64 TOTP seed.
var testSharedSecret = base64.StdEncoding.EncodeToString([]byte("01234567890123456789"))

func useFakeAuthenticator(t *testing.T, fc *fake.Client) {
	t.Helper()
	orig := newAuthenticator
	newAuthenticator = func(*slog.Logger) authenticator { return fc }
	t.Cleanup(func() { newAuthenticator = orig })
}

func TestAuthCommand(t *testing.T) {
	bots := "bots:\n  - name: bot-a\n    username: bota\n  - name: bot-b\n"
	secrets := fmt.Sprintf("secrets:\n  bots:\n    bot-a: { password: hunter2, shared_secret: %q }\n    bot-b: {}\n", testSharedSecret)
	exp := time.Now().Add(200 * 24 * time.Hour).Truncate(time.Second)
	minted := testJWT(t, "76561198000000001", exp)

	tests := []struct {
		name     string
		args     []string
		lock     bool
		want     int
		wantTOTP bool
		saved    bool
	}{
		{"headless bot", []string{"--bot", "BOT-A"}, false, exitOK, true, true},
		{"unknown bot", []string{"--bot", "nobody"}, false, exitError, false, false},
		{"no password", []string{"--bot", "bot-b"}, false, exitError, false, false},
		{"service running", []string{"--bot", "bot-a"}, true, exitError, false, false},
		{"service running, forced", []string{"--bot", "bot-a", "--force"}, true, exitOK, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags, stateDir := writeConfig(t, "8080", bots, secrets)
			if tt.lock {
				l, err := auth.AcquireLock(stateDir)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = l.Release() }()
			}
			fc := fake.New()
			fc.SetToken(minted)
			useFakeAuthenticator(t, fc)

			c, stdout, stderr := newCLI()
			got := c.run(context.Background(), append(append([]string{"auth"}, tt.args...), flags...))
			if got != tt.want {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", got, tt.want, stdout, stderr)
			}
			calls := fc.Calls("Authenticate")
			if tt.wantTOTP {
				if len(calls) != 1 {
					t.Fatalf("Authenticate calls = %d", len(calls))
				}
				creds := calls[0].Args[0].(gc.Credentials)
				if creds.Username != "bota" || creds.Password.Reveal() != "hunter2" || len(creds.TwoFactorCode.Reveal()) != 5 {
					t.Errorf("credentials: user %q, code %q", creds.Username, creds.TwoFactorCode)
				}
				// No terminal and no --code: a guard prompt must fail fast.
				if creds.SteamGuard != nil {
					t.Error("SteamGuard set without a terminal or --code")
				}
			}
			tok, err := auth.NewStore(filepath.Join(stateDir, "bots", "bota")).Load()
			if tt.saved {
				if err != nil || tok.RefreshToken != minted || tok.SteamID != 76561198000000001 || tok.Source != auth.SourceAuthenticate {
					t.Fatalf("stored token = %v, %v", tok, err)
				}
				if !strings.Contains(stdout.String(), "76561198000000001") || strings.Contains(stdout.String(), minted) {
					t.Errorf("stdout: %s", stdout)
				}
			} else if err == nil {
				t.Errorf("a token was saved: %v", tok)
			}
		})
	}
}

func TestGuardPromptCode(t *testing.T) {
	c, _, _ := newCLI()
	prompt := c.guardPrompt("ABCDE")
	if prompt == nil {
		t.Fatal("no prompt with --code")
	}
	ctx := context.Background()
	if code, err := prompt(ctx, []gc.GuardKind{gc.GuardDeviceCode}); err != nil || code != "ABCDE" {
		t.Fatalf("first prompt = %q, %v", code, err)
	}
	// A second ask means Steam rejected the code; without a terminal, stop.
	if _, err := prompt(ctx, []gc.GuardKind{gc.GuardDeviceCode}); !errors.Is(err, gc.ErrSteamGuardRequired) {
		t.Fatalf("second prompt err = %v", err)
	}
}
