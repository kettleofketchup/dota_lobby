# Gap 04: Protocol freshness audit (per-symbol, dated by GameTracking-Dota2 commit)

> Date: 2026-11-06. Scope: every enum, message and message ID that the plan's `gc/wire.go`,
> create/verify logic and seat logic depend on. This is a docs-only change. No Go code was modified.
>
> Legend: **VERIFIED** means seen in source, in git history, or in a local probe run. **INFERRED** and **UNCERTAIN** are marked where they apply.
>
> Path abbreviations:
> - `$V` = vendored protos `~/.local/share/go/pkg/mod/github.com/paralin/go-dota2@v0.0.0-20250623204622-532cb1851217/protocol/`
> - `$U` = `SteamDatabase/GameTracking-Dota2` `master` at `b671cd08d5da5820ff0ad64fd26fcf32f615d197` (2026-11-05), `Protobufs/`. GitHub now redirects the repo to `SteamTracking/GameTracking-Dota2`.
> - `loc:` = `$U/../game/dota/pak01_dir/resource/localization/dota_english.txt` at the same commit

## TL;DR

1. **VERIFIED: only one renumbering affects us.** It is `LobbyDotaTVDelay`, changed in
   [`c592bf50`](https://github.com/SteamDatabase/GameTracking-Dota2/commit/c592bf50cbc4524fe8e5f4f5df1e44d5911692e6)
   (2026-08-12, client 6898). No other enum or message on the list below was renumbered between the vendored
   baseline and current master. A scan of every enum present in both trees (2,255 symbols) also found that
   `LobbyDotaTVDelay` is the **only** enum anywhere whose existing values changed.
2. **VERIFIED: seat and verification symbols are unchanged.** That covers `PopupID` (all 63 values,
   `NOT_LEAGUE_ADMIN = 27`), `DOTAJoinLobbyResult`, `CSODOTALobby.State`, `EMatchOutcome`, `DOTA_GC_TEAM`,
   `ELeagueStatus`, `ELeagueTier`, `ELeagueRegion`, `CMsgDOTALeagueInfo`/`List`, `CMsgPracticeLobbySetDetails`
   (all 43 fields) and message IDs 7038/7040/7041/7044/7046/7047/7055/7434/7633/7634/7650/7651/8073/8074.
3. **VERIFIED: the other changes only add things.** `CMsgPracticeLobbyLaunch.nonce = 6` (field),
   `CSODOTALobby.current_primary_event_for_display = 104` (field), `LobbyType.AUTOMATED_BOT_ONLY_MATCH = 16`
   (value) and `DOTA_GAMEMODE_BOT_CHALLENGE = 26` (value).
4. **VERIFIED: the vendored decoder mislabels current TV-delay values without an error.** Value `1` decodes as
   `LobbyDotaTV_120` but means 60 s. Value `3` decodes as `_900` but means 300 s. Value `4` decodes as the
   unnamed enum `4`. Sending `LobbyDotaTV_120.Enum()` puts bytes `c0 01 01` on the wire, which is now 60 s.
5. **Localization (VERIFIED for text, UNCERTAIN for meaning).** `DOTA_Lobby_Settings_NonLobbyDelay`,
   `NotLeagueAdmin` and `DOTA_LeagueUnsetMessage` have the same text in 2023 and 2026. A new
   `DOTA_Lobby_Settings_Dota_TV_Delay_1_Minute` ("1 Minute") exists, and the client binary gained
   `DotaTVDelayDropdown_1_Minute` in the same patch as the renumbering. Whether non-league lobbies may pick
   60 s is **UNCERTAIN**. The "two minute delay" string was not updated, but its age alone does not prove it is stale.

## 1. Method

1. I made a partial clone (`--filter=blob:none --sparse`) of `SteamDatabase/GameTracking-Dota2` into a scratch
   directory (`/tmp/gap04/gt`), checking out only `Protobufs/` and `dota_english.txt`.
2. A small parser (`/tmp/gap04scripts/syms.py`) builds a symbol table for both trees: enum `name → value` and
   message `field → label type = number [default]`. Nested enums and messages are included, `[...]` options
   (`boxed_type`, `synthetic_default`) are stripped, and the leading `.` on type names is removed.
3. **Finding the baseline.** The vendored protos are symbol-identical (0 differences across the six relevant
   files) to GameTracking
   [`ab1f047d`](https://github.com/SteamDatabase/GameTracking-Dota2/commit/ab1f047da124125eaf3b605f8ed35d3a30a0a715)
   (2025-06-17). The next commit, `fef468d2` (2025-07-01), already differs by 2 Fantasy enum values. The
   module pin is dated 2025-06-23, which is consistent with this.
4. **History walk.** I extracted the seven relevant files
   (`common_lobby`, `client_match_management`, `shared_enums`, `common_league`, `client`, `msgid`,
   `econ_shared_enums`) at **each of the 23 commits** since the baseline that touched them, then printed every
   change to a target symbol relative to the previous commit. A change followed by a revert would therefore
   still show up. Output (complete):

```text
56032bb7 2025-08-05 CSODOTALobby current_primary_event_for_display: None -> optional EEvent = 104
b25cf388 2025-11-10 CSODOTALobby.LobbyType AUTOMATED_BOT_ONLY_MATCH: None -> 16
b25cf388 2025-11-10 DOTA_GameMode DOTA_GAMEMODE_BOT_CHALLENGE: None -> 26
c592bf50 2026-08-12 LobbyDotaTVDelay LobbyDotaTV_120: 1 -> 2
c592bf50 2026-08-12 LobbyDotaTVDelay LobbyDotaTV_300: 2 -> 3
c592bf50 2026-08-12 LobbyDotaTVDelay LobbyDotaTV_60: None -> 1
c592bf50 2026-08-12 LobbyDotaTVDelay LobbyDotaTV_900: 3 -> 4
e84714d6 2026-09-25 CMsgPracticeLobbyLaunch nonce: None -> optional fixed64 = 6
```

The last commit to touch `Protobufs/` is `2151a803` (2026-10-07). It changes no target symbol.

**Limits of the parser.** It ignores `map<>` fields and oneof membership, and it treats a `required` to
`optional` change as a field change. None of these affect the target symbols, which were also eyeballed.

## 2. Per-symbol results

"Vendored" means `$V` (= GameTracking `ab1f047d`, 2025-06-17). "Current" means `$U` at `b671cd08`.

| Symbol | Vendored | Current | Change (SHA, date) | Kind | Status |
|---|---|---|---|---|---|
| `LobbyDotaTVDelay` | `_10=0, _120=1, _300=2, _900=3` (`$V/dota_gcmessages_common_lobby.proto:14-19`) | `_10=0, _60=1, _120=2, _300=3, _900=4` (`$U/dota_gcmessages_common_lobby.proto:16-22`) | `c592bf50`, 2026-08-12 (client 6898, [steamdb patch 24700034](https://steamdb.info/patchnotes/24700034/)) | **Renumbering plus an added value** | VERIFIED |
| `CMsgPracticeLobbySetDetails` (43 fields) | e.g. `dota_tv_delay = 24 [default=LobbyDotaTV_120]` (`$V/…match_management.proto:144`), `leagueid = 16`, `league_node_id = 49`, `server_region = 4` | identical (`$U/…match_management.proto:119`, `:142`, `:135`, `:161`, `:127`) | none | none (only `boxed_type` options were added) | VERIFIED. **Note:** the default is the *name* `_120`, which is now integer 2 on Valve's side |
| `CMsgPracticeLobbyLaunch` | `client_version = 5` | `+ optional fixed64 nonce = 6` (`$U/…match_management.proto:211-214`) | [`e84714d6`](https://github.com/SteamDatabase/GameTracking-Dota2/commit/e84714d6ff871cfae0b59f3f9038860f052fa247), 2026-09-25 (client 6941) | Added field | VERIFIED field; whether it is required is UNCERTAIN (03 §8.3) |
| `CMsgPracticeLobbyCreate`, `…SetTeamSlot`, `…JoinResponse` | — | identical | none | — | VERIFIED |
| `CMsgDOTAPopup` and `.PopupID` (63 values) | `NOT_LEAGUE_ADMIN = 27` (`$V/dota_gcmessages_client.proto:171`) | `NOT_LEAGUE_ADMIN = 27` (`$U/dota_gcmessages_client.proto:173`) | none | — | VERIFIED |
| `DOTAJoinLobbyResult` (0-14) | `$V/dota_shared_enums.proto:324` | `$U/dota_shared_enums.proto:276` | none | — | VERIFIED |
| `CSODOTALobby.State` | `UI=0, SERVERSETUP=1, RUN=2, POSTGAME=3, READYUP=4, NOTREADY=5, SERVERASSIGN=6` | identical (`$U/…common_lobby.proto:217-225`) | none | — | VERIFIED |
| `CSODOTALobby` (90 shared fields) | max field 132; `dota_tv_delay = 53 [default=LobbyDotaTV_10]` (`$V:272`) | `+ current_primary_event_for_display = 104` (`$U:314`); `dota_tv_delay` at `$U:276`; max field still 132 | [`56032bb7`](https://github.com/SteamDatabase/GameTracking-Dota2/commit/56032bb72f94ad945c2d2f54e569889849b5b89b), 2025-08-05 | Added field | VERIFIED |
| `CSODOTALobby.LobbyType` | `INVALID(-1)` … `FEATURED_GAMEMODE=15` | `+ AUTOMATED_BOT_ONLY_MATCH = 16` (`$U:239`) | [`b25cf388`](https://github.com/SteamDatabase/GameTracking-Dota2/commit/b25cf388af98e477a008b27c3cdfaef4ecc5e324), 2025-11-10 | Added value | VERIFIED |
| `CSODOTALobbyMember` (14), `CLobbyTeamDetails` (14) | — | identical | none | — | VERIFIED |
| `EMatchOutcome` | `Unknown=0, RadVictory=2, DireVictory=3, … NotScored_*=64-69` | identical (`$U/dota_shared_enums.proto:410`) | none | — | VERIFIED |
| `DOTA_GC_TEAM` (0-14) | `GOOD_GUYS=0, BAD_GUYS=1, BROADCASTER=2, SPECTATOR=3, PLAYER_POOL=4, NOTEAM=5, …` | identical (`$U:55`) | none | — | VERIFIED |
| `ELeagueStatus` (0-6) | `UNSET … ACCEPTED=3 … DELETED=6` | identical (`$U:476`) | none | — | VERIFIED |
| `ELeagueTier` (0-9) | — | identical (`$U:496`) | none | — | VERIFIED |
| `ELeagueRegion` (0-6) | — | identical (`$U:486`) | none | — | VERIFIED |
| `CMsgDOTALeagueInfo` (9 fields) / `CMsgDOTALeagueInfoList` | `$V/…common_league.proto:182,194` | identical (`$U:186,198`) | none | — | VERIFIED |
| `CMsgDOTALeagueInfoListAdminsRequest {}`, `CMsgDOTALeagueAvailableLobbyNodes[Request]`, `.NodeInfo` | — | identical | none | — | VERIFIED |
| `CMsgDOTALeagueNodeGroup.TeamStanding` (not used by the plan) | `score_tiebreak_group/below/random = 11/12/13` | replaced by `tiebreak_*`; `+ win_loss_limit = 27` | [`404d1ab1`](https://github.com/SteamDatabase/GameTracking-Dota2/commit/404d1ab185308e9f9da26f89888c7a8e1cd5302e), 2025-07-10 | Fields removed and added | VERIFIED; does not affect the plan |
| `CMsgGenericResult` (7055 body) | `eresult = 1 [default=2]`, `debug_message = 2` | identical (`$U/econ_shared_enums.proto:37`) | none | — | VERIFIED |
| `DOTALobbyVisibility`, `LobbyDotaPauseSetting`, `DOTASelectionPriorityRules`, `DOTA_CM_PICK`, `DOTABotDifficulty` | — | identical | none | — | VERIFIED |
| `DOTA_GameMode` | up to 25 | `+ DOTA_GAMEMODE_BOT_CHALLENGE = 26` (`$U/dota_shared_enums.proto:35`) | `b25cf388`, 2025-11-10 | Added value | VERIFIED |
| `EDOTAGCMsg` 7038/7040/7041/7044/7046/7047/7055/7434/7633/7634/7650/7651/8073/8074 | `$V/dota_gcmessages_msgid.proto:17-26,167,273-279,368-369` | identical (`$U/dota_gcmessages_msgid.proto:14-23,164,270-276,365-366`) | none | — | VERIFIED |
| `CMsgClientHello.secret_key = 5` | present | removed | [`54772daf`](https://github.com/SteamDatabase/GameTracking-Dota2/commit/54772dafdae956aaf2709cb87fcc0c5e59adad2f), 2026-07-17 | Removed field | VERIFIED. go-dota2 never sets it (`client_session.go:32-35`), so there is no impact |
| Server region IDs | no proto enum (`server_region` is `uint32`) | still no enum | n/a | n/a | See §4 |

### 2.1 Wire effect of the TV-delay renumbering (VERIFIED by a local probe)

I ran a probe in a scratch module (`/tmp/gap04go/main.go`) against the pinned go-dota2 and
`google.golang.org/protobuf v1.36.6`, using Go 1.27.1:

```text
wire=1 has=true get=LobbyDotaTV_120 (1) unknownBytes=0   # GC means 60 s
wire=3 has=true get=LobbyDotaTV_900 (3) unknownBytes=0   # GC means 300 s
wire=4 has=true get=4 (4) unknownBytes=0                 # GC means 900 s; no name
SetDetails{_120} wire bytes=c0 01 01                     # field 24 = 1  -> 60 s today
```

The read-back of `CSODOTALobby.dota_tv_delay` returns no error, but its *names* are wrong. Any
check like "requested `_120`, read back `_120`" passes even though both sides are 60 s. The plan's D6
approach (integer table keyed by seconds, `10→0, 60→1, 120→2, 300→3, 900→4`) is correct. Its enum-drift
test (plan:855, :467) must pin to the **GameTracking SHA** (`c592bf50` or later), not to "the generated
enums of the proto generation in use". Against the vendored generation, that test would *enforce* the stale
numbering.

## 3. Localization keys

The localization file was not tracked in GameTracking between
[`8da23ad9`](https://github.com/SteamDatabase/GameTracking-Dota2/commit/8da23ad958459da9c5f2ad4a79adef4085ffea10)
("Remove extracted pak01", 2023-02-16) and
[`3f630cc1`](https://github.com/SteamDatabase/GameTracking-Dota2/commit/3f630cc18bfa63f63b3ce259be52209d922558c3)
(re-added, 2026-10-07). Changes in that window cannot be dated more precisely than "between those two
commits". The comparison below uses `a3d1ba29` (2023-02-06) against `HEAD`.

| Key | 2023-02-06 (`a3d1ba29`) | Current (`loc:` line) | Change |
|---|---|---|---|
| `DOTA_Lobby_Settings_NonLobbyDelay` | "Non-league games are restricted to a two minute delay." (l.22137) | same text (l.20606) | none (VERIFIED) |
| `NotLeagueAdmin` | "You must have activated the specified league's Admin Pass to apply it to this lobby." (l.3721) | same (l.3528) | none (VERIFIED) |
| `DOTA_LeagueUnsetMessage` | "No league is set for this lobby, but you're able to set one.  Really start the match?" (l.22315) | same (l.20791) | none (VERIFIED) |
| `DOTA_Lobby_Settings_Dota_TV_Delay_*` | `_2_Minutes`, `_5_Minutes`, `_10_Seconds`, `_15_Minutes` (l.22258-22262) | **`+ _1_Minute` "1 Minute"** (l.20734), plus the same four (l.20733-20738) | Added value. Its first appearance in GameTracking is 2026-10-07 only because of the tracking gap |
| `DOTA_Lobby_TV_Delay_10/_120/_300` (legacy) | l.2826-2828 | l.2632-2634 | none. There was never a `_60`/`_900` variant |

Client binary strings, which are tracked continuously (VERIFIED, `c592bf50` diff, then `HEAD:game/dota/bin/win64/client_strings.txt:49217-49222`):
`LobbyDotaTV_60` and `DotaTVDelayDropdown_1_Minute` were added in the **same** commit as the renumbering
(2026-08-12). The dropdown now has 10 s, 1 min, 2 min, 5 min and 15 min. `#DOTA_LeagueUnsetMessage` (l.2717)
and `#NotLeagueAdmin` (l.4532) are still referenced from the binary. `NonLobbyDelay` does **not** appear
in `client_strings.txt`. It is either referenced only from Panorama layouts (not tracked) or unused.
**UNCERTAIN.**

**Is "restricted to a two minute delay" outdated?** **UNCERTAIN.** Two readings fit the evidence.
(a) 60 s is a new league-only option, and the string still holds: the minimum for non-league is 2 min.
(b) 60 s is offered to everyone, and the string is stale.
The web gave no patch note either way. A mid-2026 community guide and Liquipedia still list only
10 s/2/5/15 min ([dota2lobby.com](https://dota2lobby.com/casting-spectating-basics-dota-2-community-games/),
[Liquipedia Lobby](https://liquipedia.net/dota2/Lobby)), but both predate or ignore patch 6898.
**Live test:** create a *non-league* lobby with wire `dota_tv_delay = 1` and another with `= 0`, then read back
`CSODOTALobby.dota_tv_delay` (field 53). The test is cheap, and it also settles the server-side enforcement
question in 02 §6.

## 4. Server region values

`server_region` has no proto enum in either tree (VERIFIED). The game's `scripts/regions.txt` was tracked
until 2023-02-16. The last tracked copy (`8da23ad9^`) gives (VERIFIED for 2023):
`0 unspecified, 1 USWest, 2 USEast, 3 Europe, 5 Singapore, 6 Dubai, 7 Australia, 8 Stockholm, 9 Austria,
10 Brazil, 11 SouthAfrica, 12 PW Telecom (Shanghai), 13 PW Unicom, 14 Chile, 15 Peru, 16 India,
17 PW Telecom Guangdong, 18 PW Telecom Zhejiang, 19 Japan, 20 PW Telecom Wuhan, 25 PW Unicom Tianjin,
37 Taiwan, 38 Argentina`.

Compared with the node-dota2 table in 01 §7.10:
- `4 KOREA` is **absent** from the 2023 file.
- 25, 37 and 38 are missing from 01.

Current `loc:3595-3632` adds a `dota_region_warsaw` display key. Its region ID is **UNKNOWN**
(regions.txt is no longer tracked). No evidence suggests that any existing ID was renumbered. However,
regions are not protobuf data, so this audit cannot prove that for 2026. The plan already keeps regions
operator-configurable (plan:467, :772), which is the right mitigation. `us_west 1, us_east 2, europe 3, sea 5`
matches the 2023 file (VERIFIED for 2023).

## 5. Stale or contradictory places in the docs

The task quoted "02:132". The "boxed_type only, current" claim is actually at **02:150-151**. 02:132 is a
filter/date-window UNCERTAIN note and is still accurate.

| File:line | Current text (short) | Problem | Fix |
|---|---|---|---|
| `01-lobby-protocol.md:220` | `LobbyDotaTV_10(0)`, `_120(1)`, `_300(2)`, `_900(3)` | Stale numbering; no 60 s | Use current `10=0, 60=1, 120=2, 300=3, 900=4` (since `c592bf50`, 2026-08-12) and note that the vendored constants are off by one |
| `01-lobby-protocol.md:282` | `DotaTvDelay: protocol.LobbyDotaTVDelay_LobbyDotaTV_120.Enum()` | Sends 1, which is **60 s** | `protocol.LobbyDotaTVDelay(2) // 120 s, current numbering` or the D6 table |
| `01-lobby-protocol.md:398-400` | `LobbyType` list ends at `FEATURED_GAMEMODE(15)` | Missing `AUTOMATED_BOT_ONLY_MATCH(16)` (`b25cf388`) | Add the value (harmless) |
| `01-lobby-protocol.md:439` | `dota_tv_delay` read-back default `LobbyDotaTV_10`; "differs from SetDetails default (`_120`)" | The default is still 0 = 10 s (correct), but read-back *names* are wrong under the vendored enum (§2.1) | Add: "decode as an integer with the current table; vendored names mislabel 1/2/3 and do not name 4" |
| `01-lobby-protocol.md:596` | §7.8 `LobbyDotaTVDelay` … `_120(1)`, `_300(2)`, `_900(3)` | Stale | Same as 01:220 |
| `01-lobby-protocol.md:609-628` | §7.10 region table (node-dota2) | `4 KOREA` absent in the 2023 regions.txt; 25/37/38 missing; Warsaw ID unknown | Replace with the §4 list, dated 2023-02 |
| `01-lobby-protocol.md:1135` | "All enum values in §7.1-7.9 … and `LobbyType`" listed as verified | No longer true for §7.8 TV delay or for LobbyType 16 | Qualify: "verified against the vendored pin (= GameTracking `ab1f047d`, 2025-06-17)" |
| `02-league-tickets.md:93` | NonLobbyDelay: "(120, 300 and 900 s stay allowed). Only league lobbies get `LobbyDotaTV_10` (`…common_lobby.proto:14-19`)" | Omits the 60 s option; cites stale numbering | Add 60 s (`_60 = 1`) and mark its league-only status UNCERTAIN (§3) |
| `02-league-tickets.md:150-151` | "GameTracking-Dota2 (2026-11-06) has the same IDs and fields, with only `boxed_type` annotations added, so the module's protos are current for this area." | True for 7633/7634/7650/7651 and the league messages. **False** for the lobby fields the same doc relies on (TV delay renumbered; `CMsgPracticeLobbyLaunch.nonce`) and for `CMsgDOTALeagueNodeGroup` (`404d1ab1`) | Narrow the claim to the league admin/info/available-nodes messages and IDs, and link this gap doc |
| `02-league-tickets.md:200` | `dota_tv_delay = 24` (`LobbyDotaTV_10/120/300/900`) | Missing 60; numbering stale | `10/60/120/300/900 = 0..4 (current)` |
| `02-league-tickets.md:352` | `DotaTvDelay: protocol.LobbyDotaTVDelay_LobbyDotaTV_120.Enum(), // _10 allowed only for league lobbies` | Sends **60 s** | Same as 01:282 |
| `02-league-tickets.md:377` | "Non-league lobbies have a minimum of 120 s (120/300/900 s allowed)…`common_lobby.proto:14-19`" | Missing 60 s; stale cite | Add "60 s exists since 2026-08-12; whether non-league may use it is UNCERTAIN" and extend the live test to wire values 0 and 1 |
| `02-league-tickets.md:406` | "matching upstream GameTracking-Dota2 on 2026-11-06" | Accurate for the IDs and league messages listed; fine if scoped | Keep, but scope it as for 150 |
| `02-league-tickets.md:499` | Verification note: `LobbyDotaTV_10 = 0`, `_120`, `_300`, `_900` | Stale enum list | Update the list |
| `docs/plans/2026-11-06-lobby-service-architecture.md:467`, `:855` | "A unit test pins each table to the proto generation in `go.mod`"; drift test vs "generated enums of the proto generation in use" | With the vendored pin, this test would assert the *stale* mapping | Pin the table to a GameTracking SHA (`c592bf50`+) via a golden file. Fail if the generated enum disagrees **and** the generation is older than that SHA |
| `…lobby-service-architecture.md:661` | `10, 60, 120, 300, 900`; "`10` requires `league_id`" | The rule for `60` on non-league lobbies is unresolved; per the UI string, 60 s might also need a league | Mark `60` without `league_id` as UNCERTAIN pending the §3 live test |
| `03-go-dota2-internals.md:428` (§8.1 table) | `dota_gcmessages_client.proto`: "nothing lobby or league critical in the diff sample" | Confirmed by the full walk (PopupID unchanged) | No change; optionally cite this doc |
| `03-go-dota2-internals.md:432` | "since SteamDatabase `c983b3e16435`" | That is a SteamDatabase/**Protobufs** SHA. The GameTracking SHA is `c592bf50` (same day) | Add the GameTracking SHA |
| `06-match-results.md:32`, `:556` | `CSODOTALobby` numbering unchanged; 132 still the highest field | **Still correct** (VERIFIED; only 104 was added) | None |
| `README.md:68`, `:172`, `:203` | Renumbering summary | Correct | Optionally add "only renumbering found; see gap-04" |

## 6. Verified vs uncertain

**VERIFIED**
- Vendored protos = GameTracking `ab1f047d` (2025-06-17) at symbol level, for the seven relevant files.
- Exactly one renumbering (`LobbyDotaTVDelay`, `c592bf50`, 2026-08-12) across all 2,255 shared symbols. All
  other changes to plan-relevant symbols only add things (§1 output).
- `NOT_LEAGUE_ADMIN = 27`, `DOTAJoinLobbyResult`, `CSODOTALobby.State`, `EMatchOutcome`, `DOTA_GC_TEAM`,
  `ELeague*`, `CMsgDOTALeagueInfo*`, `CMsgPracticeLobbySetDetails` and the listed message IDs are unchanged
  through 2026-11-05.
- The vendored Go decoder keeps unknown value 4 and mislabels 1 and 3 (local probe).
- The three localization strings are unchanged in text. `1 Minute` and `DotaTVDelayDropdown_1_Minute` exist now.

**UNCERTAIN / INFERRED**
- Whether non-league lobbies may use 60 s, and whether `NonLobbyDelay` is still shown at all (it has no binary reference).
- When exactly the `_1_Minute` localization string was added. INFERRED: 2026-08-12, together with the binary strings.
- Server region IDs after 2023-02 (Warsaw ID unknown; Korea 4 status unknown).
- Whether `CMsgPracticeLobbyLaunch.nonce` is required (unchanged from 03 §8.3).

## Is the gap resolved?

**Mostly yes.** The per-symbol, commit-dated audit is complete. It confirms that TV delay is the only
renumbering and that league verification and seat logic are safe on the wire. Stale doc locations are listed
in §5 for a follow-up edit. Two items remain open, and only a live GC test can close them: 60 s on non-league
lobbies, and `nonce`. Region IDs after 2023 also stay operator-maintained.

## Sources

- GameTracking-Dota2 commits:
  [c592bf50](https://github.com/SteamDatabase/GameTracking-Dota2/commit/c592bf50cbc4524fe8e5f4f5df1e44d5911692e6),
  [e84714d6](https://github.com/SteamDatabase/GameTracking-Dota2/commit/e84714d6ff871cfae0b59f3f9038860f052fa247),
  [b25cf388](https://github.com/SteamDatabase/GameTracking-Dota2/commit/b25cf388af98e477a008b27c3cdfaef4ecc5e324),
  [56032bb7](https://github.com/SteamDatabase/GameTracking-Dota2/commit/56032bb72f94ad945c2d2f54e569889849b5b89b),
  [404d1ab1](https://github.com/SteamDatabase/GameTracking-Dota2/commit/404d1ab185308e9f9da26f89888c7a8e1cd5302e),
  [54772daf](https://github.com/SteamDatabase/GameTracking-Dota2/commit/54772dafdae956aaf2709cb87fcc0c5e59adad2f),
  [ab1f047d](https://github.com/SteamDatabase/GameTracking-Dota2/commit/ab1f047da124125eaf3b605f8ed35d3a30a0a715),
  [8da23ad9](https://github.com/SteamDatabase/GameTracking-Dota2/commit/8da23ad958459da9c5f2ad4a79adef4085ffea10),
  [3f630cc1](https://github.com/SteamDatabase/GameTracking-Dota2/commit/3f630cc18bfa63f63b3ce259be52209d922558c3)
- SteamDB patch notes for 6898: <https://steamdb.info/patchnotes/24700034/>; 6941: <https://steamdb.info/patchnotes/25539253/>
- Liquipedia Lobby: <https://liquipedia.net/dota2/Lobby>; community guide: <https://dota2lobby.com/casting-spectating-basics-dota-2-community-games/>
- Local: `$V/*.proto`, `$V/../client_session.go:32-35`, scratch scripts `/tmp/gap04scripts/{syms,cmp,all,hist}.py`, probe `/tmp/gap04go/main.go`
