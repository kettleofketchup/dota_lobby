# 09 - Live probe results (2026-11-08)

Results of live tests run on 2026-11-08 with `tools/probe` against Steam and the Dota 2 GC. One bot account was used ("the test bot"): a mobile-authenticator account that administers one test league and had never launched Dota 2. A second, human-controlled account joined lobbies as the invited player.

Library versions: go-steam `v0.0.0-20260922075330-336e824f8540` and go-dota2 `v0.0.0-20260922075533-6d32dc38d075` (the upstream masters this PR builds on).

Every result below comes from the probe's JSONL report (one line per event, tagged with the question ID) and the raw GC bodies it captured. Timings are wall-clock from the probe host. Labels follow the [index](README.md): everything here is **VERIFIED live** unless marked otherwise, with one account, one league and one day of runs behind it.

---

## 1. Summary

| Question | Result |
|---|---|
| Q1 token auth | Answered: password + TOTP `Authenticate` gives a refresh token; token-only `LogOn` reaches GC welcome repeatedly |
| Q21 never-launched account | Answered: no Dota 2 install or prior launch is needed for a GC session |
| Q2 league listing | Answered: 7633 returns the admin league list |
| Q18 job-ID echo | Answered: 7634 and the create 7055 echo our source job ID |
| Q4 league on create | Positive case answered: an administered `leagueid` is accepted and read back. The rejection path is still untested |
| Q7 create result | Answered for creates with a source job: 7055 `CMsgGenericResult`, `eresult` 1 |
| Q6 TV delay | Answered for league lobbies: default read-back is wire 2 = 120 s |
| Q15 (part) | `game_mode` set on create reads back; `SetLobbyDetails` must resend the full details set |
| Invites | Invite to a non-friend works; there is no GC message to cancel a lobby invite |
| Lobby chat | `Lobby_<lobby_id>` works; the join reply does not echo the job ID; `!` commands arrive, `/` text never leaves the client |

Still open after this run: Q3, Q5, Q8, Q12, Q19, Q22, and seat enforcement by kick-from-team (section 10).

---

## 2. Q1: token auth and login

**Authenticate.** `Auth.Authenticate` with password and a TOTP code generated from the `shared_secret` returned a refresh token in **0.72 s**, with no Steam Guard prompt or email confirmation. The token is a JWT whose claims are:

- `iss` `steam`, `aud` `["client", "web", "renew", "derive"]`
- `exp - iat` = 18,146,130 s, about **210 days (roughly 7 months)**. This one expires 2027-06-06.

`renew` in the audience is consistent with the token being usable for CM renewal (Q12), which this run did not test.

**Token-only login.** `LogOn(ctx, {AccessToken})` with the stored refresh token, followed by `SetPlaying` and `SayHello`, reached the GC welcome on every attempt (seven runs, each a fresh process, over about 30 minutes):

| Step | Observed |
|---|---|
| TCP connect to CM | 0.4-1.0 s |
| `LoggedOnEvent` | `EResult_OK` |
| `PlayingSessionStateEvent` | app 0, then app 570, `blocked: false` |
| GC welcome (from start) | 1.7-7.4 s; most runs about 6-7 s |

On the first login the GC queued the session: `GCConnectionStatus` went `NO_SESSION` → `NO_SESSION_IN_LOGON_QUEUE` → `HAVE_SESSION` within about 150 ms. Later logins went straight to `HAVE_SESSION`. A readiness gate should treat the logon-queue status as "not yet", not as a failure.

The legacy password `CMsgClientLogon` path was not retested; the service no longer uses it.

## 3. Q21: an account that never launched Dota 2

The test bot had never launched Dota 2 and has no client install anywhere. It still got `PlayingSessionStateEvent` for app 570 and a full GC welcome, and it created league lobbies. No manual first launch was needed for this account.

What this run does not show: whether an account without a free-licence entry for app 570 behaves the same. See [10 - Headless bot accounts](10-headless-bot-accounts.md) §2.

## 4. Q2 and Q18: league listing and job-ID echo

**7633 → 7634.** `k_EMsgDOTALeagueInfoListAdminsRequest` (7633) returned a `CMsgDOTALeagueInfoList` with **one** league, the test league the bot administers (amateur tier, NA region, status 3). The reply took about 0.5 s.

**Job-ID echo.** Three attempts were made in one session:

| Attempt | `job_id_source` sent | 7634 `job_id_target` | Result |
|---|---|---|---|
| go-dota2 master `MakeRequest` | set by go-dota2 | equal to ours | completed, 1 league |
| Raw send with our job ID | `1099511627777` | `1099511627777` | echoed |
| Raw send without a job ID | none | none (`0xFFFFFFFFFFFFFFFF`) | reply still sent, not addressed |

So master `MakeRequest` works for 7633 as is. A request sent without a source job still gets a reply, but only a type-matched tap can pick it up.

**Lobby nodes.** `RequestLeagueAvailableLobbyNodes` for the same league completed (reply 7651 echoed the job ID) with an empty body: this league has no bracket nodes, so `league_node_id` stays 0.

**`is_league_admin`.** After the welcome, `CSODOTAGameAccountClient` was in the SOCache with `is_league_admin = true`. That is a free "administers at least one league" check before calling 7633.

## 5. Q4 and Q7: league lobby create and the 7055 reply

Four creates with `leagueid` set to the administered league, each sent with a source job ID:

| Measure | Observed |
|---|---|
| `CSODOTALobby` (SO 2004) appears | 0.4-1.1 s after the send |
| 7055 arrives | at the same time as the SO, `job_id_target` = our `job_id_source` |
| 7055 body | `CMsgGenericResult{eresult: 1}`, `debug_message` empty |
| Read-back `CSODOTALobby.leagueid` (42) | equal to the requested league on every create |
| `lobby_type` read-back | `PRACTICE` |

This settles the success side of Q7: 7055 is sent for a successful create, decodes as `CMsgGenericResult`, and 1 means OK. Whether the GC sends 7055 for a create without a source job was not tested.

Q4 is answered only for the positive case. A create with a non-admin or bogus league was not run, so the rejection signal (popup 27, `leagueid` reset to 0, or a non-OK 7055) is still unknown. The read-back stays the authority.

`DestroyLobby` replied `k_EMsgDestroyLobbyResponse` (8247) with `SUCCESS` each time, and the lobby left the SOCache.

## 6. Q6: TV delay on a league lobby

Creates that left `dota_tv_delay` unset read back `CSODOTALobby.dota_tv_delay` (53) = **2**, which is **120 s** in the current numbering (`10=0, 60=1, 120=2, 300=3, 900=4`, see [gap-04](gap-04-proto-freshness-audit.md)). The vendored go-dota2 enum would label 2 as `_300`, so decode it as an integer.

Not tested: sending an explicit wire value, and the non-league case (Q19).

## 7. Game mode and `SetLobbyDetails`

- `game_mode` sent on the create (`DOTA_GAMEMODE_TURBO`, 23) read back as Turbo.
- `SetLobbyDetails` is not a partial update: a details message replaces the lobby settings, so every update must resend the full set (name, pass key, league, mode, region and so on), not only the changed field. This settles that part of Q15.

## 8. Invites

- `InviteLobbyMember` to a player who is **not** on the bot's friends list produced `k_EMsgGCInvitationCreated` (4502) with `user_offline = false`, and the player joined: the lobby member count went from 1 to 2 in the SO.
- There is **no GC message to cancel a lobby invite**. The protos have cancel messages only for party invites and guild invites. An outstanding lobby invite stays visible in `CSODOTALobby.pending_invites` (field 10) until the player answers, and the only way to void it is to destroy the lobby.

## 9. Lobby chat

| Check | Result |
|---|---|
| Channel name | `"Lobby_" + decimal(lobby_id)` with `DOTAChannelType_Lobby` returns `JOIN_SUCCESS` |
| Join reply 7010 | `job_id_target` is **not** our job ID (it is none), so go-dota2's `JoinChatChannel` (`MakeRequest`) never completes and times out |
| Workaround | send 7009 and match the 7010 reply by `channel_name` on a type tap |
| Incoming messages | pushed to every joined client as `k_EMsgGCChatMessage` (7273); there is no polling API |
| `!help`, `!ready`, `!test` typed in the client | arrived as chat messages and the probe replied in the channel |
| `/`-prefixed text | consumed by the Dota client as a console command and never sent to the channel |

So chat commands for players must use a prefix the client does not intercept, such as `!`.

## 10. Still open after this run

| Question | Why it is still open |
|---|---|
| Q3 drop-down matches 7634 | Needs a human client opening the lobby League drop-down for an account that has a 7634 list |
| Q4 rejection | No create with a non-admin or bogus league was run |
| Q5 host seat for launch | No lobby was launched |
| Q8 match results | No match was played |
| Q12 CM token renewal | `probe login -renew` was not run |
| Q19 60 s delay without a league | No non-league lobby was created |
| Q22 limited-account invites | The test bot is not a limited account |
| Seat enforcement | Kick-from-team on a misplaced member was not exercised |

Also untested: Q7 without a source job, and a second bot account (all of the above is one account).
