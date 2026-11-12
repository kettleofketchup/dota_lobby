package config

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const simBots = "state_dir: /tmp/sim-state\nbots:\n  - name: bot1\n    leagues: [100]\n  - name: bot2\n"

func TestSimulationDefaults(t *testing.T) {
	cfg := mustLoad(t, files{config: simBots})
	s := cfg.Simulation
	if s.Enabled || s.AutoJoin || s.AutoReady || s.AutoStart || s.AutoJoinDelay != time.Second || s.AutoStartDelay != 5*time.Second {
		t.Errorf("defaults = %+v", s)
	}
	if cfg.Store.Path != filepath.Join("/tmp/sim-state", "dota_lobby.db") {
		t.Errorf("store path = %q", cfg.Store.Path)
	}
}

func TestSimulationConfig(t *testing.T) {
	cfg := mustLoad(t, files{config: simBots + `simulation:
  enabled: true
  auto_join: true
  auto_join_delay: 250ms
  auto_ready: true
  bots:
    - name: BOT2
      account_id: 4100000000
      leagues: [200, 300]
`})
	s := cfg.Simulation
	if !s.Enabled || !s.AutoJoin || !s.AutoReady || s.AutoJoinDelay != 250*time.Millisecond {
		t.Errorf("simulation = %+v", s)
	}
	b1, b2 := cfg.SimulatedBot(0), cfg.SimulatedBot(1)
	if b1.Name != "bot1" || b1.AccountID != SimulationAccountBase+1 || !slices.Equal(b1.Leagues, []uint32{100}) {
		t.Errorf("bot1 = %+v", b1)
	}
	if b2.Name != "bot2" || b2.AccountID != 4_100_000_000 || !slices.Equal(b2.Leagues, []uint32{200, 300}) {
		t.Errorf("bot2 = %+v", b2)
	}
	// A simulated run gets its own database and backups next to the real
	// ones.
	if cfg.Store.Path != filepath.Join("/tmp/sim-state", SimulationStoreFile) ||
		cfg.Store.Backup.Dir != filepath.Join("/tmp/sim-state", SimulationBackupDir) {
		t.Errorf("store = %+v", cfg.Store)
	}
}

func TestSimulationEnv(t *testing.T) {
	t.Setenv("DOTA_LOBBY_SIMULATION_ENABLED", "true")
	t.Setenv("DOTA_LOBBY_SIMULATION_AUTO_START", "true")
	cfg := mustLoad(t, files{config: simBots})
	if !cfg.Simulation.Enabled || !cfg.Simulation.AutoStart {
		t.Errorf("simulation = %+v", cfg.Simulation)
	}
}

// Real credentials and simulation never mix.
func TestSimulationRefusesCredentials(t *testing.T) {
	_, err := load(t, files{
		config:  simBots + "simulation: { enabled: true }\n",
		secrets: "secrets:\n  bots:\n    bot1: { password: \"hunter2\" }\n    bot2: { refresh_token: \"tok\", shared_secret: \"" + sharedSecret + "\" }\n",
	})
	if err == nil {
		t.Fatal("loaded a simulated config with credentials")
	}
	for _, want := range []string{"refuses bot bot1", "(password)", "refuses bot bot2", "shared_secret, refresh_token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Error("error leaks the password")
	}
}

// loadSimulate loads like load, with Options.Simulate set (serve
// --simulate), and returns what Load logged.
func loadSimulate(t *testing.T, f files) (*Config, string, error) {
	t.Helper()
	isolate(t)
	dir := t.TempDir()
	var logs bytes.Buffer
	opts := Options{SearchPaths: []string{}, Simulate: true, Logger: slog.New(slog.NewTextHandler(&logs, nil))}
	if f.config != "" {
		opts.ConfigFile = write(t, dir, "config.yaml", f.config)
	}
	if f.secrets != "" {
		opts.SecretsFile = write(t, dir, "secrets.yaml", f.secrets)
	}
	cfg, err := Load(opts)
	return cfg, logs.String(), err
}

// --simulate turns the mode on during Load, with the same checks.
func TestSimulateOption(t *testing.T) {
	cfg, logs, err := loadSimulate(t, files{config: simBots})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Simulation.Enabled || cfg.Store.Path != filepath.Join("/tmp/sim-state", SimulationStoreFile) {
		t.Errorf("with Simulate: %+v %q", cfg.Simulation, cfg.Store.Path)
	}
	// Simulated bots need no credentials, so they are not warned about.
	if strings.Contains(logs, "no password or refresh token") {
		t.Errorf("credential warning in simulation mode:\n%s", logs)
	}

	// Explicit store paths stay where they are.
	cfg, _, err = loadSimulate(t, files{config: simBots + "store: { path: /data/x.db, backup: { dir: /data/bk } }\n"})
	if err != nil || cfg.Store.Path != "/data/x.db" || cfg.Store.Backup.Dir != "/data/bk" {
		t.Errorf("explicit paths: %+v %v", cfg.Store, err)
	}

	_, _, err = loadSimulate(t, files{config: simBots, secrets: "secrets:\n  bots:\n    bot2: { password: \"x\" }\n"})
	if err == nil || !strings.Contains(err.Error(), "refuses bot bot2") {
		t.Errorf("credentials with --simulate: %v", err)
	}
}

func TestSimulationValidation(t *testing.T) {
	tests := []struct {
		name, sim, want string
	}{
		{"negative delay", "auto_join_delay: -1s", "auto_join_delay must not be negative"},
		{"negative start delay", "auto_start_delay: -1s", "auto_start_delay must not be negative"},
		{"unknown bot", "bots: [{ name: nobody }]", `no bot named "nobody"`},
		{"twice", "bots: [{ name: bot1 }, { name: BOT1 }]", "listed twice"},
		{"same account", "bots: [{ name: bot2, account_id: 4000000001 }]", "same account_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, files{config: simBots + "simulation:\n  enabled: true\n  " + tt.sim + "\n"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
	// Not checked while simulation is off.
	if _, err := load(t, files{config: simBots + "simulation:\n  bots: [{ name: nobody }]\n"}); err != nil {
		t.Errorf("disabled simulation validated: %v", err)
	}
}
