# Configuration reference

Settings and secrets are kept apart:

- **`config.yaml`** holds settings and the list of bots. It contains nothing secret, so it can
  be world-readable or live in a Kubernetes ConfigMap.
- **Secrets** (the API key and the bots' Steam credentials) come from a secrets file, from one
  decrypted credentials file per bot, or from environment variables. In the recommended
  [Docker Compose](../deployment/docker-compose.md) setup they come from the sops files in
  `secrets/`, decrypted onto a tmpfs at start.

Configuration is read **once, at startup**. Restart the service after a change
(`just docker restart` with Compose).

## Layers

Each layer overrides the ones before it:

1. built-in defaults (the [table below](#keys));
2. `config.yaml`;
3. the secrets file (`secrets.yaml`);
4. per-bot credentials files (`credentials_dir`, or `bots[].credentials_file`);
5. `DOTA_LOBBY_*` environment variables;
6. `DOTA_LOBBY_*_FILE` environment variables, which read the value from a file.

**Unknown keys are an error.** A typo such as `stem:` or `hello_intervall:` stops the service
at startup with a `decode config` error instead of being ignored. All problems found by
validation are reported together.

### Environment variables

Every key has an environment name: `DOTA_LOBBY_`, then the key path joined with `_`,
uppercased, with `-` turned into `_`.

| Key | Environment variable |
|---|---|
| `server.port` | `DOTA_LOBBY_SERVER_PORT` |
| `state_dir` | `DOTA_LOBBY_STATE_DIR` |
| `steam.totp_skew` | `DOTA_LOBBY_STEAM_TOTP_SKEW` |
| `secrets.api_key` | `DOTA_LOBBY_SECRETS_API_KEY` |
| password of bot `league-bot-1` | `DOTA_LOBBY_SECRETS_BOTS_LEAGUE_BOT_1_PASSWORD` |

Append `_FILE` to read the value from a file, the Docker and Kubernetes secrets convention:
`DOTA_LOBBY_SECRETS_API_KEY_FILE=/run/secrets/api_key`. A trailing newline is stripped.
Setting both `FOO` and `FOO_FILE` is an error.

The container image sets `DOTA_LOBBY_STATE_DIR=/app/state`, which overrides `state_dir` from
`config.yaml`. `compose.yaml` sets `DOTA_LOBBY_CREDENTIALS_DIR`.

## Where the files are found

| File | Looked up in, first match wins |
|---|---|
| `config.yaml` (or `.yml`) | `--config FILE`, then `$DOTA_LOBBY_CONFIG_FILE`, then the search path: `$DOTA_LOBBY_CONFIG_DIR`, `/etc/dota_lobby`, `.`, `./config`, `~/.config/dota_lobby` |
| `secrets.yaml` (or `.yml`) | `--secrets FILE`, then `$DOTA_LOBBY_SECRETS_FILE`, then `$DOTA_LOBBY_SECRETS_DIR` (default `/run/secrets/dota_lobby`), then the same search path |

`.` and `./config` are relative to the working directory. In the container that is `/app`, so
`config.yaml` is read from `/app/config/config.yaml` and the secrets file from
`/run/secrets/dota_lobby/secrets.yaml` (written by `just docker up`) or, if that does not
exist, `/app/config/secrets.yaml`. Only one secrets file is read.

A missing file found through the search path is fine (defaults apply). A file named
explicitly (flag or `*_FILE` variable) must exist. A file that exists but cannot be read is a
fatal error. A secrets file that is still sops-encrypted is refused with
`... is still sops-encrypted; decrypt it first`.

## Keys

`config.yaml` keys. Durations use Go syntax (`10s`, `5m`, `3h`).

### Used now

| Key | Type | Default | Meaning |
|---|---|---|---|
| `server.host` | string | `0.0.0.0` | Listen address. Must stay `0.0.0.0` in a container |
| `server.port` | int | `8080` | Listen port. In a container, change the host side of the port mapping instead |
| `server.max_body_bytes` | int | `65536` | Largest accepted request body |
| `state_dir` | path | `/var/lib/dota_lobby` | Token store and `LOCK` file. Must be writable and persistent. `/app/state` in the image |
| `credentials_dir` | path | empty | Directory with one decrypted credentials file per bot, see [Bot credentials](#bot-credentials) |
| `log.level` | string | `info` | `debug`, `info`, `warn` or `error` |
| `log.format` | string | `json` | `json` or `text` |
| `readiness.min_ready_bots` | int | `1` | `/readyz` answers `200` once this many bots are `gc_ready`. `0` makes it ready with no bots |
| `steam.login_stagger` | duration | `10s` | Gap between two bots' logons |
| `steam.reconnect_backoff_min` | duration | `5s` | First reconnect delay (full jitter, doubling) |
| `steam.reconnect_backoff_max` | duration | `5m` | Largest reconnect delay |
| `steam.cooldown_rate_limited` | duration | `30m` | Pause after `RateLimitExceeded` (84) or `AccountLoginDeniedThrottle` (87); logons pause for every bot |
| `steam.cooldown_elsewhere` | duration | `10m` | Pause after `LoggedInElsewhere` (6) or `LogonSessionReplaced` (34) |
| `steam.totp_skew` | duration | `0s` | Steam's clock minus the host clock, added before computing Steam Guard codes. At most ±10m. Keep the host on NTP instead whenever you can |
| `gc.hello_interval` | duration | `10s` | How often the game coordinator hello is repeated until the welcome |
| `gc.request_timeout` | duration | `10s` | Default timeout of a game coordinator request. For a request an API call waits on, such as `/v1/leagues/{id}/nodes`, it includes the time queued behind the bot's other requests and is capped at `12s` so the answer arrives before the 15 s write timeout |
| `gc.create_timeout` | duration | `2m` | How long a lobby create may take before the lobby fails with `create_timeout` |
| `league.sources` | list | `[gc, config]` | Where a bot's leagues come from: `gc` asks the game coordinator which leagues the account administers, `config` uses `bots[].leagues`. `web` is accepted but not used yet |
| `league.refresh_interval` | duration | `30m` | How often each ready bot's league list is fetched again. It is also fetched every time the bot reaches `gc_ready` |
| `league.allow_unverified` | bool | `false` | Keep using a bot's configured leagues after the game coordinator list is known, even those the game coordinator does not list. Leave it off unless the game coordinator list is wrong |
| `store.path` | path | empty | The SQLite database. Empty means `<state_dir>/dota_lobby.db`. Created mode `0600`; keep it on the persistent state volume. It holds the lobby passwords |
| `store.retention` | duration | `720h` | How long ended lobbies and their events are kept before an hourly sweep deletes them |
| `webhooks` | list | empty | Event receivers, see [`webhooks[]`](#webhooks) |
| `webhook_delivery.timeout` | duration | `10s` | How long one delivery attempt may take, response included |
| `webhook_delivery.max_age` | duration | `24h` | How long after an event a failing delivery is retried before it is given up |
| `bots` | list | empty | The bots, see below |

### `lobby`

How lobbies run. See [Lobbies](../api/index.md#lobbies) for what each step does.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `lobby.host_seat_strategy` | list | `[caster, self_kick]` | How the bot leaves its player slot, tried in order: `caster`, `self_kick`, `player_pool`, `spectator`, `no_team`. A launch that fails moves on to the next one. See [Host seat](../api/index.md#host-seat) |
| `lobby.host_seat_confirm_timeout` | duration | `3s` | How long one strategy may take to show in the lobby before the next is tried |
| `lobby.max_launch_attempts` | int | `3` | Launches before only a forced launch tries again, 1-10 |
| `lobby.pending_timeout` | duration | `10m` | How long a lobby waits for a free bot before it times out |
| `lobby.idle_timeout` | duration | `30m` | How long after it was accepted a lobby may wait for its launch |
| `lobby.auto_launch_delay` | duration | `10s` | How long a `launch: auto` lobby stays `ready` before it launches |
| `lobby.launch_settle_timeout` | duration | `2m` | A launch that leaves the lobby screen unchanged this long has failed |
| `lobby.server_stall_timeout` | duration | `10m` | How long a game server may take before the lobby times out |
| `lobby.ingame_backstop` | duration | `3h` | How long a match is followed without an outcome |
| `lobby.postgame_linger` | duration | `30s` | How long the bot stays in the lobby after the outcome |
| `lobby.enforce_cooldown` | duration | `10s` | Least time between two seat corrections for the same member |
| `lobby.readopt_timeout` | duration | `5m` | After a restart, how long a lobby waits for its bot to be ready again |
| `lobby.drain_timeout` | duration | `10s` | Time the lobby step of shutdown may take |
| `lobby.destroy_on_shutdown` | bool | `false` | Destroy lobbies that have not launched at shutdown, instead of leaving them for the next start |
| `lobby.admins` | list of int | `[]` | Account IDs allowed admin commands in every lobby's chat. See [Lobby chat](../operations/lobby-chat.md) |
| `lobby.chat.enabled` | bool | `false` | The bot joins each lobby's chat and answers commands; also turns on the ready check for lobbies that ask for it |
| `lobby.chat.prefix` | string | `"!"` | What every chat command starts with, 1-4 characters without spaces. Not `/`: the Dota client never sends those |
| `lobby.chat.send_interval` | duration | `1s` | Least time between two bot messages in one lobby |
| `lobby.chat.start_countdown` | duration | `10s` | Time from `!start` to the launch, during which an admin can `!cancel` |
| `lobby.invite.min_resend` | duration | `30s` | Least time between two invites to the same player |
| `lobby.invite.max_per_player` | int | `5` | Invites per player, the automatic one included |
| `lobby.regions` | map | `{us_west: 1, us_east: 2, europe: 3, sea: 5}` | Region names for requests. The numbers are unconfirmed; keep them current by hand |
| `lobby.game_modes` | map | `{ap: 1, cm: 2, cd: 16, ad: 22, turbo: 23}` | Game mode names for requests |

### `bots[]`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `name` | string | required | Identifies the bot in the API, the logs, environment names and credentials file names. 1-64 letters, digits, `_` or `-`. `bot-a` and `bot_a` count as the same name |
| `username` | string | `STEAM_USER` from the credentials file, then `name` | The Steam account name. It names the token store directory |
| `enabled` | bool | `true` | A disabled bot is not started |
| `credentials_file` | path | `<credentials_dir>/<name>.yaml` | This bot's credentials file, if it is not in `credentials_dir` |
| `leagues` | list of int | empty | Leagues this bot may host, used until the game coordinator list has been fetched once (unverified). See [`GET /v1/leagues`](../api/index.md#get-v1leagues) |
| `tags` | list of string | empty | Free-form labels |

With no `bots:` list, every file in `credentials_dir` becomes an enabled bot named after the
file. Two bots may not use the same Steam account.

### `webhooks[]`

Each entry is one receiver of signed event deliveries; see [Webhooks](../operations/webhooks.md).

| Key | Meaning |
|---|---|
| `url` | Absolute `http` or `https` URL. Each URL may appear once (compared case-insensitively). Use `https` beyond a private network |
| `events` | Event types to send, as globs: `["lobby.*", "match.*"]`, `["match.ended"]`, `["*"]` for every event. Empty or left out also sends every event |
| `token_header` | Optional request header that carries `secrets.webhook_tokens[url]`, for a receiver that already checks a static token. `Content-Type`, `User-Agent` and `X-Dota-Lobby-*` are taken |

A webhook without `secrets.webhook_secrets[url]` stops startup, as does a `token_header`
without its token or a token without a `token_header`.

### Accepted, not used yet

These keys are validated so a config written now keeps working, but nothing reads them in this
version: `metrics.addr` (no metrics listener yet), `league.webapi_enrichment`,
`lobby.series_types` and `match.*`. Their defaults are in
[`config.yaml.example`](https://github.com/kettleofketchup/dota_lobby/blob/main/config.yaml.example).

## Secrets

| Key | Meaning |
|---|---|
| `secrets.api_key` | API key for everything under `/v1`. Without it `/v1` is public and the service logs `API key authentication disabled; /v1 endpoints are public` |
| `secrets.webapi_key`, `secrets.opendota_key` | Accepted, used from later phases |
| `secrets.webhook_secrets` | Map from webhook URL to its signing secret. Required for every entry in `webhooks` |
| `secrets.webhook_tokens` | Map from webhook URL to the token sent in its `token_header` |
| `secrets.bots.<name>.password` | The bot's Steam password |
| `secrets.bots.<name>.shared_secret` | The authenticator's base64 `shared_secret`. With it the bot re-authenticates on its own |
| `secrets.bots.<name>.refresh_token` | Seeds the token store, see [Bot accounts](bot-accounts.md#how-the-service-uses-the-credentials) |
| `secrets.bots.<name>.device_name` | Device name shown in the account's authorised devices |

Every secret is a `Secret` value: it prints as `***REDACTED***` in logs, `fmt` output and JSON,
and never appears in an API response. **Quote every secret value** in YAML: an unquoted value
that YAML reads as a number or boolean is rejected at startup rather than rewritten (the
message names the key, never the value).

`server.api_key` from older configs still works: it is moved to `secrets.api_key` with the
warning `server.api_key is deprecated; move it to secrets.api_key`.

## Bot credentials

A bot's credentials can come from any of these. Later sources override earlier ones, key by
key:

1. **The secrets file**, under `secrets.bots.<name>`:

    ```yaml
    secrets:
      api_key: "..."
      bots:
        league_bot_1:
          password: "..."
          shared_secret: "..."
    ```

2. **A credentials file** per bot: `<credentials_dir>/<name>.yaml` (or `.yml`, `.json`,
   `.env`, or `<name>.sops.yaml` decrypted in place), or the path in `bots[].credentials_file`.
   It uses the same keys as the sops files in `secrets/bots/`:

    | Key | Field |
    |---|---|
    | `STEAM_USER` | Steam account name (`username`) |
    | `STEAM_PASS` | `password` |
    | `STEAM_SHARED_SECRET` | `shared_secret` |
    | `STEAM_REFRESH_TOKEN` | `refresh_token` |
    | `STEAM_DEVICE_NAME` | `device_name` |

    The lowercase field names work too. YAML (what `sops decrypt` writes for a `.sops.yaml` file), JSON and dotenv
    (`sops decrypt --output-type dotenv`) are all accepted. Unknown keys are ignored, so the
    file can carry notes. If `bots[].username` is set it must match `STEAM_USER`.

3. **Environment variables**: `DOTA_LOBBY_SECRETS_BOTS_<NAME>_PASSWORD`, `..._SHARED_SECRET`,
   `..._REFRESH_TOKEN`, or the same with `_FILE`.

The service never decrypts anything itself; a file that still has a `sops:` block is refused.

### The old `secrets.yaml` format

The pre-1.0 format, a list of `username`/`password` pairs, still loads:

```yaml
secrets:
  bots:
    - username: "my_league_bot"
      password: "..."
```

The service logs `secrets.bots as a list is deprecated; key it by bot name` and, if
`config.yaml` has no `bots:` list, runs one bot per entry, named after the username. A
`sentry_hash` key is accepted and ignored with a warning. Such a bot has no `shared_secret`,
so it can only log in if Steam does not ask for a Steam Guard code; set up an authenticator as
described in [Bot accounts](bot-accounts.md).

## Example files

The repository ships
[`config.yaml.example`](https://github.com/kettleofketchup/dota_lobby/blob/main/config.yaml.example)
with every key and its default, and
[`secrets.yaml.example`](https://github.com/kettleofketchup/dota_lobby/blob/main/secrets.yaml.example).
A minimal `config.yaml` for one bot:

```yaml
bots:
  - name: league_bot_1
```

## What happens when something is wrong

| Situation | Result |
|---|---|
| `config.yaml` not found | Defaults, and no bots unless `credentials_dir` or a secrets file supplies them |
| Unknown key, bad value or bad duration | Fatal at startup: `dota_lobby serve: load config: ...`, listing every problem |
| Unquoted numeric secret | Fatal: `secret must be a quoted string` (or `key STEAM_PASS: quote the value`) |
| Encrypted sops file given to the service | Fatal: `still sops-encrypted; decrypt it first` |
| `credentials_dir` set but missing | Fatal: `read credentials_dir: ...` |
| `shared_secret` not base64 | Fatal: `bot <name>: shared_secret is not valid base64` |
| Bot with neither password nor refresh token | Warning; the bot can only use a token already in `state_dir` |
| `secrets.bots` entry that matches no bot | Warning `secrets.bots entry matches no configured bot` |
| No bots at all | Warning `no bots configured`; the service runs and `/readyz` stays `503` unless `min_ready_bots` is `0` |
| `state_dir` locked by another process | Fatal: `start bots: auth: lock <state_dir>/LOCK: auth: state dir is locked by another process` |
| `state_dir` not writable | Fatal: `start bots: auth: open lock: ... permission denied` (or `create state dir`) |
