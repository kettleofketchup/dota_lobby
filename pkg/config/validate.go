package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

var (
	// Bot names end up in env names, file names and metric labels.
	botNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	// Steam account names are letters, digits and underscores. '-' is let
	// through for bots named only by a label; no '.' or '/' keeps
	// <state_dir>/bots/<username> inside state_dir.
	usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

	logLevels          = []string{"debug", "info", "warn", "error"}
	logFormats         = []string{"json", "text"}
	leagueSources      = []string{"gc", "config", "web"}
	hostSeatStrategies = []string{"self_kick", "player_pool", "spectator", "caster"}
)

func validateBotName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if !botNamePattern.MatchString(name) {
		return fmt.Errorf("name %q must be 1-64 letters, digits, '_' or '-', starting with a letter or digit", name)
	}
	return nil
}

// Validate checks the whole configuration and reports every problem at once.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if c.Server.Port < 1 || c.Server.Port > 65535 {
		add("server.port %d out of range 1-65535", c.Server.Port)
	}
	if c.Server.MaxBodyBytes <= 0 {
		add("server.max_body_bytes must be positive")
	}
	if c.StateDir == "" {
		add("state_dir is required")
	}
	if !slices.Contains(logLevels, c.Log.Level) {
		add("log.level %q must be one of %v", c.Log.Level, logLevels)
	}
	if !slices.Contains(logFormats, c.Log.Format) {
		add("log.format %q must be one of %v", c.Log.Format, logFormats)
	}
	if c.Readiness.MinReadyBots < 0 {
		add("readiness.min_ready_bots must not be negative")
	}

	positive := func(name string, d time.Duration) {
		if d <= 0 {
			add("%s must be positive, got %s", name, d)
		}
	}
	if c.Steam.LoginStagger < 0 {
		add("steam.login_stagger must not be negative")
	}
	positive("steam.reconnect_backoff_min", c.Steam.ReconnectBackoffMin)
	positive("steam.reconnect_backoff_max", c.Steam.ReconnectBackoffMax)
	if c.Steam.ReconnectBackoffMax < c.Steam.ReconnectBackoffMin {
		add("steam.reconnect_backoff_max %s is below reconnect_backoff_min %s", c.Steam.ReconnectBackoffMax, c.Steam.ReconnectBackoffMin)
	}
	positive("steam.cooldown_rate_limited", c.Steam.CooldownRateLimited)
	positive("steam.cooldown_elsewhere", c.Steam.CooldownElsewhere)
	positive("gc.hello_interval", c.GC.HelloInterval)
	positive("gc.request_timeout", c.GC.RequestTimeout)
	positive("gc.create_timeout", c.GC.CreateTimeout)
	positive("league.refresh_interval", c.League.RefreshInterval)
	positive("lobby.idle_timeout", c.Lobby.IdleTimeout)
	positive("lobby.launch_settle_timeout", c.Lobby.LaunchSettleTimeout)
	positive("lobby.server_stall_timeout", c.Lobby.ServerStallTimeout)
	positive("lobby.ingame_backstop", c.Lobby.IngameBackstop)
	positive("match.settle_after", c.Match.SettleAfter)
	if c.Lobby.Invite.MinResend < 0 {
		add("lobby.invite.min_resend must not be negative")
	}
	if c.Lobby.Invite.MaxPerPlayer < 1 {
		add("lobby.invite.max_per_player must be at least 1")
	}

	checkSet := func(name string, got, allowed []string, required bool) {
		if required && len(got) == 0 {
			add("%s must not be empty", name)
		}
		seen := map[string]bool{}
		for _, s := range got {
			if !slices.Contains(allowed, s) {
				add("%s: unknown value %q (allowed: %v)", name, s, allowed)
			}
			if seen[s] {
				add("%s: %q listed twice", name, s)
			}
			seen[s] = true
		}
	}
	checkSet("league.sources", c.League.Sources, leagueSources, true)
	checkSet("lobby.host_seat_strategy", c.Lobby.HostSeatStrategy, hostSeatStrategies, true)

	if c.Match.OpenDota.Enabled {
		if err := checkHTTPURL(c.Match.OpenDota.BaseURL); err != nil {
			add("match.opendota.base_url: %w", err)
		}
		if c.Match.OpenDota.RPS <= 0 {
			add("match.opendota.rps must be positive")
		}
	}
	if c.Match.WebAPI.Enabled && c.Match.WebAPI.RPS <= 0 {
		add("match.webapi.rps must be positive")
	}
	for i, w := range c.Webhooks {
		if err := checkHTTPURL(w.URL); err != nil {
			add("webhooks[%d].url: %w", i, err)
		}
		if len(w.Events) == 0 {
			add("webhooks[%d].events must not be empty", i)
		}
	}

	names := map[string]string{}
	users := map[string]string{}
	for _, b := range c.Bots {
		if err := validateBotName(b.Name); err != nil {
			errs = append(errs, fmt.Errorf("bot: %w", err))
			continue
		}
		// bot-a and bot_a share an env name, so they count as duplicates.
		norm := strings.ToLower(strings.ReplaceAll(b.Name, "-", "_"))
		if prev, dup := names[norm]; dup {
			add("bots %q and %q collide (names are case-insensitive and '-' equals '_')", prev, b.Name)
		}
		names[norm] = b.Name
		if !usernamePattern.MatchString(b.Username) {
			add("bot %s: username %q must be letters, digits, '_' or '-'", b.Name, b.Username)
		}
		lu := strings.ToLower(b.Username)
		if prev, dup := users[lu]; dup {
			add("bots %q and %q use the same Steam account %q", prev, b.Name, b.Username)
		}
		users[lu] = b.Name
		if s := b.Credentials.SharedSecret; s.IsSet() {
			if raw, err := base64.StdEncoding.DecodeString(s.Reveal()); err != nil || len(raw) == 0 {
				// The decode error can echo input bytes, so it is not wrapped.
				add("bot %s: shared_secret is not valid base64", b.Name)
			}
		}
	}

	return errors.Join(errs...)
}

func checkHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q is not an absolute http(s) URL", raw)
	}
	return nil
}
