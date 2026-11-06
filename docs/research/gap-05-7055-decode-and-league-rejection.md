# Gap 05: decoding 7055 (`k_EMsgGCPracticeLobbyResponse`) and the GC's league-rejection signal

Date: 2026-11-06. Scope: settle the decode type, success condition and job addressing of message 7055, and look for evidence of what the GC sends when a bot creates a lobby with a `leagueid` it does not administer. The plan depends on both (D3 and D5 in `docs/plans/2026-11-06-lobby-service-architecture.md`).

Labels:
- **VERIFIED**: read in source or protos, with file:line refs.
- **REPORTED**: stated by a third party in an issue, or implied by production code that depends on it. Not reproduced here.
- **INFERRED**: my own reasoning from the above.
- **UNRESOLVED**: no evidence found.

`$GOD2` = `~/.local/share/go/pkg/mod/github.com/paralin/go-dota2@v0.0.0-20250623204622-532cb1851217`
`$GOSTEAM` = `~/.local/share/go/pkg/mod/github.com/paralin/go-steam@v0.0.0-20250502043548-f167ff28a93a`

---

## 1. Answer

| Question | Answer | Status |
|---|---|---|
| Decode type of 7055 | **`CMsgGenericResult`** (`eresult` uint32 field 1, `debug_message` string field 2). No `CMsgPracticeLobbyResponse` type exists anywhere. On the wire it matches `CMsgPracticeLobbyJoinResponse` at field 1, so both decode without error, but only `CMsgGenericResult` captures field 2. | VERIFIED (protos). The two clients that still run in production use it. |
| Value meaning | `eresult` is a Steam **EResult**: `1` = `k_EResultOK`, `2` = `k_EResultFail`. It is **not** a `DOTAJoinLobbyResult`, where 1 would be `ALREADY_IN_GAME`. | REPORTED (dota-ihl-bot checks `eresult === EResult.OK`; node-dota2 #506 saw `2` on failed creates) / INFERRED |
| Success condition | `eresult == 1` | REPORTED |
| Job-addressed? | **Yes.** On create, the GC sends 7055 with `job_id_target` set to the request's `job_id_source`. Every client that waits for 7055 does so by job ID. | VERIFIED (client code). REPORTED for the wire behaviour (stack trace in node-dota2 #513, dota-ihl-bot's 5 s job wait). |
| Does go-dota2 send a source job ID? | **No.** `d.write()` sends a default header, so 7055 (if the GC sends it at all) arrives with no target job and is surfaced as `UnhandledGCPacket`. | VERIFIED (code) / UNRESOLVED (whether the GC replies at all without a source job) |
| Signal for a `leagueid` the bot does not administer | **No client handles it and no issue records it.** Popup `NOT_LEAGUE_ADMIN` (27) exists in the protos, but no source or issue ties it to lobby create. Nor has anyone recorded a non-OK 7055 or a silent `leagueid=0` read-back for this case. | UNRESOLVED; needs a live test |

**Is the gap resolved?** It is **partly** resolved. The 7055 decode type, its success value and its job addressing are now settled well enough to implement. The league-rejection signal is still unknown, so D3 has to keep all three signals. Of the three, only the `CSODOTALobby.leagueid` read-back is guaranteed to be observable.

---

## 2. Local protos (ground truth)

| Fact | Ref | Status |
|---|---|---|
| `k_EMsgGCPracticeLobbyResponse = 7055` | `$GOD2/protocol/dota_gcmessages_msgid.proto:26` | VERIFIED |
| `k_EMsgGCPracticeLobbyJoinResponse = 7113` (a separate message) | `dota_gcmessages_msgid.proto:60` | VERIFIED |
| No `CMsgPracticeLobbyResponse`, `CMsgGCPracticeLobbyResponse` or `CMsgDOTAPracticeLobbyResponse` message. The string `PracticeLobbyResponse` appears only in the msgid enum. There is no comment near the 7055 enum line. | grep across `$GOD2/protocol/*.proto` | VERIFIED |
| `message CMsgGenericResult { optional uint32 eresult = 1 [default = 2]; optional string debug_message = 2; }` | `$GOD2/protocol/econ_shared_enums.proto:37-40` | VERIFIED |
| `message CMsgPracticeLobbyJoinResponse { optional DOTAJoinLobbyResult result = 1 [default = DOTA_JOIN_RESULT_SUCCESS]; }` | `dota_gcmessages_client_match_management.proto:272-274` | VERIFIED |
| Other messages with a `DOTAJoinLobbyResult` field: only `CMsgQuickJoinCustomLobbyResponse.result` | `dota_gcmessages_client_match_management.proto:343` | VERIFIED |
| `DOTAJoinLobbyResult`: 0 SUCCESS, 1 ALREADY_IN_GAME, 2 INVALID_LOBBY, 3 INCORRECT_PASSWORD, 4 ACCESS_DENIED, 5 GENERIC_ERROR … 14 NO_PLAYTIME | `dota_shared_enums.proto:324-340` | VERIFIED |
| Other `eresult` carriers (none lobby-related): `CMsgStartFindingMatchResult.legacy_generic_eresult`, several in `dota_gcmessages_client.proto` (316, 427, 440, 669, 677), `gcsdk_gcmessages.proto:163,171` | grep | VERIFIED |
| `CMsgDOTAPopup.PopupID.NOT_LEAGUE_ADMIN = 27`. Its neighbours are `TOURNAMENT_GAME_NOT_FOUND` through `TOURNAMENT_GAME_SQL_UPDATE_FAILED` (21-26), which suggests popup 27 belongs to the old ticketed "tournament game" create flow. | `dota_gcmessages_client.proto:143-172`. `k_EMsgGCPopup = 7102` at `dota_gcmessages_msgid.proto:57` | VERIFIED (enum) / INFERRED (association) |
| Upstream today has the same definitions: `CMsgGenericResult` at `econ_shared_enums.proto:37` and `CMsgPracticeLobbyJoinResponse` at `dota_gcmessages_client_match_management.proto:271` in SteamDatabase/GameTracking-Dota2 | https://github.com/SteamDatabase/GameTracking-Dota2/blob/master/Protobufs/econ_shared_enums.proto | VERIFIED (fetched 2026-11-06) |

go-dota2 itself:
- `$GOD2/apigen/msg_overrides.go:33` marks 7055 as `MsgSenderNone`, so the code generator gives it no wrapper. The pinned `client_*.go` has no reference to 7055. **VERIFIED**
- `$GOD2/client.go:116-117`: `write()` = `GC.Write(gamecoordinator.NewGCMsgProtobuf(AppID, messageType, msg))`, which never calls `SetSourceJobId`. **VERIFIED**
- `$GOD2/client.go:170-176`: a packet that is neither handled nor a pending response is emitted as `devents.UnhandledGCPacket`. **VERIFIED**
- `$GOSTEAM/protocol/gamecoordinator/packet.go:18,38`: an incoming `GCPacket` carries `TargetJobId` (from `header.Proto.GetJobidTarget()`). `msg.go:64` provides `GCMsgProtobuf.SetSourceJobId`. A caller can therefore send a job-addressed create and match the reply without forking go-dota2. **VERIFIED**

---

## 3. Per-client findings

### 3.1 Arcana/node-dota2 (deprecated 2022-06-08, HEAD `5fe3817`) and devilesk/node-dota2

| | |
|---|---|
| Decode type | `Dota2.schema.CMsgGenericResult.decode(message)`: [handlers/lobbies.js:612-626](https://github.com/Arcana/node-dota2/blob/5fe3817/handlers/lobbies.js#L612-L626). devilesk's fork is identical, apart from one unrelated line (lobbies.js:429), at HEAD `25625a8` and at the `a71891d` pin used by dota-ihl-bot (lines 614-615). |
| Proto that defines it | Since v7.0.0 (`25731bf`, 2019-01-27) node-dota2 loads SteamDatabase/Protobufs through `steam-resources`. Before that it bundled `proto/econ_shared_enums.proto:44-47`, which already defined `eresult = 1`, `debug_message = 2`. **No version has a field called `result`.** |
| Success check | `if (practiceLobbyResponse.result === 1) callback(null, …) else callback(practiceLobbyResponse.result, …)` (lines 619-623). `.result` does not exist on `CMsgGenericResult`, so it is always `undefined`. Every 7055 goes down the else branch with `err === undefined`. **The library's own success test is broken, but harmless in effect: callers see a falsy error every time.** VERIFIED (code) / INFERRED (effect) |
| History | `919d5f8` (2015-11-21) added `result === 1` while the decode type was still `CMsgPracticeLobbyJoinResponse`. Under that type, 1 would mean `ALREADY_IN_GAME`, and the authors still treated it as success, which suggests the values were EResult all along. [`d67dfaf`](https://github.com/Arcana/node-dota2/commit/d67dfafc3097f6ce9cf1582f2ce6438018bddc23) (2017-04-17) switched the decode to `CMsgGenericResult` with the message *"Fix parsing to wrong message type causing weird debug message when creating lobby"*. In other words, the GC was sending a field-2 string (`debug_message`) that the JoinResponse decode could not place. REPORTED / INFERRED |
| Shared with | create, config, kick, kick-from-team, flip, shuffle, launch, leave, AI-bot add and team join all pass `onPracticeLobbyResponse` (lobbies.js:141, 163, 220, 239, 284, 308, 353, 392, 412, 439, 493). VERIFIED |
| Job-addressed? | Yes. `sendToGC(type, payload, handler, callback)` ([index.js:274-286](https://github.com/Arcana/node-dota2/blob/5fe3817/index.js#L274-L286)) → node-steam `SteamGameCoordinator._send` sets `job_id_source` and stores the callback in `_jobs` ([node-steam game_coordinator/index.js:33-47](https://github.com/seishun/node-steam/blob/master/lib/handlers/game_coordinator/index.js#L33-L47)). Replies are routed to the callback only when `targetJobID in this._jobs` (line 104-105); otherwise they go to the generic handler, which has no callback. VERIFIED |

Issue evidence:
- [#513](https://github.com/Arcana/node-dota2/issues/513) (2017-06-27): a create crashes in `onPracticeLobbyResponse` at `lobbies.js:616` (the else branch). The stack runs through `steam/lib/handlers/game_coordinator/index.js:105`, which is the **job-callback path**. This shows that the GC replied to `PracticeLobbyCreate` with 7055 carrying `job_id_target` = the request's source job. **REPORTED (stack trace)**
- [#506](https://github.com/Arcana/node-dota2/issues/506) (2017-06-06): a user reads `{ eresult }` from `createPracticeLobbyAsync` and maps it through `DOTAJoinLobbyResult` names. They accept `0` or `1`. After 10 min to 5 h, every create returns **`2`**. A maintainer blames a stale lobby after a GC reconnect. Read as an EResult, `2` = `k_EResultFail`. **REPORTED**: the GC returns 7055 `eresult=2` when a create fails (here: the bot was already in a lobby).
- [#654](https://github.com/Arcana/node-dota2/issues/654) (2019-04-27): flip, shuffle and abandon produce **no** 7055 (also checked in NetHook). Launch does produce a callback. So 7055 is not an acknowledgement for every lobby op. **REPORTED**
- [#29](https://github.com/Arcana/node-dota2/issues/29) (2014): a NetHook capture of a create showed only `CacheSubscribed` and the SO object, with no `JoinResponse`. That is consistent with 7113 never being the create reply. It says nothing about 7055 either way. **REPORTED**
- The web-search summary of [PR #417](https://github.com/Arcana/node-dota2/pull/417) quotes a node-dota2 comment saying there is "no CMsgPracticeLobbyResponse … so we map it onto a CMsgPracticeLobbyJoinResponse". I could not find that sentence in the PR diff, in lobbies.js or in any README revision I checked. **Treat it as unverified.** It does match the pre-2017 code, though.

### 3.2 devilesk/dota-ihl-bot (production in-house league bot, HEAD `5d79429`, 2019-05-04, pins `devilesk/node-dota2#a71891d`)

- [lib/dotaBot.js:1040-1049](https://github.com/devilesk/dota-ihl-bot/blob/5d79429/lib/dotaBot.js#L1040-L1049): `this.Dota2.createPracticeLobby(opts, (err, body) => resolve(body.eresult === steam.EResult.OK))` with `.timeout(5000)`. It ignores node-dota2's always-undefined `err` and **checks `eresult == 1` itself**. VERIFIED (code)
- [lib/dotaBot.js:268-286](https://github.com/devilesk/dota-ihl-bot/blob/5d79429/lib/dotaBot.js#L268-L286): `createDotaBotLobby` treats a false result as failure, then leaves the lobby and frees the bot. VERIFIED
- The bot only works if a job-addressed 7055 with `eresult=1` arrives within 5 s of every successful create. Since it ran league lobbies in production (per prior research, 05), this is the strongest available evidence for decode type + success value + job addressing. **REPORTED (implied by production code)**
- `lib/dotaBot.js:416-418` logs every `practiceLobbyResponse`. There is **no Popup handler** (only `unhandled` at line 387 logs the message ID) and **no `leagueid` read-back check** after create. It avoids non-admin leagues up front by choosing bots from `requestLeagueInfoListAdmins` tickets (`lib/dotaBot.js:1052-1072`, `lib/db.js:214`). VERIFIED

### 3.3 ValvePython/dota2 (HEAD `6ccebc3`, 2021-05-02)

- [dota2/features/lobby.py:77-107](https://github.com/ValvePython/dota2/blob/6ccebc3/dota2/features/lobby.py#L77-L107): create uses `self.send(EMsgGCPracticeLobbyCreate, …)`. This is **not** `send_job`. It is fire-and-forget and waits for nothing. VERIFIED
- [dota2/msg.py:46-98](https://github.com/ValvePython/dota2/blob/6ccebc3/dota2/msg.py#L46-L98): `find_proto` tries name rewrites (`CMsgGCPracticeLobbyResponse`, `CMsgDOTAPracticeLobbyResponse`, `CMsgPracticeLobbyResponse`, …). None of these exist in its bundled protos (checked all 20 files in `/protobufs`). The explicit map at lines 101-115 has no 7055 entry. The result is **no decode type**. VERIFIED
- [dota2/client.py:98-104](https://github.com/ValvePython/dota2/blob/6ccebc3/dota2/client.py#L98-L104): when `proto is None` it logs `"Failed to parse: …"` and returns, so 7055 is dropped. Job events are emitted only after a successful parse (lines 117-120). VERIFIED
- `join_practice_lobby` (lines 269-289) does use `send_job` and reads `resp.result` as `DOTAJoinLobbyResult`, but that is the 7113 join reply. VERIFIED
- Issues: [#52](https://github.com/ValvePython/dota2/issues/52) (`create_tournament_lobby` KeyError on `tournament_game`, a stale field; the user fell back to `leagueid` via config) and [#77](https://github.com/ValvePython/dota2/issues/77) (a league lobby created with `leagueid` shows as live on Dotabuff but "after TI10's updates, it doesn't work anymore"). Neither mentions 7055 or Popup 27. REPORTED

### 3.4 hudi1/dota2-java-game-coordiator (JavaSteam-based, HEAD `dc336a8`, 2024-01-30)

- [handlers/features/Dota2Lobby.java:79](https://github.com/hudi1/dota2-java-game-coordiator/blob/dc336a8/dota-gc/src/main/java/org/tomass/dota/gc/handlers/features/Dota2Lobby.java#L79) maps 7055 to `handlePracticeLobby`. Lines 99-104 decode it as **`CMsgPracticeLobbyJoinResponse`** and post `PracticeLobbyCallback(data.getTargetJobID(), body.getResult())`. VERIFIED
- Lines 248-259: `createPracticeLobby` = `sendJobAndWait(protobuf, 60)`, i.e. it sets a source job and blocks on a future keyed by that JobID (`CommonSteamClient.java:298-321, 339-357`). It is **job-addressed**. VERIFIED
- Success condition: none in the library. It returns `DOTAJoinLobbyResult` to the caller. Because of the type mismatch, a successful create (`eresult=1`) appears as `DOTA_JOIN_RESULT_ALREADY_IN_GAME`. INFERRED
- Popups are posted generically (`Dota2SharedObjects.java:56, 163-167`). Nothing handles `NOT_LEAGUE_ADMIN`. VERIFIED

### 3.5 itsjfx/node-dota2-user (HEAD `1b76784`, 2025-05-02; the repo is `node-dota2-user`)

- [src/bin/genny.ts:43](https://github.com/itsjfx/node-dota2-user/blob/1b76784/src/bin/genny.ts#L43) overrides `'EDOTAGCMsg.k_EMsgGCPracticeLobbyResponse': { CMsg: 'CMsgGenericResult' }`. The generated `src/protobufs/protobuf-mappings.ts:448, 649, 849` follows it. The comment at lines 31-36 points to go-dota2's msg_overrides and admits these pairings are hand-picked ("a lot of 'trust me bro'", line 58). VERIFIED
- `Dota2User.sendJob` (`src/Dota2User.ts:114-134`) resolves on the reply to node-steam-user's job callback ([components/gamecoordinator.js:22-34, 70-80](https://github.com/DoctorMcKay/node-steam-user/blob/master/components/gamecoordinator.js)). It is job-addressed, with a generic decode and **no lobby-specific success check**. Its own TODO says "figure out which messages return jobs". VERIFIED
- No lobby helpers, no Popup handling and no league logic. VERIFIED

### 3.6 SteamRE/SteamKit and the SteamKit Dota plugin paralin/Dota2

- SteamKit (HEAD `a6f60da`) ships only `Samples/022_DotaMatchRequest`. Its `DotaClient.cs:141-142` handles only Welcome and MatchDetails. SteamKit has no Dota lobby code and no 7055 handler. VERIFIED
- [paralin/Dota2](https://github.com/paralin/Dota2) (C#, archived, HEAD `3cb01da`): the `src/Dota2/GC/DotaGCHandler.cs:824-850` message map handles 7113 (`HandlePracticeLobbyJoinResponse`, lines 1109-1113) and Popup (lines 1210-1225, which only special-cases the KICKED_* ids). **It has no 7055 entry**, so 7055 falls through to `UnhandledDotaGCCallback`. VERIFIED
- **This contradicts research 08:390**, which called 7055 "`CMsgPracticeLobbyJoinResponse`-shaped (SteamKit convention)". Neither SteamKit nor its Dota plugin decodes 7055. That convention comes from pre-2017 node-dota2 and from the Java client.

### 3.7 Summary table

| Client | 7055 decode type | Success condition | Job-addressed | Source |
|---|---|---|---|---|
| go-dota2 (pinned) | none (`UnhandledGCPacket`) | n/a | no source job sent | `$GOD2/apigen/msg_overrides.go:33`, `client.go:116` |
| Arcana / devilesk node-dota2 | `CMsgGenericResult` | `result === 1` (always false; field does not exist) | yes | lobbies.js:612-626 |
| dota-ihl-bot (on devilesk node-dota2) | `CMsgGenericResult` | `eresult === EResult.OK` (1) | yes, 5 s timeout | dotaBot.js:1040-1049 |
| node-dota2-user | `CMsgGenericResult` | none | yes (generic) | genny.ts:43 |
| hudi1 Java | `CMsgPracticeLobbyJoinResponse` | none (returns enum; OK shows as ALREADY_IN_GAME) | yes, 60 s | Dota2Lobby.java:79, 99-104, 248-259 |
| ValvePython dota2 | none (dropped, "Failed to parse") | n/a | create not sent as job | msg.py:46-115, client.py:98-104, lobby.py:107 |
| paralin/Dota2 (SteamKit) | none (unhandled callback) | n/a | n/a | DotaGCHandler.cs:824-850 |
| SteamKit samples | none | n/a | n/a | Samples/022_DotaMatchRequest |

---

## 4. League rejection: what anyone has observed

Searches covered the code of every client above plus dota-ihl-bot, issue searches (`league`, `league admin`, `NOT_LEAGUE_ADMIN`, `popup`, `leagueid 0`, `PracticeLobbyResponse`, `eresult`) across Arcana/node-dota2, devilesk/node-dota2, devilesk/dota-ihl-bot, ValvePython/dota2, hudi1/dota2-java-game-coordiator, itsjfx/node-dota2-user, paralin/go-dota2 and paralin/Dota2, and a GitHub code search for `NOT_LEAGUE_ADMIN`. The code search found only proto dumps and generated docs, with no handler.

| Item | Finding | Status |
|---|---|---|
| Code that handles `NOT_LEAGUE_ADMIN` / popup 27 | **None** in any client or bot | VERIFIED (absence, in the repos listed) |
| Code that checks `CSODOTALobby.leagueid` read-back against the request | **None**. dota-ihl-bot avoids the case by choosing bots from their admin-ticket list. | VERIFIED |
| [node-dota2 #414](https://github.com/Arcana/node-dota2/issues/414) (2017-01) | "For the bot to be able to make the league lobby it needs to be an admin of that league, otherwise it wont be able to use that league id." It does not say how the failure shows up. | REPORTED |
| [node-dota2 #532](https://github.com/Arcana/node-dota2/issues/532) (2017-09) | `leagueid` "never seems to carry into the lobby creation" until it was passed as a number, after which the read-back showed `leagueid: 5300`. The cause is **client-side**: `Dota2._parseOptions` silently drops options with the wrong JS type ([handlers/helper.js:75-95](https://github.com/Arcana/node-dota2/blob/5fe3817/handlers/helper.js#L75-L95)). This is not GC behaviour. | VERIFIED (cause) |
| [node-dota2 #349](https://github.com/Arcana/node-dota2/issues/349) | The only issue that mentions `NOT_LEAGUE_ADMIN`, and only inside a pasted Popup enum. The trigger there was a chat restriction on a limited account. | VERIFIED (irrelevant) |
| [ValvePython #77](https://github.com/ValvePython/dota2/issues/77) | League lobbies via `leagueid` "doesn't work anymore" after TI10 (2021). The symptom was match tracking, not a create error. | REPORTED |
| Non-OK 7055 for a bad league | Not reported anywhere. The only non-OK 7055 on record is `eresult=2` for a bot stuck in a stale lobby (#506). | UNRESOLVED |

INFERRED: popup 27 sits among the `TOURNAMENT_GAME_*` popups (21-26), which belonged to the old ticketed create path (`tournament_game` / `tournament_game_id`, which ValvePython still sends and which no longer exist in `CMsgPracticeLobbyCreate`, issue #52). It may never fire for today's plain `leagueid` path. Equally plausible outcomes are a create that succeeds with `leagueid` stripped to 0, or a 7055 with `eresult != 1` (and a `debug_message`). No public source tells these apart.

---

## 5. Consequences for the plan (D3 / D5)

1. **Decode 7055 as `protocol.CMsgGenericResult`** and treat `GetEresult() == 1` as OK. Log `GetDebugMessage()` because d67dfaf shows the GC fills it on create. Do not use `CMsgPracticeLobbyJoinResponse`. It decodes, but it drops field 2 and gives the value the wrong enum names. Fix the wording in README:35/167, 03:327 (the node-dota2 `.result` check is broken; the real field is `eresult`) and 08:390 (it is not a "SteamKit convention").
2. **Send the create job-addressed if fail-fast is wanted.** go-dota2's `CreateLobby` and `LeaveCreateLobby` never set a source job. Build `gamecoordinator.NewGCMsgProtobuf(570, 7038, &CMsgPracticeLobbyCreate{…})`, call `SetSourceJobId(n)`, write it with `steamClient.GC.Write`, and match the reply in a `GCPacketHandler` tap on `packet.MsgType == 7055 && packet.TargetJobId == n`. This avoids confusing the create reply with 7055s from launch, kick and other ops. All the pieces are exported (`$GOSTEAM/protocol/gamecoordinator/msg.go:30-66`, `packet.go:18,38`). INFERRED (design), VERIFIED (APIs exist)
3. Whether the GC sends 7055 at all when there is **no** source job, which is what go-dota2 does today, is **UNRESOLVED**. D5 must not depend on it. Keep the SOCache create event plus a timeout as the authoritative success signal.
4. **D3 order of trust:** (a) the read-back `CSODOTALobby.leagueid != requested` is the only signal that will be observable whatever the GC does, so treat it as authoritative. (b) A job-addressed 7055 with `eresult != 1` means the create failed, but not necessarily because of the league (#506 shows it for a stale lobby). (c) Popup 27 is a hint only, with no observed link to create. Keep all three, but classify `league_not_authorized` only on (a), or on (c) arriving inside the create window. Classify (b) alone as `create_failed`.
5. The Phase 0 live test (plan Q4/Q7) is still required. Create with a good league, a non-admin league and a bogus league, each sent **with** a source job, and capture the raw 7055 (including `debug_message`), any Popup and the read-back. This is the only way to close §4.

---

## 6. Sources

Local:
- `$GOD2/protocol/dota_gcmessages_msgid.proto:26,57,60`; `econ_shared_enums.proto:37-40`; `dota_gcmessages_client_match_management.proto:77-84,272-274,343`; `dota_shared_enums.proto:324-340`; `dota_gcmessages_client.proto:143-172,210`
- `$GOD2/apigen/msg_overrides.go:33,73`; `$GOD2/client.go:116-117,170-176`; `$GOD2/client_lobby.go:13-21`
- `$GOSTEAM/protocol/gamecoordinator/packet.go:15-52`; `msg.go:18-66`

Web (fetched 2026-11-06):
- node-dota2: https://github.com/Arcana/node-dota2/blob/5fe3817/handlers/lobbies.js, `index.js`, `handlers/helper.js`; commits `d67dfaf`, `919d5f8`, `25731bf`; issues #29, #349, #414, #432, #506, #513, #532, #654; PR #417
- devilesk/node-dota2 HEAD and `a71891d`: https://github.com/devilesk/node-dota2/blob/a71891d/handlers/lobbies.js
- node-steam GC jobs: https://github.com/seishun/node-steam/blob/master/lib/handlers/game_coordinator/index.js
- steam-resources proto source: https://github.com/seishun/node-steam-resources/blob/master/package.json, `index.js`
- dota-ihl-bot: https://github.com/devilesk/dota-ihl-bot/blob/5d79429/lib/dotaBot.js
- ValvePython: https://github.com/ValvePython/dota2/blob/6ccebc3/dota2/features/lobby.py, `dota2/msg.py`, `dota2/client.py`, `protobufs/`; issues #52, #77
- Java: https://github.com/hudi1/dota2-java-game-coordiator/blob/dc336a8/dota-gc/src/main/java/org/tomass/dota/gc/handlers/features/Dota2Lobby.java, `clients/CommonSteamClient.java`, `handlers/features/Dota2SharedObjects.java`
- node-dota2-user: https://github.com/itsjfx/node-dota2-user/blob/1b76784/src/bin/genny.ts, `src/Dota2User.ts`, `src/protobufs/protobuf-mappings.ts`
- node-steam-user: https://github.com/DoctorMcKay/node-steam-user/blob/master/components/gamecoordinator.js
- paralin/Dota2: https://github.com/paralin/Dota2/blob/3cb01da/src/Dota2/GC/DotaGCHandler.cs
- SteamKit: https://github.com/SteamRE/SteamKit/tree/master/Samples/022_DotaMatchRequest
- Upstream protos: https://github.com/SteamDatabase/GameTracking-Dota2/tree/master/Protobufs, https://github.com/SteamDatabase/Protobufs/tree/master/dota2
