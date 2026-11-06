# API reference

The service exposes a small HTTP API, by default on port `8080`. It speaks plain HTTP: there
is no TLS, so put a reverse proxy in front of it before exposing it beyond localhost (see
[Exposing it beyond localhost](../deployment/docker-compose.md#exposing-it-beyond-localhost)).

The examples below use `http://127.0.0.1:8080`, which is where the default `compose.yaml`
publishes the API.

!!! danger "The lobby endpoints are placeholders"

    `POST /lobby/create` and `/lobby/info` validate their input and answer with `success`, but
    **no lobby is created and no lobby is looked up**. Lobby creation is not implemented yet.
    See [Known limitations](../known-limitations.md).

## Overview

| Method | Path | Auth | Purpose |
|---|---|---|---|
| `GET` | `/health` | Never | Process is running |
| `GET` | `/bots` | When `api_key` is set | Steam logon state of each bot |
| `POST` | `/lobby/create` | When `api_key` is set | Placeholder: validates a lobby request |
| `GET`, `POST` | `/lobby/info` | When `api_key` is set | Placeholder: echoes a lobby ID |

Any other path returns `404` with the body `404 page not found`.

## Authentication

Authentication is on only when `server.api_key` is set in `config.yaml` (see
[Configuration](../configuration/index.md#serverapi_key)). When it is unset or empty, every
endpoint is public and the startup log says
`WARNING: API key authentication disabled - all endpoints are public`.

When it is on, send the key in either header:

```text
X-API-Key: <your api_key>
Authorization: Bearer <your api_key>
```

- `X-API-Key` is checked first. If it is present, `Authorization` is ignored, even when the
  `X-API-Key` value is wrong.
- The `Bearer ` prefix is case-sensitive and needs exactly one space.
- The key is compared in constant time.

A missing or wrong key gets:

```text
HTTP/1.1 401 Unauthorized
Content-Type: text/plain; charset=utf-8

Unauthorized: Invalid or missing API key
```

!!! note "Authentication runs before the method check"

    On the protected endpoints the key is checked first. A request with the wrong method and
    no key gets `401`, not `405`. Only once the key is accepted does a wrong method get
    `405 Method not allowed`.

## Errors

Successful responses are JSON. **Errors are plain text**, not JSON, written with
`Content-Type: text/plain; charset=utf-8`. Every endpoint answers a wrong method with
`405` and the body `Method not allowed`.

## `GET /health`

No authentication. Returns `200` while the process is running:

```json
{"status":"healthy","time":"2026-11-06T12:00:00.123456789Z"}
```

`time` is the current server time in UTC, in RFC 3339 format (with fractional seconds).

!!! warning "Always healthy"

    `/health` says `healthy` whatever the bots are doing, including when no bot is logged
    on. It proves only that the HTTP listener is up. It is not a readiness check.

```bash
curl -fsS http://127.0.0.1:8080/health
```

## `GET /bots`

Returns `200` with one entry per configured bot, keyed by Steam username:

```json
{"bots":{"my_bot_account":true}}
```

- `true` means the bot is **logged on to Steam**. It does not mean it is connected to the
  Dota 2 game coordinator, and it does not mean it can host a lobby.
- `false` means it is not logged on: still connecting, failing to log on, or stopped after a
  connection failure (check the logs, see [Troubleshooting](../operations/troubleshooting.md)).
- With no bots configured the answer is `{"bots":{}}`.

```bash
curl -fsS -H 'X-API-Key: <your api_key>' http://127.0.0.1:8080/bots
```

## `POST /lobby/create`

!!! danger "Placeholder"

    This endpoint checks the request and that a bot is logged on, then answers `201` with
    `"status":"success"`. **No lobby is created.** `server_region` and `game_mode` are
    accepted and ignored.

Request body (JSON):

| Field | Type | Required | Rules |
|---|---|---|---|
| `lobby_name` | string | Yes | At most 100 characters |
| `password` | string | No | At most 50 characters |
| `server_region` | string | No | Accepted and ignored |
| `game_mode` | string | No | Accepted and ignored |

Response `201 Created`:

```json
{"status":"success","message":"Lobby creation initiated","lobby_name":"League match 1","bot_used":"my_bot_account"}
```

`bot_used` is a bot that is currently logged on to Steam.

Errors (plain text):

| Status | Body | Cause |
|---|---|---|
| `400` | `Invalid request body: ...` | The body is not valid JSON (an empty body gives `Invalid request body: EOF`) |
| `400` | `lobby_name is required` | `lobby_name` missing or empty |
| `400` | `lobby_name must be 100 characters or less` | `lobby_name` too long |
| `400` | `password must be 50 characters or less` | `password` too long |
| `503` | `No available bots: no available bots` | No bot is logged on to Steam |
| `503` | `Bot not ready: bot is not connected` | The chosen bot dropped off between checks |

```bash
curl -fsS -X POST \
  -H 'X-API-Key: <your api_key>' \
  -H 'Content-Type: application/json' \
  -d '{"lobby_name":"League match 1","password":"secret"}' \
  http://127.0.0.1:8080/lobby/create
```

## `GET /lobby/info` and `POST /lobby/info`

!!! warning "Placeholder"

    This endpoint only echoes the lobby ID back. It does not look anything up.

Pass the ID as a query parameter with `GET`, or as JSON with `POST`:

```json
{"lobby_id":"12345"}
```

Response `200 OK`:

```json
{"status":"success","lobby_id":"12345","message":"Lobby info retrieval not yet fully implemented"}
```

Errors (plain text):

| Status | Body | Cause |
|---|---|---|
| `400` | `Invalid request body: ...` | `POST` body is not valid JSON |
| `400` | `lobby_id is required` | No `lobby_id` given |

```bash
curl -fsS -H 'X-API-Key: <your api_key>' 'http://127.0.0.1:8080/lobby/info?lobby_id=12345'

curl -fsS -X POST \
  -H 'X-API-Key: <your api_key>' \
  -H 'Content-Type: application/json' \
  -d '{"lobby_id":"12345"}' \
  http://127.0.0.1:8080/lobby/info
```
