# 05 - Patterns used by existing lobby bots

Reusable patterns and known pitfalls from open-source Dota 2 lobby bots (Node, Python, C#, C++ and Go, 2017-2026), checked against go-dota2 where a library call is named. Researched 2026-11-06. Facts our own live run settled are marked VERIFIED live and point to [09 - Live probe](09-live-probe.md); the rest is **REPORTED** field experience, not Valve documentation.

go-dota2 references are to the pinned module, `go-dota2@532cb1851217/...` (`github.com/paralin/go-dota2 v0.0.0-20250623204622-532cb1851217`).

---

## 1. What every project agrees on

- **One lobby per account.** Every bot treats an account as hosting one practice lobby at a time and gets concurrency by pooling accounts. This matches the SOCache model (one `CSODOTALobby` per account, see [01](01-lobby-protocol.md) §1).
- **League lobbies need league admin rights.** `leagueid` on `CMsgPracticeLobbySetDetails` only works if the bot account is an admin of that league on dota2.com/league ([node-dota2 README](https://github.com/Arcana/node-dota2#readme): "The bot should be a league admin for this to work"). VERIFIED live for the accepted case ([09](09-live-probe.md) §5). No project redeems a ticket item.
- **Bootstrap order.** Steam logon, then `SetPlaying(true)`, then `SayHello()` repeated until the GC session is ready. Sending GC messages before `GamesPlayed` + `ClientHello` leaves lobbies stuck at "FINDING SERVER" ([go-dota2 #21](https://github.com/paralin/go-dota2/issues/21), [SteamKit #791](https://github.com/SteamRE/SteamKit/issues/791)).
- **Results from the lobby SO.** `state == POSTGAME` and `match_outcome` (2..13 winner, 64..69 not scored) are the primary signal; GC `MatchDetailsRequest` (7095) or the Web API / OpenDota are fallbacks ([06](06-match-results.md)).

## 2. Reusable patterns

| # | Pattern | Why |
|---|---|---|
| 1 | **League-aware bot pool.** Refresh each bot's admin leagues from 7633/7634 on GC welcome and periodically; pick a free bot that holds the requested league; send non-league lobbies to bots with no leagues first | Keeps league-capable bots free for league games. 7633 VERIFIED live ([09](09-live-probe.md) §4) |
| 2 | **Durable claim + idempotency key.** `POST /lobby/create` takes a client key; re-posting returns the same job; a bot holds at most one persisted claim | A restart never double-assigns a bot |
| 3 | **Explicit job state machine** with timeouts: `pending → assigned → creating → ready → starting → running → over \| failed \| released` (create 2 min, launch 2 min, idle 30 min, leave-confirm 20 s, running backstop 3 h) | Every stuck state has an exit |
| 4 | **Per-bot GC command queue** with a rate limit and backoff, blocked while the GC is not ready | Avoids bursts and requests before welcome |
| 5 | **Verify after create.** Read back `leagueid`, `server_region`, `game_mode` and leader = bot before inviting or launching | Create is fire-and-forget; the read-back is the authority ([01](01-lobby-protocol.md) §5) |
| 6 | **Invite policy.** Invite once per job, record before sending, skip present members, 30 s per-player resend gap, at most 5 | A restart never re-sends a round; there is no GC invite cancel ([09](09-live-probe.md) §8) |
| 7 | **Seat enforcement.** Keep an intended-team map and kick misplaced members back to unassigned with `KickLobbyMemberFromTeam`; never act on a member whose team was absent on the wire (decoders default it to Radiant 0) | The host cannot seat a player in a slot ([01](01-lobby-protocol.md) §8.4) |
| 8 | **Kick the host out of its slot right after create** (`KickLobbyMemberFromTeam(self)` or join the player pool) | The creator can land in a team slot |
| 9 | **Forced resync.** `SetPlaying(false)`, wait about 2 s, `SetPlaying(true)`, `SayHello()` to get a full welcome that replays all caches; bound it (at least 1 min apart, at most 3 per job) | Recovers a stale lobby picture ([03](03-go-dota2-internals.md) §3.4). INFERRED for go-dota2: `SetPlaying(false)` does call `SetGamesPlayed()` (`go-dota2@532cb1851217/client_session.go:14-22`) |
| 10 | **Logging hygiene.** Never log lobby passwords, refresh tokens or Steam IDs; whitelist logged fields | Logs get shared |

Architectures seen: an in-process pool with a scheduler loop (simple, one deploy, but one crash takes every bot down), a process per account pulling work from a queue (isolated, needs a coordinator), and a container per lobby (strong isolation, but a fresh login for every lobby, 20+ s start-up and more login exposure). An in-process pool with one goroutine and one `steam.Client`/`dota2.Dota2` pair per bot fits this service.

## 3. Known pitfalls

| Pitfall | Evidence | Mitigation |
|---|---|---|
| GC drops (Tuesday maintenance, ad-hoc restarts) | REPORTED by several bots | Block the work queue while not ready; reconnect with backoff; exit for a supervisor restart after a long offline period |
| Stale lobby state: a finished game stuck in `RUN` while the GC session stays up | REPORTED (Oct 2026) | Resync (pattern 9); a 3 h running backstop |
| GC ignores `PracticeLobbyLeave` while the lobby is in `RUN` | REPORTED (Oct 2026; also [node-dota2 #179](https://github.com/Arcana/node-dota2/issues/179)) | Leave at POSTGAME; size the pool to peak concurrency |
| Abandon-then-leave to free a bot early | Mixed reports ([node-dota2 #179](https://github.com/Arcana/node-dota2/issues/179)) | Do not depend on it |
| A leftover lobby from a previous session blocks create | node-dota2 #179; the reason `LeaveCreateLobby(destroyOld=true)` exists | Leave or destroy before create |
| League ID silently not applied | node-dota2 README | Read back `CSODOTALobby.leagueid` (pattern 5) |
| `create_tournament_lobby` broken in ValvePython | [ValvePython/dota2 #52](https://github.com/ValvePython/dota2/issues/52) | Use a practice lobby with `leagueid` |
| Callbacks that never fire (flip, abandon, invite-created) | REPORTED | Always time out and confirm through the SOCache |
| A create result of `Fail` treated as success | REPORTED | Check 7055 `eresult == 1` ([09](09-live-probe.md) §5) and the SO |
| Not-scored outcomes (64..69) reported as wins | REPORTED | Map every outcome explicitly |
| A player's "Block party invites from non-friends" setting makes a blocked invite look delivered | REPORTED | Fall back to lobby name + password |
| Limited ($5) accounts may be unable to chat | REPORTED | Fund bot accounts; log the limited flag. Lobby invites from limited accounts are Q22 |
| Same account logged in twice | REPORTED; Steam `LoggedInElsewhere` | One process per account set, with a lock ([07](07-deployment.md)) |
| Bots cannot matchmake | [go-dota2 #21](https://github.com/paralin/go-dota2/issues/21) | Lobbies only |

## 4. Rate limits

- paralin: "there are definitely rate limits", with a risk of being "IP banned, steam account banned, or rate limited if abusing the privilege" ([go-dota2 #21](https://github.com/paralin/go-dota2/issues/21)).
- GC match details: ValvePython documents `request_match_details` as "Rate limited to 100 requests/day" and `request_matches` 50/day ([docs](https://dota2.readthedocs.io/en/stable/dota2.features.match.html)). The OpenDota maintainer gives "100 requests/accounts/24 hours and 500 requests/IP/24 hours" ([odota/core #1482](https://github.com/odota/core/issues/1482)). These limits are for match data, not lobby or league calls. REPORTED; not measured by us.
- Field practice: one match-details request in flight at 1/min, invite resends at least 30 s apart, at least 1 min between resyncs, and chat sent through a timed queue.

## 5. Still open

- Whether abandon-then-leave frees a bot in a league lobby without penalties (Q14).
- Whether limited accounts can send lobby invites or be league admins (Q22, Q13).
- Any cap on leagues per admin account or concurrent lobbies per league.
- Whether `CMsgDOTALeagueInfo.status` and the timestamps distinguish active leagues from expired ones (Q3).
