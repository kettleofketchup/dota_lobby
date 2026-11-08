package lobby

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

// Limits on a request.
const (
	MaxPlayersPerSide = 5
	MaxMetadata       = 16
	maxNameLen        = 64
	maxPasswordLen    = 32
	maxRefLen         = 128
	maxMetaValueLen   = 256
)

// defaultGameMode is All Pick, sent when a request names no mode; the GC
// treats a missing mode in a full detail set as 0, which is no mode.
const defaultGameMode = 1

var refPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// UnknownNameError is a game mode or region that is neither a configured
// name nor a number. It matches ErrUnknownName.
type UnknownNameError struct {
	Field string // "game_mode" or "region"
	Value string
}

func (e *UnknownNameError) Error() string {
	return fmt.Sprintf("lobby: unknown %s %q", e.Field, e.Value)
}

// Is makes errors.Is(err, ErrUnknownName) true.
func (e *UnknownNameError) Is(target error) bool { return target == ErrUnknownName }

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// normalize fills defaults and lowercases names.
func (s *Spec) normalize() {
	s.GameMode = strings.ToLower(strings.TrimSpace(s.GameMode))
	s.Region = strings.ToLower(strings.TrimSpace(s.Region))
	if s.Visibility == "" {
		s.Visibility = VisibilityPublic
	}
	if s.Launch == "" {
		s.Launch = LaunchManual
	}
}

// validate checks a whole spec; names are checked against cfg. The message
// of an ErrInvalid error names the field.
func (s Spec) validate(cfg config.LobbyConfig) error {
	if !refPattern.MatchString(s.ExternalRef) {
		return invalid("external_ref must be 1-%d characters of letters, digits, '.', '_', ':' or '-'", maxRefLen)
	}
	if n := utf8.RuneCountInString(s.Name); n < 1 || n > maxNameLen || strings.TrimSpace(s.Name) == "" {
		return invalid("name must be 1-%d characters", maxNameLen)
	}
	if utf8.RuneCountInString(s.Password.Reveal()) > maxPasswordLen {
		return invalid("password must be at most %d characters", maxPasswordLen)
	}
	if s.TVDelaySeconds != 0 {
		if _, ok := gc.TVDelayToWire(s.TVDelaySeconds); !ok {
			return invalid("tv_delay_seconds must be 10, 60, 120, 300 or 900")
		}
	}
	switch s.Visibility {
	case VisibilityPublic, VisibilityFriends, VisibilityUnlisted:
	default:
		return invalid("visibility must be public, friends or unlisted")
	}
	switch s.Launch {
	case LaunchManual, LaunchAuto:
	default:
		return invalid("launch must be manual or auto")
	}
	if _, err := resolve("game_mode", s.GameMode, cfg.GameModes); err != nil {
		return err
	}
	if _, err := resolve("region", s.Region, cfg.Regions); err != nil {
		return err
	}
	for _, a := range s.Admins {
		if a == 0 {
			return invalid("admins: 0 is not an account ID")
		}
	}
	if len(s.Metadata) > MaxMetadata {
		return invalid("metadata may have at most %d entries", MaxMetadata)
	}
	for k, v := range s.Metadata {
		if k == "" || len(k) > maxNameLen {
			return invalid("metadata keys must be 1-%d bytes", maxNameLen)
		}
		if len(v) > maxMetaValueLen {
			return invalid("metadata[%q] must be at most %d bytes", k, maxMetaValueLen)
		}
	}
	return s.Teams.validate()
}

func (r Roster) validate() error {
	seen := map[uint32]Side{}
	for _, side := range []Side{Radiant, Dire} {
		t := r.Team(side)
		if len(t.TeamRef) > maxRefLen {
			return invalid("teams.%s.team_ref must be at most %d characters", side, maxRefLen)
		}
		if utf8.RuneCountInString(t.Name) > maxNameLen {
			return invalid("teams.%s.name must be at most %d characters", side, maxNameLen)
		}
		if len(t.Players) > MaxPlayersPerSide {
			return invalid("teams.%s.players: at most %d per side", side, MaxPlayersPerSide)
		}
		for _, p := range t.Players {
			if p.AccountID == 0 {
				return invalid("teams.%s.players: account_id 0 is not an account", side)
			}
			if prev, dup := seen[p.AccountID]; dup {
				return invalid("teams.%s.players: account %d is already on %s", side, p.AccountID, prev)
			}
			seen[p.AccountID] = side
		}
	}
	return nil
}

// resolve maps a name from table, or a decimal number, to its wire value.
// "" resolves to 0, meaning "not given".
func resolve(field, v string, table map[string]uint32) (uint32, error) {
	if v == "" {
		return 0, nil
	}
	if n, ok := table[v]; ok {
		return n, nil
	}
	if n, err := strconv.ParseUint(v, 10, 32); err == nil {
		return uint32(n), nil
	}
	return 0, &UnknownNameError{Field: field, Value: v}
}

// details builds the full detail set the GC gets for this spec. lobbyID is
// 0 for a create. Validate first: unknown names resolve to 0 here.
func (s Spec) details(cfg config.LobbyConfig, lobbyID uint64) gc.LobbyDetails {
	mode, _ := resolve("game_mode", s.GameMode, cfg.GameModes)
	if mode == 0 {
		mode = defaultGameMode
	}
	region, _ := resolve("region", s.Region, cfg.Regions)
	vis := gc.VisibilityPublic
	switch s.Visibility {
	case VisibilityFriends:
		vis = gc.VisibilityFriends
	case VisibilityUnlisted:
		vis = gc.VisibilityUnlisted
	}
	return gc.LobbyDetails{
		LobbyID:         lobbyID,
		GameName:        s.Name,
		PassKey:         s.Password.Reveal(),
		ServerRegion:    region,
		GameMode:        mode,
		LeagueID:        s.LeagueID,
		LeagueNodeID:    s.LeagueNodeID,
		Visibility:      vis,
		AllowSpectating: true,
		AllowCheats:     s.AllowCheats,
		TVDelaySeconds:  s.TVDelaySeconds,
	}
}

// Patch changes a spec; nil fields stay as they are.
type Patch struct {
	Name             *string
	Password         *PassKey
	GameMode         *string
	Region           *string
	TVDelaySeconds   *uint32
	Visibility       *string
	AllowCheats      *bool
	Launch           *string
	ReadyCheck       *bool
	AllowShortHanded *bool
	KickStrangers    *bool
	Admins           *[]uint32
	Metadata         *map[string]string
}

func (p Patch) apply(s Spec) Spec {
	s = s.clone()
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&s.Name, p.Name)
	set(&s.GameMode, p.GameMode)
	set(&s.Region, p.Region)
	set(&s.Visibility, p.Visibility)
	set(&s.Launch, p.Launch)
	if p.Password != nil {
		s.Password = *p.Password
	}
	if p.TVDelaySeconds != nil {
		s.TVDelaySeconds = *p.TVDelaySeconds
	}
	for _, b := range []struct {
		dst *bool
		v   *bool
	}{{&s.AllowCheats, p.AllowCheats}, {&s.ReadyCheck, p.ReadyCheck}, {&s.AllowShortHanded, p.AllowShortHanded}, {&s.KickStrangers, p.KickStrangers}} {
		if b.v != nil {
			*b.dst = *b.v
		}
	}
	if p.Admins != nil {
		s.Admins = append([]uint32(nil), (*p.Admins)...)
	}
	if p.Metadata != nil {
		s.Metadata = map[string]string{}
		for k, v := range *p.Metadata {
			s.Metadata[k] = v
		}
	}
	s.normalize()
	return s
}

// requestKey is the part of a spec that decides whether two create
// requests are the same, without the password (compared on its own) and
// without per-player progress.
func (s Spec) requestKey() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q|%d|%d|%q|%q|%d|%t|%q|%q|%t|%t|%t|%v|", s.Name, s.LeagueID, s.LeagueNodeID,
		s.GameMode, s.Region, s.TVDelaySeconds, s.AllowCheats, s.Visibility, s.Launch, s.ReadyCheck,
		s.AllowShortHanded, s.KickStrangers, s.Admins)
	keys := make([]string, 0, len(s.Metadata))
	for k := range s.Metadata {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%q=%q,", k, s.Metadata[k])
	}
	for _, side := range []Side{Radiant, Dire} {
		t := s.Teams.Team(side)
		fmt.Fprintf(&b, "|%s:%q:%q:", side, t.TeamRef, t.Name)
		for _, p := range t.Players {
			fmt.Fprintf(&b, "%d/%t,", p.AccountID, p.Captain)
		}
	}
	return b.String()
}
