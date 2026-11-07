package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
)

// healthcheck is for Docker's HEALTHCHECK, which has no curl in a
// distroless image: GET /livez and exit 0 only on 200. It reads no config
// file, since the probe must not depend on secrets being readable; the port
// comes from DOTA_LOBBY_SERVER_PORT when set.
func (c cli) healthcheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	url := fs.String("url", defaultHealthURL(), "URL to probe")
	timeout := fs.Duration("timeout", 5*time.Second, "request timeout")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *url, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s %s", *url, resp.Status, body)
	}
	return nil
}

func defaultHealthURL() string {
	port := "8080"
	if p := os.Getenv(config.EnvPrefix + "_SERVER_PORT"); p != "" {
		port = p
	}
	return "http://" + net.JoinHostPort("127.0.0.1", port) + "/livez"
}
