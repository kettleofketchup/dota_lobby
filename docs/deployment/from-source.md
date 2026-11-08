# Build from source

You can build and run the service as a plain binary, without containers. The service and its
configuration are the same; only the packaging differs.

## Requirements

- Go **1.26 or newer** (`go.mod` says `go 1.26.0`). The repository pins Go 1.27.1 and
  golangci-lint in `mise.toml`; with [mise](https://mise.jdx.dev) installed, run
  `mise install` once and `make`/`just` use those versions through `mise exec`.
- `git` and `make` (or [just](https://just.systems/) instead of `make`).
- For the bot credentials: sops, age and jq, as on [Bot accounts](../configuration/bot-accounts.md).
- The same outbound network access and clock requirements as the container; see
  [Prerequisites](../getting-started/prerequisites.md).

## Build

```bash
git clone --branch v1.2.3 --depth 1 https://github.com/kettleofketchup/dota_lobby.git
cd dota_lobby
make build        # or: just go build
```

This produces `bin/dota_lobby`. The version comes from `git describe`.

## Configure

```bash
mkdir -p config
cp config.yaml.example config/config.yaml
```

Edit `config/config.yaml`:

- set `state_dir` to a directory you own that survives restarts, for example
  `~/.local/state/dota_lobby` (written as an absolute path). The service creates it with mode
  `0700`;
- list your bots under `bots:` (or leave the list out to use every file in
  `credentials_dir`);
- set `server.host` to `127.0.0.1` unless other machines need to reach the API.

!!! warning "The default `host` listens on all interfaces"

    Outside a container, `host: 0.0.0.0` makes the API reachable from your whole network. Set
    an API key and `server.host: 127.0.0.1` unless other machines need it (behind a TLS
    reverse proxy, see
    [Exposing it beyond localhost](docker-compose.md#exposing-it-beyond-localhost)).

The service looks for `config.yaml` in `$DOTA_LOBBY_CONFIG_DIR`, `/etc/dota_lobby`, `.`,
`./config` and `~/.config/dota_lobby`, relative to the directory you start it **from**. The
full lookup rules are in the [Configuration reference](../configuration/index.md#where-the-files-are-found).

## Give it the credentials

The service does not read sops files; decrypt them for it. With the bots set up as in
[Bot accounts](../configuration/bot-accounts.md):

=== "Several bots (tmpfs)"

    ```bash
    umask 077   # passwords and shared secrets: owner-only files
    d=$XDG_RUNTIME_DIR/dota_lobby/creds
    install -d -m 700 "$d"
    for f in secrets/bots/*.sops.yaml; do
      sops decrypt "$f" > "$d/$(basename "$f" .sops.yaml).yaml"
    done
    DOTA_LOBBY_CREDENTIALS_DIR=$d bin/dota_lobby serve
    ```

    Delete the directory when the service stops (`rm -rf "$d"`). It is on tmpfs, so it is gone
    after a reboot anyway.

=== "One bot (sops exec-file)"

    ```bash
    sops exec-file --no-fifo --filename league_bot_1.yaml \
      secrets/bots/league_bot_1.sops.yaml \
      'DOTA_LOBBY_CREDENTIALS_DIR=$(dirname {}) bin/dota_lobby serve'
    ```

    The decrypted file exists only while the service runs.

=== "Environment variables"

    ```bash
    sops exec-env secrets/bots/league_bot_1.sops.yaml \
      'DOTA_LOBBY_SECRETS_BOTS_LEAGUE_BOT_1_PASSWORD="$STEAM_PASS" \
       DOTA_LOBBY_SECRETS_BOTS_LEAGUE_BOT_1_SHARED_SECRET="$STEAM_SHARED_SECRET" \
       DOTA_LOBBY_SECRETS_BOTS_LEAGUE_BOT_1_REFRESH_TOKEN="$STEAM_REFRESH_TOKEN" \
       bin/dota_lobby serve'
    ```

The API key goes in `secrets.yaml` (anywhere on the search path, mode `0600`) or in
`DOTA_LOBBY_SECRETS_API_KEY`.

## Run

```bash
bin/dota_lobby serve --config config/config.yaml
curl -s http://127.0.0.1:8080/readyz
```

`make run` and `just go run` build first and then run `serve` from the repository root.

Stop it with Ctrl+C or SIGTERM. The service fails `/readyz`, finishes in-flight requests (up
to 5 seconds), disconnects every bot (up to 15 seconds) and releases the state lock. A second
Ctrl+C kills a shutdown that hangs. Config is read once at startup, so restart it after
editing a file.

To keep it running as a service, use your init system: a systemd unit with
`WorkingDirectory=` set to the directory that holds `config/`, `StateDirectory=dota_lobby`
(which gives you `/var/lib/dota_lobby`, the default `state_dir`) and `TimeoutStopSec=30`, and
the credentials in `$RUNTIME_DIRECTORY` or through `LoadCredential=`. Or use
[Docker Compose](docker-compose.md), which is the tested path.

## Other subcommands

```text
dota_lobby [serve] [--config FILE] [--secrets FILE]
dota_lobby auth --bot NAME [--code CODE] [--force] [--timeout 2m] [--config FILE] [--secrets FILE]
dota_lobby healthcheck [--url URL] [--timeout 5s]
dota_lobby version
```

- **auth** logs one bot in with its password once and stores the refresh token in
  `state_dir`. With a `shared_secret` it needs no input; without one it takes `--code` or asks
  on the terminal. It refuses to run while `serve` holds the state directory unless you pass
  `--force`.
- **healthcheck** does `GET /livez` on `127.0.0.1` and exits `0` only on `200`.

Exit codes: `0` success, `1` failure, `2` usage error.
