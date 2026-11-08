package lobby

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/kettleofketchup/dota_lobby/pkg/gc"
)

// Chat command names, without the prefix.
const (
	cmdHelp    = "help"
	cmdTeams   = "teams"
	cmdReady   = "ready"
	cmdUnready = "unready"
	cmdStart   = "start"
	cmdCancel  = "cancel"
)

// Per-player reply cooldowns. A cooldown silences the same command from
// the same player; it is what keeps a spammed command from flooding the
// lobby.
const (
	helpCooldown  = 30 * time.Second
	teamsCooldown = 10 * time.Second
	// denyCooldown follows a refusal, so a player hammering a command they
	// may not use gets one answer.
	denyCooldown = 10 * time.Second
)

// maxChatText bounds one message the bot sends.
const maxChatText = 300

// parseCommand returns the lowercased command of a chat line, or false when
// the line does not start with prefix. Arguments are ignored.
func parseCommand(prefix, text string) (string, bool) {
	text = strings.TrimSpace(text)
	if prefix == "" || !strings.HasPrefix(text, prefix) {
		return "", false
	}
	f := strings.Fields(text[len(prefix):])
	if len(f) == 0 || strings.HasPrefix(text[len(prefix):], " ") {
		return "", false
	}
	return strings.ToLower(f[0]), true
}

// isAdmin reports whether an account may run admin commands: the
// lobby's admins and lobby.admins.
func isAdmin(l Lobby, cfg config.LobbyConfig, account uint32) bool {
	return slices.Contains(l.Spec.Admins, account) || slices.Contains(cfg.Admins, account)
}

// chatEffect is what a command changes.
type chatEffect int

const (
	effNone chatEffect = iota
	effReady
	effUnready
	effStart
	effCancel
)

// chatInput is everything a command looks at.
type chatInput struct {
	Lobby   Lobby
	View    *gc.LobbyView
	Host    uint64
	Account uint32
	Admin   bool
	// Countdown is true while a start countdown runs.
	Countdown bool
	// Names are persona names seen in chat, for display only.
	Names map[uint32]string
	Cfg   config.LobbyConfig
}

// chatOutcome is a command's answer.
type chatOutcome struct {
	Effect chatEffect
	// Force is set for an admin start: it may launch from waiting.
	Force bool
	Reply string
	// Cooldown, when set, silences the same command from the same player
	// for that long.
	Cooldown time.Duration
}

func deny(reply string) chatOutcome { return chatOutcome{Reply: reply, Cooldown: denyCooldown} }

// runCommand decides what a command from a current lobby member does. It is
// pure; the caller applies the effect. Unknown commands get no answer.
func runCommand(cmd string, in chatInput) chatOutcome {
	l := in.Lobby
	p := in.Cfg.Chat.Prefix
	prep := l.State == StateWaiting || l.State == StateReady
	side, rostered := l.Spec.Teams.Side(in.Account)
	switch cmd {
	case cmdHelp:
		return chatOutcome{Reply: helpText(in), Cooldown: helpCooldown}
	case cmdTeams:
		return chatOutcome{Reply: teamsText(in), Cooldown: teamsCooldown}
	case cmdReady, cmdUnready:
		want := cmd == cmdReady
		switch {
		case !readyCheckOn(l, in.Cfg):
			return deny("There is no ready check in this lobby.")
		case !prep:
			return deny("Too late to change ready now.")
		case !rostered:
			return deny("Only rostered players can " + p + cmd + ".")
		}
		if want && !seatedOn(in.View, in.Host, in.Account, side) {
			return deny("Take a " + sideName(side) + " slot first, then " + p + "ready.")
		}
		if l.Spec.Teams.Player(in.Account).Ready == want {
			if want {
				return deny("You are already ready.")
			}
			return deny("You are not marked ready.")
		}
		eff := effUnready
		if want {
			eff = effReady
		}
		word := "not ready"
		if want {
			word = "ready"
		}
		ready, total := readyCount(l, in.Account, want)
		return chatOutcome{Effect: eff, Reply: fmt.Sprintf("%s is %s (%d/%d ready).", displayName(in.Names, in.Account), word, ready, total)}
	case cmdStart:
		switch {
		case !prep:
			return deny("The lobby cannot start now.")
		case in.Countdown:
			return deny("Already starting.")
		case !in.Admin && !rostered:
			return deny("Only rostered players and admins can " + p + "start.")
		case !in.Admin && l.State != StateReady:
			_, missing := readiness(l, in.View, in.Host, readyCheckOn(l, in.Cfg))
			return deny("Not ready yet: " + missingText(missing, in.Names) + ".")
		case !in.Admin && !canAutoLaunch(l, in.Cfg):
			return deny("No launch attempts left; an admin can " + p + "start.")
		case !hostGate(in.View, in.Host, l.HostSeat):
			return deny("The bot is still leaving its player slot; try again in a moment.")
		}
		return chatOutcome{Effect: effStart, Force: in.Admin, Reply: fmt.Sprintf(
			"Starting in %d s. An admin can %scancel.", int(in.Cfg.Chat.StartCountdown.Round(time.Second)/time.Second), p)}
	case cmdCancel:
		switch {
		case !in.Admin:
			return deny("Only an admin can " + p + "cancel.")
		case !in.Countdown:
			return deny("No start to cancel.")
		}
		return chatOutcome{Effect: effCancel, Reply: "Start cancelled."}
	}
	return chatOutcome{}
}

func helpText(in chatInput) string {
	p := in.Cfg.Chat.Prefix
	cmds := []string{p + cmdTeams}
	if readyCheckOn(in.Lobby, in.Cfg) {
		cmds = append(cmds, p+cmdReady, p+cmdUnready)
	}
	cmds = append(cmds, p+cmdStart)
	if in.Admin {
		cmds = append(cmds, p+cmdCancel)
	}
	return "Commands: " + strings.Join(cmds, ", ") + "."
}

// teamsText is the !teams answer: per side, who is seated, who is missing,
// who sits on the wrong side and, with a ready check, who is ready.
func teamsText(in chatInput) string {
	l := in.Lobby
	if l.Spec.Teams.Empty() {
		counts := map[gc.Team]int{}
		if in.View != nil {
			for _, m := range in.View.Members {
				if player(m, in.Host) {
					counts[m.Team]++
				}
			}
		}
		return fmt.Sprintf("Open lobby: Radiant %d, Dire %d.", counts[gc.TeamRadiant], counts[gc.TeamDire])
	}
	rc := readyCheckOn(l, in.Cfg)
	parts := make([]string, 0, 2)
	for _, s := range []Side{Radiant, Dire} {
		t := l.Spec.Teams.Team(s)
		var seated, ready int
		var missing, wrong []string
		for _, p := range t.Players {
			switch {
			case seatedOn(in.View, in.Host, p.AccountID, s):
				seated++
				if p.Ready {
					ready++
				}
			case seatedOn(in.View, in.Host, p.AccountID, other(s)):
				wrong = append(wrong, displayName(in.Names, p.AccountID))
			default:
				missing = append(missing, displayName(in.Names, p.AccountID))
			}
		}
		b := sideName(s)
		if t.Name != "" {
			b += " " + t.Name
		}
		b += fmt.Sprintf(": %d/%d seated", seated, len(t.Players))
		if rc {
			b += fmt.Sprintf(", %d ready", ready)
		}
		if len(missing) > 0 {
			b += ", missing " + strings.Join(missing, " ")
		}
		if len(wrong) > 0 {
			b += ", on the wrong side " + strings.Join(wrong, " ")
		}
		parts = append(parts, b)
	}
	return strings.Join(parts, ". ") + "."
}

func other(s Side) Side {
	if s == Dire {
		return Radiant
	}
	return Dire
}

func sideName(s Side) string {
	if s == Dire {
		return "Dire"
	}
	return "Radiant"
}

// seatedOn reports whether account sits on side as a player.
func seatedOn(v *gc.LobbyView, host uint64, account uint32, s Side) bool {
	if v == nil {
		return false
	}
	for _, m := range v.Members {
		if gc.AccountID(m.SteamID) == account {
			return player(m, host) && m.Team == s.team()
		}
	}
	return false
}

// readyCount counts ready rostered players as they will be once account's
// flag is set to want.
func readyCount(l Lobby, account uint32, want bool) (ready, total int) {
	for _, e := range l.Spec.Teams.All() {
		total++
		r := e.Ready
		if e.AccountID == account {
			r = want
		}
		if r {
			ready++
		}
	}
	return ready, total
}

// displayName is the persona name seen in chat, with the account ID, or the
// account ID alone. Persona names are only ever shown, never trusted.
func displayName(names map[uint32]string, account uint32) string {
	id := strconv.FormatUint(uint64(account), 10)
	if n := names[account]; n != "" {
		return n + " (" + id + ")"
	}
	return id
}

// missingText turns readiness's missing list into words.
func missingText(missing []string, names map[uint32]string) string {
	if len(missing) == 0 {
		return "the lobby is still settling"
	}
	out := make([]string, 0, len(missing))
	for _, m := range missing {
		kind, id, ok := strings.Cut(m, ":")
		if !ok {
			if n, err := strconv.ParseUint(m, 10, 32); err == nil {
				out = append(out, displayName(names, uint32(n))+" not seated")
			} else {
				out = append(out, "nobody on "+m)
			}
			continue
		}
		n, _ := strconv.ParseUint(id, 10, 32)
		switch kind {
		case "unready":
			out = append(out, displayName(names, uint32(n))+" not ready")
		case "stranger":
			out = append(out, displayName(names, uint32(n))+" is not rostered")
		default:
			out = append(out, m)
		}
	}
	return strings.Join(out, ", ")
}

// truncateChat cuts a message to maxChatText bytes on a rune boundary.
func truncateChat(s string) string {
	if len(s) <= maxChatText {
		return s
	}
	cut := maxChatText - len("...")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// cleanName keeps a persona name printable and short for display.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if utf8.RuneCountInString(s) > 32 {
		s = string([]rune(s)[:32])
	}
	return s
}
