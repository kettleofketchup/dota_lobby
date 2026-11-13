# Simulation mode

Simulation mode runs the service against an in-memory Steam and Dota 2 game coordinator. No
bot logs on to Steam. Everything above the game coordinator runs as it does in production:
the lobby manager, seating enforcement, chat commands, the database and signed webhooks.
Use it to test a consumer such as a league site end to end, in CI or on a laptop, without
Steam accounts.

The simulator stands in for the players and the game server. A test drives it through the
`/v1/sim` API: players join, take seats and chat, and matches start and end on request. In
auto mode, invited players join and sit down by themselves.

!!! danger "Never point a production consumer at a simulated service"
    Lobbies, players and match IDs are made up. Match IDs start at `7000000000000000001` and
    GC lobby IDs at `900000000000000001`, far above any real one, so neither can be taken for a
    real one. Every webhook delivery carries `X-Dota-Lobby-Simulation: true`; a production
    receiver should refuse it. Give a simulated service its own webhook receivers.

## Turning it on

Any one of these:

- `dota_lobby serve --simulate`
- `simulation.enabled: true` in `config.yaml`
- `DOTA_LOBBY_SIMULATION_ENABLED=true`

The image is the same: pass `--simulate` to the published image.

A simulated service:

- **refuses to start when any bot has Steam credentials**, a `password`, `shared_secret` or
  `refresh_token` from any source (secrets file, credentials file or environment). The bots
  need only a name. A simulated config must never be one switch away from logging real
  accounts on.
- **refuses to start when any bot has a Steam token stored** in its state directory
  (`<state_dir>/bots/<username>/token.json`, written by `auth --bot` or a logon), as a
  production deployment does. Give a simulated service its own `state_dir`.
- logs a `SIMULATION MODE` warning at startup.
- marks every webhook delivery with an `X-Dota-Lobby-Simulation: true` header.
- reports `"simulation": true` on `GET /health` and `GET /v1/bots`.
- routes the `/v1/sim` operations, which answer 404 otherwise.
- uses `<state_dir>/dota_lobby-simulation.db` and `<state_dir>/backups-simulation` unless
  `store.path` and `store.backup.dir` are set, so it never touches a real database or prunes
  its backups in the same state directory. It never reads or writes bot tokens.
- logs every bot on at once and reaches `gc_ready` within about a second;
  `steam.login_stagger` does not apply.

The simulated lobbies live in memory. After a restart, lobbies in the database are re-adopted,
find no game coordinator lobby and fail as `lobby_lost`, as they would after the game
coordinator lost them.

## What the simulator does

| The service asks for | The simulator answers |
|---|---|
| Logon, game coordinator hello | At once; the bot gets account ID `4000000000` plus its position in `bots` (1, 2, ...) unless `simulation.bots[].account_id` says otherwise |
| Admin league list | The bot's `simulation.bots[].leagues`, or its `bots[].leagues` |
| Create lobby | A lobby with the requested name, password, mode, region, TV delay and league. Without a TV delay it reads back 120 s for a league lobby, as live, and 10 s otherwise. A league the bot does not administer reads back as `0` with the `NOT_LEAGUE_ADMIN` answer, so the lobby fails as `league_rejected` exactly as it would live |
| Host seat (pool, self kick, no team, ...) | Moves the bot as asked |
| Invite | Adds a pending invite; in auto mode the player joins later |
| Kick, kick from team, flip, shuffle | As the game coordinator does them; the shuffle deals seated players to the sides by account ID |
| Join the lobby chat, send a message | The bot's messages appear in the lobby's chat log |
| Launch | The lobby goes to `server_assign`. The match starts on `POST .../start`, or by itself with `auto_start` |
| Destroy, leave | The lobby is gone |

## Configuration

```yaml
simulation:
  enabled: false
  auto_join: false         # invited players join and sit on their rostered side
  auto_join_delay: 1s      # from the invite to the join, and from the seat to !ready
  auto_ready: false        # auto-joined players then say the ready command in chat
  auto_start: false        # a launched lobby starts its match by itself
  auto_start_delay: 5s     # from the launch to match.started
  bots:                    # optional, per bot name in bots[]
    - name: league_bot_1
      account_id: 4000000101
      leagues: [17000]     # default: the bot's bots[].leagues
```

Auto mode decides the seat from the lobby's roster: a rostered player sits on its side, in the
first free slot; anyone else stays in the unassigned pool. Players invited together join, sit
down and say the ready command in invite order, so their slots and the order of their webhooks
are the same on every run. The ready command is
`lobby.chat.prefix` plus `ready`. It only counts when `lobby.chat.enabled` is on and the lobby
was created with `"ready_check": true`; the simulator holds it until the bot has joined the
lobby chat. Every auto-mode setting can also be changed at runtime with
`PATCH /v1/sim/settings`.

## Docker Compose example

A league site's integration tests next to a simulated service:

```yaml
services:
  dota_lobby:
    image: ghcr.io/kettleofketchup/dota_lobby:latest
    command: ["serve", "--simulate"]
    environment:
      DOTA_LOBBY_SECRETS_API_KEY: test-key
      DOTA_LOBBY_SECRETS_FILE: /app/config/secrets.yaml
    volumes:
      - ./dota_lobby-sim.yaml:/app/config/config.yaml:ro
      - ./dota_lobby-sim-secrets.yaml:/app/config/secrets.yaml:ro
    tmpfs:
      - /app/state             # nothing worth keeping between test runs
    ports:
      - "127.0.0.1:8080:8080"

  site:
    build: .
    environment:
      DOTA_LOBBY_URL: http://dota_lobby:8080
      DOTA_LOBBY_API_KEY: test-key
```

`dota_lobby-sim.yaml`:

```yaml
state_dir: /app/state
readiness: { min_ready_bots: 1 }
bots:
  - name: sim_bot_1
    leagues: [17000]
  - name: sim_bot_2
    leagues: [17000]
lobby:
  auto_launch_delay: 1s
  postgame_linger: 5s
  chat: { enabled: true, start_countdown: 1s, send_interval: 100ms }
simulation:
  auto_join: true
  auto_join_delay: 200ms
  auto_ready: true
  auto_start: true
  auto_start_delay: 500ms
webhooks:
  - url: http://site:8000/hooks/dota
```

`dota_lobby-sim-secrets.yaml` holds the webhook signing secret only; no bot credentials:

```yaml
secrets:
  webhook_secrets:
    "http://site:8000/hooks/dota": "test-webhook-secret"
```

Wait for `GET /readyz` to answer `200` before the first request: the bots are then
`gc_ready`.

## Control API

Every operation needs the API key and is in the [OpenAPI document](../api/index.md) under the
**Simulation** tag. `{id}` is the service's lobby ID (`lob_...`) or the simulated game
coordinator lobby ID; `{account_id}` is a player's 32-bit account ID.

| Method and path | What it does |
|---|---|
| `GET /v1/sim/lobbies` | Every simulated lobby: details, members with their seats, pending invites, chat log |
| `GET /v1/sim/lobbies/{id}` | One simulated lobby |
| `POST /v1/sim/lobbies/{id}/players` | `{"account_id": 11111111, "persona_name": "Alice"}`: a player joins the unassigned pool. No invite is needed |
| `POST /v1/sim/lobbies/{id}/players/{account_id}/seat` | `{"team": "radiant", "slot": 0}`: take a seat. `team` is `radiant`, `dire`, `player_pool`, `spectator` or `broadcaster`. `slot` is the Radiant or Dire slot (0-4, default the first free one) or the broadcaster channel (default 0); other teams take none |
| `POST /v1/sim/lobbies/{id}/players/{account_id}/ready` | The player says the ready command in chat |
| `DELETE /v1/sim/lobbies/{id}/players/{account_id}` | The player leaves |
| `POST /v1/sim/lobbies/{id}/chat` | `{"account_id": 11111111, "text": "!start"}`: a chat line from a member, so chat commands can be tested |
| `POST /v1/sim/lobbies/{id}/start` | Start the match of a launched lobby; optional `{"match_id": "8000000001"}` |
| `POST /v1/sim/lobbies/{id}/end` | `{"result": "radiant"}`, `"dire"` or `"not_scored"`: end the running match |
| `POST /v1/sim/lobbies/{id}/fail` | `{"mode": "lost"}` drops the lobby from the bot's cache (it fails as `lobby_lost`, or `lost_in_game`, after 10 s); `{"mode": "load_failed"}` sends a launched lobby back to the lobby screen (`lobby.launch_failed`) |
| `GET /v1/sim/settings` | The auto-mode settings and the ready command |
| `PATCH /v1/sim/settings` | Change some of `auto_join`, `auto_join_delay_ms`, `auto_ready`, `auto_start`, `auto_start_delay_ms` |

Chat answers say whether the bot saw the line: `"delivered": false` means the bot was not in
the lobby chat, as with a player typing too early in the real client. The chat log marks
lines the same way; the bot's own lines are always `"delivered": true`. Errors are problem
details: `404 not_found` for an unknown lobby, `409 not_member`, `already_member`,
`seat_taken` or `invalid_state`, `422` for a bad account ID, slot or body.

## A whole lobby by hand

With `auto_join` off, this plays one lobby through to its result:

```bash
api() { curl -sf -H "X-API-Key: test-key" -H "Content-Type: application/json" "$@"; }
ID=$(api -X POST localhost:8080/v1/lobbies -d '{
  "external_ref": "test-1", "name": "Sim Cup", "league_id": 17000, "launch": "manual",
  "teams": {"radiant": {"team_ref": "r", "players": [{"account_id": 11111111}]},
            "dire":    {"team_ref": "d", "players": [{"account_id": 22222222}]}}}' | jq -r .id)
# 409 until the bot has created the game coordinator lobby (lobby.created)
until api localhost:8080/v1/sim/lobbies/$ID >/dev/null; do sleep 0.2; done
for a in 11111111 22222222; do
  api -X POST localhost:8080/v1/sim/lobbies/$ID/players -d "{\"account_id\": $a}"
done
api -X POST localhost:8080/v1/sim/lobbies/$ID/players/11111111/seat -d '{"team": "radiant"}'
api -X POST localhost:8080/v1/sim/lobbies/$ID/players/22222222/seat -d '{"team": "dire"}'
# The service applies the seats on its own time: launch once it says ready (lobby.ready)
until [ "$(api localhost:8080/v1/lobbies/$ID | jq -r .state)" = ready ]; do sleep 0.2; done
api -X POST localhost:8080/v1/lobbies/$ID/launch
# The bot launches on its own time too: start once the simulated lobby is launched
until [ "$(api localhost:8080/v1/sim/lobbies/$ID | jq -r .state)" = server_assign ]; do sleep 0.2; done
api -X POST localhost:8080/v1/sim/lobbies/$ID/start    # match.started
api -X POST localhost:8080/v1/sim/lobbies/$ID/end -d '{"result": "dire"}'   # match.ended
```

Every step after a control call waits for the state it needs: the control calls answer as soon
as the simulator has changed, before the service has seen the change. A test can wait for the
matching webhook instead.

The webhooks carry `lobby.created`, `lobby.player_joined`, `lobby.player_seated`,
`lobby.ready`, `lobby.launching`, `match.started` and `match.ended`, signed as in production.
The service's own test, `TestServeSimulatedLobbyLifecycle` in `cmd/dota_lobby`, runs this
flow in auto mode and checks every signature.
