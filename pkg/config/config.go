// Package config loads the service configuration: config.yaml for settings,
// a secrets file for credentials, per-bot credential files in the sops format,
// then DOTA_LOBBY_* environment overrides and DOTA_LOBBY_*_FILE indirection.
// See plan §8 and research 07 §3.
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

// SteamConfig paces logons and reconnects (plan §4.1).
type SteamConfig struct {
	LoginStagger        time.Duration `mapstructure:"login_stagger"`
	ReconnectBackoffMin time.Duration `mapstructure:"reconnect_backoff_min"`
	ReconnectBackoffMax time.Duration `mapstructure:"reconnect_backoff_max"`
	CooldownRateLimited time.Duration `mapstructure:"cooldown_rate_limited"`
	CooldownElsewhere   time.Duration `mapstructure:"cooldown_elsewhere"`
}

// GCConfig times the Dota 2 game coordinator session and requests.
type GCConfig struct {
	HelloInterval  time.Duration `mapstructure:"hello_interval"`
	RequestTimeout time.Duration `mapstructure:"request_timeout"`
	CreateTimeout  time.Duration `mapstructure:"create_timeout"`
}

// LeagueConfig controls league discovery (D2).
type LeagueConfig struct {
	Sources          []string      `mapstructure:"sources"`
	RefreshInterval  time.Duration `mapstructure:"refresh_interval"`
	AllowUnverified  bool          `mapstructure:"allow_unverified"`
	WebAPIEnrichment bool          `mapstructure:"webapi_enrichment"`
}

// LobbyConfig holds lobby lifecycle timeouts and the operator-maintained
// name-to-wire tables. Map keys are lowercased by the loader.
type LobbyConfig struct {
	HostSeatStrategy    []string          `mapstructure:"host_seat_strategy"`
	IdleTimeout         time.Duration     `mapstructure:"idle_timeout"`
	LaunchSettleTimeout time.Duration     `mapstructure:"launch_settle_timeout"`
	ServerStallTimeout  time.Duration     `mapstructure:"server_stall_timeout"`
	IngameBackstop      time.Duration     `mapstructure:"ingame_backstop"`
	Invite              InviteConfig      `mapstructure:"invite"`
	Chat                ChatConfig        `mapstructure:"chat"`
	Regions             map[string]uint32 `mapstructure:"regions"`
	GameModes           map[string]uint32 `mapstructure:"game_modes"`
	SeriesTypes         map[string]uint32 `mapstructure:"series_types"`
}

// InviteConfig rate-limits invites per player.
type InviteConfig struct {
	MinResend    time.Duration `mapstructure:"min_resend"`
	MaxPerPlayer int           `mapstructure:"max_per_player"`
}

// ChatConfig toggles lobby chat (D10).
type ChatConfig struct {
	Enabled bool `mapstructure:"enabled"`
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
}

// WebhookConfig is one event receiver. Its signing secret is looked up with
// Secrets.WebhookSecret(URL).
type WebhookConfig struct {
	URL    string   `mapstructure:"url"`
	Events []string `mapstructure:"events"`
}

// Secrets are the service-wide secrets from the secrets file and env.
type Secrets struct {
	APIKey      Secret `mapstructure:"api_key"`
	WebAPIKey   Secret `mapstructure:"webapi_key"`
	OpenDotaKey Secret `mapstructure:"opendota_key"`
	// WebhookSecrets is keyed by lowercased webhook URL.
	WebhookSecrets map[string]Secret `mapstructure:"webhook_secrets"`
}

// WebhookSecret returns the signing secret for a webhook URL. Keys are
// matched case-insensitively because the loader lowercases map keys.
func (s Secrets) WebhookSecret(url string) Secret {
	return s.WebhookSecrets[strings.ToLower(url)]
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
	// Leagues is the static, unverified league fallback (D2).
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

		"gc::hello_interval":  "10s",
		"gc::request_timeout": "10s",
		"gc::create_timeout":  "2m",

		"league::sources":           []string{"gc", "config"},
		"league::refresh_interval":  "30m",
		"league::allow_unverified":  false,
		"league::webapi_enrichment": true,

		"lobby::host_seat_strategy":     []string{"self_kick", "player_pool", "spectator", "caster"},
		"lobby::idle_timeout":           "30m",
		"lobby::launch_settle_timeout":  "2m",
		"lobby::server_stall_timeout":   "10m",
		"lobby::ingame_backstop":        "3h",
		"lobby::invite::min_resend":     "30s",
		"lobby::invite::max_per_player": 5,
		"lobby::chat::enabled":          false,
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

		"store::path": "",
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
