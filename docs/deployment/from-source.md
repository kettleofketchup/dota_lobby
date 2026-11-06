# Build from source

You can build and run the service as a plain binary, without containers. The service and its
config files are the same; only the packaging differs.

## Requirements

- Go 1.24 or newer. `go.mod` asks for Go 1.24.11; an older 1.24 release downloads that
  toolchain automatically unless `GOTOOLCHAIN=local` is set.
- `git` and `make` (or [just](https://github.com/casey/just) instead of `make`).
- The same outbound network access and bot accounts as the container; see
  [Prerequisites](../getting-started/prerequisites.md).

## Build

```bash
git clone --branch v1.2.3 --depth 1 https://github.com/kettleofketchup/dota_lobby.git
cd dota_lobby
make build
```

This produces `bin/dota_lobby`. `make build` runs `go mod tidy` and `go mod download` first,
so it may touch `go.mod` and `go.sum` in your checkout.

Alternatively, with just:

```bash
just go::build
```

It builds the same `bin/dota_lobby` at the repository root, and works from any directory
inside the repository.

Both take the version for the first log line from `git describe`, which already starts with
a `v`, so a build of the `v1.2.3` tag logs `Dota Lobby Manager vv1.2.3`. That is cosmetic.

## Configure

Copy the two example files and edit them as described in the
[Quick start](../getting-started/quick-start.md#3-edit-the-two-files) and the
[Configuration reference](../configuration/index.md):

```bash
mkdir config
cp config.yaml.example config/config.yaml
cp secrets.yaml.example config/secrets.yaml
chmod 600 config/*.yaml
grep -n your_steam config/secrets.yaml
```

The last command must print nothing. No `chown` is needed: the binary runs as you.

The service looks for `config.yaml` and `secrets.yaml` in this order:

1. the current working directory (`.`);
2. a `config/` subdirectory of the current working directory (`./config`);
3. `$HOME/.config/dota_lobby/`.

The first two are relative to the directory you start the binary **from**, not to where the
binary lives.

!!! warning "The default `host` listens on all interfaces"

    Outside a container, `host: "0.0.0.0"` makes the API reachable from your whole network.
    Set `server.api_key`, and set `server.host` to `127.0.0.1` unless other machines need to
    reach it (behind a TLS reverse proxy, see
    [Exposing it beyond localhost](docker-compose.md#exposing-it-beyond-localhost)).

## Run

From the repository root (so `./config` is found):

```bash
./bin/dota_lobby
```

or `make run` / `just go::run`, which build first and then start it from the repository
root. Check the startup lines the same way as in the
[Quick start](../getting-started/quick-start.md#5-start-it-and-watch-the-logs), and stop at the
first `Failed to log on:` line.

Stop it with Ctrl+C. The service handles SIGINT and SIGTERM: it shuts down the HTTP server
(waiting up to 10 seconds) and disconnects the bots. Config is read once at startup, so
restart it after editing a file.

To keep it running as a service, use your init system (for example a systemd unit with
`WorkingDirectory=` set to the directory that holds `config/`), or use
[Docker Compose](docker-compose.md), which is the tested path.
