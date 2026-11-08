# Research index: dota_lobby

Research done on 2026-11-06 for the `dota_lobby` service. The service is a Go REST API that creates and manages Dota 2 practice and league lobbies through headless Steam bot accounts. It is built on `paralin/go-steam` and `paralin/go-dota2`.

Pinned versions under study (`go.mod:6-7`):

- `github.com/paralin/go-dota2 v0.0.0-20250623204622-532cb1851217`
- `github.com/paralin/go-steam v0.0.0-20250502043548-f167ff28a93a`

Upstream masters evaluated as the upgrade target ([gap-01](gap-01-upstream-master-build-auth.md)): go-steam `v0.0.0-20260922075330-336e824f8540` and go-dota2 `v0.0.0-20260922075533-6d32dc38d075`. They resolve together and need `go 1.26.0`.

The design that follows from this research is in [`../plans/2026-11-06-lobby-service-architecture.md`](../plans/2026-11-06-lobby-service-architecture.md).

Confidence labels used across the docs:

- **VERIFIED** (or **[V]**): read in local source, in a primary document, or reproduced locally.
- **REPORTED** (or **[R]**): field experience from a production bot. Credible, but not Valve documentation.
- **LIKELY**: strong secondary evidence. Not live-tested by us.
- **INFERRED** / **UNCERTAIN**: reasoned from evidence, or the sources conflict. Each one needs a live test.

---

## 1. Documents

### [01 - Lobby lifecycle protobufs](01-lobby-protocol.md)

This doc covers the whole practice-lobby protocol as the pinned go-dota2 exposes it:

- every `CMsgPracticeLobbySetDetails` field (number, Go type, default, meaning)
- every `CSODOTALobby` and `CSODOTALobbyMember` field
- the lobby enums (`State`, `DOTA_GameState`, `EMatchOutcome`, `DOTA_GC_TEAM`, visibility, pause, TV delay)
- a message-ID map and the full set of lobby, chat and team wrappers, with file:line refs
- the lifecycle from create through configure, invite, seat, launch, in-game and post-game to destroy

Key points:

- Creating a lobby is fire-and-forget. Success shows up only as a `CSODOTALobby` (SO type 2004) appearing in the SOCache. The GC reply `k_EMsgGCPracticeLobbyResponse` (7055) has no go-dota2 handler. It decodes as `CMsgGenericResult` with an EResult (1 = OK), settled in [gap-05](gap-05-7055-decode-and-league-rejection.md). 7055 is shared by create, kick, launch and leave (not flip/shuffle/abandon).
- `LeaveCreateLobby` blocks until its ctx expires when the GC rejects a create.
- A league lobby is just `leagueid` (field 16), optionally with `league_node_id` (49). Each lobby takes one league.
- One account sits in at most one lobby.
- The host cannot seat another member in a specific slot. It can kick a member back to the pool, kick them out, flip sides, shuffle, clear a team or add AI bots.
- Kick wrappers take 32-bit account IDs, while members carry SteamID64.

### [02 - League / tournament tickets and the lobby drop-down](02-league-tickets.md)

Today a "league ticket" means a league ID that the bot account administers. You create the league at dota2.com/league and add each bot account under **Admins**. Ticket items have not been needed for free-to-spectate leagues since 6 July 2017.

The GC lists the leagues an account administers through `k_EMsgDOTALeagueInfoListAdminsRequest` (7633). The reply, `...Reponse` (7634, Valve's typo), carries a `CMsgDOTALeagueInfoList`. Two independent clients decode it that way, and devilesk/dota-ihl-bot uses it to load several tickets per bot. go-dota2 has no wrapper for it (apigen marks it `MsgSenderNone`). A roughly 10-line `MakeRequest` helper fills the gap and compiles against our `go.mod`.

A few related points:

- `GetProfileTickets` lists owned league **passes** (econ items). Passes do not grant hosting rights.
- The public `www.dota2.com/webapi/IDOTA2League/*` endpoints list leagues but return an empty admins list.
- `IDOTA2Match_570/GetLeagueListing` now returns 404.
- `CSODOTAGameAccountClient.is_league_admin` is a cheap "any league at all?" check.

### [03 - go-dota2 client internals, SOCache, staleness](03-go-dota2-internals.md)

This doc covers the internals of `Dota2` (`New`, `SetPlaying`, `SayHello`, events, state, SOCache, `MakeRequest`, apigen) and how to send or receive GC messages that have no wrapper. Its findings:

- **Event delivery:** events arrive on `steamClient.Events()`, a 30-slot channel whose `Emit` blocks. Blocking that loop on a GC round-trip deadlocks the socket read loop.
- **SOCache:**
  - it stores objects under the owner SOID and decodes only `object_data[0]`
  - it is never cleared when the GC session drops
  - its 10-slot subscriber channels drop events when full
  - `Dota2State.Lobby` is never populated
- **`MakeRequest`:** it matches replies by message type only.
- **Upstream drift:** the pin is about 50 commits behind upstream. Upstream HEAD moved to protobuf-go-lite and Go 1.26, and correlates replies by job ID.
- **Proto staleness:** even upstream HEAD is stale.
  - `LobbyDotaTVDelay` was **renumbered** on 2026-08-12 (`_120` is now 2, not 1).
  - `CMsgPracticeLobbyLaunch` gained `nonce = 6` on 2026-09-25.
  - Upstream's proto auto-update workflow has failed on every run since 2026-04-19.

The recommendation was to run our own fork with a regenerate-and-check-enum-drift process. The gap pass (gap-02, gap-04) downgrades that: the nonce is not required today and TV delay can be sent as raw integers, so v1 runs on unforked upstream master with a golden-file drift test.

### [04 - Headless Steam auth for many bots](04-steam-auth-headless.md)

The pinned go-steam can only send the legacy password `CMsgClientLogon`. It has no `access_token` (field 108) and no IAuthenticationService client. In go-steam PR #11, contributors report that Steam has rejected this login with `InvalidPassword` since about June 2026. Upstream go-steam fixed it in `6b06e91640b1` (2026-06-27). That fix brings `LogOnDetails.AccessToken`, `Auth.Authenticate()`, `LogOn(ctx, ...) error` and `PlayingSessionStateEvent`. These are breaking changes, so go-steam and go-dota2 must be upgraded together.

The recommended design:

- **Steam Guard:** give each bot a mobile authenticator and generate the TOTP from its `shared_secret`.
- **Tokens:** call `Authenticate` once, keep the refresh token (about 200 days) on a 0600 volume, then always log in with `AccessToken`.
- **Shutdown:** only Disconnect.
- **Failures:** classify each `EResult` and run one reconnect owner per bot, with exponential backoff and jitter.
- **Accounts:** one dedicated, non-limited account per concurrent bot, with Dota 2 (app 570) launched once.

### [05 - Prior art](05-prior-art.md)

This doc surveys 11 projects, among them dota-ihl-bot, Dota2-EU-Ladder, under-5k-league, Dota2SentinelBot, meepow (the only go-dota2 lobby service), daas and Challengermode. Its findings:

- Every one of them treats an account as hosting one lobby at a time, and gets concurrency by pooling accounts.
- Leaving mid-game to free a bot is unreliable: under-5k's 2026 logs show the GC ignores Leave during `RUN`.
- dota-ihl-bot is the reference for this project's goals 3 and 4. It refreshes each bot's admin leagues from 7633/7634 as "tickets" and picks a free bot that holds the requested league. Un-ticketed lobbies go to bots with no tickets.
- The doc lists the pitfalls these projects hit and 10 patterns we can reuse: durable claims, idempotency keys, per-bot GC queues, verify-after-create, an invite policy, seat enforcement and resync.

### [06 - Match tracking and results](06-match-results.md)

The fastest result signal is the host bot's `CSODOTALobby` update. Its `state` goes UI → SERVERASSIGN/SERVERSETUP → RUN → POSTGAME, `match_id` (30) is set at launch, and `match_outcome` (70) holds the result: 2..13 means a winner, 64..69 means not scored.

The production under-5k bot reports two problems:

- the GC ignores Leave during RUN
- `game_state` jumps straight from 1 to 6

The Web API returns only ticketed matches, and even those are not guaranteed. The fallback order is:

1. lobby SO
2. GC `RequestMatchDetails`
3. Web API (ticketed only)
4. OpenDota, using `/leagues/{id}/matchIds` for amateur leagues
5. STRATZ
6. a replay or manual import

The doc also gives a pure `derive()` phase function, timeouts, and a signed, idempotent webhook catalogue with JSON payloads and a persistence schema.

### [07 - Docker / headless deployment and ops](07-deployment.md)

The deployment design covers:

- **Image:** a multi-stage build with CGO off on `distroless/static:nonroot`.
- **Config layers:** `config.yaml` (ConfigMap), then `secrets.yaml` (file), then `DOTA_LOBBY_*` env, then `*_FILE` indirection. Viper reads no env at all today.
- **State:** a per-bot state volume at `/var/lib/dota_lobby/bots/<user>/`.
- **Probes:** `/livez` does not depend on Steam or the GC. `/readyz` is based on GC `HAVE_SESSION`.
- **Shutdown:** a graceful drain that destroys only `UI`-state lobbies.
- **Ops:** Prometheus on a separate port, egress on TCP 27017 plus 443, Tuesday-maintenance handling and conservative pacing.
- **Replicas:** exactly **one process per account set**, using `Recreate` plus a flock or Lease. Scale out by sharding.

It also confirms by running it that `Secret` leaks under `%#v`, and warns that `Secret.MarshalJSON` would corrupt a persisted token.

### [08 - Current repo gap analysis](08-gap-analysis.md)

The repo is a scaffold. These gaps are verified:

- No GC session is ever established, because `SetPlaying` and `SayHello` are never called.
- `/lobby/create` returns 201 without doing anything.
- There is no league support of any kind.
- `sentry_hash` is never loaded.
- A failed dial kills the bot permanently, since a `FatalErrorEvent` without a `DisconnectedEvent` is never handled.
- A logon failure stacks reconnect timers.
- Each logon leaks a GC handler, which is also a data race.
- There is no bot reservation.
- There are zero tests and no Dockerfile.

The doc ends with a prioritized 12-item backlog and lists the session notes and skill references that call go-dota2 APIs that do not exist.

### Gap follow-ups (second pass, same day)

These close or narrow the open questions left by 01-08. Each one says what is still open.

- **[gap-01 - Upstream master build and auth](gap-01-upstream-master-build-auth.md).** The go-steam and go-dota2 masters build together (same go-steam commit; `go 1.26.0`, no `toolchain` line). Every plan snippet compiles and vets after mechanical porting (protobuf-go-lite: `proto.String(x)` becomes `new(x)`, `proto.Unmarshal` becomes `UnmarshalVT`; `LogOn(ctx, ...)`). The one semantic change: master `MakeRequest` matches replies by **GC job ID**, so 7633/7055 calls complete only if the GC echoes our job ID (REPORTED by node-dota2/dota-ihl-bot code, not tested). Also: register handlers before connect, `Dota2.Close()` is terminal, `Emit` blocks. No renewal helper, no `LoggedInElsewhere` handling and no EResult 121 in go-steam master. A CM `GenerateAccessTokenForApp` sketch compiles. **Open:** a live token/TOTP login on master.
- **[gap-02 - under-5k-league live prior art](gap-02-under5k-live-prior-art.md).** An empty `CMsgPracticeLobbyLaunch` (the same bytes as go-dota2's `LaunchLobby()`) still reached RUN after the 2026-09-25 nonce change: match 9050201642 (2026-10-18, league 20004). So the nonce is optional for now. under-5k sends TV delay 1 believing it is 120 s (it is 60 s now) and never reads it back. It uses read-back `leagueid` as the authority and logs non-OK 7055s, but never saw a rejection and never calls 7633. Its refresh-token login works and the token was portable across hosts.
- **[gap-03 - League portal and Web API](gap-03-league-portal-webapi.md).** No second JSON source for "leagues this bot administers" exists. The dota2.com league portal is server-rendered PHP behind an OAuth-cookie login, and no shipped bundle calls an admin-scoped league method. Node methods live under `IDOTA2DPC` (`GetLeagueNodeData`, `GetLeagueNodeGroupData`); an empty `text/html` 200 on `IDOTA2League` means "unknown method". Public league lists contain only status 3/5. GC 7633→7634 stays the only goal-4 source.
- **[gap-04 - Proto freshness audit](gap-04-proto-freshness-audit.md).** Vendored protos = GameTracking `ab1f047d` (2025-06-17). Across 2,255 shared enum symbols, `LobbyDotaTVDelay` is the only renumbering (`c592bf50`, 2026-08-12). Everything the league check and seating logic use is unchanged; other changes only add things (`nonce`, `CSODOTALobby` field 104, `LobbyType` 16). The vendored decoder silently mislabels TV-delay read-backs. Region IDs are only confirmed from a 2023 `regions.txt`.
- **[gap-05 - 7055 decode and league rejection](gap-05-7055-decode-and-league-rejection.md).** 7055 = `CMsgGenericResult` (`eresult` EResult, 1 = OK; `debug_message`). The create reply is job-addressed, and go-dota2 never sets a source job, so fail-fast needs our own job-addressed send. No client handles popup 27 or checks the `leagueid` read-back, and nobody has recorded what the GC does with a non-admin league. **Open:** the rejection signal.

Commit-ID note: gap-02 and 03 §8.2 cite SteamDatabase/**Protobufs** SHAs (`c983b3e16435` renumbering, `623ebf57b33a` nonce). gap-04 cites the same changes in SteamDatabase/**GameTracking-Dota2** (`c592bf50`, `e84714d6`). Both are correct for their repos.

---

## 2. Key facts

Rows marked *(gap)* were added or changed by the gap follow-ups.

| # | Fact | Confidence | Source |
|---|---|---|---|
| 1 | No GC session is ever started in this repo: `SetPlaying(true)` / `SayHello()` are never called (`pkg/bot/manager.go:86-99`) | VERIFIED | 01, 03, 08 |
| 2 | `dota2.New` is called on every `LoggedOnEvent` and appends a GC packet handler that is never removed (unlocked slice, data race). On master the documented contract is "register before connect" | VERIFIED | 03, 07, 08, gap-01 |
| 3 | `POST /lobby/create` and `/lobby/info` are placeholders | VERIFIED | 08 |
| 4 | Pinned go-steam has no `access_token` (field 108) and no IAuthenticationService; logon is legacy password only | VERIFIED | 04, 08 |
| 5 | Legacy password logon has been rejected (`InvalidPassword`) since about June 2026 | LIKELY (PR #11 thread, go-dota2 #78; not live-tested; no counter-evidence in gap-02) | 03, 04, gap-01 |
| 6 | *(gap)* go-steam master `336e824f8540` + go-dota2 master `6d32dc38d075` resolve together, need `go 1.26.0`, and every plan snippet compiles and vets after mechanical porting; upstream unit tests pass | VERIFIED (built in `/tmp/gap01`) | gap-01 |
| 7 | *(gap)* Master has `Authenticate` (password + `TwoFactorCode`), `LogOn(ctx, {AccessToken})` writing field 108, and `PlayingSessionStateEvent`. No public report of a token or TOTP login on the 2026-09-22 master | VERIFIED (source) / UNCERTAIN (live) | gap-01 |
| 8 | *(gap)* Master `MakeRequest` matches replies by **GC job ID**, not type. A reply that does not echo `job_id_source` times out and surfaces as `UnhandledGCPacket` | VERIFIED (source) | gap-01 |
| 9 | *(gap)* The GC echoes job IDs on 7634 and on the create 7055 | REPORTED (dota-ihl-bot / node-dota2 depend on it; node-dota2 #513 stack trace) | gap-01, gap-05 |
| 10 | *(gap)* On master, `Dota2.Close()` is terminal, handler registration is unlocked, and `Emit` blocks; build a fresh steam+Dota2 pair per connection and never `MakeRequest` on the event goroutine | VERIFIED | gap-01 |
| 11 | *(gap)* go-steam master has no token-renewal helper, no `LoggedInElsewhere` handling and no `InvalidSignature` (121) enum value; a CM `GenerateAccessTokenForApp` sketch (EMsg 151) compiles | VERIFIED (source, compile) / UNCERTAIN (live) | gap-01 |
| 12 | *(gap)* A SteamClient refresh token copied to another host logged in with no new sign-in | REPORTED (under-5k, node steam-session) | gap-02 |
| 13 | One account hosts at most one lobby; N concurrent lobbies need N bot accounts | VERIFIED (SOCache) + consensus of prior art | 01, 05 |
| 14 | League lobby = `CMsgPracticeLobbySetDetails.leagueid` (16), optional `league_node_id` (49); read back from `CSODOTALobby.leagueid` (42) | VERIFIED | 01, 02, 08 |
| 15 | Hosting a league lobby requires the bot account to be an admin of the league on dota2.com/league | VERIFIED (node-dota2 README/#414, IESF guide, `NotLeagueAdmin` string) | 02, 05, gap-05 |
| 16 | 7633 → 7634 returns `CMsgDOTALeagueInfoList` = leagues the account administers | LIKELY (two independent clients; dota-ihl-bot in production ~2019; under-5k does not use it; not live-tested) | 02, 05, gap-02 |
| 17 | 7634 is the same list that fills the client's lobby "League" drop-down | INFERRED (weak hint: Valve's own UIs filter league lists by date and tier) | 02, 05, gap-03 |
| 18 | One bot can administer and host several leagues; the choice is per lobby | LIKELY (repeated field; dota-ihl-bot wiki; under-5k US+EU) | 02, 05 |
| 19 | go-dota2 has no wrapper for 7633/7634; `MakeRequest` is exported and works for it (pinned and master) | VERIFIED (compiles) | 02, 03, 05, gap-01 |
| 20 | *(gap)* No other programmatic source of "leagues this account administers" exists: the dota2.com league portal is login-gated HTML; no bundle calls an admin-scoped method; public lists hold only status 3/5 | VERIFIED | gap-03 |
| 21 | `GetProfileTickets` lists owned league passes, not admin rights | VERIFIED (it is not the mechanism) / INFERRED (exact semantics) | 02, 08 |
| 22 | Public webapi `GetLeagueData` returns `admins: []`; `GetLeagueListing` is 404; league node data is `IDOTA2DPC/GetLeagueNodeData?league_id&node_id` | VERIFIED (live calls) | 02, gap-03 |
| 23 | Create is fire-and-forget; success = SO type 2004 appears; `LeaveCreateLobby` hangs until ctx on rejection | VERIFIED | 01, 03 |
| 24 | *(gap)* 7055 decodes as `CMsgGenericResult` (`eresult` = Steam EResult, 1 = OK, 2 = Fail; `debug_message` field 2). Not every lobby op replies with 7055 | VERIFIED (protos) + REPORTED (dota-ihl-bot, node-dota2 #506/#654) | gap-05 |
| 25 | *(gap)* go-dota2 never sets a source job ID (`client.go:116-117`); whether the GC sends 7055 at all without one is unknown | VERIFIED / UNCERTAIN | gap-05 |
| 26 | *(gap)* No client handles popup `NOT_LEAGUE_ADMIN` (27) or checks the `leagueid` read-back; the GC's reaction to a non-admin league is unrecorded | VERIFIED (absence) / UNCERTAIN (behaviour) | gap-02, gap-05 |
| 27 | Pinned `MakeRequest` correlates by response message type only; serialize same-type requests per bot | VERIFIED | 01, 03, 06 |
| 28 | go-dota2 events arrive on `steamClient.Events()` (buffer 30, blocking); blocking that loop on `MakeRequest` can deadlock the read loop | VERIFIED (by reading; same on master) | 03, 07, 08, gap-01 |
| 29 | `ClientStateChanged` is emitted by value; other events by pointer | VERIFIED | 01, 03, 08 |
| 30 | SOCache: keyed by owner SOID, only `object_data[0]` decoded, never cleared on GC loss, 10-slot lossy subscriptions; `state.Lobby` never set | VERIFIED | 03, 06 |
| 31 | *(gap)* `LobbyDotaTVDelay` is the **only** renumbered enum (GameTracking `c592bf50`, 2026-08-12): current `10=0, 60=1, 120=2, 300=3, 900=4`. The vendored `_120` sends 1 = 60 s and read-backs are mislabelled | VERIFIED (history + local probe) | 03, gap-04 |
| 32 | *(gap)* Everything the league check and seat logic use (`PopupID`, `DOTAJoinLobbyResult`, `CSODOTALobby.State`, `EMatchOutcome`, `DOTA_GC_TEAM`, `ELeague*`, `CMsgDOTALeagueInfo*`, all `SetDetails` fields, IDs 7055/7633/7634/7650/7651) is unchanged through 2026-11-05 | VERIFIED | gap-04 |
| 33 | *(gap)* `CMsgPracticeLobbyLaunch.nonce = 6` was added 2026-09-25, but an empty launch body still reached RUN in Oct 2026 (match 9050201642, league 20004) | VERIFIED (field, match record) / INFERRED (that the bot's own launch started it) | 03, gap-02, gap-04 |
| 34 | The host bot must not sit in a Radiant/Dire slot at launch; caster slot vs unassigned pool is contested | REPORTED / UNCERTAIN | 01, 06 |
| 35 | GC ignores Leave while lobby is `RUN`; lobby `game_state` jumps 1 → 6 without a resync | REPORTED (under-5k, Oct 2026) | 05, 06, gap-02 |
| 36 | `match_outcome` 2..13 = scored winner, 64..69 = not scored | VERIFIED (enum) + REPORTED (usage) | 06 |
| 37 | Steam Web API returns only ticketed matches, and not always | REPORTED | 06 |
| 38 | A failed TCP dial emits only `FatalErrorEvent` (no `DisconnectedEvent`) and the bot never reconnects | VERIFIED | 07, 08 |
| 39 | A logon failure emits `LogOnFailedEvent` **and** `DisconnectedEvent`; the manager schedules both, and timers accumulate | VERIFIED | 04, 08 |
| 40 | `sentry_hash` is never loaded into `BotConfig.SentryHash` (no `mapstructure` tag) and never passed to `LogOn` | VERIFIED (local probe) | 07, 08 |
| 41 | `config.Secret` leaks under `%#v`; `Secret.MarshalJSON` always redacts (so it must not be used in persisted structs) | VERIFIED (ran it) | 07 |
| 42 | Same-IP duplicate logon → `LogonSessionReplaced` (34); cross-host → `LoggedInElsewhere` (6) on the playing session | INFERRED for go-steam (master only surfaces it as `LoggedOffEvent`) | 07, gap-01 |
| 43 | go-steam is TCP-only (27017); GC traffic is tunnelled in the CM connection | VERIFIED | 07 |
| 44 | Refresh tokens last about 200 days; dead token on logon = `InvalidPassword`/`AccessDenied`/`Revoked`/`Expired`/`InvalidSignature` (121, compare as `steamlang.EResult(121)`) | LIKELY | 04, gap-01 |
| 45 | Steam Guard TOTP = HMAC-SHA1(base64(shared_secret), t/30), 5 chars from `23456789BCDFGHJKMNPQRTVWXY` | VERIFIED (several implementations) | 04 |
| 46 | Per-account GC limits around 100 match-details per 24 h, 500 per IP | REPORTED (ValvePython docs, OpenDota) | 05 |
| 47 | Zero tests, no Dockerfile; `go build` / `go vet` pass | VERIFIED | 08 |
| 48 | Session notes and skill references use non-existent APIs (`Events()`, `GetState()`, `client.On`, `e.Msg`, `InviteToLobby`, `SetLobbyTeamSlot`) | VERIFIED | 01, 03, 08 |
| 49 | *(gap)* Server region IDs are confirmed only from the 2023 `regions.txt`; `4 KOREA` is absent from it and the Warsaw ID is unknown | VERIFIED (2023) / UNCERTAIN (2026) | 01, gap-04 |

---

## 3. Open questions and risks, ranked by impact

The ranking reflects how much each item blocks the four user goals: (1) API-managed lobbies, (2) many headless bots, (3) league drop-downs, possibly several per bot, and (4) listing tickets. Each item names the live test that would settle it. Most can share one test rig: two bot accounts (each with a mobile authenticator), two test leagues (one accepted, one unsubmitted), and a bogus league ID.

**Q numbers are stable IDs** (the plan cites them). The gap pass re-ranked the list and added Q18-Q20, so the order below no longer follows the numbers.

### Tier 1: blocks a goal outright

- **Q1. Auth: does a token login work on upstream master?** The legacy pinned logon is LIKELY dead. Building on master is now VERIFIED (gap-01), so the remaining step is one live run: `Authenticate` with password + TOTP, then `LogOn(ctx, {AccessToken})`, then `SetPlaying` + `SayHello` → welcome, then a restart with the stored token. gap-01 §7 has the procedure. Testing the pinned legacy logon first is optional. (04, gap-01)
- **Q18. Job-ID echo on master** *(new, gap-01)*: do 7634 and the create 7055 carry `job_id_target` = our `job_id_source`? If not, master `MakeRequest` never completes for them and we fall back to a `GCPacketHandler` tap matched by type. REPORTED yes by two node bots. Test: log `TargetJobId` on a tap while calling `MakeRequest(7633)`. Run it together with Q2.
- **Q2. League listing (goal 4): does 7633 still return `CMsgDOTALeagueInfoList` on today's GC?** Also: is there any reply when the account administers nothing (an empty list or a timeout)? No prior art used it after 2019 (under-5k uses static config), and no other source exists (gap-03). Test: `MakeRequest(7633→7634)` from a bot that is admin of two leagues and from one that is admin of none. (02, 05, gap-02, gap-03)
- **Q3. League drop-down (goal 3): does the 7634 list match the client's League drop-down?** The list may be filtered by status (unsubmitted, rejected, concluded) or date window; Valve's own league UIs do filter by date and tier (gap-03, weak hint). Test: open the lobby drop-down in a real client and compare it with 7634 for the same account. Optionally capture with NetHook2. (02, 05, gap-03)
- **Q4. League rejection: how does the GC reject a `leagueid` the host does not administer?** Popup 27, a silent reset of `leagueid` to 0, or a non-OK 7055. Still UNRESOLVED after gap-02 and gap-05; nobody has recorded it. Until it is known, the read-back is authoritative (plan D3). Test: create with a good, a non-admin and a bogus league ID, each sent **with** a source job, and record the popup, the raw 7055 (`eresult`, `debug_message`) and the read-back. (01, 02, gap-02, gap-05)
- **Q5. Host seat at launch: which seat lets the headless host launch without the server waiting for it to load?** Candidates: a self-kick to the pool, `PLAYER_POOL`, `SPECTATOR`, or the caster slot. Test: launch with each and check for `RUN`. (01, 06)

### Tier 2: correctness and reliability of the core loop

- **Q6. Proto staleness on the wire.** *Nonce:* resolved enough to proceed. An empty launch body reached RUN in Oct 2026 (gap-02); keep a launch-to-RUN timeout as a guard. *TV delay:* send current wire value 2 for 120 s and read back `CSODOTALobby.dota_tv_delay` (field 53) once live. No fork is needed for either: the adapter sends raw integers. (03, gap-02, gap-04)
- **Q19. Is 60 s TV delay allowed for non-league lobbies?** *(new, gap-04)* The UI string still says non-league games are limited to two minutes. Test: create non-league lobbies with wire values 0 and 1 and read back field 53. (gap-04)
- **Q7. Create result signal.** Decode type and success value are settled (`CMsgGenericResult`, `eresult == 1`, gap-05). Still open: does the GC send 7055 at all when the create has no source job (go-dota2's default)? Test: capture 7055 for creates with and without a source job; keep the bodies as golden files. (01, 03, gap-05)
- **Q8. Result capture for a host that stays out of the game:** Is `match_outcome` reliably present at POSTGAME for ticketed lobbies, and how quickly? Does `RequestMatchDetails` work for a host that did not play? Test: one ticketed and one unticketed match. (06)
- **Q9. Stale lobby state:** the SOCache is never cleared when the GC drops, and its event stream loses events. under-5k saw a finished game stuck in RUN. A forced resync (`SetPlaying(false)` then `(true)`) is INFERRED to replay the welcome. (03, 05, 06)
- **Q10. Duplicate sessions and replicas:** same IP gives `LogonSessionReplaced`, different hosts give `LoggedInElsewhere`. go-steam master only surfaces these as `LoggedOffEvent{Result}`. Run a single replica plus a lock until tested. (07, gap-01)
- **Q11. Rate limits:** logon, GC request and lobby-create thresholds are unpublished. Stagger logins and pace GC requests. (04, 05, 07)

### Tier 3: operational and secondary

- **Q12. Token renewal from Go:** no go-steam helper; a CM `GenerateAccessTokenForApp` sketch compiles (gap-01 §3.1) but is untested. Fallback that needs no renewal: re-run `Authenticate` with password + TOTP before the roughly 200-day expiry. (04, gap-01)
- **Q13. Account prerequisites:** can limited accounts send invites or be league admins? Does the GC require an explicit app-570 licence before it accepts the hello? (04, 05)
- **Q14. Leaving early to free a bot:** does Abandon then Leave work without penalties? Treat it as not working. (05, 06)
- **Q15. Protocol details:** whether `SetLobbyDetails` is a partial or full update, the meaning of `member_indices`, whether `Get(lobbyID)` equals the owner SOID, current `server_region` IDs (2023 table in 01 §7.10; Korea and Warsaw unknown), and the `series_type` values. (01, gap-04)
- **Q16. League admin timing:** how quickly a newly added web admin appears in 7634, and what the 7434 `LeagueAdminList` push means. (02)
- **Q17. Weekly maintenance:** the exact Tuesday window and how long it lasts. (07)
- **Q20. Portal scrape fallback** *(new, gap-03)*: would a bot's SteamClient access token be accepted as the dota2.com `dota_oauth_token` cookie, letting us scrape `/league/0/list`? Do not build or test this unless 7634 is broken, and only with explicit operator approval, since it uses a real credential. (gap-03)
