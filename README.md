# dota_lobby

A Go service that keeps a set of Steam bot accounts logged on to the Dota 2
Game Coordinator and exposes them over a REST API, for creating and running
league lobbies.

**Status: Phase 1.** Bots log on headless (refresh token, with password +
TOTP re-authentication), reach a GC session, reconnect with backoff, and
report their true state. Lobby endpoints arrive in Phase 3 and answer
`501 Not Implemented` until then. The design is in
[docs/plans/2026-11-06-lobby-service-architecture.md](docs/plans/2026-11-06-lobby-service-architecture.md).
Read [Known limitations](https://kettleofketchup.github.io/dota_lobby/known-limitations/)
before you use real accounts.

**Documentation:** <https://kettleofketchup.github.io/dota_lobby/>

## Requirements

- [mise](https://mise.jdx.dev) for the toolchain: `mise.toml` pins Go and
  golangci-lint, and `make`/`just` run them through `mise exec`.
- One Steam account per bot. For fully headless operation each account needs
  a Steam Guard mobile authenticator whose `shared_secret` you hold (see
  [docs/research/04-steam-auth-headless.md](docs/research/04-steam-auth-headless.md)).
- [sops](https://github.com/getsops/sops) + age, if you keep bot
  credentials encrypted in the repo (`just secrets ...`).

## Quick start (Docker Compose)

Set up each bot account and its sops credentials file first, as described in
[Bot accounts](https://kettleofketchup.github.io/dota_lobby/configuration/bot-accounts/).
Then:

```bash
mkdir -p config && cp config.yaml.example config/config.yaml   # edit the bots list
export DOTA_LOBBY_TAG=1.2.3   # image tag, no leading "v"
just docker up                # decrypt secrets/bots/*.sops.yaml to a tmpfs, compose up -d
curl -s http://127.0.0.1:8080/readyz
just docker down              # stop and delete the decrypted files
```

The container keeps its token store in the `dota_lobby_state` volume; the
decrypted credentials stay on a host tmpfs (`$XDG_RUNTIME_DIR/dota_lobby`)
and are mounted read-only. See
[Docker Compose](https://kettleofketchup.github.io/dota_lobby/deployment/docker-compose/).

## Build and test

```bash
make build        # bin/dota_lobby (or: just go build)
make test         # go test -race ./...
make lint         # golangci-lint run ./...
```

## Commands

```text
dota_lobby [serve] [--config FILE] [--secrets FILE]
dota_lobby auth --bot NAME [--code CODE] [--force] [--timeout 2m] [--config FILE] [--secrets FILE]
dota_lobby healthcheck [--url URL] [--timeout 5s]
dota_lobby version
```

- **serve** (the default) runs the service until SIGINT/SIGTERM. Shutdown
  fails `/readyz`, stops the HTTP server (5 s), then disconnects every bot in
  parallel (15 s budget) and releases the state-dir lock.
- **auth** logs one bot on with its password once and stores the refresh
  token in the token store. With a `shared_secret` it needs no input. Without
  one it takes `--code` (authenticator or email code) or asks on the
  terminal. It refuses to run while a `serve` process holds the state dir
  unless you pass `--force`; the running service then uses the new token at
  the bot's next logon.
- **healthcheck** does `GET http://127.0.0.1:$DOTA_LOBBY_SERVER_PORT/livez`
  (port 8080 by default) and exits 0 only on `200`. It is meant for Docker's
  `HEALTHCHECK CMD ["/usr/local/bin/dota_lobby","healthcheck"]`.

Exit codes: `0` success, `1` failure, `2` usage error.

## Configuration

Settings and secrets are separate. Copy the examples and edit them:

```bash
cp config.yaml.example config.yaml     # settings, safe to put in a ConfigMap
cp secrets.yaml.example secrets.yaml   # credentials, never committed
```

Layers, lowest first: built-in defaults, `config.yaml`, `secrets.yaml`,
per-bot credentials files, `DOTA_LOBBY_*` env vars, `DOTA_LOBBY_*_FILE`.

- Env names are the key path uppercased with `.` and `-` turned into `_`:
  `DOTA_LOBBY_SERVER_PORT=9000`,
  `DOTA_LOBBY_SECRETS_BOTS_LEAGUE_BOT_1_PASSWORD=...`.
- Adding `_FILE` reads the value from a file (Docker/K8s secrets):
  `DOTA_LOBBY_SECRETS_API_KEY_FILE=/run/secrets/api_key`.
- Files are found via `--config`/`--secrets`, `DOTA_LOBBY_CONFIG_FILE`/
  `DOTA_LOBBY_SECRETS_FILE`, or the search path (`$DOTA_LOBBY_CONFIG_DIR`,
  `/etc/dota_lobby`, `.`, `./config`, `~/.config/dota_lobby`; secrets also in
  `/run/secrets/dota_lobby`).
- Unknown keys are an error, so typos fail at startup.

Every secret is a `config.Secret`, which prints as `***REDACTED***` in logs,
`fmt` and JSON. Tokens, passwords and `shared_secret` never appear in an API
response.

### Bot credentials

Each bot needs a password and, for headless re-authentication, the
authenticator's `shared_secret`. A `refresh_token` is optional: it only seeds
the token store the first time.

They can come from `secrets.yaml` (`secrets.bots.<name>`), from a per-bot
credentials file, or from env. A credentials file uses the same keys as the
sops files in `secrets/bots/` (`STEAM_USER`, `STEAM_PASS`,
`STEAM_SHARED_SECRET`, `STEAM_REFRESH_TOKEN`) or the lowercase names, as YAML,
JSON or dotenv. The service looks for `<credentials_dir>/<bot>.yaml` (or
`.yml`, `.json`, `.env`), or `bots[].credentials_file`. With no `bots:` list
in the config, every file in `credentials_dir` becomes a bot.

### Using the sops files

The service never reads encrypted files; decrypt them for it. Manage them
with `just secrets init`, `just secrets new-bot <name>`, `just secrets edit
<name>` and `just secrets list`.

One bot, decrypted only for the life of the process:

```bash
sops exec-file --no-fifo --filename league_bot_1.yaml \
  secrets/bots/league_bot_1.sops.yaml \
  'DOTA_LOBBY_CREDENTIALS_DIR=$(dirname {}) bin/dota_lobby serve'
```

Several bots: decrypt into a tmpfs and point `credentials_dir` at it.

```bash
umask 077   # passwords and shared secrets: owner-only files
install -d -m 700 /run/dota_lobby/creds
for f in secrets/bots/*.sops.yaml; do
  sops decrypt "$f" > "/run/dota_lobby/creds/$(basename "$f" .sops.yaml).yaml"
done
DOTA_LOBBY_CREDENTIALS_DIR=/run/dota_lobby/creds bin/dota_lobby serve
```

`sops exec-env` works too if you map the variables to the service's names:

```bash
sops exec-env secrets/bots/league_bot_1.sops.yaml \
  'DOTA_LOBBY_SECRETS_BOTS_LEAGUE_BOT_1_PASSWORD="$STEAM_PASS" \
   DOTA_LOBBY_SECRETS_BOTS_LEAGUE_BOT_1_SHARED_SECRET="$STEAM_SHARED_SECRET" \
   DOTA_LOBBY_SECRETS_BOTS_LEAGUE_BOT_1_REFRESH_TOKEN="$STEAM_REFRESH_TOKEN" \
   bin/dota_lobby serve'
```

### Tokens and state

`state_dir` (default `/var/lib/dota_lobby`) holds a `LOCK` file, taken with
`flock` so two processes never run the same accounts, and one token store
per bot at `<state_dir>/bots/<username>/token.json` (mode 0600). On start a
bot uses its stored token. `refresh_token` (`STEAM_REFRESH_TOKEN`) seeds the
store when it holds nothing usable (no file, an unreadable one, or an expired
token), and replaces a stored token that expires earlier, so a token minted
elsewhere can be shipped through sops. If there is still no token, or Steam
rejects it, the bot authenticates with password + TOTP and stores the new
token. Quote secret values in the sops files: an unquoted `STEAM_PASS: 0123`
is a number to YAML and is rejected at startup. If the host clock cannot be
kept on NTP, `steam.totp_skew` shifts the Steam Guard code time. Tokens close to
expiry are renewed in the background. Renewed tokens stay in `state_dir`;
nothing is written back to sops, so keep `state_dir` on a persistent volume.

A bot without a `shared_secret` cannot recover on its own: when its token
dies it goes to `needs_attention`. Run `dota_lobby auth --bot NAME --force`
(with `--code` or at a terminal) while the service runs, then
`POST /v1/bots/NAME/retry` to make the bot log on with the new token.

## API

Health endpoints are open. Everything under `/v1` needs the API key
(`secrets.api_key`) when one is set, as `X-API-Key: <key>` or
`Authorization: Bearer <key>`. Without a key `/v1` is open and a warning is
logged. Errors share one envelope:
`{"error": {"code": "...", "message": "...", "retryable": false}}`.

| Method | Path | Auth | Response |
|---|---|---|---|
| GET | `/livez` | no | `200 {"status":"ok"}`; `503` with a `stalled_bots` count if a bot's loop has not moved for 30 s (names go to the log). Does not depend on Steam |
| GET | `/readyz` | no | `200` when at least `readiness.min_ready_bots` bots are `gc_ready`; `503` otherwise and while shutting down |
| GET | `/health` | no | Always `200`: readiness, bot counts per state, version, uptime |
| GET | `/v1/bots` | yes | `{"bots": [...]}`: name, username, state, ready, reason, last_error (EResult name), last_error_message, since, steam_id (string), limited, lobby_stale, token_expires_at, attempt, gc_queue_position |
| GET | `/v1/bots/{name}` | yes | One bot, same shape; `404` if unknown |
| POST | `/v1/bots/{name}/retry` | yes | `202`: a bot in `needs_attention`, `backoff` or `cooling_down` re-reads its token store and tries again now; a no-op otherwise. `404` if unknown |
| any | `/v1/lobbies`, `/v1/lobbies/...` | yes | `501 not_implemented` until Phase 3 |
| any | `/bots`, `/lobby/create`, `/lobby/info` | yes | `410 gone`; the pre-v1 placeholders were removed |

Bot states: `disconnected`, `connecting`, `logging_on`, `authenticating`,
`steam_online`, `gc_hello`, `gc_queued`, `gc_ready`, `backoff`,
`cooling_down`, `needs_attention`, `suspended`, `stopped`.

```bash
curl -s localhost:8080/readyz
curl -s -H "X-API-Key: $KEY" localhost:8080/v1/bots
```

## Layout

```text
cmd/dota_lobby/   serve, auth, healthcheck
pkg/config/       Viper loading, env + _FILE, credentials files, Secret
pkg/gc/           the only importer of go-steam/go-dota2; gc.Client + fake
pkg/auth/         token store, TOTP, EResult classifier, state-dir lock
pkg/bot/          per-bot supervisor state machine, GC worker, manager
pkg/api/          HTTP API
tools/probe/      Phase 0 live probe (its own module)
```

## Development

Build, test, lint, docs preview and the release process are described on the
[Development](https://kettleofketchup.github.io/dota_lobby/development/) page.

## License

See LICENSE file for details.

## References

- [go-dota2](https://github.com/paralin/go-dota2) - Dota 2 client implementation
- [go-steam](https://github.com/paralin/go-steam) - Steam client implementation
- [Viper](https://github.com/spf13/viper) - Configuration management
