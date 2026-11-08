package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
)

// healthcheck is for Docker's HEALTHCHECK, which has no curl in a
// distroless image: GET /livez and exit 0 only on 200. Without --url it
// finds the port the way serve does (--config, DOTA_LOBBY_CONFIG_FILE, the
// config search path and DOTA_LOBBY_SERVER_*), but reads only the server
// section: the probe must not depend on secrets or bot credentials being
// readable. --secrets is accepted for the same command line as serve and
// ignored.
func (c cli) healthcheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	var cf configFlags
	cf.register(fs)
	url := fs.String("url", "", "URL to probe (default: /livez on the configured server port)")
	timeout := fs.Duration("timeout", 5*time.Second, "request timeout")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if *url == "" {
		u, err := defaultHealthURL(cf)
		if err != nil {
			return err
		}
		*url = u
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

// defaultHealthURL is /livez on the configured server port, reached over
// loopback when the server listens on every interface.
func defaultHealthURL(cf configFlags) (string, error) {
	sc, err := config.LoadServer(config.Options{ConfigFile: cf.configFile})
	if err != nil {
		return "", fmt.Errorf("find server address: %w", err)
	}
	return "http://" + net.JoinHostPort(probeHost(sc.Host), strconv.Itoa(sc.Port)) + "/livez", nil
}

// probeHost maps a wildcard listen host to loopback.
func probeHost(host string) string {
	switch strings.Trim(host, "[]") {
	case "", "0.0.0.0":
		return "127.0.0.1"
	case "::":
		return "::1"
	}
	return host
}
