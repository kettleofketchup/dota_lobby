# API reference

The service exposes an HTTP API, by default on port `8080`. It speaks plain HTTP: there is no
TLS, so put a reverse proxy in front of it before exposing it beyond localhost (see
[Exposing it beyond localhost](../deployment/docker-compose.md#exposing-it-beyond-localhost)).

The examples use `http://127.0.0.1:8080`, which is where the default `compose.yaml` publishes
the API.

!!! warning "Lobbies are not available yet"

    Everything under `/v1/lobbies` answers `501 not_implemented`. This version runs the bots,
    reports their state and finds the leagues each bot may host; lobby hosting comes later. See [Known limitations](../known-limitations.md).

## Overview

| Method | Path | Auth | Purpose |
|---|---|---|---|
| `GET` | `/livez` | Never | Liveness: the process serves and no bot loop is stuck |
| `GET` | `/readyz` | Never | Readiness: enough bots are connected to the game coordinator |
| `GET` | `/health` | Never | Always `200`, with counts per bot state |
| `GET` | `/v1/bots` | When an API key is set | Every bot and its state |
| `GET` | `/v1/bots/{name}` | When an API key is set | One bot |
| `POST` | `/v1/bots/{name}/retry` | When an API key is set | Make a waiting bot try again now |
| `GET` | `/v1/leagues` | When an API key is set | Every league a bot may host, and which bots hold it |
| `GET` | `/v1/leagues/{id}/nodes` | When an API key is set | A league's lobby nodes, asked from the game coordinator |
| any | `/v1/lobbies`, `/v1/lobbies/...` | When an API key is set | `501`, not implemented yet |
| any | `/bots`, `/lobby/create`, `/lobby/info` | When an API key is set | `410`, the old endpoints are gone |

`GET` endpoints also answer `HEAD`. A known path with the wrong method gets `405` with an
`Allow` header; an unknown path gets `404`. Request bodies are limited to
`server.max_body_bytes` (64 KiB).

## Authentication

Authentication is on when `secrets.api_key` is set (see
[Configuration](../configuration/index.md#secrets)). Without it, everything is public and the
startup log says `API key authentication disabled; /v1 endpoints are public`. The health
endpoints never need the key.

Send the key in either header:

```text
X-API-Key: <your api_key>
Authorization: Bearer <your api_key>
```

- `X-API-Key` is checked first; if it is present, `Authorization` is ignored.
- The `Bearer` scheme is matched case-insensitively.
- The key is compared in constant time.

A missing or wrong key gets `401` with a `WWW-Authenticate: Bearer realm="dota_lobby"` header
and the error body below. The key is checked before the method: a wrong method on a
protected path such as `/v1/bots` gets `401` without a valid key, so the `Allow` list is not
revealed, and `405` with one. The open probe paths (`/livez`, `/readyz`, `/health`) answer `405`
to a wrong method without a key.

## Errors

Every error is JSON with the same envelope:

```json
{"error":{"code":"unauthorized","message":"missing or invalid API key","retryable":false}}
```

| Status | `code` | When |
|---|---|---|
| `400` | `invalid_request` | A malformed path parameter, such as a league ID that is not a positive number |
| `401` | `unauthorized` | Missing or wrong API key |
| `404` | `not_found` | Unknown path, no bot with that name, or no configured bot holds that league |
| `405` | `method_not_allowed` | Wrong method on a known path |
| `410` | `gone` | One of the removed pre-v1 endpoints |
| `501` | `not_implemented` | The lobby endpoints |
| `503` | `unavailable` | A bot did not take a `retry` within 5 s because its run loop is backed up, or no ready bot can answer a game coordinator request |
| `504` | `gc_timeout` | The game coordinator did not answer within `gc.request_timeout` |

`retryable` is `true` only for `503` and `504`.

## `GET /livez`

```json
{"status":"ok"}
```

`200` while the process serves and every bot's run loop has moved in the last 30 seconds.
Otherwise `503`:

```json
{"status":"stalled","stalled_bots":1}
```

The names of stalled bots go to the log (`bot run loop stalled`), not into this open
endpoint. `/livez` **does not depend on Steam**: a Steam outage or a bot that cannot log in
leaves it at `200`, so a container healthcheck on it never turns an outage into a restart loop.
The container image's `HEALTHCHECK` uses it through `dota_lobby healthcheck`.

## `GET /readyz`

```json
{"status":"ready","draining":false,"bots_ready":2,"bots_total":2,"min_ready_bots":1}
```

`200` when at least `readiness.min_ready_bots` bots are `gc_ready`. `503` otherwise, with
`status` set to `not_ready`, and also while the service shuts down (`draining`).

## `GET /health`

Always `200`, for dashboards and old monitors:

```json
{
  "status": "healthy",
  "ready": false,
  "draining": false,
  "bots_ready": 0,
  "bots_total": 1,
  "min_ready_bots": 1,
  "states": {"cooling_down": 1},
  "stalled_bots": 0,
  "version": "1.2.3",
  "uptime_seconds": 742,
  "time": "2026-11-08T12:00:00Z"
}
```

`states` counts bots per [state](#bot-states). Bot names are only shown under `/v1`.

## `GET /v1/bots`

```json
{
  "bots": [
    {
      "name": "league_bot_1",
      "username": "my_league_bot",
      "state": "gc_ready",
      "ready": true,
      "reason": null,
      "last_error": null,
      "last_error_message": null,
      "since": "2026-11-08T11:48:03Z",
      "steam_id": "76561190000000000",
      "limited": false,
      "lobby_stale": false,
      "token_expires_at": "2027-05-27T11:47:52Z",
      "renew_blocked": null,
      "attempt": 0,
      "gc_queue_position": null,
      "ready_since": "2026-11-08T11:48:03Z",
      "leagues": [17929],
      "lobby": null
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `name` | The bot's name from the config |
| `username` | The Steam account name |
| `state` | See [Bot states](#bot-states) |
| `ready` | `true` only in `gc_ready`, and not while the bot is being torn down |
| `reason` | Why the bot is in its state, for example `rate_limited`, `logged_in_elsewhere`, `bad_credentials`, `guard_required`, `dead_token`, `totp_mismatch`, `no_credentials`, `agreement_required`, `transient` |
| `last_error` | The last Steam EResult name, for example `RateLimitExceeded` |
| `last_error_message` | The last error text, which may have no EResult |
| `since` | When the bot entered its state |
| `steam_id` | 64-bit Steam ID **as a string**, or `null` before the first logon |
| `limited` | Steam reports the account as limited |
| `lobby_stale` | The game coordinator session dropped while the bot was ready |
| `token_expires_at` | Expiry of the stored refresh token, or `null` |
| `renew_blocked` | Why background token renewal stopped (`bad_credentials`, `guard_required`, `totp_mismatch`, ...), or `null`. The bot stays connected; see [token renewal](../operations/troubleshooting.md#token-renewal-stopped) |
| `attempt` | Retry counter for the current failure |
| `gc_queue_position` | Position in the game coordinator logon queue, only in `gc_queued` |
| `ready_since` | When the bot last reached `gc_ready`, or `null` while it is not ready |
| `leagues` | League IDs this bot may host lobbies for, sorted; `[]` when none. See [`GET /v1/leagues`](#get-v1leagues) for where each comes from |
| `lobby` | ID of the lobby this bot hosts, or `null`. Always `null` in this version, which creates no lobbies |

Unknown values are `null`, not zero. Passwords, secrets and tokens never appear.

```bash
curl -fsS -H "X-API-Key: $KEY" http://127.0.0.1:8080/v1/bots
```

## `GET /v1/bots/{name}`

One bot, in the same shape. The name is matched case-insensitively. `404` if there is no such
bot.

## `POST /v1/bots/{name}/retry`

```json
{"status":"retry_requested"}
```

`202`. A bot in `needs_attention`, `backoff` or `cooling_down` re-reads its token store and
tries again at once. For a bot in any other state it does nothing. `404` if there is no such
bot, and `503` (`unavailable`, `retryable: true`) if the bot's run loop does not take the request within 5 seconds. Use it after fixing the cause, typically after `dota_lobby auth --bot <name> --force`;
see [Recovering a bot](../operations/troubleshooting.md#recovering-a-bot-in-needs_attention).

```bash
curl -fsS -X POST -H "X-API-Key: $KEY" http://127.0.0.1:8080/v1/bots/league_bot_1/retry
```

Retrying a cooldown early is allowed but rarely wise: Steam set it for a reason.

## `GET /v1/leagues`

```json
{
  "leagues": [
    {
      "league_id": 17929,
      "name": "Spring Cup",
      "bots": [
        {"name": "league_bot_1", "source": "gc", "verified": true,
         "refreshed_at": "2026-11-08T11:48:04Z", "stale": false},
        {"name": "league_bot_2", "source": "config", "verified": false,
         "refreshed_at": null, "stale": false}
      ]
    }
  ]
}
```

Every league at least one enabled bot may host lobbies for, by `league_id`, with those bots in
config order. A bot gets its leagues from two sources:

| `source` | Where the list comes from | `verified` |
|---|---|---|
| `gc` | The game coordinator's list of leagues the bot's Steam account administers, fetched when the bot reaches `gc_ready` and every `league.refresh_interval` (30 min) after | `true` |
| `config` | The bot's `leagues` in `config.yaml` | `false` |

The game coordinator list wins. A bot's configured leagues count only until its list has been
fetched once, or always when `league.allow_unverified` is `true`. A league the game coordinator
does not list for a bot is not shown for that bot, even if it is configured; see
[A bot does not list my league](../operations/troubleshooting.md#a-bot-does-not-list-my-league).

| Field | Meaning |
|---|---|
| `name` | The league's name from the game coordinator, or `null` until a bot has fetched it in this run |
| `refreshed_at` | When the game coordinator list was fetched; `null` for `config` |
| `stale` | The list was saved by an earlier run and has not been fetched again since the service started (the bot is not ready yet) |

Lists are kept in the database, so they are available right after a restart, marked `stale`.
`leagues` is `[]` when no bot holds any league.

## `GET /v1/leagues/{id}/nodes`

```json
{
  "league_id": 17929,
  "bot": "league_bot_1",
  "nodes": [
    {"node_id": 3, "name": "Grand final", "group_name": "Playoffs", "team_id_1": 8255888, "team_id_2": null}
  ]
}
```

The league's lobby nodes (the bracket positions a league lobby can be attached to), asked from
the game coordinator through a ready bot that holds the league, verified holders first. `bot`
is the bot that answered. Empty fields are `null`.

| Status | When |
|---|---|
| `400 invalid_request` | `id` is not a positive 32-bit number |
| `404 not_found` | No enabled bot holds the league |
| `503 unavailable` | Bots hold the league, but none is `gc_ready`, or the request failed. Retry later |
| `504 gc_timeout` | The game coordinator did not answer within `gc.request_timeout` (10 s) |

```bash
curl -fsS -H "X-API-Key: $KEY" http://127.0.0.1:8080/v1/leagues/17929/nodes
```

## Bot states

| State | Meaning |
|---|---|
| `disconnected` | Not connected; about to connect |
| `connecting` | Opening the connection to a Steam server |
| `logging_on` | Logging on with the stored token |
| `authenticating` | Getting a new token with password and Steam Guard code |
| `steam_online` | Logged on to Steam |
| `gc_hello` | Saying hello to the Dota 2 game coordinator until it answers |
| `gc_queued` | In the game coordinator's logon queue |
| `gc_ready` | Connected to the game coordinator. The only ready state |
| `backoff` | Waiting before a reconnect (jittered, `5s` up to `5m`) |
| `cooling_down` | Waiting out a rate limit (30 min) or another session on the account (10 min) |
| `needs_attention` | Stopped; an operator must fix something, then call `retry` |
| `suspended` | The game coordinator says the account is suspended |
| `stopped` | Shut down |

## Removed endpoints

The placeholders from the first version, `GET /bots`, `POST /lobby/create` and `/lobby/info`,
now answer `410 gone`. Use `/v1/bots` instead; lobby endpoints come with a later version.
