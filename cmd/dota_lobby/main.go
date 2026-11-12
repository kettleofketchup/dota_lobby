// Command dota_lobby runs the lobby service and its operator tools.
//
//	dota_lobby [serve] [flags]          run the service (the default); --simulate fakes Steam
//	dota_lobby auth --bot NAME [flags]  log a bot on with its password once and store the token
//	dota_lobby healthcheck [flags]      GET /livez on this host; exit 0 when alive
//	dota_lobby backup --out PATH        copy the database, also while serve runs
//	dota_lobby version                  print the build version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// errUsage marks a command-line mistake; run maps it to exitUsage.
var errUsage = errors.New("usage error")

// cli holds the process's streams so commands can be tested.
type cli struct {
	stdin          io.Reader
	stdout, stderr io.Writer
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	// After the first signal, restore the default handling so a second one
	// kills a shutdown that hangs.
	go func() {
		<-ctx.Done()
		stop()
	}()
	code := cli{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}.run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

const usageText = `usage:
  dota_lobby [serve] [--config FILE] [--secrets FILE] [--simulate]
  dota_lobby auth --bot NAME [--code CODE] [--force] [--timeout 2m] [--config FILE] [--secrets FILE]
  dota_lobby healthcheck [--url URL] [--timeout 5s]
  dota_lobby backup --out PATH|DIR/ [--keep-manual N] [--missing-ok] [--db FILE] [--timeout 2m] [--config FILE]
  dota_lobby version
`

func (c cli) run(ctx context.Context, args []string) int {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = c.serve(ctx, args)
	case "auth":
		err = c.auth(ctx, args)
	case "healthcheck":
		err = c.healthcheck(ctx, args)
	case "backup":
		err = c.backup(ctx, args)
	case "version":
		_, err = fmt.Fprintf(c.stdout, "dota_lobby %s (commit %s, built %s)\n", version, commit, buildDate)
	case "help", "-h", "--help":
		_, _ = io.WriteString(c.stdout, usageText)
		return exitOK
	default:
		err = fmt.Errorf("%w: unknown command %q", errUsage, cmd)
	}
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintf(c.stderr, "%v\n%s", err, usageText)
		return exitUsage
	default:
		_, _ = fmt.Fprintf(c.stderr, "dota_lobby %s: %v\n", cmd, err)
		return exitError
	}
}

// configFlags are the flags every config-reading command takes.
type configFlags struct {
	configFile, secretsFile string
}

func (f *configFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.configFile, "config", "", "config file (default: $DOTA_LOBBY_CONFIG_FILE or the search path)")
	fs.StringVar(&f.secretsFile, "secrets", "", "secrets file (default: $DOTA_LOBBY_SECRETS_FILE or the search path)")
}

func (f configFlags) load(log *slog.Logger) (*config.Config, error) {
	cfg, err := config.Load(config.Options{ConfigFile: f.configFile, SecretsFile: f.secretsFile, Logger: log})
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

// parse parses a subcommand's flags; positional arguments are a usage
// error.
func (c cli) parse(fs *flag.FlagSet, args []string) error {
	fs.SetOutput(c.stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%w: unexpected arguments %q", errUsage, fs.Args())
	}
	return nil
}

// defaultLogConfig is used until the config is loaded.
var defaultLogConfig = config.LogConfig{Level: "info", Format: "json"}

// newLogger builds the service logger from log.level and log.format.
func newLogger(w io.Writer, lc config.LogConfig) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(lc.Level)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if lc.Format == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}
