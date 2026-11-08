package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"
)

// EnvPrefix prefixes every environment override. A key's env name is the
// prefix plus the key path joined by "_", uppercased, with "-" turned into
// "_": secrets.bots.bot-a.password is DOTA_LOBBY_SECRETS_BOTS_BOT_A_PASSWORD.
// Appending _FILE to any of them reads the value from that file instead.
const EnvPrefix = "DOTA_LOBBY"

// keyDelim replaces Viper's "." so that map keys holding dots (webhook URLs,
// bot names) survive Unmarshal.
const keyDelim = "::"

var envReplacer = strings.NewReplacer(keyDelim, "_", "-", "_", ".", "_")

// Options locate the input files. The zero value searches the default paths.
type Options struct {
	// ConfigFile is an explicit config.yaml; it must exist. Otherwise
	// DOTA_LOBBY_CONFIG_FILE, then config.yaml in the search paths.
	ConfigFile string
	// SecretsFile is an explicit secrets file; it must exist. Otherwise
	// DOTA_LOBBY_SECRETS_FILE, then secrets.yaml in DOTA_LOBBY_SECRETS_DIR,
	// /run/secrets/dota_lobby and the search paths.
	SecretsFile string
	// SearchPaths replaces the default config search path list:
	// DOTA_LOBBY_CONFIG_DIR, /etc/dota_lobby, ., ./config,
	// $HOME/.config/dota_lobby.
	SearchPaths []string
	// Logger receives deprecation and consistency warnings. Nil discards them.
	Logger *slog.Logger
}

// fileConfig is the decode target: Config plus the raw shapes that Load
// turns into Config.Bots and Config.Secrets.
type fileConfig struct {
	Config  `mapstructure:",squash"`
	Bots    []botEntry  `mapstructure:"bots"`
	Secrets fileSecrets `mapstructure:"secrets"`
}

type fileSecrets struct {
	Secrets `mapstructure:",squash"`
	Bots    map[string]botSecrets `mapstructure:"bots"`
}

type botEntry struct {
	Name     string   `mapstructure:"name"`
	Username string   `mapstructure:"username"`
	Enabled  *bool    `mapstructure:"enabled"`
	Leagues  []uint32 `mapstructure:"leagues"`
	Tags     []string `mapstructure:"tags"`
	// CredentialsFile overrides the credentials_dir lookup for this bot.
	CredentialsFile string `mapstructure:"credentials_file"`
}

type botSecrets struct {
	Password     Secret `mapstructure:"password"`
	SharedSecret Secret `mapstructure:"shared_secret"`
	RefreshToken Secret `mapstructure:"refresh_token"`
	DeviceName   string `mapstructure:"device_name"`
	// SentryHash is accepted from the legacy format and ignored.
	SentryHash Secret `mapstructure:"sentry_hash"`
}

// pendingBot is a bot whose credentials have been routed into Viper but not
// yet decoded.
type pendingBot struct {
	entry      botEntry
	name       string
	username   string
	secretsKey string
}

// Load reads every layer, lowest first: built-in defaults, config.yaml, the
// secrets file, per-bot credentials files, DOTA_LOBBY_* env, then
// DOTA_LOBBY_*_FILE. It returns a validated Config.
//
// Bot credentials are keyed by bot name (falling back to the Steam username)
// under secrets.bots, and may also come from a credentials file per bot: the
// decrypted form of secrets/bots/<bot>.sops.yaml with STEAM_USER, STEAM_PASS,
// STEAM_SHARED_SECRET and STEAM_REFRESH_TOKEN. Map keys are lowercased.
func Load(opts Options) (*Config, error) {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	v := viper.NewWithOptions(viper.KeyDelimiter(keyDelim))
	for k, val := range defaults() {
		v.SetDefault(k, val)
	}
	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(envReplacer)
	v.AutomaticEnv()
	for _, k := range secretKeys {
		if err := v.BindEnv(k); err != nil {
			return nil, fmt.Errorf("bind env %s: %w", k, err)
		}
	}

	search := opts.SearchPaths
	if search == nil {
		search = defaultSearchPaths()
	}

	cfgPath, err := locate(opts.ConfigFile, "CONFIG_FILE", "config", search)
	if err != nil {
		return nil, err
	}
	if cfgPath != "" {
		v.SetConfigFile(cfgPath)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("read config %s: %w", cfgPath, err)
		}
		log.Debug("loaded config file", "path", cfgPath)
	}

	secretsSearch := search
	if opts.SearchPaths == nil {
		secretsSearch = append([]string{envOr("SECRETS_DIR", "/run/secrets/dota_lobby")}, search...)
	}
	secPath, err := locate(opts.SecretsFile, "SECRETS_FILE", "secrets", secretsSearch)
	if err != nil {
		return nil, err
	}
	var legacyBots []string
	if secPath != "" {
		legacyBots, err = mergeSecretsFile(v, secPath, log)
		if err != nil {
			return nil, err
		}
		log.Debug("loaded secrets file", "path", secPath)
	}

	bots, err := collectBots(v, legacyBots, log)
	if err != nil {
		return nil, err
	}

	if err := applyFileEnv(v); err != nil {
		return nil, err
	}

	var fc fileConfig
	if err := v.Unmarshal(&fc, strictDecode); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	cfg := fc.Config
	cfg.Secrets = fc.Secrets.Secrets
	if cfg.Server.APIKey.IsSet() {
		if !cfg.Secrets.APIKey.IsSet() {
			log.Warn("server.api_key is deprecated; move it to secrets.api_key")
			cfg.Secrets.APIKey = cfg.Server.APIKey
		} else if cfg.Secrets.APIKey != cfg.Server.APIKey {
			log.Warn("server.api_key and secrets.api_key differ; using secrets.api_key")
		}
		cfg.Server.APIKey = ""
	}
	if cfg.Store.Path == "" {
		cfg.Store.Path = filepath.Join(cfg.StateDir, "dota_lobby.db")
	}

	used := map[string]bool{}
	for _, p := range bots {
		s := fc.Secrets.Bots[p.secretsKey]
		used[p.secretsKey] = true
		if s.SentryHash.IsSet() {
			log.Warn("sentry_hash is no longer used and is ignored", "bot", p.name)
		}
		b := BotConfig{
			Name:     p.name,
			Username: p.username,
			Enabled:  p.entry.Enabled == nil || *p.entry.Enabled,
			Leagues:  p.entry.Leagues,
			Tags:     p.entry.Tags,
			Credentials: Credentials{
				Password:     s.Password,
				SharedSecret: s.SharedSecret,
				RefreshToken: s.RefreshToken,
				DeviceName:   s.DeviceName,
			},
		}
		if b.Enabled && !b.Credentials.Password.IsSet() && !b.Credentials.RefreshToken.IsSet() {
			log.Warn("bot has no password or refresh token; it can log on only with a token already in state_dir", "bot", b.Name)
		}
		cfg.Bots = append(cfg.Bots, b)
	}
	for k := range fc.Secrets.Bots {
		if !used[k] {
			log.Warn("secrets.bots entry matches no configured bot", "key", k)
		}
	}
	if len(cfg.Bots) == 0 {
		log.Warn("no bots configured")
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// strictDecode rejects unknown keys so a typo fails loudly instead of being
// silently ignored (08 §3.3: sentry_hash was never loaded), and refuses
// secrets that YAML did not read as strings.
func strictDecode(c *mapstructure.DecoderConfig) {
	c.ErrorUnused = true
	c.DecodeHook = mapstructure.ComposeDecodeHookFunc(secretMustBeString, c.DecodeHook)
}

var secretType = reflect.TypeFor[Secret]()

// secretMustBeString stops weak decoding from rewriting a secret: an
// unquoted `password: 0123` is the integer 83 to YAML, and the bot would
// then fail every logon with InvalidPassword for no visible reason.
func secretMustBeString(from, to reflect.Type, data any) (any, error) {
	if to != secretType || from.Kind() == reflect.String || data == nil {
		return data, nil
	}
	// The value is not echoed; mapstructure adds the key name.
	return nil, fmt.Errorf("secret must be a quoted string, YAML read it as %s", from.Kind())
}

func defaultSearchPaths() []string {
	var paths []string
	if dir := os.Getenv(EnvPrefix + "_CONFIG_DIR"); dir != "" {
		paths = append(paths, dir)
	}
	paths = append(paths, "/etc/dota_lobby", ".", "./config")
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "dota_lobby"))
	}
	return paths
}

func envOr(suffix, def string) string {
	if v := os.Getenv(EnvPrefix + "_" + suffix); v != "" {
		return v
	}
	return def
}

// locate returns the explicit path, the path named by env, or the first
// <name>.yaml/.yml found in search. Explicit paths must exist; a missing
// searched file returns "".
func locate(explicit, envSuffix, name string, search []string) (string, error) {
	if explicit == "" {
		explicit = os.Getenv(EnvPrefix + "_" + envSuffix)
	}
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("%s file: %w", name, err)
		}
		return explicit, nil
	}
	for _, dir := range search {
		for _, ext := range []string{".yaml", ".yml"} {
			p := filepath.Join(dir, name+ext)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, nil
			}
		}
	}
	return "", nil
}

// mergeSecretsFile merges a YAML or JSON secrets file into v's config layer,
// converting the legacy secrets.bots list into the keyed map. It returns the
// usernames from a legacy list so they can stand in for a missing bots list.
func mergeSecretsFile(v *viper.Viper, path string, log *slog.Logger) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read secrets file: %w", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse secrets file %s: %w", path, err)
	}
	if _, ok := doc["sops"]; ok {
		return nil, fmt.Errorf("secrets file %s is still sops-encrypted; decrypt it first", path)
	}

	var legacy []string
	if sec, ok := doc["secrets"].(map[string]any); ok {
		if list, ok := sec["bots"].([]any); ok {
			log.Warn("secrets.bots as a list is deprecated; key it by bot name", "path", path)
			keyed := make(map[string]any, len(list))
			for i, item := range list {
				m, ok := item.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("secrets.bots[%d]: want a mapping", i)
				}
				user, _ := m["username"].(string)
				if user == "" {
					return nil, fmt.Errorf("secrets.bots[%d]: username is required", i)
				}
				delete(m, "username")
				keyed[strings.ToLower(user)] = m
				legacy = append(legacy, user)
			}
			sec["bots"] = keyed
		}
	}
	if err := v.MergeConfigMap(doc); err != nil {
		return nil, fmt.Errorf("merge secrets file: %w", err)
	}
	return legacy, nil
}

// collectBots decodes the bots list, reads each bot's credentials file into
// the config layer and binds the per-bot secret env names. With no bots list
// it falls back to the legacy secrets list, then to credentials_dir.
func collectBots(v *viper.Viper, legacy []string, log *slog.Logger) ([]pendingBot, error) {
	var entries []botEntry
	if err := v.UnmarshalKey("bots", &entries, strictDecode); err != nil {
		return nil, fmt.Errorf("decode bots: %w", err)
	}

	var dirFiles map[string]string
	if dir := v.GetString("credentials_dir"); dir != "" {
		var err error
		if dirFiles, err = scanCredentialsDir(dir); err != nil {
			return nil, err
		}
	}

	switch {
	case len(entries) > 0:
	case len(legacy) > 0:
		for _, u := range legacy {
			entries = append(entries, botEntry{Name: u})
		}
	case len(dirFiles) > 0:
		keys := make([]string, 0, len(dirFiles))
		for k := range dirFiles {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			entries = append(entries, botEntry{Name: botNameFromFile(dirFiles[k])})
		}
		log.Info("no bots list; using every file in credentials_dir", "bots", len(entries))
	}

	var errs []error
	var out []pendingBot
	for i, e := range entries {
		name := e.Name
		if name == "" {
			name = e.Username
		}
		if err := validateBotName(name); err != nil {
			errs = append(errs, fmt.Errorf("bots[%d]: %w", i, err))
			continue
		}

		var cf credentialFile
		path := e.CredentialsFile
		if path == "" {
			path = dirFiles[strings.ToLower(name)]
		}
		if path != "" {
			var err error
			if cf, err = readCredentialFile(path); err != nil {
				errs = append(errs, fmt.Errorf("bot %s: %w", name, err))
				continue
			}
		}

		username := e.Username
		if fileUser := cf.fields["username"]; fileUser != "" {
			if username != "" && !strings.EqualFold(username, fileUser) {
				errs = append(errs, fmt.Errorf("bot %s: username %q differs from STEAM_USER %q in %s", name, username, fileUser, cf.path))
				continue
			}
			if username == "" {
				username = fileUser
			}
		}
		if username == "" {
			username = name
		}

		key := strings.ToLower(name)
		if alt := strings.ToLower(username); !v.IsSet("secrets::bots::"+key) && v.IsSet("secrets::bots::"+alt) {
			key = alt
		}
		if len(cf.fields) > 0 {
			fields := make(map[string]any, len(cf.fields))
			for k, val := range cf.fields {
				if k != "username" {
					fields[k] = val
				}
			}
			patch := map[string]any{"secrets": map[string]any{"bots": map[string]any{key: fields}}}
			if err := v.MergeConfigMap(patch); err != nil {
				return nil, fmt.Errorf("bot %s: merge credentials: %w", name, err)
			}
		}
		for _, f := range botSecretFields {
			if err := v.BindEnv("secrets" + keyDelim + "bots" + keyDelim + key + keyDelim + f); err != nil {
				return nil, fmt.Errorf("bot %s: bind env: %w", name, err)
			}
		}
		out = append(out, pendingBot{entry: e, name: name, username: username, secretsKey: key})
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// envName is the environment variable that overrides a key.
func envName(key string) string {
	return envReplacer.Replace(EnvPrefix + "_" + strings.ToUpper(key))
}

// applyFileEnv implements the Docker/Kubernetes *_FILE convention for every
// known key. Setting both FOO and FOO_FILE is an error rather than a silent
// precedence rule.
func applyFileEnv(v *viper.Viper) error {
	keys := v.AllKeys()
	sort.Strings(keys)
	for _, k := range keys {
		name := envName(k)
		path := os.Getenv(name + "_FILE")
		if path == "" {
			continue
		}
		if os.Getenv(name) != "" {
			return fmt.Errorf("both %s and %s_FILE are set", name, name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%s_FILE: %w", name, err)
		}
		v.Set(k, strings.TrimRight(string(data), "\r\n"))
	}
	return nil
}

// LoadServer reads only the server section (host and port) from the same
// defaults, config file and DOTA_LOBBY_SERVER_* env that Load uses. It never
// opens the secrets file, bot credentials or state_dir, so a probe such as
// the container healthcheck can find the listen address without depending on
// them. It does not validate the rest of the config.
func LoadServer(opts Options) (ServerConfig, error) {
	v := viper.NewWithOptions(viper.KeyDelimiter(keyDelim))
	for k, val := range defaults() {
		v.SetDefault(k, val)
	}
	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(envReplacer)
	v.AutomaticEnv()

	search := opts.SearchPaths
	if search == nil {
		search = defaultSearchPaths()
	}
	cfgPath, err := locate(opts.ConfigFile, "CONFIG_FILE", "config", search)
	if err != nil {
		return ServerConfig{}, err
	}
	if cfgPath != "" {
		v.SetConfigFile(cfgPath)
		if err := v.ReadInConfig(); err != nil {
			return ServerConfig{}, fmt.Errorf("read config %s: %w", cfgPath, err)
		}
	}
	sc := ServerConfig{Host: v.GetString("server::host"), Port: v.GetInt("server::port")}
	if sc.Port < 1 || sc.Port > 65535 {
		return ServerConfig{}, fmt.Errorf("server.port %d out of range 1-65535", sc.Port)
	}
	return sc, nil
}
