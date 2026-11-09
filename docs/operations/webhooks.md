# Webhooks

The service can push every lobby event to one or more HTTP receivers. Each delivery is a signed
`POST` with one event in a JSON envelope. Deliveries are queued in the database in the same
transaction that stores the event, so a restart or a receiver that is down loses nothing; they
are retried with backoff for up to `webhook_delivery.max_age` (24 h).

Webhooks are a fast path. The lobby itself (`GET /v1/lobbies/{id}`) is the authoritative
state, and `GET /v1/lobbies/{id}/events` lists every event again, so a receiver that missed
something can always catch up.

## Configure a receiver

In `config.yaml`:

```yaml
webhooks:
  - url: https://league.internal/api/internal/lobbies/events/
    events: ["lobby.*", "match.*"]    # path.Match globs; ["*"] or none for every event
    token_header: X-Internal-Token    # optional, see below
webhook_delivery:
  timeout: 10s      # one attempt, response included
  max_age: 24h      # retry a failing delivery this long after the event
```

In the secrets file, keyed by the exact webhook URL (compared case-insensitively):

```yaml
secrets:
  webhook_secrets:
    "https://league.internal/api/internal/lobbies/events/": "a long random string"
  webhook_tokens:   # only with token_header
    "https://league.internal/api/internal/lobbies/events/": "the receiver's own token"
```

Every webhook needs a signing secret; the service refuses to start without one. With the
[Docker Compose](../deployment/docker-compose.md) setup, `just secrets webhook-secret <url>`
generates one into `secrets/service.sops.yaml` (see [DraftForge](#draftforge) for the
recipes). Otherwise generate it with `openssl rand -hex 32`. Give the same value to the
receiver. `token_header` and its
token go together: one without the other is a startup error.

Each event is queued for the webhooks whose `events` match its type when the event is stored.
Changing `events` affects new events only. Removing a webhook gives up its queued deliveries
on the next start (logged as `the webhook is no longer configured`).

## The request

```text
POST /api/internal/lobbies/events/ HTTP/1.1
Content-Type: application/json
User-Agent: dota_lobby/<version>
X-Dota-Lobby-Event-Id: evt_01JC9ZB2J4K6M8N0P2Q4R6S8T0
X-Dota-Lobby-Event-Type: match.started
X-Dota-Lobby-Timestamp: 1762624800
X-Dota-Lobby-Signature: v1=<64 hex digits>
X-Internal-Token: <secrets.webhook_tokens[url]>   # only with token_header
```

```json
{
  "id": "evt_01JC9ZB2J4K6M8N0P2Q4R6S8T0",
  "type": "match.started",
  "seq": 9,
  "occurred_at": "2026-11-08T18:05:00Z",
  "lobby": {
    "id": "lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V",
    "external_ref": "game-42",
    "state": "in_game",
    "bot": "bot1",
    "gc_lobby_id": "27110012345678901",
    "league_id": 17929,
    "metadata": {"tournament_id": "3"}
  },
  "data": {"match_id": "8123456789", "host_seat_strategy": "self_kick",
           "radiant_team_ref": "team-7", "dire_team_ref": "team-9"}
}
```

| Field | Meaning |
|---|---|
| `id` | Unique per event. The same event always has the same ID, also on a retry |
| `type` | See [Event types](#event-types) |
| `seq` | The event's number within its lobby: starts at 1, grows by one, no gaps |
| `occurred_at` | When the change happened (UTC) |
| `lobby` | The lobby right after the change: `state` is the state it moved to. `bot`, `gc_lobby_id` and `league_id` are `null` until known. 64-bit IDs are strings |
| `data` | Depends on the type. Never the password |

The `X-Dota-Lobby-Event-Id` and `X-Dota-Lobby-Event-Type` headers repeat `id` and `type`, so a
receiver can route or drop a delivery before parsing the body. Request bodies are at most
64 KiB.

## Verify the signature

`X-Dota-Lobby-Signature` is `v1=` followed by the hex HMAC-SHA256 of
`"<X-Dota-Lobby-Timestamp>.<raw body>"` under the webhook's secret. The timestamp is the
attempt's time in Unix seconds, so every retry has a fresh one. A receiver must:

1. Compute the signature over the **raw** request body, before any JSON parsing.
2. Compare it in constant time with each comma-separated value of the header.
3. Reject a timestamp more than 5 minutes from its own clock, which stops replays.

```python
import hashlib
import hmac
import time

TOLERANCE_SECONDS = 300


def verify_dota_lobby(secret: bytes, headers, body: bytes) -> bool:
    ts = headers.get("X-Dota-Lobby-Timestamp", "")
    signatures = headers.get("X-Dota-Lobby-Signature", "")
    try:
        sent = int(ts)
    except ValueError:
        return False
    if abs(time.time() - sent) > TOLERANCE_SECONDS:
        return False
    mac = hmac.new(secret, ts.encode() + b"." + body, hashlib.sha256)
    expected = "v1=" + mac.hexdigest()
    return any(hmac.compare_digest(expected, s.strip()) for s in signatures.split(","))
```

To check an implementation: secret `whsec_test`, timestamp `1762624800` and body
`{"id":"evt_1"}` sign to
`v1=898d18dde6b7357e818e3ae907e65e07b079679e0f30cb710a2f307c7e7ee5a0`.

Keep both hosts on NTP; a clock that drifts past the tolerance makes every delivery fail.

## Delivery, retries and order

| Receiver answers | What happens |
|---|---|
| Any `2xx` | Delivered |
| No answer within `webhook_delivery.timeout`, a connection error, `408`, `425`, `429`, any `5xx` | Retried |
| A `3xx` | Given up. Redirects are not followed; configure the final URL |
| Any other `4xx` | Given up at once, logged at error |

Retries wait 10 s, 30 s, 2 min, 10 min, 30 min and then 1 h between tries, each ±20 %. A
`Retry-After` header on a retried answer (seconds or an HTTP date) replaces the wait. A
delivery whose next try would fall more than `webhook_delivery.max_age` after the event is
given up.

- **In order per lobby.** For each webhook and lobby only the lowest undelivered `seq` is in
  flight; the next event waits until it is delivered or given up. Different lobbies do not
  wait for each other, and neither do different webhooks: each webhook has up to 4
  deliveries in flight, so a receiver that hangs until `webhook_delivery.timeout` slows only
  its own deliveries.
- **At least once.** A delivery can arrive twice, for example when the service stops after the
  receiver answered but before the answer was stored, or when the database refuses the write
  (a full disk, for example; the service logs `webhook delivered but not recorded` and tries
  again 30 s later). De-duplicate on `id`.
- **Restarts.** At shutdown, attempts in flight are cancelled and stay queued without counting
  as a try; the next start sends them. Queued deliveries are deleted with their lobby by the
  `store.retention` sweep.
- **Given up.** The service logs `webhook delivery given up` with the event ID, type, lobby and
  the last status or error. A given-up event is still in `GET /v1/lobbies/{id}/events`.

## Event types

| Type | `data` |
|---|---|
| `lobby.state_changed` | `from`, `to`, `reason`. Sent with every state change, next to the specific event |
| `lobby.created` | `gc_lobby_id`, `league_verified` |
| `lobby.create_failed` | `code` (`league_rejected`, `create_timeout`, `gc_error`), `message`; `lobby.failed` follows |
| `lobby.updated` | `fields`: the settings a `PATCH` changed |
| `lobby.roster_changed`, `lobby.teams_flipped` | `radiant_team_ref`, `dire_team_ref` after the change |
| `lobby.player_invited` | `account_id`, `steam_id`, `invites_sent`, `confirmed` |
| `lobby.player_joined`, `lobby.player_left` | `account_id`, `rostered`, `team_ref` |
| `lobby.player_seated` | `account_id`, `seat`, `correct` |
| `lobby.player_kicked` | `account_id`, `from` (`team`, `lobby`), `reason` (`wrong_side`, `stranger`, `api`, `roster`) |
| `lobby.enforcement_stopped` | `account_id`, `kicks`: the bot stopped moving this member |
| `lobby.player_ready`, `lobby.player_unready` | `account_id`; `reason` on unready (`chat`, `moved`, `left`) |
| `lobby.ready`, `lobby.unready` | `missing` on unready |
| `lobby.launching` | `trigger` (`api`, `chat`, `auto`), `by_account_id`, `attempt`, `host_seat_strategy` |
| `lobby.launch_failed` | `reason`, `attempt`, `next_strategy` |
| `match.started` | `match_id`, `host_seat_strategy`, `radiant_team_ref`, `dire_team_ref` |
| `match.ended` | `match_id`, `outcome` (number), `outcome_name`, `winner_side` (`radiant`, `dire` or `null`), `winner_team_ref`, `scored`, `duration_seconds`, `provisional: true`, `source: "lobby"` |
| `lobby.finished`, `lobby.closed`, `lobby.failed`, `lobby.timed_out` | `reason`. The lobby ended |

`match.ended` is provisional: it is what the lobby reported, not the Steam Web API's match
record. See [Known limitations](../known-limitations.md).

## DraftForge

DraftForge receives the events on its internal
API, which authenticates callers with an `X-Internal-Token` header and an IP allowlist. The
research behind this setup is in
[`docs/research/11-draftforge-integration.md`](https://github.com/kettleofketchup/dota_lobby/blob/main/docs/research/11-draftforge-integration.md).

```yaml
# config.yaml
webhooks:
  - url: https://draftforge.example/api/internal/lobbies/events/
    events: ["*"]
    token_header: X-Internal-Token
```

```yaml
# secrets file
secrets:
  webhook_secrets:
    "https://draftforge.example/api/internal/lobbies/events/": "<DOTA_LOBBY_WEBHOOK_SECRET>"
  webhook_tokens:
    "https://draftforge.example/api/internal/lobbies/events/": "<INTERNAL_SERVICE_TOKEN>"
```

Set the secrets up with the `just secrets` recipes. None of them prints a secret:

```bash
url=https://draftforge.example/api/internal/lobbies/events/

# 1. Generate the signing secret into secrets/service.sops.yaml.
just secrets webhook-secret "$url"

# 2. Give DraftForge the same secret as DOTA_LOBBY_WEBHOOK_SECRET in its env file.
just secrets webhook-secret-env "$url" ../draftforge/backend/.env

# 3. Store DraftForge's INTERNAL_SERVICE_TOKEN, read from stdin (or typed at the prompt).
just secrets webhook-token "$url" < /path/to/internal-service-token

# 4. Add the webhooks[] entry above to config/config.yaml, then:
just docker restart
just secrets list   # shows which URLs have a secret and a token
```

- `webhook-secret` stores the secret under the lowercased URL, the way the service looks it
  up. It refuses to replace an existing secret; pass `true` as a second argument to rotate it
  (see [Rotating the secret](#rotating-the-secret)).
- `webhook-secret-env URL ENV_FILE [VAR]` writes `VAR=<secret>` (default
  `DOTA_LOBBY_WEBHOOK_SECRET`) into `ENV_FILE`, replacing an existing `VAR=` line and leaving
  the rest. The file ends up mode `0600`. The value travels from sops to the file through a
  pipe, so it never shows up on the terminal or in `ps`. For a DraftForge deployed with Helm,
  write to a scratch file and move the value into its secret from there.
- `webhook-secret-env-add URL ENV_FILE [VAR]` does the same, but puts the secret in front of
  the existing comma-separated `VAR=` list instead of replacing it, for rotation. Run twice,
  it adds the secret once.
- `webhook-token URL` reads one line from stdin. Keep the token out of your shell history:
  redirect it from a file or type it at the prompt.

- **Token.** `X-Internal-Token` carries DraftForge's `INTERNAL_SERVICE_TOKEN`, so its existing
  internal auth accepts the request unchanged.
- **Signature.** The receiver must also verify `X-Dota-Lobby-Signature` with
  `DOTA_LOBBY_WEBHOOK_SECRET` as shown above. Do not rely on the IP allowlist: it trusts the
  first `X-Forwarded-For` value, which a client behind DraftForge's proxy can set.
- **Allowlist.** Add the `dota_lobby` host to `INTERNAL_SERVICE_ALLOWED_IPS` anyway, or the
  receiver answers `403` and every delivery is given up at once. A `403` is not retried, so
  fix the allowlist before you add the webhook.
- **Helm ingress.** DraftForge's Helm chart sets `ingress.blockInternalApi: true` by default,
  which answers `403` to every request for `/api/internal` from outside the cluster. Those
  deliveries are given up at once. Point the webhook at DraftForge's in-cluster service URL
  when `dota_lobby` runs in the same cluster, or turn the block off deliberately and rely on
  the allowlist, the token and the signature.
- **Apply in order.** De-duplicate on `id` and apply a lobby's events in `seq` order; ignore an
  event whose `seq` is not above the last one applied.
- **Match ID.** Take `Game.gameid` from `match.started`. Keep the league sync as the authority
  for the winner; `match.ended` is provisional.
- **Reconcile.** Find the game by `lobby.external_ref` (`game-<pk>`). A periodic task that reads
  `GET /v1/lobbies?external_ref=game-<pk>` for every game whose lobby has not ended covers
  anything a webhook missed. When the lobby's `seq` is ahead of the last applied event, read
  `GET /v1/lobbies/{id}/events?after=<last seq>`.

### Rotating the secret

`dota_lobby` signs with one secret per webhook, and a `401` or `403` from the receiver is not
retried. If both sides switch to a new secret at once, every delivery between the two restarts
fails the signature check and is given up; DraftForge then only catches those events up
through its reconcile task. DraftForge accepts a comma-separated list in
`DOTA_LOBBY_WEBHOOK_SECRET`, so rotate in two phases, with the receiver accepting both secrets
while the sender switches:

```bash
url=https://draftforge.example/api/internal/lobbies/events/
env=../draftforge/backend/.env

# 1. A new secret in secrets/service.sops.yaml. The running service keeps signing
#    with the old one until it is restarted.
just secrets webhook-secret "$url" true

# 2. DraftForge accepts the new and the old secret. Restart DraftForge.
just secrets webhook-secret-env-add "$url" "$env"

# 3. dota_lobby re-decrypts its secrets and signs with the new one.
just docker restart

# 4. Drop the old secret from DraftForge. Restart DraftForge again.
just secrets webhook-secret-env "$url" "$env"
```

Do not run `just docker restart` (or `just docker up`) between steps 1 and 2: it would make
`dota_lobby` sign with the new secret before DraftForge accepts it.
