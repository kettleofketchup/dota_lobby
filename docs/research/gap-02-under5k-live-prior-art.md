# Gap 02: Live prior art from under-5k-league (Oct–Nov 2026)

Questions covered: launch nonce, TV delay, league rejection, league listing, and auth. Researched 2026-11-06.

## Sources

- **under-5k-league**: `github.com/timothyjjcrow/under-5k-league`, cloned at HEAD `5beadd76` (2026-11-02). Paths below are relative to the repo root. `BOT` = `ops/dota-lobby-bot`.
- **dota2-user**: npm `dota2-user@2.1.1` (the version under-5k pins), upstream `github.com/itsjfx/node-dota2-user`.
- **Current Valve protos**: `SteamDatabase/Protobufs` master, fetched 2026-11-06.
- **Local go-dota2**: `$G` = `~/.local/share/go/pkg/mod/github.com/paralin/go-dota2@v0.0.0-20250623204622-532cb1851217`.
- **OpenDota match API**: `api.opendota.com/api/matches/<id>`.

Labels: **VERIFIED** means I saw it in code, a log quote, or an API record. **INFERRED** means reasoning from verified facts.

## What the bot is

The bot is Node 22, `steam-user@5.3.0` + `steam-session@1.9.4` + `dota2-user@2.1.1` (`BOT/package.json:3-9`). It hand-encodes the lobby request messages with protobufjs from its own `BOT/lobby.proto`. The header comment explains why: "Dota2User ... published package omits these client lobby request encoders" (`BOT/lobby.proto:1-5`). One bot account hosts one lobby at a time on league ticket 20004 (`docs/DOTA-BOT-HOSTING.md:5`). It has run real in-house league games since about 2026-10-14.

Relevant PRs:

- [#73](https://github.com/timothyjjcrow/under-5k-league/pull/73), [#74](https://github.com/timothyjjcrow/under-5k-league/pull/74) (2026-10-15)
- [#83](https://github.com/timothyjjcrow/under-5k-league/pull/83) (2026-10-18)
- [#126](https://github.com/timothyjjcrow/under-5k-league/pull/126), [#128](https://github.com/timothyjjcrow/under-5k-league/pull/128) (2026-10-30)
- [#134](https://github.com/timothyjjcrow/under-5k-league/pull/134) (2026-11-02)

---

## (a) Launch nonce

**Answer: no nonce, not even `client_version`. Launches still reached RUN after 2026-09-25.**

- **VERIFIED.** `BOT/lobby.proto` has no `CMsgPracticeLobbyLaunch`. Start is `send(7041, "Empty")` (`BOT/server.mjs:99`), where `Empty` is a message with no fields (`BOT/lobby.proto:31-32`). The GC therefore receives an empty 7041 body.
- **VERIFIED.** History has no nonce anywhere in the bot. `git log -S nonce` hits only two unrelated 2026-08-04 privacy commits. The 7041 line dates from `a218bfa3` (2026-09-04) and was last touched in `a9094cef` (2026-10-18, #88), with the body unchanged.
- **VERIFIED.** Valve added the field in SteamDatabase `623ebf57b33a` on 2026-09-25T23:04Z. Current master has `CMsgPracticeLobbyLaunch { optional uint32 client_version = 5; optional fixed64 nonce = 6; }` (`dota2/dota_gcmessages_client_match_management.proto:211-214`).
- **VERIFIED.** Bot-hosted games ran after that date:
  - Match **9050201642** ran 2026-10-18 02:10:43Z. Its end never reached the bot while the GC session stayed up (`docs/DOTA-LOBBY-BOT.md:215`).
  - OpenDota has that match with `leagueid 20004`, `game_mode 2`, `lobby_type 1`, `duration 3194`. Its `start_time` 1792289443 is exactly 2026-10-18T02:10:43Z.
  - PR #83 says "every played job since 2026-10-16 still held the bot at postgame."
  - PR #128 says "Every bot-hosted game since 2026-10-18" went from game state 1 straight to 6 with no updates in between.
  - PR #126/#128 report early-leave lines (`early-leave result=confirmed`) for games running 53 minutes.
- **One failed launch.** PR #73 (2026-10-14 23:42Z): a launch got match id 9045478659, but "the game never became a real match." OpenDota returns `Not Found` for that id. PR #74 then handles failed loads and stalls generically. Nothing ties that failure to the nonce, since later launches worked. That is **INFERRED**.
- **INFERRED.** The launch for 9050201642 was the bot's own 7041. A job reaches Start only when the bot is lobby leader (`BOT/controller.mjs:893-900`, `leaderId` check). The PR #74 table does mention "player-led launch" as a fallback path, so this is not 100% proven for that one match.
- **Go equivalence: VERIFIED.** go-dota2's `LaunchLobby()` sends an empty `CMsgPracticeLobbyLaunch{}` (`$G/client_generated.go:2030-2033`), which is the same zero-byte body. **Conclusion:** the pinned go-dota2 launch should work as of Oct 2026, and the nonce is optional. That is strongly INFERRED from live behaviour of an identical wire body.

## (b) TV delay

**Answer: it sends integer 1 and believes that means two minutes. No log records the resulting delay.**

- **VERIFIED.** The create sends `lobbyDetails.dotaTvDelay: 1` (`BOT/server.mjs:94`). The field is `optional uint32 dota_tv_delay = 24` (`BOT/lobby.proto:19`).
- **VERIFIED.** The docs claim "a two-minute DotaTV delay" (`docs/DOTA-LOBBY-BOT.md:192`). That matches the old enum (`LobbyDotaTV_120 = 1`). The old enum is still what dota2-user ships (`protobufs/generated/dota_gcmessages_common_lobby.d.ts:12-15`) and what the local go-dota2 has (`$G/protocol/dota_gcmessages_common_lobby.pb.go:87-90`).
- **VERIFIED.** The renumbering came in SteamDatabase `c983b3e16435` (2026-08-12T21:29Z). Current enum: `_10=0, _60=1, _120=2, _300=3, _900=4` (`dota_gcmessages_common_lobby.proto:16-22`). The value 1 was written on 2026-09-04 (`a218bfa3`), after the renumbering, so it was copied from stale knowledge.
- **VERIFIED.** The bot never reads back `CSODOTALobby.dota_tv_delay` (field 53). `settingsMatch` checks name, password, leagueid, mode, region, cheats, bots and series only (`BOT/controller.mjs:72-84`). The event-log whitelist has no delay field (`BOT/event-log.mjs:9-40`). No PR or doc reports an observed delay.
- **INFERRED.** Under-5k games since 2026-09-04 most likely run with a **60 s** delay, unnoticed. **The gap is not resolved by this source:** the GC's interpretation of 1 is still unobserved. For us: send integer **2** for 120 s and read back field 53 once live.

## (c) League rejection

**Answer: readback of `leagueid` plus a 7055 log line. No popup handling, and no rejection ever logged.**

- **VERIFIED: readback.** After create, a lobby-screen snapshot is `ready` only if `settingsMatch(lobby, spec)`, which includes `lobby.leagueid === spec.leagueId` (`BOT/controller.mjs:77`). Otherwise it is `blocked` (`BOT/controller.mjs:1105-1110`). Start and invite throw `SETTINGS` on a mismatch (`:896-900`, `:959-963`).
  - `leagueid` is decoded by dota2-user's `CSODOTALobby` (`BOT/server.mjs:209-218`). Valve's `CSODOTALobby.leagueid` is field **42** (`dota_gcmessages_common_lobby.proto:269`).
  - The docs say "Wrong tickets/settings block launch. Check the bot account's ticket permissions rather than repeatedly clicking Create" (`docs/DOTA-LOBBY-BOT.md:415`).
  - A 30 s unconfirmed create also reads `blocked` (`BOT/controller.mjs:116-118`, `UNCONFIRMED_MS`).
- **VERIFIED: 7055.** `dota.router.on(k_EMsgGCPracticeLobbyResponse, a => { if (a?.eresult !== 1) log("lobby-refused", {result}) })` (`BOT/server.mjs:289-291`). dota2-user maps 7055 to `CMsgGenericResult` (`dota2-user/protobufs/protobuf-mappings.js:230`). That is a third client, after node-dota2, decoding it this way and treating 1 as OK. The log field is documented at `docs/DOTA-LOBBY-BOT.md:294`.
- **VERIFIED: no popup handling.** There is no `popup` / `k_EMsgGCPopup` / `NOT_LEAGUE_ADMIN` anywhere in `BOT/*.mjs`.
- **VERIFIED: only a positive example exists.** The 2026-09-04 live test says "the GC confirmed Captains Mode (2), US East (2), and league 20004" by readback (`docs/DOTA-LOBBY-BOT.md:443`).
- **No negative example.** No create with a non-admin or bogus league is recorded anywhere: no `lobby-refused` line in docs or PRs, and no repo issues. Europe never got a ticket (`docs/DECISIONS.md:110`, `docs/DOTA-LOBBY-BOT.md:457-459`), so a rejection was never exercised.
- **Not resolved.** The rejection signal (popup 27 vs leagueid reset to 0 vs 7055 eresult) is still unknown. Under-5k's design (readback as authority, 7055 as diagnostic) is the right defensive pattern either way.

## (d) League listing (7633)

**Answer: not used. Static config.**

- **VERIFIED.** Grep of `BOT/` for `7633|7634|LeagueInfo|LeagueAdmin` finds nothing.
- **VERIFIED: where the league id comes from.** The site sends `leagueId` per request from env `DOTA_INHOUSE_LEAGUE_ID` (20004, looked up manually on OpenDota's league list), or from `Season.dotaLeagueId` (`docs/DOTA-LOBBY-BOT.md:33-35`, `:85`). The bot only validates `leagueId > 0` (`BOT/controller.mjs:44-46`). Ticket permission was granted by hand via dota2.com/league (`docs/DOTA-LOBBY-BOT.md:33`).
- **No logged 7634 payload. Not resolved.**
- **dota2-user has only the ids.** It knows `k_EMsgDOTALeagueInfoListAdminsRequest = 7633` and `...Reponse = 7634` (`protobufs/generated/dota_gcmessages_msgid.d.ts:270-271`), but has no router mapping or helper for them.

## (e) Login

**Answer: steam-session refresh token (SteamClient platform). No InvalidPassword reports.**

- **VERIFIED: the login helper.** `new LoginSession(EAuthTokenPlatformType.SteamClient, …)` (`BOT/login.mjs:27`) signs in with `startWithQR()` (`:115`) by default, or `startWithCredentials()` with `--password` (`:93`). It stores `refreshToken`/`steamId`/`accountName` in `state/steam-auth.json` (`:65-67`, `BOT/auth-store.mjs:32-49`).
- **VERIFIED: the worker.** It runs `user.logOn({refreshToken, steamID})` (`BOT/server.mjs:470-476`) with `renewRefreshTokens: true` (`:46-50`), and saves rotated tokens on the `refreshToken` event (`:353-364`). Env username and password are only a fallback when no session is saved.
- **VERIFIED: token portability.** On 2026-10-18 the Mac's saved token was copied to a Linux droplet and "Steam accepted the Mac's saved session with no new sign-in" (`docs/DOTA-LOBBY-BOT.md:162`). That is evidence a SteamClient refresh token is portable across hosts/IPs.
- **VERIFIED: failure handling.** The only documented failure mode is a generic `Steam login/connection failed` with the instruction to re-run `npm run login` (`docs/DOTA-LOBBY-BOT.md:184`, `docs/DOTA-BOT-HOSTING.md:59`). No `InvalidPassword` appears in code, docs or PRs.
- **INFERRED: the password fallback.** steam-user 5.x routes even an `accountName/password` `logOn` through the steam-session auth flow, not legacy `CMsgClientLogon` password, so this bot never exercises the legacy path that go-steam uses.
- **node-steam-user issues since 2026-06-01: VERIFIED none on password logon.** The only issues are #542/#543 (2026-08, AuthSession tickets, unrelated). node-steam-session's newest is #56 (2026-05-20, cookie via refresh token, AccessDenied), which predates the window and is a different topic. So under-5k neither confirms nor refutes the go-steam PR #11 `InvalidPassword` report. Its success with refresh tokens supports the plan to port `IAuthenticationService` + `access_token` logon.

---

## dota2-user (itsjfx), narrow pass

- **VERIFIED: snapshot date and freshness.** The proto snapshot is the `Protobufs` submodule at SteamDatabase `397cc9d7`, dated **2025-04-15**. The last upstream commit and npm publish was 2.1.1 on 2025-05-02, and there has been no activity since. It is **not fresher** than go-dota2's pin: both predate the 2026-08-12 renumbering and the 2026-09-25 nonce.
- **VERIFIED: launch and nonce.** It has no `CMsgPracticeLobbyLaunch` / `CMsgPracticeLobbySetDetails` encoders in the published package, which is why under-5k hand-wrote `lobby.proto`. It has no nonce. Its TV-delay enum is the old numbering (`d.ts:12-15`).
- **VERIFIED: league admin.** It has message ids 7633/7634 only, with no helper or decoder mapping. It maps 7055 to `CMsgGenericResult` (`protobuf-mappings.js:230`).

## Effect on the open questions in README.md

| README Q | Status after this pass |
|---|---|
| Q6 nonce | **Resolved enough to proceed.** An empty 7041 body (identical to go-dota2 `LaunchLobby()`) reached RUN and produced ticketed league matches in Oct 2026 (VERIFIED: match 9050201642, leagueid 20004). Keep a launch-to-RUN timeout as a guard. |
| Q6 TV delay | **Not resolved.** Under-5k sends 1 and assumes 120 s, but never reads it back. Current Valve enum makes 1 = 60 s. Send 2 and verify via `CSODOTALobby.dota_tv_delay` (53). |
| Q4 league rejection | **Not resolved.** Only a positive readback is on record. Adopt readback-of-leagueid as authoritative, log 7055 `eresult != 1`, and watch for popups. |
| Q7 7055 body | **Partly.** A third client decodes it as `CMsgGenericResult` with 1 = OK (VERIFIED code). No captured bodies are published. |
| Q2 7633 reply | **Not resolved.** Under-5k uses a static league id and never calls 7633. |
| Auth | **Supports** the refresh-token plan, including tokens copied across hosts. No new evidence on legacy password rejection. |
