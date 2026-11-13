// Package config loads the service configuration: config.yaml for settings,
// a secrets file for credentials, per-bot credential files in the sops format,
// then DOTA_LOBBY_* environment overrides and DOTA_LOBBY_*_FILE indirection.
// See research 07 §3.
package config

import (
	"path/filepath"
	"strings"
	"time"
)

// Config is the fully loaded and validated service configuration.
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	Metrics   MetricsConfig   `mapstructure:"metrics"`
	StateDir  string          `mapstructure:"state_dir"`
	Log       LogConfig       `mapstructure:"log"`
	Readiness ReadinessConfig `mapstructure:"readiness"`
	Steam     SteamConfig     `mapstructure:"steam"`
	GC        GCConfig        `mapstructure:"gc"`
	League    LeagueConfig    `mapstructure:"league"`
	Lobby     LobbyConfig     `mapstructure:"lobby"`
	Match     MatchConfig     `mapstructure:"match"`
	Store     StoreConfig     `mapstructure:"store"`
	Webhooks  []WebhookConfig `mapstructure:"webhooks"`
	// WebhookDelivery applies to every webhook.
	WebhookDelivery WebhookDeliveryConfig `mapstructure:"webhook_delivery"`
	// Simulation runs the service against a simulated Steam and GC.
	Simulation SimulationConfig `mapstructure:"simulation"`

	// CredentialsDir holds one decrypted credentials file per bot, named
	// <bot>.yaml (or .yml, .json, .env), typically a tmpfs filled from the
	// sops files. When the bots list is empty every file there becomes a bot.
	CredentialsDir string `mapstructure:"credentials_dir"`

	// Bots is built from the bots list with credentials attached.
	Bots []BotConfig `mapstructure:"-"`
	// Secrets holds the service-wide secrets; bot credentials are on Bots.
	Secrets Secrets `mapstructure:"-"`
}

// ServerConfig is the HTTP API listener.
type ServerConfig struct {
	Host         string `mapstructure:"host"`
	Port         int    `mapstructure:"port"`
	MaxBodyBytes int64  `mapstructure:"max_body_bytes"`

	// Deprecated: set secrets.api_key. Load moves a value found here into
	// Secrets.APIKey and clears this field.
	APIKey Secret `mapstructure:"api_key"`
}

// MetricsConfig is the separate Prometheus listener; an empty Addr disables it.
type MetricsConfig struct {
	Addr string `mapstructure:"addr"`
}

// LogConfig selects the slog handler.
type LogConfig struct {
	Level  string `mapstructure:"level"`  // debug, info, warn, error
	Format string `mapstructure:"format"` // json, text
}

// ReadinessConfig drives /readyz.
type ReadinessConfig struct {
	MinReadyBots int `mapstructure:"min_ready_bots"`
}

// SteamConfig paces logons and reconnects.
type SteamConfig struct {
	LoginStagger        time.Duration `mapstructure:"login_stagger"`
	ReconnectBackoffMin time.Duration `mapstructure:"reconnect_backoff_min"`
	ReconnectBackoffMax time.Duration `mapstructure:"reconnect_backoff_max"`
	CooldownRateLimited time.Duration `mapstructure:"cooldown_rate_limited"`
	CooldownElsewhere   time.Duration `mapstructure:"cooldown_elsewhere"`
	// TOTPSkew is Steam's clock minus this host's, added before computing
	// Steam Guard codes. Keep the host on NTP; this is for a host that
	// cannot be (code at now+skew).
	TOTPSkew time.Duration `mapstructure:"totp_skew"`
}

// GCConfig times the Dota 2 game coordinator session and requests.
type GCConfig struct {
	HelloInterval  time.Duration `mapstructure:"hello_interval"`
	RequestTimeout time.Duration `mapstructure:"request_timeout"`
	CreateTimeout  time.Duration `mapstructure:"create_timeout"`
}

// LeagueConfig controls league discovery.
type LeagueConfig struct {
	Sources          []string      `mapstructure:"sources"`
	RefreshInterval  time.Duration `mapstructure:"refresh_interval"`
	AllowUnverified  bool          `mapstructure:"allow_unverified"`
	WebAPIEnrichment bool          `mapstructure:"webapi_enrichment"`
}

// LobbyConfig holds lobby lifecycle timeouts and the operator-maintained
// name-to-wire tables. Map keys are lowercased by the loader.
type LobbyConfig struct {
	// HostSeatStrategy is the ordered list of ways to get the host out of
	// a player slot: player_pool, self_kick, no_team, spectator. caster is
	// rejected.
	HostSeatStrategy []string `mapstructure:"host_seat_strategy"`
	// HostSeatConfirmTimeout is how long one strategy may take to show in
	// the lobby before the next is tried.
	HostSeatConfirmTimeout time.Duration `mapstructure:"host_seat_confirm_timeout"`
	// MaxLaunchAttempts bounds automatic and chat launches; after that only
	// a forced API launch tries again.
	MaxLaunchAttempts   int           `mapstructure:"max_launch_attempts"`
	PendingTimeout      time.Duration `mapstructure:"pending_timeout"`
	AutoLaunchDelay     time.Duration `mapstructure:"auto_launch_delay"`
	IdleTimeout         time.Duration `mapstructure:"idle_timeout"`
	LaunchSettleTimeout time.Duration `mapstructure:"launch_settle_timeout"`
	ServerStallTimeout  time.Duration `mapstructure:"server_stall_timeout"`
	IngameBackstop      time.Duration `mapstructure:"ingame_backstop"`
	PostgameLinger      time.Duration `mapstructure:"postgame_linger"`
	// EnforceCooldown spaces seating kicks aimed at the same member.
	EnforceCooldown time.Duration `mapstructure:"enforce_cooldown"`
	// ReadoptTimeout is how long a lobby found in the database at startup
	// waits for its bot to be ready again.
	ReadoptTimeout time.Duration `mapstructure:"readopt_timeout"`
	// DrainTimeout bounds the lobby step of shutdown.
	DrainTimeout time.Duration `mapstructure:"drain_timeout"`
	// DestroyOnShutdown destroys lobbies that have not launched at
	// shutdown instead of leaving them to be re-adopted.
	DestroyOnShutdown bool `mapstructure:"destroy_on_shutdown"`
	// Admins are account IDs that may run admin chat commands in every
	// lobby, on top of each lobby's own list.
	Admins      []uint32          `mapstructure:"admins"`
	Invite      InviteConfig      `mapstructure:"invite"`
	Chat        ChatConfig        `mapstructure:"chat"`
	Regions     map[string]uint32 `mapstructure:"regions"`
	GameModes   map[string]uint32 `mapstructure:"game_modes"`
	SeriesTypes map[string]uint32 `mapstructure:"series_types"`
}

// InviteConfig rate-limits invites per player.
type InviteConfig struct {
	MinResend    time.Duration `mapstructure:"min_resend"`
	MaxPerPlayer int           `mapstructure:"max_per_player"`
}

// ChatConfig is lobby chat: the bot joins each lobby's chat channel and
// answers commands.
type ChatConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// Prefix starts every command. The Dota client handles text starting
	// with "/" itself and never sends it, so "/" cannot be used.
	Prefix string `mapstructure:"prefix"`
	// SendInterval spaces the bot's messages in one lobby.
	SendInterval time.Duration `mapstructure:"send_interval"`
	// StartCountdown is the time between a start command and the launch.
	StartCountdown time.Duration `mapstructure:"start_countdown"`
}

// MatchConfig configures the result resolver ladder.
type MatchConfig struct {
	SettleAfter time.Duration  `mapstructure:"settle_after"`
	OpenDota    OpenDotaConfig `mapstructure:"opendota"`
	WebAPI      WebAPIConfig   `mapstructure:"webapi"`
}

// OpenDotaConfig is the OpenDota fallback source.
type OpenDotaConfig struct {
	Enabled bool    `mapstructure:"enabled"`
	BaseURL string  `mapstructure:"base_url"`
	RPS     float64 `mapstructure:"rps"`
}

// WebAPIConfig is the Steam Web API source.
type WebAPIConfig struct {
	Enabled bool    `mapstructure:"enabled"`
	RPS     float64 `mapstructure:"rps"`
}

// StoreConfig locates the SQLite database. An empty Path resolves to
// <state_dir>/dota_lobby.db.
type StoreConfig struct {
	Path string `mapstructure:"path"`
	// Retention is how long finished lobbies and their events are kept.
	Retention time.Duration `mapstructure:"retention"`
	Backup    BackupConfig  `mapstructure:"backup"`
}

// BackupConfig is the copy of the database taken before migrations run.
// An empty Dir resolves to <state_dir>/backups.
type BackupConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Dir     string `mapstructure:"dir"`
	// Keep is how many automatic backups are kept; older ones are deleted
	// once the migrations after a new one have succeeded. On-demand
	// backups do not count and are not pruned by this setting.
	Keep int `mapstructure:"keep"`
}

// WebhookConfig is one event receiver. Its signing secret is looked up with
// Secrets.WebhookSecret(URL), its optional token with Secrets.WebhookToken.
type WebhookConfig struct {
	URL string `mapstructure:"url"`
	// Events are path.Match globs on the event type, such as "lobby.*".
	// Empty matches every event.
	Events []string `mapstructure:"events"`
	// TokenHeader names a request header that carries
	// secrets.webhook_tokens[url], for receivers that already check a
	// static token. Empty sends none.
	TokenHeader string `mapstructure:"token_header"`
}

// WebhookDeliveryConfig bounds webhook delivery attempts.
type WebhookDeliveryConfig struct {
	// Timeout bounds one attempt, response included.
	Timeout time.Duration `mapstructure:"timeout"`
	// MaxAge is how long after the event a failing delivery is retried
	// before it is given up.
	MaxAge time.Duration `mapstructure:"max_age"`
}

// SimulationConfig is simulation mode: the bots run against an in-memory
// Steam and game coordinator instead of logging on, for integration tests
// of the service's consumers. It refuses bots with real credentials.
type SimulationConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// AutoJoin makes an invited player join AutoJoinDelay after the invite
	// and take a slot on its rostered side.
	AutoJoin      bool          `mapstructure:"auto_join"`
	AutoJoinDelay time.Duration `mapstructure:"auto_join_delay"`
	// AutoReady makes an auto-joined player say the ready chat command
	// once seated.
	AutoReady bool `mapstructure:"auto_ready"`
	// AutoStart starts the match of a launched lobby AutoStartDelay after
	// the launch.
	AutoStart      bool          `mapstructure:"auto_start"`
	AutoStartDelay time.Duration `mapstructure:"auto_start_delay"`
	// Bots set per-bot simulation details, by bot name.
	Bots []SimulationBot `mapstructure:"bots"`
}

// SimulationBot is one bot's simulated account.
type SimulationBot struct {
	Name string `mapstructure:"name"`
	// AccountID is the simulated account ID; 0 picks one from the bot's
	// position in the bots list.
	AccountID uint32 `mapstructure:"account_id"`
	// Leagues are the leagues the simulated account administers; empty
	// uses the bot's bots[].leagues.
	Leagues []uint32 `mapstructure:"leagues"`
}

// SimulationAccountBase is the first simulated bot account ID, far above
// the real accounts players have.
const SimulationAccountBase uint32 = 4_000_000_000

// SimulatedBot returns the simulated account of the i-th bot in Bots.
func (c *Config) SimulatedBot(i int) SimulationBot {
	b := c.Bots[i]
	out := SimulationBot{Name: b.Name, AccountID: SimulationAccountBase + uint32(i) + 1, Leagues: b.Leagues}
	for _, s := range c.Simulation.Bots {
		if !strings.EqualFold(s.Name, b.Name) {
			continue
		}
		if s.AccountID != 0 {
			out.AccountID = s.AccountID
		}
		if len(s.Leagues) > 0 {
			out.Leagues = s.Leagues
		}
	}
	return out
}

// Simulation mode's defaults for store.path and store.backup.dir, inside
// state_dir, so a simulated run never touches the real database or its
// backups in the same state_dir.
const (
	SimulationStoreFile = "dota_lobby-simulation.db"
	SimulationBackupDir = "backups-simulation"
)

// Secrets are the service-wide secrets from the secrets file and env.
type Secrets struct {
	APIKey      Secret `mapstructure:"api_key"`
	WebAPIKey   Secret `mapstructure:"webapi_key"`
	OpenDotaKey Secret `mapstructure:"opendota_key"`
	// WebhookSecrets is keyed by lowercased webhook URL.
	WebhookSecrets map[string]Secret `mapstructure:"webhook_secrets"`
	// WebhookTokens is keyed by lowercased webhook URL.
	WebhookTokens map[string]Secret `mapstructure:"webhook_tokens"`
}

// WebhookSecret returns the signing secret for a webhook URL. Keys are
// matched case-insensitively because the loader lowercases map keys.
func (s Secrets) WebhookSecret(url string) Secret {
	return s.WebhookSecrets[strings.ToLower(url)]
}

// WebhookToken returns the static token for a webhook URL, matched like
// WebhookSecret.
func (s Secrets) WebhookToken(url string) Secret {
	return s.WebhookTokens[strings.ToLower(url)]
}

// BotConfig is one Steam account the service runs.
type BotConfig struct {
	// Name identifies the bot in the API, logs, env names and credential
	// file names.
	Name string
	// Username is the Steam account name. It defaults to the STEAM_USER of
	// the bot's credentials file, then to Name.
	Username string
	Enabled  bool
	// Leagues is the static, unverified league fallback.
	Leagues []uint32
	Tags    []string
	// Credentials are resolved from every layer; see Load.
	Credentials Credentials
}

// BotStateDir is where this bot's token and other state live:
// <state_dir>/bots/<username>, lowercased because Steam account names are
// case-insensitive.
func (c *Config) BotStateDir(b BotConfig) string {
	return filepath.Join(c.StateDir, "bots", strings.ToLower(b.Username))
}

// Bot returns the bot with the given name, compared case-insensitively.
func (c *Config) Bot(name string) (BotConfig, bool) {
	for _, b := range c.Bots {
		if strings.EqualFold(b.Name, name) {
			return b, true
		}
	}
	return BotConfig{}, false
}

// Credentials are one bot's Steam secrets.
type Credentials struct {
	Password Secret
	// SharedSecret is the base64 TOTP seed from the mobile authenticator. With
	// it, re-authentication needs no human.
	SharedSecret Secret
	// RefreshToken seeds the token store when it has no token for this bot.
	// Renewed tokens go to the store, never back to this source.
	RefreshToken Secret
	DeviceName   string
}

// CanAuthenticate reports whether a password logon can be attempted.
func (c Credentials) CanAuthenticate() bool { return c.Password.IsSet() }

// Headless reports whether the bot can re-authenticate without an operator.
func (c Credentials) Headless() bool { return c.Password.IsSet() && c.SharedSecret.IsSet() }

// defaults are the built-in layer. Every leaf key must appear here (or be
// bound in Load) so that env overrides reach Unmarshal.
func defaults() map[string]any {
	return map[string]any{
		"server::host":           "0.0.0.0",
		"server::port":           8080,
		"server::max_body_bytes": 65536,
		"metrics::addr":          ":9090",
		"state_dir":              "/var/lib/dota_lobby",
		"credentials_dir":        "",
		"log::level":             "info",
		"log::format":            "json",

		"readiness::min_ready_bots": 1,

		"steam::login_stagger":         "10s",
		"steam::reconnect_backoff_min": "5s",
		"steam::reconnect_backoff_max": "5m",
		"steam::cooldown_rate_limited": "30m",
		"steam::cooldown_elsewhere":    "10m",
		"steam::totp_skew":             "0s",

		"gc::hello_interval":  "10s",
		"gc::request_timeout": "10s",
		"gc::create_timeout":  "2m",

		"league::sources":           []string{"gc", "config"},
		"league::refresh_interval":  "30m",
		"league::allow_unverified":  false,
		"league::webapi_enrichment": true,

		"lobby::host_seat_strategy":        []string{"player_pool", "self_kick", "no_team"},
		"lobby::host_seat_confirm_timeout": "3s",
		"lobby::max_launch_attempts":       3,
		"lobby::pending_timeout":           "10m",
		"lobby::auto_launch_delay":         "10s",
		"lobby::postgame_linger":           "30s",
		"lobby::enforce_cooldown":          "10s",
		"lobby::readopt_timeout":           "5m",
		"lobby::drain_timeout":             "10s",
		"lobby::destroy_on_shutdown":       false,
		"lobby::admins":                    []uint32{},
		"lobby::idle_timeout":              "30m",
		"lobby::launch_settle_timeout":     "2m",
		"lobby::server_stall_timeout":      "10m",
		"lobby::ingame_backstop":           "3h",
		"lobby::invite::min_resend":        "30s",
		"lobby::invite::max_per_player":    5,
		"lobby::chat::enabled":             true,
		"lobby::chat::prefix":              "!",
		"lobby::chat::send_interval":       "1s",
		"lobby::chat::start_countdown":     "10s",
		// Region and series values are UNCERTAIN (01 §7.10, §7.11); operators
		// maintain them.
		"lobby::regions":      map[string]any{"us_west": 1, "us_east": 2, "europe": 3, "sea": 5},
		"lobby::game_modes":   map[string]any{"ap": 1, "cm": 2, "cd": 16, "ad": 22, "turbo": 23},
		"lobby::series_types": map[string]any{"none": 0, "bo3": 1, "bo5": 2},

		"match::settle_after":       "6h",
		"match::opendota::enabled":  true,
		"match::opendota::base_url": "https://api.opendota.com/api",
		"match::opendota::rps":      1.0,
		"match::webapi::enabled":    true,
		"match::webapi::rps":        1.0,

		"store::path":            "",
		"store::retention":       "720h",
		"store::backup::enabled": true,
		"store::backup::dir":     "",
		"store::backup::keep":    5,

		"webhook_delivery::timeout": "10s",
		"webhook_delivery::max_age": "24h",

		"simulation::enabled":          false,
		"simulation::auto_join":        false,
		"simulation::auto_join_delay":  "1s",
		"simulation::auto_ready":       false,
		"simulation::auto_start":       false,
		"simulation::auto_start_delay": "5s",
	}
}

// secretKeys are bound to env even when no file sets them.
var secretKeys = []string{
	"secrets::api_key",
	"secrets::webapi_key",
	"secrets::opendota_key",
}

// botSecretFields are the per-bot keys under secrets::bots::<bot>.
var botSecretFields = []string{"password", "shared_secret", "refresh_token", "device_name"}
