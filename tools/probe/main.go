// Command probe runs live experiments against Steam and the
// Dota 2 GC with a real bot account. See tools/probe/README.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type config struct {
	user         string
	pass         string
	sharedSecret string
	guardCode    string
	refreshToken string
	tokenOut     string
	stateDir     string
	verbose      bool
	quiet        bool

	welcomeTimeout time.Duration
	replyTimeout   time.Duration
	createWindow   time.Duration

	renew bool

	leagueID  uint
	nodeID    uint
	tvWire    int
	noJob     bool
	hold      bool
	lobbyName string
	lobbyPass string
	region    uint
	gameMode  string
	chat      bool
}

const usage = `usage: probe <command> [flags]

commands:
  auth      password + Steam Guard -> refresh token in the state dir (Q1)
  login     token login to GC welcome; --renew tries CM renewal (Q1, Q12)
  leagues   list leagues this account administers via 7633/7634 (Q2, Q18)
  create    create a lobby and record the GC's answer (Q4, Q6, Q7, Q19)
  code      print the current Steam Guard code from STEAM_SHARED_SECRET

env: STEAM_USER, STEAM_PASS (auth only), STEAM_SHARED_SECRET,
     STEAM_REFRESH_TOKEN (instead of the token file; set by sops exec-env)
run "probe <command> -h" for flags`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	run, ok := map[string]func(context.Context, *config, *reporter) error{
		"auth":    cmdAuth,
		"login":   cmdLogin,
		"leagues": cmdLeagues,
		"create":  cmdCreate,
		"code":    cmdCode,
	}[cmd]
	if !ok {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	c := &config{
		user:         os.Getenv("STEAM_USER"),
		pass:         os.Getenv("STEAM_PASS"),
		sharedSecret: os.Getenv("STEAM_SHARED_SECRET"),
		refreshToken: os.Getenv("STEAM_REFRESH_TOKEN"),
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.StringVar(&c.stateDir, "state", "", "state dir for token, captures and report (default probe-state/<user>)")
	fs.BoolVar(&c.verbose, "v", false, "debug logging from go-dota2")
	fs.BoolVar(&c.quiet, "q", false, "hide raw GC traffic on stderr (still recorded)")
	fs.DurationVar(&c.welcomeTimeout, "welcome-timeout", 90*time.Second, "time allowed to reach GC welcome")
	fs.DurationVar(&c.replyTimeout, "reply-timeout", 10*time.Second, "time allowed for each GC reply")
	switch cmd {
	case "auth", "login":
		fs.StringVar(&c.tokenOut, "token-out", "", `where to write a new refresh token: "-" for stdout (pipe into sops), a path, or empty for the state dir`)
	}
	switch cmd {
	case "auth":
		fs.StringVar(&c.guardCode, "guard-code", "", "Steam Guard code instead of generating one from STEAM_SHARED_SECRET")
	case "login":
		fs.BoolVar(&c.renew, "renew", false, "try CM refresh-token renewal once (Q12)")
	case "create":
		fs.UintVar(&c.leagueID, "league", 0, "league ID to request (0 = no league)")
		fs.UintVar(&c.nodeID, "node", 0, "league node ID (0 = unset)")
		fs.IntVar(&c.tvWire, "tv-wire", -1, "raw dota_tv_delay wire value, current numbering 0=10s 1=60s 2=120s 3=300s 4=900s (-1 = unset)")
		fs.BoolVar(&c.noJob, "no-job", false, "send the create without a source job ID (Q7)")
		fs.BoolVar(&c.hold, "hold", false, "keep the lobby and open the console (Q5, Q8)")
		fs.StringVar(&c.lobbyName, "name", "dota_lobby probe", "lobby name")
		fs.StringVar(&c.lobbyPass, "pass", "probe", "lobby pass key (use a throwaway; it is captured)")
		fs.UintVar(&c.region, "region", 0, "server region (0 = auto)")
		fs.BoolVar(&c.chat, "chat", false, "with -hold, join the lobby chat and answer !commands (D10)")
		fs.StringVar(&c.gameMode, "mode", "", "game mode: ap cm rd sd ar cd ad 1v1 turbo, or a DOTA_GameMode number (empty = GC default)")
		fs.DurationVar(&c.createWindow, "window", 30*time.Second, "how long to wait for the create outcome")
	}
	_ = fs.Parse(os.Args[2:])

	if c.user == "" {
		fmt.Fprintln(os.Stderr, "STEAM_USER is required")
		os.Exit(2)
	}
	if c.stateDir == "" {
		c.stateDir = filepath.Join("probe-state", c.user)
	}

	rep, err := newReporter(c.stateDir, c.user, cmd, c.quiet)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer rep.close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	rep.emit("", "start", nil)
	if err := run(ctx, c, rep); err != nil {
		rep.emit("", "error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
	rep.emit("", "done", nil)
}
