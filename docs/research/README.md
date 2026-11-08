# Research index: dota_lobby

Research for the `dota_lobby` service, a Go REST API that creates and manages Dota 2 practice and league lobbies through headless Steam bot accounts, built on `paralin/go-steam` and `paralin/go-dota2`. The desk research was done on 2026-11-06 and 2026-11-07; a live run against Steam and the GC followed on 2026-11-08 ([09](09-live-probe.md)).

Module versions:

- **Studied as the starting point** (the pre-PR pin): go-dota2 `v0.0.0-20250623204622-532cb1851217`, go-steam `v0.0.0-20250502043548-f167ff28a93a`.
- **Upstream masters, used by the service and the live run**: go-steam `v0.0.0-20260922075330-336e824f8540` and go-dota2 `v0.0.0-20260922075533-6d32dc38d075`. They resolve together and need `go 1.26.0` ([gap-01](gap-01-upstream-master-build-auth.md)).

Source paths in the docs are module-relative, for example `go-dota2@6d32dc38d075/client.go:148` or `go-steam@f167ff28a93a/auth.go:22`.

Confidence labels used across the docs:

- **VERIFIED** (or **[V]**): read in source, in a primary document, or reproduced. **VERIFIED live** means seen in the 2026-11-08 run.
- **REPORTED** (or **[R]**): field experience from another bot or library. Credible, but not Valve documentation.
- **LIKELY**: strong secondary evidence, not live-tested.
- **INFERRED** / **UNCERTAIN**: reasoned from evidence, or the sources conflict.

---

## 1. Documents

### [01 - Lobby lifecycle protobufs](01-lobby-protocol.md)

Every `CMsgPracticeLobbySetDetails`, `CSODOTALobby` and `CSODOTALobbyMember` field, the lobby enums, a message-ID map, every lobby/chat/team wrapper with file:line refs, and the lifecycle from create to destroy. Key points:

- Create is fire-and-forget: success is a `CSODOTALobby` (SO 2004) in the SOCache. The GC also replies 7055 `CMsgGenericResult`, `eresult` 1 on success, with our job ID echoed (VERIFIED live). 7055 is shared by create, kick, launch and leave.
- `LeaveCreateLobby` blocks until its ctx expires when the GC rejects a create.
- A league lobby is `leagueid` (field 16), optionally `league_node_id` (49). One account sits in at most one lobby.
- The host cannot seat a member in a slot; it can kick to the pool, kick out, flip, shuffle, clear a team or add AI bots. Kick wrappers take 32-bit account IDs.
- Lobby chat is `Lobby_<lobby_id>`; its join reply does not echo the job ID (VERIFIED live).

### [02 - League tickets and the lobby drop-down](02-league-tickets.md)

A "league ticket" today is a league ID the bot account administers (dota2.com/league, **Admins**). The GC lists them through 7633 → 7634 `CMsgDOTALeagueInfoList` (VERIFIED live). go-dota2 has no wrapper; a 10-line `MakeRequest` call fills the gap. `GetProfileTickets` lists owned league passes, which grant nothing. The public `IDOTA2League` web API blanks the admins list, and `GetLeagueListing` is gone. `CSODOTAGameAccountClient.is_league_admin` is a cheap "any league?" check (VERIFIED live).

### [03 - go-dota2 client internals, SOCache, staleness](03-go-dota2-internals.md)

- Events arrive on `steamClient.Events()`, a 30-slot channel whose `Emit` blocks; blocking that loop on a GC round-trip deadlocks the read loop.
- The SOCache stores objects under the owner SOID, decodes only `object_data[0]`, is never cleared when the GC session drops, and drops events from full 10-slot subscriber channels. `Dota2State.Lobby` is never set.
- Pinned `MakeRequest` matches by message type; master matches by job ID.
- Even master's protos are stale: `LobbyDotaTVDelay` was renumbered on 2026-08-12 and `CMsgPracticeLobbyLaunch` gained `nonce` on 2026-09-25. Upstream's proto auto-update has failed since 2026-04-19. Sending raw TV-delay integers and an empty launch body avoids a fork.

### [04 - Headless Steam auth for many bots](04-steam-auth-headless.md)

The pinned go-steam only has the legacy password logon, which Steam has rejected since about June 2026. Master (`6b06e91640b1` and later) adds `Auth.Authenticate`, `LogOnDetails.AccessToken` and `PlayingSessionStateEvent`. Design: a mobile authenticator per bot with TOTP from `shared_secret`, `Authenticate` once, keep the refresh token (about 210 days, VERIFIED live) on a 0600 volume, log in with the token, only ever Disconnect, classify each `EResult`, and run one reconnect owner per bot with backoff and jitter.

### [05 - Patterns used by existing lobby bots](05-prior-art.md)

Reusable patterns (league-aware pool, durable claims, idempotency keys, per-bot GC queues, verify-after-create, invite policy, seat enforcement, resync) and known pitfalls (Leave ignored during `RUN`, stale `RUN` lobbies, callbacks that never fire), with rate-limit reports.

### [06 - Match tracking and results](06-match-results.md)

The host bot's `CSODOTALobby` update is the fastest result: `state` goes UI → SERVERASSIGN/SERVERSETUP → RUN → POSTGAME, `match_id` (30) is set at launch, and `match_outcome` (70) holds the result (2..13 winner, 64..69 not scored). Fallbacks: GC `RequestMatchDetails`, the Web API (ticketed only), OpenDota (`/leagues/{id}/matchIds` for amateur leagues), STRATZ, and a replay or manual import. Also a pure `derive()` phase function, timeouts, and a signed, idempotent webhook catalogue. Untested live (Q8).

### [07 - Docker / headless deployment and ops](07-deployment.md)

Multi-stage CGO-off build on `distroless/static:nonroot`; layered config (`config.yaml`, `secrets.yaml`, `DOTA_LOBBY_*` env, `*_FILE`); a per-bot state volume; `/livez` independent of Steam and `/readyz` on GC `HAVE_SESSION`; a drain that destroys only `UI` lobbies; Prometheus on a separate port; egress on TCP 27017 and 443; exactly one process per account set. `Secret` leaks under `%#v`, and `Secret.MarshalJSON` would corrupt a persisted token.

### [09 - Live probe results (2026-11-08)](09-live-probe.md)

One bot account, one test league. Answers Q1, Q2, Q18, Q21, Q7 (with a source job), Q6 for league lobbies, the success side of Q4, part of Q15, invites and lobby chat. Lists what is still open.

### [10 - Headless bot accounts](10-headless-bot-accounts.md)

Account creation (the human part, once per bot), adding a mobile authenticator with steamguard-cli and no phone, zero-touch TOTP login on go-steam master (VERIFIED live), never running Dota 2 (VERIFIED live), limited accounts, ToS risk, rate limits and a from-scratch runbook.

### Gap follow-ups

- **[gap-01 - Upstream master build and auth](gap-01-upstream-master-build-auth.md).** The masters build together; every research snippet compiles after mechanical porting (protobuf-go-lite, `LogOn(ctx, ...)`). Master `MakeRequest` matches by GC job ID, which the GC echoes on 7634 and 7055 but not 7010 (VERIFIED live). Register handlers before connect, `Dota2.Close()` is terminal, `Emit` blocks. No renewal helper; a CM `GenerateAccessTokenForApp` sketch compiles (Q12).
- **[gap-03 - League portal and Web API](gap-03-league-portal-webapi.md).** No second source for "leagues this bot administers": the portal is login-gated HTML and no bundle calls an admin-scoped method. Node data lives under `IDOTA2DPC`. GC 7633 stays the only source.
- **[gap-04 - Proto freshness audit](gap-04-proto-freshness-audit.md).** Vendored protos = GameTracking `ab1f047d` (2025-06-17). `LobbyDotaTVDelay` is the only renumbering across 2,255 shared enum symbols (`c592bf50`, 2026-08-12); everything the league check and seating use is unchanged. Region IDs are confirmed only from 2023.
- **[gap-05 - 7055 decode and league rejection](gap-05-7055-decode-and-league-rejection.md).** 7055 = `CMsgGenericResult` (`eresult`, 1 = OK; `debug_message`), job-addressed. go-dota2 never sets a source job, so fail-fast needs our own job-addressed send. The GC's reaction to a non-admin league is still unrecorded (Q4).

Commit-ID note: some docs cite SteamDatabase/**Protobufs** SHAs (`c983b3e16435` renumbering, `623ebf57b33a` nonce), gap-04 the same changes in SteamDatabase/**GameTracking-Dota2** (`c592bf50`, `e84714d6`). Both are correct for their repos.

---

## 2. Key facts

| # | Fact | Confidence | Source |
|---|---|---|---|
| 1 | Legacy password logon has been rejected (`InvalidPassword`) since about June 2026 | LIKELY (go-steam PR #11, go-dota2 #78) | 04, gap-01 |
| 2 | go-steam master `336e824f8540` + go-dota2 master `6d32dc38d075` resolve together, need `go 1.26.0`, and pass their unit tests | VERIFIED | gap-01 |
| 3 | Password + TOTP `Authenticate` returns a refresh token with no prompt; JWT `aud` `client, web, renew, derive`, about 210-day lifetime | VERIFIED live | 09, 04 |
| 4 | Token-only `LogOn` reaches the GC welcome in 1.7-7.4 s, repeatedly and across restarts; the GC may pass through `NO_SESSION_IN_LOGON_QUEUE` first | VERIFIED live | 09 |
| 5 | An account that never launched Dota 2 gets a GC session and can host league lobbies | VERIFIED live | 09, 10 |
| 6 | Master `MakeRequest` matches replies by GC job ID; the GC echoes it on 7634, 7651 and the create 7055, not on the chat join reply 7010 | VERIFIED (source) + VERIFIED live | gap-01, 09 |
| 7 | On master, `Dota2.Close()` is terminal, handler registration is unlocked, and `Emit` blocks; build a fresh steam+Dota2 pair per connection and never `MakeRequest` on the event goroutine | VERIFIED | gap-01 |
| 8 | go-steam master has no token-renewal helper, no `LoggedInElsewhere` handling and no `InvalidSignature` (121); a CM `GenerateAccessTokenForApp` sketch compiles | VERIFIED (source, compile) / UNCERTAIN (live) | gap-01 |
| 9 | A SteamClient refresh token copied to another host logs in with no new sign-in | REPORTED | 04 |
| 10 | One account hosts at most one lobby; N concurrent lobbies need N bot accounts | VERIFIED (SOCache) + consensus of existing bots | 01, 05 |
| 11 | League lobby = `leagueid` (16), optional `league_node_id` (49); read back from `CSODOTALobby.leagueid` (42). An admin bot's league create is accepted and reads back the requested league | VERIFIED live | 01, 02, 09 |
| 12 | Hosting a league lobby requires league admin rights on dota2.com/league | VERIFIED (node-dota2 README, IESF guide, `NotLeagueAdmin` string) | 02, gap-05 |
| 13 | 7633 → 7634 returns `CMsgDOTALeagueInfoList` = leagues the account administers | VERIFIED live (one league) | 02, 09 |
| 14 | 7634 is the same list that fills the client's lobby "League" drop-down | INFERRED | 02, gap-03 |
| 15 | One bot can administer and host several leagues; the choice is per lobby | LIKELY (repeated field; not live-tested) | 02 |
| 16 | No other programmatic source of "leagues this account administers" exists | VERIFIED | gap-03 |
| 17 | `CSODOTAGameAccountClient.is_league_admin` is `true` for an admin account | VERIFIED live | 02, 09 |
| 18 | A league without a bracket returns an empty `RequestLeagueAvailableLobbyNodes` reply | VERIFIED live | 02, 09 |
| 19 | Public webapi `GetLeagueData` returns `admins: []`; `GetLeagueListing` is 404; node data is `IDOTA2DPC/GetLeagueNodeData` | VERIFIED (live calls) | 02, gap-03 |
| 20 | Create is fire-and-forget; success = SO 2004 appears; `LeaveCreateLobby` hangs until ctx on rejection | VERIFIED | 01, 03 |
| 21 | 7055 decodes as `CMsgGenericResult`; a successful create returns `eresult` 1 in 0.4-1.1 s with our source job echoed | VERIFIED live | gap-05, 09 |
| 22 | go-dota2 never sets a source job ID; whether the GC sends 7055 without one is unknown | VERIFIED / UNCERTAIN | gap-05 |
| 23 | The GC's reaction to a non-admin league (popup 27, `leagueid` reset to 0, or a non-OK 7055) is unrecorded | UNCERTAIN | gap-05 |
| 24 | `game_mode` set on create reads back; `SetLobbyDetails` must resend the full details set | VERIFIED live | 01, 09 |
| 25 | Invites to non-friends work (`InvitationCreated`, player joins); there is no GC message to cancel a lobby invite | VERIFIED live / VERIFIED (protos) | 01, 09 |
| 26 | Lobby chat channel `Lobby_<lobby_id>` works; messages are pushed; `!` text arrives, `/` text is consumed by the client | VERIFIED live | 01, 09 |
| 27 | Pinned `MakeRequest` correlates by response message type only; serialize same-type requests per bot | VERIFIED | 01, 03, 06 |
| 28 | Events arrive on `steamClient.Events()` (buffer 30, blocking); blocking that loop on `MakeRequest` can deadlock the read loop | VERIFIED (pinned and master) | 03, gap-01 |
| 29 | SOCache: keyed by owner SOID, only `object_data[0]` decoded, never cleared on GC loss, lossy 10-slot subscriptions; `state.Lobby` never set | VERIFIED | 03, 06 |
| 30 | `LobbyDotaTVDelay` is the only renumbered enum: current `10=0, 60=1, 120=2, 300=3, 900=4`. A league lobby's default reads back 2 = 120 s | VERIFIED (history) + VERIFIED live | 03, gap-04, 09 |
| 31 | Everything the league check and seat logic use is unchanged through 2026-11-05 | VERIFIED | gap-04 |
| 32 | `CMsgPracticeLobbyLaunch.nonce = 6` was added 2026-09-25, but an empty launch body still reached `RUN` in Oct 2026 | VERIFIED (field) / REPORTED (launch) | 03, gap-04 |
| 33 | The host bot must not sit in a Radiant/Dire slot at launch; caster slot vs unassigned pool is contested | REPORTED / UNCERTAIN | 01, 06 |
| 34 | The GC ignores Leave while the lobby is `RUN`; lobby `game_state` jumps 1 → 6 without a resync | REPORTED (Oct 2026) | 05, 06 |
| 35 | `match_outcome` 2..13 = scored winner, 64..69 = not scored | VERIFIED (enum) + REPORTED (usage) | 06 |
| 36 | The Steam Web API returns only ticketed matches, and not always | REPORTED | 06 |
| 37 | A failed TCP dial emits only `FatalErrorEvent` (no `DisconnectedEvent`) | VERIFIED | 07 |
| 38 | `config.Secret` leaks under `%#v`; `Secret.MarshalJSON` always redacts, so it must not be used in persisted structs | VERIFIED (ran it) | 07 |
| 39 | Same-IP duplicate logon → `LogonSessionReplaced` (34); cross-host → `LoggedInElsewhere` (6) on the playing session | INFERRED for go-steam | 07, gap-01 |
| 40 | go-steam is TCP-only (27017); GC traffic is tunnelled in the CM connection | VERIFIED | 07 |
| 41 | A dead token on logon = `InvalidPassword`/`AccessDenied`/`Revoked`/`Expired`/`InvalidSignature` (121, compare as `steamlang.EResult(121)`) | LIKELY | 04, gap-01 |
| 42 | Steam Guard TOTP = HMAC-SHA1(base64(shared_secret), t/30), 5 chars from `23456789BCDFGHJKMNPQRTVWXY` | VERIFIED (several implementations; live login) | 04 |
| 43 | Per-account GC limits around 100 match-details per 24 h, 500 per IP | REPORTED (ValvePython docs, OpenDota) | 05 |
| 44 | Server region IDs are confirmed only from the 2023 `regions.txt`; `4 KOREA` is absent and the Warsaw ID is unknown | VERIFIED (2023) / UNCERTAIN (2026) | 01, gap-04 |

---

## 3. Questions

**Q numbers are stable IDs**: `tools/probe` tags its report lines with them. The order below is by impact, not by number. The four goals they are ranked against: (1) API-managed lobbies, (2) many headless bots, (3) league drop-downs, possibly several per bot, and (4) listing tickets.

### Answered by the live run ([09](09-live-probe.md))

- **Q1. Token login on upstream master:** yes. `Authenticate` with password + TOTP, then token-only `LogOn`, `SetPlaying`, `SayHello` → welcome, also after restarts ([09 §2](09-live-probe.md#2-q1-token-auth-and-login)).
- **Q21. Account that never launched Dota 2:** gets a GC session ([09 §3](09-live-probe.md#3-q21-an-account-that-never-launched-dota-2)).
- **Q2. League listing (goal 4):** 7633 returns the admin list ([09 §4](09-live-probe.md#4-q2-and-q18-league-listing-and-job-id-echo)). Not yet tested: an account that administers nothing.
- **Q18. Job-ID echo on master:** 7634 and the create 7055 echo it, so master `MakeRequest` works; 7010 does not ([09 §4](09-live-probe.md#4-q2-and-q18-league-listing-and-job-id-echo), [§9](09-live-probe.md#9-lobby-chat)).
- **Q7. Create result signal:** 7055 `CMsgGenericResult{eresult: 1}` when the create carries a source job ([09 §5](09-live-probe.md#5-q4-and-q7-league-lobby-create-and-the-7055-reply)). Not yet tested: a create without a source job.
- **Q6. Proto staleness on the wire:** a league lobby's default TV delay reads back wire 2 = 120 s ([09 §6](09-live-probe.md#6-q6-tv-delay-on-a-league-lobby)). The nonce is REPORTED optional; the first live launch (Q5) will confirm.
- **Lobby chat:** `Lobby_<lobby_id>` with `DOTAChannelType_Lobby`; match the join reply by channel name; use `!` commands ([09 §9](09-live-probe.md#9-lobby-chat)).
- **Invites:** non-friend invites work; there is no invite cancel ([09 §8](09-live-probe.md#8-invites)).

### Tier 1: blocks a goal outright

- **Q4. League rejection:** how does the GC reject a `leagueid` the host does not administer? Popup 27, a silent reset of `leagueid` to 0, or a non-OK 7055. The accepted case is VERIFIED live. Until the rejection is known, the read-back is authoritative. Test: create with a non-admin and a bogus league ID, each with a source job, and record the popup, the raw 7055 and the read-back. (01, 02, gap-05)
- **Q5. Host seat at launch:** which seat lets the headless host launch without the server waiting for it to load? Candidates: a self-kick to the pool, `PLAYER_POOL`, `SPECTATOR`, or the caster slot. Test: launch with each and check for `RUN`. This also confirms the empty launch body (nonce). (01, 03, 06)
- **Q3. League drop-down (goal 3):** does the 7634 list match the client's League drop-down? It may be filtered by status or date window. Test: open the drop-down in a real client and compare with 7634 for the same account. (02, gap-03)
- **Q8. Result capture for a host that stays out of the game:** is `match_outcome` reliably present at POSTGAME, how quickly, and does `RequestMatchDetails` work for a host that did not play? Test: one ticketed and one unticketed match. (06)

### Tier 2: correctness and reliability of the core loop

- **Seat enforcement:** does kicking a misplaced member back to the pool with `KickLobbyMemberFromTeam` work from a headless host? Not exercised yet. (01, 05)
- **Q19. 60 s TV delay for non-league lobbies?** The UI string says non-league games are limited to two minutes. Test: non-league lobbies with wire 0 and 1, read back field 53. (02, gap-04)
- **Q22. Limited accounts and lobby invites:** do invites from a limited (unfunded) account arrive? Fund every bot until tested. (04, 10)
- **Q9. Stale lobby state:** the SOCache is never cleared when the GC drops and its event stream loses events; a finished game stuck in `RUN` is REPORTED. A forced resync (`SetPlaying(false)` then `(true)`) is INFERRED to replay the welcome. (03, 05, 06)
- **Q10. Duplicate sessions and replicas:** same IP gives `LogonSessionReplaced`, different hosts give `LoggedInElsewhere`; master only surfaces these as `LoggedOffEvent{Result}`. Run a single replica plus a lock until tested. (07, gap-01)
- **Q11. Rate limits:** logon, GC request and lobby-create thresholds are unpublished. Stagger logins and pace GC requests. (04, 05, 07)

### Tier 3: operational and secondary

- **Q12. Token renewal from Go:** a CM `GenerateAccessTokenForApp` sketch compiles (`probe login -renew`) but has not run. Fallback: re-run `Authenticate` with password + TOTP before expiry. (04, gap-01)
- **Q13. Account prerequisites:** can limited accounts be league admins? Does the GC need the app-570 licence entry (a never-launched account worked)? (04, 10)
- **Q14. Leaving early to free a bot:** does Abandon then Leave work without penalties? Treat it as not working. (05, 06)
- **Q15. Protocol details (rest):** the meaning of `member_indices`, whether `Get(lobbyID)` equals the owner SOID, current `server_region` IDs (Korea and Warsaw unknown), and the `series_type` values. `SetLobbyDetails` is a full update (answered). (01, gap-04)
- **Q16. League admin timing:** how quickly a newly added web admin appears in 7634, and what the 7434 `LeagueAdminList` push means. (02)
- **Q17. Weekly maintenance:** the exact Tuesday window and how long it lasts. (07)
- **Q20. Portal scrape fallback:** would a bot's SteamClient access token be accepted as the dota2.com `dota_oauth_token` cookie? Do not build or test this unless 7634 breaks, and only with explicit operator approval, since it uses a real credential. (gap-03)
