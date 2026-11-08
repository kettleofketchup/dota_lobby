# 08 - Current Repo Gap Analysis

> Track: current repo gap analysis.
> Repo snapshot: `main` @ `eec6d3e` (clean working tree), researched 2026-11-06.
> Library versions under analysis (from `go.mod:6-7`):
> `github.com/paralin/go-dota2 v0.0.0-20250623204622-532cb1851217`,
> `github.com/paralin/go-steam v0.0.0-20250502043548-f167ff28a93a`.
>
> Path shorthands used below:
> - `D2/` = `/home/kettle/.local/share/go/pkg/mod/github.com/paralin/go-dota2@v0.0.0-20250623204622-532cb1851217/`
> - `GS/` = `/home/kettle/.local/share/go/pkg/mod/github.com/paralin/go-steam@v0.0.0-20250502043548-f167ff28a93a/`
> - Repo paths are relative to `/home/kettle/git_repos/dota_lobby/`.

---

## 0. TL;DR

The repository is a **scaffold**, not a working lobby service. It logs bots into Steam and serves an HTTP API, but:

1. **The Dota 2 Game Coordinator (GC) session is never started.** The code never calls `SetPlaying(true)` / `SayHello()`, so no bot ever reaches `HAVE_SESSION`, and every go-dota2 request would fail with `ErrNotReady` (`D2/client_session.go:14-38`, `D2/client_request.go:26-29`; repo grep for `SetPlaying|SayHello` returns nothing).
2. **`POST /lobby/create` does not create a lobby.** It returns `201 "success"` without sending anything to the GC (`pkg/api/server.go:193-206`). `/lobby/info` is a stub too (`pkg/api/server.go:239-244`).
3. **There is no league support at all.** The API has no `leagueid` field, nothing lists tickets or leagues, and bots have no per-bot league configuration. go-dota2 *does* expose the pieces: `CMsgPracticeLobbySetDetails.leagueid` / `league_node_id` (`D2/protocol/dota_gcmessages_client_match_management.proto:137,163`), `GetProfileTickets` (`D2/client_generated.go:1371`), `RequestLeagueAvailableLobbyNodes` (`D2/client_generated.go:3519`), and the `LeagueAdminList` event (`D2/events/generated.go:661`).
4. **Auth is legacy-only and partly broken.** The `sentry_hash` key in `secrets.yaml` never reaches the struct (verified, see §3.3). There is no 2FA/TOTP, no Steam Guard handling, and no persisted refresh token. go-steam's `CMsgClientLogon` has no `access_token` field, and go-steam has no IAuthenticationService flow (§3.3). Valve deprecated sentry files and login keys in 2023 in favor of refresh tokens sent in `CMsgClientLogon.access_token` (field 108). The legacy password path is not confirmed removed, but Steam no longer issues sentry data, so Steam Guard prompts on every logon (§3.3).
5. **The connection state machine has real bugs:** a failed dial kills a bot permanently, a login failure schedules two reconnects, every reconnect leaks a GC packet handler, "connected" means only "Steam logged on", and concurrent lobby creation can pick the same bot (§2).
6. **Zero tests** (`go test ./...` reports `[no test files]` for every package), no Dockerfile, and the docs overclaim. README says the service "Creates a new Dota 2 lobby" (`README.md:139`), and IMPLEMENTATION_SUMMARY says it "provides a production-ready foundation for managing Dota 2 lobbies" (`IMPLEMENTATION_SUMMARY.md:154`). That is a softer claim, but it still overstates a service with no league support, no tests and the bugs in §2.

`go build ./...` and `go vet ./...` both pass. The problems are semantic, not compile-time.

---

## 1. Inventory: what exists

| File | Lines | What it does | State |
|---|---|---|---|
| `cmd/dota_lobby/main.go` | 76 | Loads config, creates `bot.Manager`, adds bots, starts HTTP server, handles SIGINT/SIGTERM | Works. Shutdown ordering issues (§2.9) |
| `pkg/config/config.go` | 110 | Viper loader for `config.yaml` + `secrets.yaml`; `Secret` redacting type | Works for host/port/api_key/username/password. `sentry_hash` broken (§3.3) |
| `pkg/bot/manager.go` | 233 | `Bot` + `Manager`; per-bot goroutine consuming `steamClient.Events()`; reconnect timers; `GetAvailableBot`, `ListBots`, `Shutdown` | Steam login only; no GC session; several bugs (§2) |
| `pkg/api/server.go` | 250 | `net/http` mux: `/health`, `/bots`, `/lobby/create`, `/lobby/info`; API-key middleware (constant-time compare) | Two of four endpoints are placeholders |
| `config.yaml.example`, `secrets.yaml.example` | 5 / 8 | Examples | `sentry_hash` example key is silently ignored |
| `Makefile`, `justfile`, `just/*.just`, `dev` | - | Build/test/lint; docs recipes | `just docs::*` references `Dockerfile.docs` and `mkdocs.yml`, which do not exist (session2 checklist unchecked, `docs/plans/sessions/session2.md:72-81`) |
| `.github/workflows/lint.yml`, `release.yml` | - | golangci-lint; tag release of a single linux binary | No test job, no Docker image build |
| `.golangci.yml` | 8 | Output settings only | No linters are enabled beyond the defaults |
| `docs/plans/2026-01-22-status-system-design.md` | 242 | Status system design (bot/lobby/system) | Not implemented; some API assumptions are wrong (§5) |
| `docs/plans/sessions/session{1,2,3}.md` | 390 | Session notes | session1 uses non-existent go-dota2 APIs (§5.3) |

No `*_test.go` files exist. There is no `Dockerfile`, even though the project context says it ships as a Docker image.

---

## 2. What is wrong or buggy (existing code)

Severity: **P0** = blocks a user goal, **P1** = correctness or reliability bug, **P2** = hygiene.

### 2.1 P0: GC session is never established

`pkg/bot/manager.go:86-99` creates a Dota client on `LoggedOnEvent` and sets the persona online, but never starts the GC session:

```go
// pkg/bot/manager.go:95-99
bot.dotaClient = dota2.New(bot.steamClient, logger)
bot.mu.Unlock()
// Set online status
bot.steamClient.Social.SetPersonaState(steamlang.EPersonaState_Online)
```

go-dota2 needs both of these calls:

- `SetPlaying(true)` sends `ClientGamesPlayed{570}` (`D2/client_session.go:14-17`).
- `SayHello()` sends `k_EMsgGCClientHello` (`D2/client_session.go:27-38`).

The GC answers with `ClientWelcome`, which flips status to `HAVE_SESSION` and creates the connection context (`D2/client_session.go:59-79,98-131`). Until then `validateConnectionContext()` returns `ErrNotReady` (`D2/client_session.go:41-56`), and every `MakeRequest` fails (`D2/client_request.go:26-29`).

**INFERRED (common practice in SteamKit/node-dota2):** the hello must be **re-sent periodically** (for example every 5-30 s) until a `ClientWelcomed` event arrives, because the GC can ignore the first hello right after `GamesPlayed`. go-dota2 has no retry loop of its own.

### 2.2 P0: "connected" means Steam, not GC

`bot.connected = true` is set on `LoggedOnEvent` (`pkg/bot/manager.go:89`). `GetAvailableBot` (`:164-179`), `GetDotaClient` (`:197-206`), and `/bots` (`pkg/api/server.go:129`) all treat that as "ready". A bot can be Steam-online with no GC session (always the case today, see 2.1). Readiness must come from `events.GCConnectionStatusChanged` / `events.ClientWelcomed` (`D2/events/session.go:9,19`), with `NewState == GCConnectionStatus_HAVE_SESSION`.

### 2.3 P1: A single failed TCP dial kills a bot permanently

`Client.ConnectToBind` calls `Disconnect()` (a no-op while `conn == nil`), then on a dial error calls `Fatalf` (`GS/client.go:156-163`). `Fatalf` emits `FatalErrorEvent` and calls `Disconnect()` again (`GS/client.go:94-97`). Because `c.conn` was never set, `Disconnect()` returns early (`GS/client.go:175-177`) and **no `DisconnectedEvent` is emitted**. The manager does not handle `steam.FatalErrorEvent` (`pkg/bot/manager.go:78-146`), so no reconnect is ever scheduled. A transient DNS or network failure at startup leaves that bot dead until the process restarts.

Also unhandled: `*steam.SteamFailureEvent` (`GS/auth.go:131`), `*steam.MachineAuthUpdateEvent`, `*steam.LoginKeyEvent`, and plain `error` values emitted via `Errorf` (`GS/client.go:100-102`).

### 2.4 P1: Login failure triggers double reconnects and hammers Steam

On a non-transient logon failure, go-steam emits `LogOnFailedEvent` **and then calls `Disconnect()`** (`GS/auth.go:135-138`), which emits `DisconnectedEvent` (`GS/client.go:185`). The manager schedules a reconnect for **both**: 30 s on `LogOnFailedEvent` (`pkg/bot/manager.go:101-121`) and 5 s on `DisconnectedEvent` (`:123-143`). The result:

- Two staggered `Connect()` calls, at about 5 s and 30 s, rather than concurrent ones. The 5 s call may establish a new connection. When the 30 s call fires, `ConnectToBind` first calls `Disconnect()` (`GS/client.go:157`), which tears down that live connection and emits another `DisconnectedEvent` (`GS/client.go:175-185`). That schedules yet another 5 s timer. Every failed logon adds a 30 s timer on top, so timers accumulate and the reconnect storm grows over time instead of staying at two calls.
- The `EResult` is never classified. `InvalidPassword`, `AccountLogonDenied` (email Steam Guard), `AccountLoginDeniedNeedTwoFactor`, and `RateLimitExceeded` all retry every 5 s forever. That is the fastest way to get the IP rate-limited or the account locked.

A fix needs an `EResult` → {retry with backoff, need-2FA, fatal-credentials} policy and a single reconnect owner (one timer per bot, cancelled on state change).

### 2.5 P1: Each reconnect leaks a GC handler and a Dota client

A new `dota2.New(...)` runs on **every** `LoggedOnEvent` (`pkg/bot/manager.go:95`). `New` calls `client.GC.RegisterPacketHandler(c)` (`D2/client.go:64`), which **appends** to a slice that is never pruned (`GS/gamecoordinator.go:28-29`). After N reconnects, every GC packet is dispatched to N Dota2 instances (`GS/gamecoordinator.go:46`). The old instances:

- answer GC pings again (`D2/client.go:180-183`), so N ping responses go out,
- emit duplicate events into the same `steamClient.Events()` channel,
- keep stale SOCache copies, so API reads can hit the wrong instance.

`GameCoordinator.handlers` also has no mutex (`GS/gamecoordinator.go:12-30`). `RegisterPacketHandler` runs on the manager goroutine while `HandlePacket` iterates on go-steam's `readLoop` goroutine (`GS/client.go:206-222`). That is a data race (`go test -race` would flag it once a test exists).

**Fix:** create the `dota2.Dota2` **once** per `steam.Client` (in `AddBot`). On each logon call `SetPlaying(true)` + `SayHello()`, and on disconnect call `SetPlaying(false)` or rely on `setConnectionStatus` to clear state.

### 2.6 P1: Dota events arrive on the Steam channel, and blocking the loop deadlocks the socket

go-dota2 has **no `Events()` method**. It emits through `d.client.Emit` (`D2/client.go:121-123`), a blocking send on go-steam's 30-slot channel (`GS/client.go:65,89-91`), which "is never closed" (`GS/client.go:83-87`). Consequences:

- The **only** place to observe `ClientWelcomed`, `GCConnectionStatusChanged`, `ClientStateChanged`, `UnhandledGCPacket`, `LeagueAdminList`, and so on is the existing per-bot `for event := range bot.steamClient.Events()` loop (`pkg/bot/manager.go:71`). It must gain cases for `*events.*` types.
- `accessState` emits **while holding `stateMtx`** (`D2/client.go:126-141`), and GC handlers run on the socket `readLoop`. If the manager's event loop ever blocks, for example by calling a `MakeRequest`-based method inline (which waits for a GC reply that the blocked readLoop can't deliver), **the bot deadlocks**. Long work must be pushed off the event loop.
- `ClientStateChanged` is emitted **by value** (`D2/client.go:136`), while the other events are pointers. A type switch on `*events.ClientStateChanged` never matches.
- After the loop returns on shutdown (`pkg/bot/manager.go:72-75`), nobody drains the channel, so any later `Emit` blocks its goroutine forever. That is acceptable at process exit but fatal if bots are ever removed at runtime.

### 2.7 P1: No bot reservation, so concurrent requests collide

`GetAvailableBot` returns the first connected bot in random map order (`pkg/bot/manager.go:168-176`) without marking it busy. A Dota account holds **one lobby at a time**: `LeaveCreateLobby` explicitly leaves or destroys the current lobby before creating (`D2/client_lobby.go:21-69`), and the SOCache lobby container is read with `GetOne()` (`D2/client_lobby.go:35`). Two simultaneous `POST /lobby/create` calls can pick the same bot, and the second call destroys the first lobby. A pool needs atomic reserve/release, plus a mapping from lobby ID to bot.

### 2.8 P1: Lobby create correctness (for when it is implemented)

| Issue | Evidence |
|---|---|
| `CreateLobby` is fire-and-forget. Success or failure is visible only through SOCache or an unhandled packet | `D2/client_lobby.go:13-18` |
| The GC reply `k_EMsgGCPracticeLobbyResponse` (7055) has **no handler or event** in go-dota2, so it surfaces as `events.UnhandledGCPacket` | `D2/protocol/dota_gcmessages_msgid.proto:26`; `D2/client.go:170-176`; no match in `D2/events/generated.go` |
| `LeaveCreateLobby(ctx, details, destroyOld)` waits on SOCache events only. If the GC rejects the create (bad league, bad region), it just waits until `ctx` expires | `D2/client_lobby.go:62-67` |
| HTTP `WriteTimeout` is 15 s (`pkg/api/server.go:33`). A synchronous create that waits for GC plus SOCache could exceed it, so use async create (`202` + poll/webhook) or a context deadline under 15 s | |
| `server_region` / `game_mode` are free-form strings (`pkg/api/server.go:145-146`). The proto wants `uint32` (`.proto:129-130`). There is no `ERegion` enum in the vendored protos (only `DOTA_GameMode`, `D2/protocol/dota_shared_enums.proto:4`), so the region table has to be maintained by hand | |
| `pass_key` must go in both `CMsgPracticeLobbyCreate.pass_key` and `details.pass_key`. go-dota2 handles this by copying `details.PassKey` (`D2/client_lobby.go:14-17`) | |
| Lobby settings persist after the host bot leaves, but a newly promoted host can change them. Keep the bot in the lobby until launch | [node-dota2 #532](https://github.com/Arcana/node-dota2/issues/532) (maintainer comment) |

### 2.9 P1/P2: Lifecycle and shutdown

- `log.Fatalf` inside the server goroutine (`cmd/dota_lobby/main.go:54-58`) skips the deferred `botManager.Shutdown()` (`:35`). A port conflict leaves Steam sessions open until the process dies (P2).
- `Shutdown` never calls `DestroyLobby`, `LeaveLobby`, or `SetPlaying(false)` (`pkg/bot/manager.go:216-233`), so live lobbies are orphaned on deploys (P1 once lobbies exist).
- The event-loop goroutines are not tracked by any WaitGroup. `reconnWg.Add(1)` can run concurrently with `reconnWg.Wait()` (`pkg/bot/manager.go:108,130` vs `:220`), which violates the `sync.WaitGroup` contract (P2 race).
- `LoggedOffEvent` (for example `LoggedInElsewhere`) only logs and does not reset `connected` (`pkg/bot/manager.go:144-145`) (P1).
- `AddBot` starts `connectBot` while holding `m.mu` (`pkg/bot/manager.go:47-62`). This is harmless today but fragile.

### 2.10 P2: API and config hygiene

- No `http.MaxBytesReader` and no `DisallowUnknownFields` (`pkg/api/server.go:157,227`). Raw decoder errors are echoed back to the client.
- Errors are `text/plain` via `http.Error`, while successes are JSON, so the envelope is inconsistent.
- The `Bearer` prefix match is case-sensitive (`pkg/api/server.go:88`).
- `lobby_id` is a `string` (`pkg/api/server.go:211`), but GC lobby IDs are `uint64` (`CSODOTALobby.lobby_id`, `D2/protocol/dota_gcmessages_common_lobby.proto:238`).
- Viper uses the global instance and has no env binding (`pkg/config/config.go:58-66`), so Docker deployments can't override config with `DOTA_LOBBY_*` variables without changes. Only `secretsViper` is a separate instance (`:89`).
- Each bot gets its own `logrus.New()` with no `username` field (`pkg/bot/manager.go:92-93`), so multi-bot logs can't be told apart. The rest of the code uses stdlib `log`, giving two logging systems.
- `.golangci.yml` enables nothing beyond the defaults, and CI has no `go test -race` job.

---

## 3. Missing functionality, per user goal

### 3.1 Goal 1: create and manage lobbies via API

| Capability | go-dota2 primitive (VERIFIED) | Repo status |
|---|---|---|
| Create (leaving any old lobby) | `LeaveCreateLobby(ctx, *CMsgPracticeLobbySetDetails, destroyOld bool)` `D2/client_lobby.go:21` | Missing |
| Update settings | `SetLobbyDetails(*CMsgPracticeLobbySetDetails)` `D2/client_generated.go:6841` (needs `lobby_id` set) | Missing |
| Invite player | `InviteLobbyMember(steamid.SteamId)` `D2/client_lobby_members.go:9` | Missing |
| Kick / kick from team | `KickLobbyMember(accountID uint32)` `:1967`; `KickLobbyMemberFromTeam(accountID)` `:1979` | Missing |
| Bot leaves its own slot | `JoinLobbyTeam(team DOTA_GC_TEAM, slot)` `D2/client_lobby_slot.go:9` (for example to spectator / `DOTA_GC_TEAM_PLAYER_POOL`) | Missing |
| Flip / shuffle teams | `FlipLobbyTeams()` `:574`; `SendBalancedShuffleLobby()` `:4775` | Missing |
| Apply registered team | `ApplyTeamToLobby(teamID)` `:77`; `CLobbyTeamDetails` via `team_details` (`dota_gcmessages_common_lobby.proto:144`) | Missing |
| Launch | `LaunchLobby()` `:2030` | Missing |
| Leave / destroy | `LeaveLobby()` `:2073`; `DestroyLobby(ctx)` `:516` (request/response); `AbandonLobby()` `:14` | Missing |
| Read lobby state | `GetCache().SubscribeType(cso.Lobby)` `D2/socache/socache_sub.go:33`; container `GetOne/Get/Range` `D2/socache/socache_container.go:160-195` | Missing |

Required new API surface: `POST /lobbies`, `GET /lobbies`, `GET /lobbies/{id}`, `PATCH /lobbies/{id}`, `POST /lobbies/{id}/invite`, `POST /lobbies/{id}/kick`, `POST /lobbies/{id}/launch`, `DELETE /lobbies/{id}`. Go 1.22+ `ServeMux` method and path patterns are available (`go.mod:3` = go 1.24.11).

### 3.2 Goal 2: headless, multiple bots concurrently

| Need | Status |
|---|---|
| One goroutine per bot draining events | **Exists** (`pkg/bot/manager.go:62,71`) |
| Per-bot state machine (disconnected → connecting → steam_online → gc_ready → in_lobby → in_game) | Missing (only a `bool`) |
| Pool: reserve / release / affinity (by league) / least-recently-used | Missing (§2.7) |
| Lobby ↔ bot registry | Missing |
| Backoff with jitter; EResult classification | Missing (§2.4) |
| Staggered startup (avoid N simultaneous logins from one IP) | Missing. **INFERRED** best practice; Steam rate-limits logons per IP |
| One `Dota2` instance per `steam.Client` | Broken (§2.5) |
| Per-bot structured logging | Missing |
| Bot add/remove at runtime | Missing (config-only at boot) |

### 3.3 Goal 2 (cont.): auth persistence and 2FA

**VERIFIED gaps:**

- `BotConfig` has no `mapstructure` tags (`pkg/config/config.go:49-53`). Viper/mapstructure matches keys to field names case-insensitively, but **underscores do not match**, so `sentry_hash` never fills `SentryHash`. I verified this with a build-overlay probe (no repo files changed) that loaded a `secrets.yaml` containing `sentry_hash: "abc"`: the result was `sentrylen=0`, while `username`/`password` loaded correctly.
- Even if it were loaded, `SentryHash` is never passed to `LogOnDetails` (`pkg/bot/manager.go:81-84`).
- `LogOnDetails` supports `AuthCode`, `TwoFactorCode`, `SentryFileHash`, `LoginKey`, and `ShouldRememberPassword` (`GS/auth.go:22-39`). The repo uses none of them, and there is no TOTP `shared_secret` config to generate `TwoFactorCode`.
- `MachineAuthUpdateEvent` and `LoginKeyEvent` (`GS/auth_events.go:21-32`) are neither handled nor persisted. There is no state directory or volume for them.
- go-steam's `CMsgClientLogon` has `login_key` (60), `sha_sentryfile` (83), and `two_factor_code` (101), but **no `access_token` field** (`GS/protocol/protobuf/client_server_login.pb.go`, struct `CMsgClientLogon`). go-steam has **no IAuthenticationService implementation**: `GS/protocol/protobuf/unified/` contains only `credentials`, `deviceauth`, `cloud`, `player`, and similar, and there is no `BeginAuthSessionViaCredentials`.

**From upstream sources:**

- VERIFIED: the current upstream proto defines `optional string access_token = 108;` in `CMsgClientLogon`, next to `login_key = 60` and `sha_sentryfile = 83` ([SteamDatabase/Protobufs steammessages_clientserver_login.proto](https://github.com/SteamDatabase/Protobufs/blob/master/steam/steammessages_clientserver_login.proto)). The field is named `access_token`, but SteamKit and node-steam-session put the JWT **refresh** token in it.
- VERIFIED: [SteamKit #1125](https://github.com/SteamRE/SteamKit/issues/1125) (opened Aug 2022, closed Mar 2023 via PR #1129) says the new client "no longer uses login keys" and uses the new `access_token` field in the logon message. It does **not** mention sentry files or the field number.
- VERIFIED (via verifier, not re-fetched): SteamKit 2.5.0 (Nov 2023) deprecated the sentry API (`SendMachineAuthResponse`, `UpdateMachineAuthCallback`) ([NuGet 2.5.0-Beta.2](https://www.nuget.org/packages/SteamKit2/2.5.0-Beta.2)).
- VERIFIED: [SteamKit discussion #1258](https://github.com/SteamRE/SteamKit/discussions/1258) says `UpdateMachineAuthCallback` no longer fires because sentry is deprecated, and xPaw says to store the `GuardData` from the auth-session poll instead, which "works similarly to old sentry files". Users there log in via `BeginAuthSessionViaCredentials`, store the refresh token, then send a normal `CMsgClientLogon`.
- The [gg-cr/go-steam fork](https://pkg.go.dev/github.com/gg-cr/go-steam) README says "Valve deprecated sentry files and login keys in mid-2023". Tokens last about 200 days ([node-steam-session](https://github.com/DoctorMcKay/node-steam-session)). The gg-cr fork is a paralin/go-steam derivative that adds field 108 plus refresh-token login, and it recommends getting the token over HTTP `IAuthenticationService/*`.

**UNCERTAIN:** whether plain username + password + `TwoFactorCode` through the legacy `CMsgClientLogon` path still succeeds today for bot accounts. Reports suggest it still works, but because Steam no longer sends sentry data, every logon triggers a Steam Guard prompt, which a headless bot cannot answer without a TOTP `shared_secret`. A live logon with a test account would settle this. Treat it as at-risk, and plan either a go-steam fork/patch (add field 108 + refresh-token store) or a vendored fork such as gg-cr.

### 3.4 Goal 3: league "tickets" and multiple league drop-downs

**Nothing exists in the repo.** What go-dota2 offers (all VERIFIED to exist):

| Primitive | Location | Use |
|---|---|---|
| `CMsgPracticeLobbySetDetails.leagueid` (uint32, field 16) | `D2/protocol/dota_gcmessages_client_match_management.proto:137` | Selects the league when creating or updating a lobby |
| `CMsgPracticeLobbySetDetails.league_node_id` (field 49) | same file `:163` | Bracket node for the match |
| `CSODOTALobby.leagueid` (42), `league_node_id` (110) | `D2/protocol/dota_gcmessages_common_lobby.proto:265,314` | **Read-back check**: after create, confirm the GC kept the league (see below) |
| `RequestLeagueAvailableLobbyNodes(ctx, leagueID)` → `CMsgDOTALeagueAvailableLobbyNodes{node_infos{node_id,node_name,node_group_name,team_id_1,team_id_2}}` | `D2/client_generated.go:3519`; proto `dota_gcmessages_common_league.proto:236-250` | Populate node pickers per league; possibly also an authorization probe (**UNCERTAIN**) |
| `events.LeagueAdminList` (`CMsgLeagueAdminList{account_ids}`), pushed by GC (`k_EMsgGCLeagueAdminList` 7434) | `D2/events/generated.go:661`; `D2/protocol/dota_gcmessages_common.proto:377`; handler registered `D2/client_generated.go:7671` | When/why the GC sends it is **UNCERTAIN** |
| `k_EMsgDOTALeagueInfoListAdminsRequest/Reponse` (7633/7634), request message empty | `D2/protocol/dota_gcmessages_msgid.proto:273-274`; `dota_gcmessages_common_league.proto:233` | **Not wrapped**: apigen marks it `MsgSenderNone` (`D2/apigen/msg_overrides.go:98-99`), and no response proto type exists in the vendored protos. A raw `MakeRequest` would need a response type |

How to support multiple leagues per bot or across bots (design implications):

1. Config gets `bots[].leagues: [ids]`, plus optional `default_league`.
2. The pool picks a bot by `(gc_ready && !busy && leagueid ∈ bot.leagues)`.
3. After `LeaveCreateLobby`, compare `CSODOTALobby.GetLeagueid()` to the requested ID. A mismatch means the bot is not authorized for that league; mark the `(bot, league)` pair as invalid. **INFERRED** behavior: per [node-dota2 #532](https://github.com/Arcana/node-dota2/issues/532), `leagueid` *does* stick when passed as a number and the bot is a league admin. What the GC does for a non-admin is unverified.
4. **INFERRED / UNCERTAIN:** a league's lobby drop-down in the client is probably tied to the account being added to the league on Valve's league admin page, most likely in an admin role. The evidence is weak. [Challengermode](https://support.challengermode.com/en/organizing/integrate-your-dota-2-league) (step 3) asks owners to add its Steam accounts "to your Dota 2 League" as "the bots that will be hosting the lobbies", and never says "admin". The strongest hint that admin status matters is [node-dota2 #532](https://github.com/Arcana/node-dota2/issues/532), whose author registered the bot as a league admin. The old [Steam Support "League Tickets"](https://support.steampowered.com/kb/1719-OPHL-1139/dota-2-league-tickets) article dates from the ticket-item era, redirects to help.steampowered.com, and its body could not be retrieved, so it is not evidence for how the current drop-down works. To resolve this, add a test bot to a league with and without admin rights and check whether `leagueid` sticks on the created lobby. Also **UNCERTAIN:** whether the GC exposes that admin list to the client in a message go-dota2 decodes.

### 3.5 Goal 4: list current tournament tickets/leagues the bots have access to

| Option | Status / evidence | Confidence |
|---|---|---|
| `GetProfileTickets(ctx, accountID)` → `CMsgDOTAProfileTickets{result, account_id, league_passes[{league_id, item_def}]}` | `D2/client_generated.go:1371-1388`; proto `dota_gcmessages_client.proto:338-351` | The API is VERIFIED. That `league_passes` equals "leagues the bot can create lobbies for" is **UNCERTAIN**. It most likely lists *purchased/owned league passes (tickets)*, which is not the same as admin rights. That is INFERRED from the `item_def` field, which points to an econ item. No external source documents these semantics. A live call against a bot known to be in a league would settle it |
| SOCache `cso.EventTicket` (type 40) / `cso.Tournament` (2009) | Constants exist (`D2/cso/cso_types.go:31,47`), but **there is no constructor** in `csoTypeCtors` (`D2/cso/cso_types.go:55-86`), so `GetContainerForTypeID` errors (`D2/socache/socache_container.go:25-31`) and those objects are dropped | VERIFIED not usable without patching go-dota2 |
| `cso.EconItem` (type 1) inventory scan for ticket item defs | Ctor exists (`D2/cso/cso_types.go:56-58`). Mapping item defs to league IDs needs `items_game` data | INFERRED |
| Static config + read-back validation (§3.4 step 3) | No protocol dependency | Reliable fallback |
| `LeagueAdminList` event capture | Exists; trigger unknown | UNCERTAIN |

Recommended API: `GET /leagues` (union across bots) and `GET /bots/{username}/leagues` returning `{league_id, source: config|profile_tickets|validated, last_validated_at, nodes?}`.

### 3.6 Lobby lifecycle and match result tracking

Nothing exists. Data that is available from `CSODOTALobby` (VERIFIED field numbers in `D2/protocol/dota_gcmessages_common_lobby.proto`):

- `state` (4): `UI=0, SERVERSETUP=1, RUN=2, POSTGAME=3, READYUP=4, NOTREADY=5, SERVERASSIGN=6` (enum at `:214-221`)
- `game_state` (22), `match_id` (30), `match_outcome` (70, `EMatchOutcome`: `RadVictory=2, DireVictory=3, ...`, `D2/protocol/dota_shared_enums.proto:453`), `connect` (5), `server_id` (6), `leader_id` (11), `lobby_type` (12), `pass_key` (39), `series_type` (46)
- `all_members` (120) of `CSODOTALobbyMember{id(fixed64 steamid), hero_id, team, slot, leaver_status, coach_team, ...}` (`:96-110`)

Post-game details: `RequestMatchDetails(ctx, matchID)` (`D2/client_generated.go:3562`), which is GC request/response. The Steam Web API `IDOTA2Match_570/GetMatchDetails` is an alternative (needs a Web API key, and is out of scope for the current code).

Missing pieces:

- A per-lobby watcher goroutine (SOCache subscription per bot) that maps `CSODOTALobby` to the service's `LobbyStatus`.
- Persistence for lobbies and results so they survive restarts (none today; there isn't even an in-memory registry).
- Webhook or SSE for state changes (callers currently have nothing to poll).
- Timeouts: auto-destroy a lobby if it isn't launched within N minutes; release the bot after `POSTGAME`.

### 3.7 Tests

Currently there are **no tests**. Minimum set:

| Test | Seam needed |
|---|---|
| Config: key mapping (`sentry_hash`, leagues), defaults, missing files | Viper instance injection (stop using the global) |
| Bot state machine transitions incl. FatalError/LogOnFailed/Disconnected | Interface over `steam.Client` (`Connect`, `Events`, `Auth.LogOn`) or feed a fake events channel into a pure `handleEvent(state, event)` function |
| Pool reservation under concurrency (`-race`) | Pool as its own type |
| Lobby tracker from synthetic `socache.CacheEvent`s | Interface `LobbyCache{ SubscribeType(...) }` |
| HTTP handlers (auth, validation, JSON envelope) | `httptest` + fake manager interface |
| CI job `go test -race ./...` | `.github/workflows/` |

---

## 4. API surface: current vs required

| Endpoint | Today | Gap |
|---|---|---|
| `GET /health` | static `healthy` (`pkg/api/server.go:105-120`) | Should split liveness and readiness (ready = at least one bot with a GC session) |
| `GET /bots` | `{username: bool}` (`:123-139`) | State, GC status, current lobby, leagues, last error |
| `POST /lobby/create` | placeholder 201 (`:150-207`) | Real create; `league_id`, `league_node_id`, `series_type`, `cm_pick`, `allow_spectating`, `dota_tv_delay`, `pause_setting`, `team_details`, `invites[]`, `bot` affinity; async result |
| `GET/POST /lobby/info` | placeholder (`:215-250`) | `GET /lobbies/{id}` backed by SOCache plus a registry |
| `GET /leagues`, `GET /bots/{u}/leagues` | — | New (§3.5) |
| `GET /leagues/{id}/nodes` | — | New, via `RequestLeagueAvailableLobbyNodes` |
| `/status*` | — | Per design doc (§5) |
| Lobby actions (invite/kick/launch/destroy/update) | — | New (§3.1) |
| Bot 2FA input (`POST /bots/{u}/auth-code`) | — | New, if interactive Steam Guard is supported |

---

## 5. Status system design (docs/plans/2026-01-22-status-system-design.md) vs reality

### 5.1 Implementation status

Nothing from Phases 1-3 exists: there is no `pkg/bot/status.go`, `pkg/lobby/tracker.go`, or `pkg/api/status.go`, and no `/status*` routes in `pkg/api/server.go:41-51`.

### 5.2 Corrections needed in the design

| Design statement | Issue | Evidence |
|---|---|---|
| `ConnectedEvent -> BotStateConnecting` (design `:173`) | `ConnectedEvent` means TCP and crypto are up. "Connecting" should be set **before** `Connect()`, and `ConnectedEvent` means "logging in" | `GS/client_events.go:11`; flow in `pkg/bot/manager.go:69-84` |
| `GC welcome -> BotStateGCConnected` | Correct, but it requires §2.1. Also drop back on `GCConnectionStatusChanged` away from `HAVE_SESSION` | `D2/client_session.go:98-131` |
| Lobby states `Creating -> Waiting -> Ready -> In Game -> Finished` | Map to `CSODOTALobby.State`: `UI`→waiting, `SERVERSETUP/SERVERASSIGN`→launching, `RUN`→in_game, `POSTGAME`→finished. "Ready (all players ready)" has no practice-lobby equivalent; it would have to be derived from team slot counts | proto `:214-221` |
| `LobbyStatus` fields | Missing `league_id`, `league_node_id`, `match_outcome`, `series_type`, `leader_id`, `lobby_type`. `ServerRegion`/`GameMode` as `uint32` is fine | proto `:238-314` |
| `BotStatus` | Missing `leagues`, `gc_status` (raw enum), `steam_id`, `auth_state` (needs_2fa / token_expired) | — |
| `/status/bots/{username}` | Needs Go 1.22 pattern routing (`mux.HandleFunc("GET /status/bots/{username}", ...)`) or a router | `go.mod:3` |
| "Subscribe to SOCache lobby events" | Must be done **per bot Dota client**, and recreated only if the Dota client is recreated (it shouldn't be, §2.5) | `D2/client.go:69-71` |

### 5.3 Wrong APIs in session notes and skill references

Implementing straight from these would fail to compile:

| Claimed API | Reality |
|---|---|
| `dotaClient.Events()` (`docs/plans/sessions/session1.md:82`) | Does not exist. Dota events arrive on `steamClient.Events()` (`D2/client.go:121-123`) |
| `d.GetState().Lobby` (`session1.md:146`; skill `go-dota2.md` "State Access") | No `GetState()`. `state` is unexported (`D2/client.go:41-42`), and `state.Lobby` is **never assigned** anywhere in go-dota2 (grep for `.Lobby =` outside `protocol/` finds nothing). Use SOCache |
| `client.On(func(e *events.X))` (skill `go-dota2.md`) | No such method in go-steam or go-dota2 |
| `ClientWelcomed.Msg` (skill) | Field is `Welcome` (`D2/client_session.go:77`) |
| `KickLobbyMember(memberId, team)`, `InviteToLobby`, `SetLobbyTeamSlot` (skill) | Real names and signatures: `KickLobbyMember(accountID uint32)`, `InviteLobbyMember(steamid.SteamId)`, `JoinLobbyTeam(team, slot)` |

---

## 6. Prioritized remediation backlog

| # | Item | Priority | Touches |
|---|---|---|---|
| 1 | Create `Dota2` once per bot; on logon call `SetPlaying(true)` + `SayHello` (with retry until welcomed); track GC state from events | P0 | `pkg/bot/manager.go` |
| 2 | Handle `FatalErrorEvent`, `SteamFailureEvent`, `LoggedOffEvent`; single reconnect owner with exponential backoff + jitter; classify `EResult` | P0 | `pkg/bot` |
| 3 | Pool with reserve/release and a lobby↔bot registry | P0 | new `pkg/bot/pool.go` |
| 4 | Real lobby create with `leagueid`, region/mode mapping, async result, read-back validation | P0 | `pkg/api`, new `pkg/lobby` |
| 5 | Lobby tracker via SOCache → status types | P1 | `pkg/lobby/tracker.go` |
| 6 | Per-bot `leagues` config + `/leagues` endpoints (config + validated + `GetProfileTickets`) | P1 | `pkg/config`, `pkg/api` |
| 7 | Auth: `mapstructure` tags, TOTP `shared_secret`, persisted state dir; plan a refresh-token (field 108) go-steam patch/fork | P1 | `pkg/config`, `go.mod` |
| 8 | Lobby actions (invite/kick/launch/destroy/update) | P1 | `pkg/api`, `pkg/lobby` |
| 9 | Match result capture (`match_id`, `match_outcome`, `RequestMatchDetails`) + persistence (SQLite/bolt) | P1 | new `pkg/store` |
| 10 | Status endpoints per corrected design | P1 | `pkg/api/status.go` |
| 11 | Graceful shutdown: destroy/leave lobbies, `SetPlaying(false)`, track goroutines; no `log.Fatalf` in goroutines | P2 | `cmd`, `pkg/bot` |
| 12 | Tests + `-race` CI; Dockerfile; env-var config; fix README/IMPLEMENTATION_SUMMARY overclaims; remove or complete docs recipes | P2 | repo-wide |

Sketch of the corrected per-bot event handling (illustrative, not committed code):

```go
// one Dota2 per steam.Client, created in AddBot
bot.dota = dota2.New(bot.steam, log.WithField("bot", cfg.Username))

for ev := range bot.steam.Events() {
    switch e := ev.(type) {
    case *steam.ConnectedEvent:
        bot.setState(StateLoggingIn); bot.steam.Auth.LogOn(details())
    case *steam.LoggedOnEvent:
        bot.setState(StateSteamOnline)
        bot.steam.Social.SetPersonaState(steamlang.EPersonaState_Online)
        bot.dota.SetPlaying(true)
        go bot.helloUntilWelcomed(ctx) // SayHello every ~5-10s until GC ready
    case *events.ClientWelcomed:
        // GC ready only after this
    case *events.GCConnectionStatusChanged:
        bot.setGCReady(e.NewState == protocol.GCConnectionStatus_GCConnectionStatus_HAVE_SESSION)
    case steam.FatalErrorEvent: // value type (error), not pointer
        bot.scheduleReconnect(classify(e))
    case *steam.LogOnFailedEvent:
        bot.lastLogonResult = e.Result // reconnect decided on the following DisconnectedEvent
    case *steam.DisconnectedEvent:
        bot.setGCReady(false); bot.scheduleReconnect(policyFor(bot.lastLogonResult))
    }
    // never call MakeRequest-based methods inline here (deadlock, §2.6)
}
```

---

## 7. Verified vs Uncertain

### Verified (seen in source, or reproduced locally)

- No `SetPlaying`/`SayHello`/SOCache/`leagueid` usage anywhere in `pkg/` or `cmd/` (repo grep).
- `/lobby/create` and `/lobby/info` are placeholders (`pkg/api/server.go:193-206,239-244`).
- go-dota2 emits events through `steam.Client.Emit` and has no `Events()` / `GetState()` (`D2/client.go:121-123`; method list of `D2/*.go`).
- `dota2.New` registers a GC handler that is never removed, and the handler slice is append-only and unlocked (`D2/client.go:64`; `GS/gamecoordinator.go:12-30,46`).
- A dial failure emits only `FatalErrorEvent`, with no `DisconnectedEvent` (`GS/client.go:156-163,94-97,171-186`).
- `LogOnFailedEvent` is followed by `Disconnect()` → `DisconnectedEvent` (`GS/auth.go:135-138`; `GS/client.go:185`).
- `sentry_hash` is not decoded into `BotConfig.SentryHash` (local overlay probe: `sentrylen=0`).
- go-steam `CMsgClientLogon` lacks `access_token`; there is no IAuthenticationService in go-steam's unified protos.
- `CMsgPracticeLobbySetDetails.leagueid` (16), `league_node_id` (49); `CSODOTALobby.leagueid` (42), `match_id` (30), `match_outcome` (70).
- `GetProfileTickets`, `RequestLeagueAvailableLobbyNodes`, `LeagueAdminList` event exist. `LeagueInfoListAdmins` is not wrapped.
- `cso.EventTicket` / `cso.Tournament` have no constructors, so the SOCache cannot hold them.
- `k_EMsgGCPracticeLobbyResponse` has no go-dota2 handler or event.
- `go build ./...` and `go vet ./...` pass; there are zero test files; no Dockerfile.
- `leagueid` persists in a lobby when passed numerically and the bot is a league admin ([node-dota2 #532](https://github.com/Arcana/node-dota2/issues/532)).

### Uncertain / inferred

- Whether the legacy password + 2FA logon path still works for bot accounts in late 2026. Upstream says sentry files and login keys are deprecated (Steam no longer sends sentry data); refresh tokens in `access_token` (field 108) are the supported route.
- Whether `GetProfileTickets.league_passes` reflects leagues usable in the lobby drop-down (likely *owned passes*, not *admin rights*).
- What the GC does with a `leagueid` the bot isn't authorized for (stripped silently, or error via `PracticeLobbyResponse`). Needs a live test.
- Whether `RequestLeagueAvailableLobbyNodes` answers non-admins. When `LeagueAdminList` is pushed.
- ~~The exact `CMsgPracticeLobbyJoinResponse`-shaped payload of message 7055 (SteamKit convention).~~ Corrected by [gap-05](gap-05-7055-decode-and-league-rejection.md): 7055 decodes as `CMsgGenericResult` (EResult, 1 = OK). Neither SteamKit nor paralin/Dota2 handles 7055; the JoinResponse decode comes from pre-2017 node-dota2 and the Java client.
- `SayHello` retry interval and necessity (common practice in other libraries).
- `server_region` numeric values (no enum in the vendored protos; community tables only).
- Steam per-IP logon rate limits for staggering many bots.

---

## 8. Sources

**Local, repo**
- `cmd/dota_lobby/main.go`, `pkg/bot/manager.go`, `pkg/api/server.go`, `pkg/config/config.go`, `go.mod`, `README.md`, `IMPLEMENTATION_SUMMARY.md`, `config.yaml.example`, `secrets.yaml.example`, `justfile`, `just/*.just`, `.github/workflows/*.yml`, `.golangci.yml`
- `docs/plans/2026-01-22-status-system-design.md`, `docs/plans/sessions/session1.md`, `session2.md`, `session3.md`

**Local, go-dota2** (`D2/`)
- `client.go`, `client_session.go`, `client_lobby.go`, `client_lobby_members.go`, `client_lobby_slot.go`, `client_request.go`, `client_cache.go`, `client_generated.go` (lines cited above)
- `socache/socache.go`, `socache/socache_container.go`, `socache/socache_sub.go`, `cso/cso_types.go`, `state/state.go`, `events/session.go`, `events/state.go`, `events/generated.go`, `apigen/msg_overrides.go`
- `protocol/dota_gcmessages_client_match_management.proto`, `dota_gcmessages_common_lobby.proto`, `dota_gcmessages_common_league.proto`, `dota_gcmessages_client.proto`, `dota_gcmessages_common.proto`, `dota_gcmessages_msgid.proto`, `dota_shared_enums.proto`

**Local, go-steam** (`GS/`)
- `client.go`, `auth.go`, `auth_events.go`, `client_events.go`, `gamecoordinator.go`, `protocol/protobuf/client_server_login.pb.go`, `protocol/protobuf/unified/`

**Local, skill notes (contain errors, see §5.3)**
- `/home/kettle/.claude/skills/go-dota2-steam/references/go-dota2.md`, `go-steam-events.md`

**Web**
- SteamKit "New Steam login flow" (login keys replaced by `access_token`; no field number or sentry mention): https://github.com/SteamRE/SteamKit/issues/1125
- SteamDatabase/Protobufs, `CMsgClientLogon.access_token = 108`: https://github.com/SteamDatabase/Protobufs/blob/master/steam/steammessages_clientserver_login.proto
- SteamKit discussion #1258 (sentry deprecated, GuardData replaces it): https://github.com/SteamRE/SteamKit/discussions/1258
- SteamKit2 2.5.0 (sentry API deprecated): https://www.nuget.org/packages/SteamKit2/2.5.0-Beta.2
- gg-cr/go-steam (paralin fork with refresh-token login, field 108): https://pkg.go.dev/github.com/gg-cr/go-steam
- node-steam-session (refresh tokens, IAuthenticationService): https://github.com/DoctorMcKay/node-steam-session
- McKay forum, refresh token login: https://dev.doctormckay.com/topic/4228-login-via-refresh-token/
- node-dota2 issue #532 (leagueid must be numeric; settings persist after host leaves): https://github.com/Arcana/node-dota2/issues/532
- Steam Support, Dota 2 League Tickets: https://support.steampowered.com/kb/1719-OPHL-1139/dota-2-league-tickets
- Challengermode, Integrate your Dota 2 League (bots added "to your Dota 2 League"; no admin role stated): https://support.challengermode.com/en/organizing/integrate-your-dota-2-league
- ValvePython dota2 lobby docs: https://dota2.readthedocs.io/en/stable/dota2.features.lobby.html
- paralin/go-dota2: https://github.com/paralin/go-dota2
- paralin/go-steam: https://github.com/paralin/go-steam

## Verification notes

Corrections applied after a verifier pass:

- **Auth (TL;DR item 4, §3.3, §6 open questions):** the claim that Steam "removed" sentry files and login keys became "deprecated". Legacy password logon may still work, but Steam Guard now prompts on every logon. Field 108 is now cited to the SteamDatabase proto, and sentry deprecation to SteamKit 2.5.0, discussion #1258 and the gg-cr README. SteamKit #1125 is now cited only for what it says (login keys replaced by `access_token`, closed Mar 2023), not for the field number or sentry files. The doc also notes that the field is named `access_token` but carries the refresh token. The local go-steam facts were re-checked: `CMsgClientLogon` ends at `web_logon_nonce = 103`, and there is no auth service under `unified/`.
- **§2.4 double reconnect:** "two concurrent `Connect()` calls" became two staggered calls at about 5 s and 30 s. The 30 s call tears down the live connection (`ConnectToBind` → `Disconnect()` → `DisconnectedEvent`), which adds further 5 s timers, so timers accumulate (`pkg/bot/manager.go:101-143`; `GS/client.go:156-185`).
- **§3.4 item 4 (league drop-down / admin):** Challengermode does not say "admin", so the admin link is now marked INFERRED. The legacy Steam KB article is no longer used as evidence. node-dota2 #532 is named as the only (weak) admin hint, and a live test is given as the way to resolve it.
- **§3.4 `GetProfileTickets.league_passes`:** still UNCERTAIN. The doc now says the "owned passes" reading is inferred from `item_def` and that no external source documents it.
- **TL;DR item 6:** the IMPLEMENTATION_SUMMARY quote now reads "production-ready foundation".
