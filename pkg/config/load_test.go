package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sharedSecret is a syntactically valid, made-up TOTP seed.
const sharedSecret = "AAECAwQFBgcICQoLDA0ODxAREhM="

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// isolate clears the env that steers file lookup so the host cannot leak in.
func isolate(t *testing.T) {
	t.Helper()
	for _, k := range []string{"CONFIG_FILE", "SECRETS_FILE", "CONFIG_DIR", "SECRETS_DIR"} {
		t.Setenv(EnvPrefix+"_"+k, "")
	}
}

type files struct {
	config, secrets string
}

func load(t *testing.T, f files) (*Config, error) {
	t.Helper()
	isolate(t)
	dir := t.TempDir()
	opts := Options{SearchPaths: []string{}}
	if f.config != "" {
		opts.ConfigFile = write(t, dir, "config.yaml", f.config)
	}
	if f.secrets != "" {
		opts.SecretsFile = write(t, dir, "secrets.yaml", f.secrets)
	}
	return Load(opts)
}

func mustLoad(t *testing.T, f files) *Config {
	t.Helper()
	cfg, err := load(t, f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestLoadDefaults(t *testing.T) {
	cfg := mustLoad(t, files{})
	if cfg.Server.Host != "0.0.0.0" || cfg.Server.Port != 8080 || cfg.Server.MaxBodyBytes != 65536 {
		t.Errorf("server defaults: %+v", cfg.Server)
	}
	if cfg.StateDir != "/var/lib/dota_lobby" || cfg.Store.Path != "/var/lib/dota_lobby/dota_lobby.db" {
		t.Errorf("state_dir %q store %q", cfg.StateDir, cfg.Store.Path)
	}
	if cfg.Steam.LoginStagger != 10*time.Second || cfg.Steam.ReconnectBackoffMax != 5*time.Minute {
		t.Errorf("steam defaults: %+v", cfg.Steam)
	}
	if cfg.GC.HelloInterval != 10*time.Second || cfg.GC.CreateTimeout != 2*time.Minute {
		t.Errorf("gc defaults: %+v", cfg.GC)
	}
	if fmt.Sprint(cfg.League.Sources) != "[gc config]" || len(cfg.Lobby.HostSeatStrategy) != 4 {
		t.Errorf("league %v seats %v", cfg.League.Sources, cfg.Lobby.HostSeatStrategy)
	}
	if cfg.Lobby.Regions["europe"] != 3 || cfg.Lobby.GameModes["turbo"] != 23 {
		t.Errorf("tables: %v %v", cfg.Lobby.Regions, cfg.Lobby.GameModes)
	}
	if len(cfg.Bots) != 0 || cfg.Secrets.APIKey.IsSet() {
		t.Errorf("unexpected bots or api key")
	}
}

const fullConfig = `
server: { host: 127.0.0.1, port: 8081 }
state_dir: /srv/state
log: { level: debug, format: text }
steam: { login_stagger: 3s }
gc: { hello_interval: 15s }
lobby:
  regions: { us_west: 7 }
webhooks:
  - url: https://league.internal/hooks/dota
    events: ["lobby.*"]
bots:
  - name: league_bot_1
    leagues: [20004]
    tags: [na]
  - name: bot-b
    username: Steam_User_B
    enabled: false
`

const fullSecrets = `
secrets:
  api_key: the-api-key
  webapi_key: the-webapi-key
  webhook_secrets:
    "https://league.internal/hooks/dota": hook-secret
  bots:
    league_bot_1:
      password: pw1
      shared_secret: ` + sharedSecret + `
      device_name: dota_lobby-1
    steam_user_b:
      password: pw2
`

func TestLoadFull(t *testing.T) {
	cfg := mustLoad(t, files{config: fullConfig, secrets: fullSecrets})

	if cfg.Server.Port != 8081 || cfg.Log.Level != "debug" || cfg.Steam.LoginStagger != 3*time.Second || cfg.GC.HelloInterval != 15*time.Second {
		t.Errorf("file values not applied: %+v %+v %+v", cfg.Server, cfg.Log, cfg.Steam)
	}
	if cfg.Store.Path != "/srv/state/dota_lobby.db" {
		t.Errorf("store path %q", cfg.Store.Path)
	}
	if cfg.Lobby.Regions["us_west"] != 7 || cfg.Lobby.Regions["europe"] != 3 {
		t.Errorf("regions should merge over defaults: %v", cfg.Lobby.Regions)
	}
	if cfg.Secrets.APIKey.Reveal() != "the-api-key" || cfg.Secrets.WebAPIKey.Reveal() != "the-webapi-key" {
		t.Errorf("secrets not loaded")
	}
	if got := cfg.Secrets.WebhookSecret("https://league.internal/hooks/dota").Reveal(); got != "hook-secret" {
		t.Errorf("webhook secret %q", got)
	}

	if len(cfg.Bots) != 2 {
		t.Fatalf("bots: %+v", cfg.Bots)
	}
	a, b := cfg.Bots[0], cfg.Bots[1]
	if a.Name != "league_bot_1" || a.Username != "league_bot_1" || !a.Enabled || a.Leagues[0] != 20004 || a.Tags[0] != "na" {
		t.Errorf("bot a: %+v", a)
	}
	if a.Credentials.Password.Reveal() != "pw1" || !a.Credentials.Headless() || a.Credentials.DeviceName != "dota_lobby-1" {
		t.Errorf("bot a creds: %+v", a.Credentials)
	}
	// bot-b has no secrets under its name, so they are found by username.
	if b.Enabled || b.Username != "Steam_User_B" || b.Credentials.Password.Reveal() != "pw2" || b.Credentials.Headless() {
		t.Errorf("bot b: %+v", b)
	}
	if got := cfg.BotStateDir(b); got != "/srv/state/bots/steam_user_b" {
		t.Errorf("BotStateDir = %q", got)
	}
	if _, ok := cfg.Bot("LEAGUE_BOT_1"); !ok {
		t.Errorf("Bot lookup should ignore case")
	}

	// The loaded config is safe to log whole.
	j, _ := json.Marshal(cfg)
	for _, out := range []string{fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg), string(j)} {
		for _, s := range []string{"pw1", "pw2", "the-api-key", "hook-secret", sharedSecret} {
			if strings.Contains(out, s) {
				t.Fatalf("formatted config leaks %q: %s", s, out)
			}
		}
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	isolate(t)
	t.Setenv("DOTA_LOBBY_SERVER_PORT", "9999")
	t.Setenv("DOTA_LOBBY_STEAM_LOGIN_STAGGER", "1m")
	t.Setenv("DOTA_LOBBY_LEAGUE_SOURCES", "gc")
	t.Setenv("DOTA_LOBBY_STATE_DIR", "/tmp/state")
	t.Setenv("DOTA_LOBBY_SECRETS_API_KEY", "env-key")
	// bot-b is in config.yaml but has no secrets entry at all.
	t.Setenv("DOTA_LOBBY_SECRETS_BOTS_BOT_B_PASSWORD", "env-pw-b")
	t.Setenv("DOTA_LOBBY_SECRETS_BOTS_LEAGUE_BOT_1_PASSWORD", "env-pw-1")

	cfg := mustLoad(t, files{config: `
bots:
  - name: league_bot_1
  - name: bot-b
`, secrets: `
secrets:
  api_key: file-key
  bots:
    league_bot_1: { password: file-pw }
`})
	if cfg.Server.Port != 9999 || cfg.Steam.LoginStagger != time.Minute || fmt.Sprint(cfg.League.Sources) != "[gc]" {
		t.Errorf("env not applied: port %d stagger %s sources %v", cfg.Server.Port, cfg.Steam.LoginStagger, cfg.League.Sources)
	}
	if cfg.StateDir != "/tmp/state" || cfg.Store.Path != "/tmp/state/dota_lobby.db" {
		t.Errorf("state_dir %q store %q", cfg.StateDir, cfg.Store.Path)
	}
	if cfg.Secrets.APIKey.Reveal() != "env-key" {
		t.Errorf("api key %q", cfg.Secrets.APIKey.Reveal())
	}
	if got := cfg.Bots[0].Credentials.Password.Reveal(); got != "env-pw-1" {
		t.Errorf("env should beat secrets.yaml, got %q", got)
	}
	if got := cfg.Bots[1].Credentials.Password.Reveal(); got != "env-pw-b" {
		t.Errorf("env for a bot with no secrets entry, got %q", got)
	}
}

func TestLoadFileEnv(t *testing.T) {
	dir := t.TempDir()
	keyFile := write(t, dir, "api_key", "file-api-key\n")
	ssFile := write(t, dir, "ss", sharedSecret+"\r\n")
	cfgYAML := "bots:\n  - name: bot-a\n"

	t.Run("reads and trims", func(t *testing.T) {
		t.Setenv("DOTA_LOBBY_SECRETS_API_KEY_FILE", keyFile)
		t.Setenv("DOTA_LOBBY_SECRETS_BOTS_BOT_A_SHARED_SECRET_FILE", ssFile)
		t.Setenv("DOTA_LOBBY_SERVER_PORT_FILE", write(t, dir, "port", "7000\n"))
		cfg := mustLoad(t, files{config: cfgYAML, secrets: "secrets: { api_key: from-yaml }\n"})
		if cfg.Secrets.APIKey.Reveal() != "file-api-key" {
			t.Errorf("api key %q", cfg.Secrets.APIKey.Reveal())
		}
		if cfg.Bots[0].Credentials.SharedSecret.Reveal() != sharedSecret {
			t.Errorf("shared secret %q", cfg.Bots[0].Credentials.SharedSecret.Reveal())
		}
		if cfg.Server.Port != 7000 {
			t.Errorf("port %d", cfg.Server.Port)
		}
	})

	errCases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"both set", map[string]string{"DOTA_LOBBY_SECRETS_API_KEY": "x", "DOTA_LOBBY_SECRETS_API_KEY_FILE": keyFile}, "both DOTA_LOBBY_SECRETS_API_KEY and"},
		{"missing file", map[string]string{"DOTA_LOBBY_SECRETS_API_KEY_FILE": filepath.Join(dir, "nope")}, "DOTA_LOBBY_SECRETS_API_KEY_FILE"},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := load(t, files{config: cfgYAML})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadCredentialFiles(t *testing.T) {
	sopsYAML := "STEAM_USER: real_user\nSTEAM_PASS: sops-pw\nSTEAM_SHARED_SECRET: " + sharedSecret + "\nSTEAM_REFRESH_TOKEN: eyJ.token\n"
	dotenv := "# decrypted\nexport STEAM_USER=real_user\nSTEAM_PASS=\"dot pw\"\nSTEAM_REFRESH_TOKEN='eyJ.dot'\n"

	cases := []struct {
		name     string
		file     string
		secrets  string
		env      map[string]string
		username string
		password string
		token    string
	}{
		{name: "sops yaml", file: sopsYAML, username: "real_user", password: "sops-pw", token: "eyJ.token"},
		{name: "dotenv", file: dotenv, username: "real_user", password: "dot pw", token: "eyJ.dot"},
		{name: "json", file: `{"STEAM_PASS": "json-pw", "STEAM_REFRESH_TOKEN": ""}`, username: "bot-a", password: "json-pw"},
		{
			name: "file beats secrets.yaml, env beats file", file: sopsYAML,
			secrets:  "secrets:\n  bots:\n    bot-a: { password: yaml-pw, refresh_token: yaml-token, device_name: dev }\n",
			env:      map[string]string{"DOTA_LOBBY_SECRETS_BOTS_BOT_A_REFRESH_TOKEN": "env-token"},
			username: "real_user", password: "sops-pw", token: "env-token",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			credFile := write(t, t.TempDir(), "bot-a.yaml", tc.file)
			cfg := mustLoad(t, files{
				config:  fmt.Sprintf("bots:\n  - name: bot-a\n    credentials_file: %s\n", credFile),
				secrets: tc.secrets,
			})
			b := cfg.Bots[0]
			if b.Username != tc.username || b.Credentials.Password.Reveal() != tc.password || b.Credentials.RefreshToken.Reveal() != tc.token {
				t.Fatalf("got user %q pass %q token %q", b.Username, b.Credentials.Password.Reveal(), b.Credentials.RefreshToken.Reveal())
			}
		})
	}
}

func TestLoadCredentialsDir(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bot-a.sops.yaml", "STEAM_USER: user_a\nSTEAM_PASS: pa\n")
	write(t, dir, "bot-b.env", "STEAM_USER=user_b\nSTEAM_REFRESH_TOKEN=tb\n")
	write(t, dir, "README.txt", "ignored")

	t.Run("discovers bots", func(t *testing.T) {
		cfg := mustLoad(t, files{config: "credentials_dir: " + dir + "\n"})
		if len(cfg.Bots) != 2 {
			t.Fatalf("bots: %+v", cfg.Bots)
		}
		a, b := cfg.Bots[0], cfg.Bots[1]
		if a.Name != "bot-a" || a.Username != "user_a" || a.Credentials.Password.Reveal() != "pa" {
			t.Errorf("bot a: %+v", a)
		}
		if b.Name != "bot-b" || b.Username != "user_b" || b.Credentials.RefreshToken.Reveal() != "tb" {
			t.Errorf("bot b: %+v", b)
		}
	})

	t.Run("bots list wins and env sets the dir", func(t *testing.T) {
		t.Setenv("DOTA_LOBBY_CREDENTIALS_DIR", dir)
		cfg := mustLoad(t, files{config: "bots:\n  - name: bot-b\n"})
		if len(cfg.Bots) != 1 || cfg.Bots[0].Username != "user_b" {
			t.Fatalf("bots: %+v", cfg.Bots)
		}
	})
}

func TestLoadLegacySecrets(t *testing.T) {
	cfg := mustLoad(t, files{
		config: "server:\n  api_key: legacy-key\n",
		secrets: `
secrets:
  bots:
    - username: old_bot
      password: old-pw
      sentry_hash: ""
`,
	})
	if len(cfg.Bots) != 1 || cfg.Bots[0].Name != "old_bot" || cfg.Bots[0].Credentials.Password.Reveal() != "old-pw" {
		t.Fatalf("legacy bots: %+v", cfg.Bots)
	}
	if cfg.Secrets.APIKey.Reveal() != "legacy-key" || cfg.Server.APIKey.IsSet() {
		t.Fatalf("server.api_key should move to secrets.api_key")
	}
}

func TestLoadErrors(t *testing.T) {
	encrypted := write(t, t.TempDir(), "enc.yaml", "STEAM_PASS: ENC[AES256_GCM,data:x]\nsops:\n  version: 3.9.0\n")
	mismatch := write(t, t.TempDir(), "m.yaml", "STEAM_USER: someone_else\n")

	cases := []struct {
		name    string
		config  string
		secrets string
		want    []string
	}{
		{"unknown key", "server: { prot: 1 }\n", "", []string{"prot"}},
		{"unknown bot key", "bots:\n  - name: a\n    leauges: [1]\n", "", []string{"leauges"}},
		{"bad duration", "gc: { hello_interval: soon }\n", "", []string{"hello_interval"}},
		{"many problems at once", `
server: { port: 70000 }
log: { level: loud, format: xml }
steam: { reconnect_backoff_min: 10m, reconnect_backoff_max: 1m }
league: { sources: [gc, portal] }
lobby: { host_seat_strategy: [] }
webhooks: [{ url: "ftp://x", events: [] }]
`, "", []string{"server.port", "log.level", "log.format", "reconnect_backoff_max", "portal", "host_seat_strategy must not be empty", "webhooks[0].url", "webhooks[0].events"}},
		{"bot name", "bots:\n  - name: ../evil\n", "", []string{"name \"../evil\""}},
		{"bot name missing", "bots:\n  - tags: [x]\n", "", []string{"name is required"}},
		{"duplicate bots", "bots:\n  - name: bot-a\n  - name: BOT_A\n", "", []string{"collide"}},
		{"shared account", "bots:\n  - { name: a, username: same }\n  - { name: b, username: SAME }\n", "", []string{"same Steam account"}},
		{"bad username", "bots:\n  - { name: a, username: \"no/slash\" }\n", "", []string{"username"}},
		{"bad shared secret", "bots:\n  - name: a\n", "secrets:\n  bots:\n    a: { shared_secret: \"not base64!\" }\n", []string{"shared_secret is not valid base64"}},
		{"encrypted credentials", "bots:\n  - { name: a, credentials_file: " + encrypted + " }\n", "", []string{"still sops-encrypted"}},
		{"STEAM_USER mismatch", "bots:\n  - { name: a, username: me, credentials_file: " + mismatch + " }\n", "", []string{"differs from STEAM_USER"}},
		{"encrypted secrets file", "", "secrets: {}\nsops: { version: 3 }\n", []string{"still sops-encrypted"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(t, files{config: tc.config, secrets: tc.secrets})
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %q", err, w)
				}
			}
			if strings.Contains(err.Error(), "not base64!") {
				t.Errorf("error echoes the secret: %v", err)
			}
		})
	}
}

func TestLoadExplicitFileMissing(t *testing.T) {
	isolate(t)
	_, err := Load(Options{ConfigFile: filepath.Join(t.TempDir(), "missing.yaml"), SearchPaths: []string{}})
	if err == nil || !strings.Contains(err.Error(), "config file") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadSearchPaths(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	write(t, dir, "config.yml", "server: { port: 1234 }\n")
	write(t, dir, "secrets.yaml", "secrets: { api_key: k }\n")
	cfg, err := Load(Options{SearchPaths: []string{filepath.Join(dir, "empty"), dir}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 1234 || cfg.Secrets.APIKey.Reveal() != "k" {
		t.Fatalf("search paths not used: port %d", cfg.Server.Port)
	}
}

func TestLoadEnvFileLocations(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	t.Setenv("DOTA_LOBBY_CONFIG_FILE", write(t, dir, "c.yaml", "server: { port: 4321 }\n"))
	t.Setenv("DOTA_LOBBY_SECRETS_FILE", write(t, dir, "s.yaml", "secrets: { api_key: k2 }\n"))
	cfg, err := Load(Options{SearchPaths: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 4321 || cfg.Secrets.APIKey.Reveal() != "k2" {
		t.Fatalf("env file locations not used")
	}
}

func TestParseDotenv(t *testing.T) {
	got, err := parseDotenv([]byte("A=1\n\n# c\nexport B = two \nC=\"x\\ty\"\nD='q'\nE=line1\\nline2\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "two", "C": "x\ty", "D": "q", "E": "line1\nline2"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if _, err := parseDotenv([]byte("just words\n")); err == nil {
		t.Fatal("expected error for a non KEY=VALUE line")
	}
}
