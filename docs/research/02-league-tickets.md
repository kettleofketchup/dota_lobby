# 02 - League / Tournament Tickets and the Lobby "League" Drop-down

> Research track: how a Steam bot account gets a league selectable in the practice-lobby
> **League** drop-down, how many it can have, how to list them programmatically, and what
> changes when a lobby is a league lobby.
>
> Researched 2026-11-06 against go-dota2 `v0.0.0-20250623204622-532cb1851217`, upstream
> SteamDatabase/GameTracking-Dota2 protobufs and localization (fetched the same day), live
> `www.dota2.com/webapi` endpoints, and open-source GC client libraries. The GC calls were run
> live on 2026-11-08 ([09 - Live probe](09-live-probe.md)).
>
> Paths:
>
> - `go-dota2@532cb1851217/...` is relative to the pinned go-dota2 module root; `protocol/...` is its `protocol/` directory
> - `loc:` = upstream `game/dota/pak01_dir/resource/localization/dota_english.txt` (GameTracking-Dota2, master, fetched 2026-11-06)

---

## 1. TL;DR

1. **A "league ticket" today is effectively a league ID that your account administers.**
   You create a league at <https://www.dota2.com/league/> and add the bot's Steam account
   under **Admins**. The IESF organizer guide (2024) describes admins as the accounts
   "who will have access to the league ticket and will be hosting the lobbies". No econ
   item is needed any more for free-to-spectate Amateur/Professional leagues. Valve
   dropped that requirement in the 6 July 2017 update.
2. **The GC tells the client which leagues it administers.** It does this with
   `k_EMsgDOTALeagueInfoListAdminsRequest` (7633), and the reply is
   `k_EMsgDOTALeagueInfoListAdminsReponse` (7634, Valve's typo) carrying a
   `CMsgDOTALeagueInfoList`. Each `CMsgDOTALeagueInfo` in it is one entry the drop-down can show.
   VERIFIED live 2026-11-08: the test bot got its one administered league back, and the GC echoes
   the job ID, so go-dota2 master `MakeRequest` completes ([09](09-live-probe.md) §4).
3. **go-dota2 has no generated wrapper for 7633/7634.** The apigen override marks it
   `MsgSenderNone`. The exported `(*Dota2).MakeRequest` and the existing protobuf types
   make it about 10 lines (see section 7.1).
4. **Multiple leagues per bot is supported.** The response is a `repeated` list and the
   choice is made per lobby
   through `CMsgPracticeLobbySetDetails.leagueid` (field 16). The optional bracket node goes in
   `league_node_id` (field 49), and its valid values come from
   `RequestLeagueAvailableLobbyNodes(leagueID)`, which go-dota2 wraps.
5. **`CMsgDOTAProfileTickets` / `GetProfileTickets` is a different thing.** It lists the
   league passes (econ items) the account owns, which is what the profile "Tickets" page
   shows. Owning a pass is about viewing or compendium access and does not give admin rights.
6. **Public web API.** `www.dota2.com/webapi/IDOTA2League/GetLeagueInfoList/v001` returns
   every accepted or concluded league (10,166 entries on 2026-11-06). `GetLeagueData` returns
   the full `CMsgDOTALeague` but with **`admins` blanked**, so the web API cannot tell you which
   leagues a bot administers. Only the GC call can. The old Steam Web API
   `IDOTA2Match_570/GetLeagueListing` now returns **404**.

---

## 2. Concepts: ticket vs pass vs admin vs league

| Term | What it is | Where it lives | Gives the right to host a league lobby? |
|---|---|---|---|
| **League** | A record with a numeric `league_id`, name, tier, region, dates, status, admins, streams, node groups (bracket) | Valve's league DB; proto `CMsgDOTALeague` (`protocol/dota_gcmessages_common_league.proto:107`) | n/a |
| **League admin** | A Steam account listed in `CMsgDOTALeague.admins` (`account_id`, `is_primary`, `email_address`; `:125-129`) | Managed at dota2.com/league ("Admins" tab) | **Yes.** This is the current mechanism (admin role VERIFIED by node-dota2 docs and the IESF guide; a league create by an admin bot was accepted live, [09](09-live-probe.md) §5) |
| **"League ticket" (community usage)** | Organizers and bots use the term for "a league ID my account can host", i.e. an entry of the 7634 admin list | n/a | Same thing as admin rights |
| **League pass / ticket item (econ)** | A purchasable or giftable inventory item tied to a league, used for paid spectating, compendium, cosmetics. Listed by `CMsgDOTAProfileTickets.LeaguePass{league_id,item_def}` (`protocol/dota_gcmessages_client.proto:338-347`). Inventory object type `CSOEconItemTournamentPassport` (`protocol/dota_gcmessages_common.proto:1631`) | Steam inventory / SOCache | **No.** It covers viewing and access only. Legacy exception: "Admin Pass", see below |
| **Admin Pass (legacy)** | Pre-2017 admins activated an *Admin Pass* item. The GC error string still reads: "You must have activated the specified league's Admin Pass to apply it to this lobby." (`loc:3528`, key `NotLeagueAdmin`) | Historical | Historically yes. Not needed for modern web-managed leagues (INFERRED from the 2017 change and from modern guides never mentioning it) |
| **Steam "tourney" license** | Free legal licence for running a tournament (`store.steampowered.com/tourney`) | Steam store | No. It is a legal and broadcasting matter and unrelated to the GC drop-down |

### 2.1 How ticketing changed over time (evidence-backed)

| When | Change | Evidence |
|---|---|---|
| 2012-2016 | Leagues were sold as **ticket items** in the Dota store. Organizers needed a League Workshop item, and "Premium Leagues divert a portion of their in-game ticket sales directly into their prize pool". Admins were managed on a league admin page. Ticket sign-ups needed 15 days' notice, and tickets with items needed 45 days | `loc:12810` (`league_premium_desc`); `loc:12690-12694` (Premium league descriptions, e.g. joinDOTA League, Champions League S2); Steam Support KB "Dota 2 - League Tickets" (as quoted in search snippets; the page body now renders empty) |
| **6 Jul 2017** | "Amateur and Professional leagues that are free-to-spectate no longer require a league ticket item to be shipped in the client. Instead, once approved, league information will automatically be downloaded directly from the web." | Steam News, *Dota 2 Update - July 6th, 2017* (store.steampowered.com/news, appid 570) |
| 2017-2023 (DPC era) | Tiers in the proto/web form: Amateur, Professional, Minor, Major, International, DPC Qualifier, DPC League Qualifier, DPC League, DPC League Finals | `ELeagueTier` `protocol/dota_shared_enums.proto:529-540`; IESF guide screenshot of the dota2.com/league form (Amateur / Professional / DPC League Qualifier / DPC League / DPC League Finals / DPC Major) |
| 2024 (current) | Create at dota2.com/league: (1) "Add Admin Info" for the head admin, (2) "Create New Tournament" (name, tier, region, URL, description, notes, dates), (3) fill in the tabs Overview / Prize Pool / **Admins** / Streams / Graphics / Messages / Summary, (4) submit. Status goes Unsubmitted, then Submitted, then Accepted/Rejected. "Leagues will receive an automatic rejection or acceptance e-mail the week prior to their event starting." Organizers report no fee | IESF *DOTA 2 Organizer's Guideline* (Feb 2024), pages 5-12; `ELeagueStatus` `protocol/dota_shared_enums.proto:509-517` |
| 2026-11 (observed) | Amateur leagues are still being created and accepted. The newest have league IDs around 20.3k and start dates in Dec 2026 / 2027. The public list holds only status 3 (ACCEPTED, 791) and 5 (CONCLUDED, 9,375). Tier mix: Amateur 7,485; Professional 2,456; others small | Live `GetLeagueInfoList` call, 2026-11-06 |

The term **"Premium"** shows up in two places: Valve's old ticket-sale leagues (`loc:12810`)
and datdota's community tier label. **No current `ELeagueTier` value is named Premium.**
The proto tiers are Amateur(1), Professional(2), Minor(3), Major(4), International(5) and DPC*(6-9).

---

## 3. What decides which leagues appear in the drop-down

### 3.1 Client-side UI evidence (localization strings)

| Key (`loc:` line) | Text | Meaning |
|---|---|---|
| `DOTA_Lobby_Settings_Tournament` (20759) | "League" | Label of the drop-down |
| `DOTA_Lobby_Settings_Tournament_None` (20758) | "None" | Default entry |
| `DOTA_Lobby_Settings_Tournament_Series` / `_Game` (20760-20761) | "Series", "Game in Series" | Extra league-lobby fields (map to `series_type`, `radiant_series_wins`/`dire_series_wins`) |
| `DOTA_Lobby_League_Admin` (2581) | "League To Admin" | Older label for the same control. It ties the drop-down to *admin* leagues |
| `DOTA_LeagueUnsetMessage` (20791) | "No league is set for this lobby, but you're able to set one. Really start the match?" | The client knows whether the host has at least one eligible league |
| `NotLeagueAdmin` (3528) / popup `NOT_LEAGUE_ADMIN = 27` (`protocol/dota_gcmessages_client.proto:171`) | "...activated the specified league's Admin Pass..." | GC rejects a `leagueid` the host does not administer |
| `DOTA_Lobby_Settings_NonLobbyDelay` (20606) | "Non-league games are restricted to a two minute delay." | Two minutes is the *minimum* for non-league lobbies (120, 300 and 900 s stay allowed). Only league lobbies get 10 s. A **60 s** option (`_60 = 1`) exists since 2026-08-12; whether non-league lobbies may use it is UNCERTAIN ([gap-04](gap-04-proto-freshness-audit.md) §3). This is a client UI string; server-side enforcement is UNCERTAIN |

### 3.2 Protocol evidence

1. **Admin list → drop-down entries.** `CMsgDOTALeagueInfoListAdminsRequest {}` is an empty request
   (`protocol/dota_gcmessages_common_league.proto:233`). Its response decodes as
   `CMsgDOTALeagueInfoList` (VERIFIED live, [09](09-live-probe.md) §4; the Java GC client
   hudi1/dota2-java-game-coordiator pairs the types the same way). Each `CMsgDOTALeagueInfo`
   (`:182-192`) has `league_id, name, tier, region, most_recent_activity, total_prize_pool,
   start_timestamp, end_timestamp, status`. That is exactly what a drop-down needs.
2. **Admin flag on the account.** `CSODOTAGameAccountClient.is_league_admin` (field 57,
   `protocol/dota_gcmessages_common.proto:164`) arrives in the SOCache (type 2002,
   `go-dota2@532cb1851217/cso/cso_types.go:35,59-61`). A server-side `k_EMsgSQLSetIsLeagueAdmin = 7630`
   exists (`protocol/dota_gcmessages_msgid.proto:270`). It is a cheap "does this bot have *any*
   league?" check before calling the admin-list request (VERIFIED live: `true` for the admin test bot).
3. **Push message.** `k_EMsgGCLeagueAdminList = 7434` carries `CMsgLeagueAdminList { repeated
   uint32 account_ids }` (`protocol/dota_gcmessages_common.proto:377-379`). go-dota2 emits it as
   `events.LeagueAdminList` (`go-dota2@532cb1851217/events/generated.go:661`, handler
   `go-dota2@532cb1851217/client_generated.go:7671`). The field is account IDs, not league IDs, so it is
   probably the admin roster of a league (for example the lobby's league) and not the bot's league list.
   **UNCERTAIN.** Nobody documents when it is sent. The Java client only logs it.
4. **Bracket nodes.** `CMsgDOTALeagueAvailableLobbyNodesRequest{league_id}` (7650) returns
   `CMsgDOTALeagueAvailableLobbyNodes{ repeated NodeInfo{node_id, node_name, node_group_name,
   team_id_1, team_id_2} }` (7651) (`:236-250`). These are the series or bracket slots a lobby in
   that league can be attached to. The selected one goes in `league_node_id`.

### 3.3 Conclusion (with confidence levels)

- **VERIFIED:** an account must be a league admin for `leagueid` on a lobby to work. Sources:
  node-dota2 README ("The bot should be a league admin for this to work"), IESF guide
  ("admins who will have access to the league ticket and will be hosting the lobbies"),
  and GC error `NOT_LEAGUE_ADMIN`.
- **LIKELY:** the drop-down contents equal the `CMsgDOTALeagueInfoList` the GC returns for 7633.
  It is the only client-reachable message that lists "leagues I administer", and the old label was
  "League To Admin". **UNCERTAIN:** whether the GC filters that list by status (only ACCEPTED, or
  also UNSUBMITTED/SUBMITTED) or by date window (ended leagues hidden). The live run returned
  one ACCEPTED league, so it does not settle this (Q3).
- **LIKELY:** an *unapproved* league cannot be used yet. The IESF guide says approval email
  arrives the week before the start date.
- **VERIFIED that it is not the mechanism:** econ league passes (`GetProfileTickets`). Paid
  spectating is gated by passes: watch result `MISSINGLEAGUESUBSCRIPTION = 6`
  (`protocol/dota_gcmessages_client_watch.proto:158`), "You don't have access to this Tournament.
  Would you like to purchase a ticket?" (`loc:7431`).

---

## 4. Message inventory (grep of all protos in `protocol/`)

Grep used: `grep -n -i -E '^message .*(league|ticket|tournament|admin)' protocol/*.proto` plus
`grep -n -i 'league|ticket|tournament' protocol/dota_gcmessages_msgid.proto`. Upstream
GameTracking-Dota2 (2026-11-06) has the same IDs and fields, with only `boxed_type` annotations
added, so the module's protos are current for the league admin/info/available-nodes messages and IDs
(7633/7634/7650/7651). This does **not** extend to the lobby fields used later in this doc (TV delay was
renumbered; `CMsgPracticeLobbyLaunch.nonce` was added) or to `CMsgDOTALeagueNodeGroup` (`404d1ab1`). See
[gap-04](gap-04-proto-freshness-audit.md).

### 4.1 Client-usable GC messages

| EMsg (id) | Direction | Proto type | go-dota2 wrapper | Notes |
|---|---|---|---|---|
| `k_EMsgDOTALeagueInfoListAdminsRequest` (7633) | C→GC | `CMsgDOTALeagueInfoListAdminsRequest {}` (`common_league.proto:233`) | **None.** `apigen/msg_overrides.go:98` sets `MsgSenderNone` | Use `MakeRequest` (section 7.1) |
| `k_EMsgDOTALeagueInfoListAdminsReponse` (7634) *(sic)* | GC→C | `CMsgDOTALeagueInfoList{repeated CMsgDOTALeagueInfo infos}` (`:194`) | None (`msg_overrides.go:99`) | Type pairing VERIFIED live ([09](09-live-probe.md) §4) |
| `k_EMsgDOTALeagueAvailableLobbyNodesRequest` (7650) | C→GC | `CMsgDOTALeagueAvailableLobbyNodesRequest{league_id}` | `RequestLeagueAvailableLobbyNodes(ctx, leagueID)` `client_generated.go:3519` | Request/response paired by apigen (`apigen/snapshot-apigen.txt:638-644`) |
| `k_EMsgDOTALeagueAvailableLobbyNodes` (7651) | GC→C | `CMsgDOTALeagueAvailableLobbyNodes` | (also a meaningless `SendLeagueAvailableLobbyNodes` at `:5491`) | |
| `k_EMsgClientToGCGetProfileTickets` (8073) | C→GC | `CMsgClientToGCGetProfileTickets{account_id}` (`client.proto:349`) | `GetProfileTickets(ctx, accountID)` `client_generated.go:1371` | Lists owned league passes, not admin rights |
| `k_EMsgClientToGCGetProfileTicketsResponse` (8074) | GC→C | `CMsgDOTAProfileTickets{result, account_id, repeated LeaguePass{league_id,item_def}}` (`client.proto:338`) | (paired) | |
| `k_EMsgGCLeagueAdminList` (7434) | GC→C push | `CMsgLeagueAdminList{repeated uint32 account_ids}` (`common.proto:377`) | event `events.LeagueAdminList` | Semantics uncertain (section 3.2 point 3) |
| `k_EMsgUpgradeLeagueItem` / `Response` (7203/7204) | C→GC | `CMsgUpgradeLeagueItem{match_id, league_id}` (`client.proto:443`) | (generated) | Legacy item upgrade, not relevant |
| `k_EMsgClientToGCTopLeagueMatchesRequest` (8036) / `k_EMsgGCToClientTopLeagueMatchesResponse` (8061) | C↔GC | `CMsgClientToGCTopLeagueMatchesRequest` (`client_watch.proto:74`) | `RequestTopLeagueMatches` | Watch tab |
| `k_EMsgGCPracticeLobbyCreate` / `k_EMsgGCPracticeLobbySetDetails` | C→GC | `CMsgPracticeLobbyCreate{lobby_details}` / `CMsgPracticeLobbySetDetails` | `CreateLobby` (`client_lobby.go:13`), `LeaveCreateLobby`, `SetLobbyDetails` (`client_generated.go:6841`) | All fire-and-forget. Confirm through the SOCache lobby object |
| `k_EMsgGCPopup` | GC→C | `CMsgDOTAPopup` with `NOT_LEAGUE_ADMIN = 27`, `TOURNAMENT_GAME_*` (`client.proto:171`, `:165-170`) | event `events.Popup` (`events/generated.go:1060`) | Subscribe to this to detect league rejection |

### 4.2 GC-internal / server-only (not callable by a client)

`k_EMsgGCToGCGetLeagueAdmin(+Response)` 7255/7256, `k_EMsgGCToGCGrantTournamentItem` 7372,
`k_EMsgSQLDelayedGrantLeagueDrop` 7496, `k_EMsgSQLGrantLeagueMatchToTicketHolders` 7592,
`k_EMsgSQLSetIsLeagueAdmin` 7630, `k_EMsgGCToGCGetLiveLeagueMatches(+Response)` 7631/7632,
`k_EMsgGCToGCLeagueMatchStarted/Completed(+Response)` 7645-7647,
`k_EMsgGCToGCLeagueRequest/Response` 7652/7653, `...LeagueNodeGroupRequest/Response` 7654/7655,
`...LeagueNodeRequest/Response` 7656/7657, `k_EMsgGC_TournamentItemEvent(+Response)` 7150/7151,
`k_EMsgSQLProcessTournamentGameOutcome` 7525 (all `protocol/dota_gcmessages_msgid.proto:76-285`).
The Java client still references a client-level `k_EMsgDOTALeagueNodeRequest`, which **no longer
exists** in current protos. Only the GCToGC variant remains, so per-node detail now has to come
from the web API.

### 4.3 League data messages (shapes shared by GC and web API)

`CMsgDOTALeague` (`:107`, nested `Info`, `Admin`, `PrizePool`, `Stream`, `SeriesInfo`,
`Player`), `CMsgDOTALeagueList` (`:178`), `CMsgDOTALeagueInfo`/`InfoList` (`:182`/`:194`),
`CMsgDOTALeagueNode` (`:27`; `node_id`, `node_group_id`, `node_type` Bo1/Bo2/Bo3/Bo5,
`team_id_1/2`, `series_id`, `matches[]`), `CMsgDOTALeagueNodeGroup` (`:61`; `node_group_type`
ROUND_ROBIN/SWISS/BRACKET_*/GSL/SHOWMATCH/PLACEMENT, nested `nodes[]`, `node_groups[]`),
`CMsgDOTALeagueLiveGames` (`:198`), `CMsgDOTALeagueMessages` (`:218`), `CMsgDOTALeaguePrizePool`
(`:228`), `CMsgDOTALeagueNodeResults` (`:252`). Enums are `ELeagueStatus` (`dota_shared_enums.proto:509`),
`ELeagueRegion` (`:519`), `ELeagueTier` (`:529`), `ELeagueTierCategory` (`:542`), `ELeaguePhase`
(`:562`), and `ELeagueAuditAction` (`:569`, which includes `LEAGUE_ADMIN_ADD/REVOKE/PROMOTE`).

### 4.4 League fields on lobbies

| Message | Field | Line |
|---|---|---|
| `CMsgPracticeLobbySetDetails` | `leagueid = 16` | `client_match_management.proto:137` |
| | `series_type = 20`, `radiant_series_wins = 21`, `dire_series_wins = 22` | `:140-142` |
| | `dota_tv_delay = 24` (current Valve: `10/60/120/300/900 s = 0..4`; vendored enum is stale, see gap-04) | `:144` |
| | `visibility = 33` (Public/Friends/Unlisted) | `:152` |
| | `selection_priority_rules = 46` | `:160` |
| | `league_node_id = 49` | `:163` |
| `CSODOTALobby` (SOCache readback) | `leagueid = 42`, `series_id = 60`, `league_node_id = 110`, `league_phase = 113` | `common_lobby.proto:265,279,314,316` |
| `CMsgPracticeLobbyListResponseEntry` | `league_id = 19` | `client_match_management.proto:245` |
| `CMsgDOTAMatch` | `leagueid = 22` | `common.proto:1063` |
| `CSourceTVGameSmall` | `league_id = 5` | `client_watch.proto:18` |

**There is no `league_selection_priority` field.** Selection priority lives in
`selection_priority_rules` and in the `series_*_selection_priority_*` fields on `CSODOTALobby`,
none of which are league-specific.

---

## 5. Multiple leagues per bot and across bots

- **One bot, many leagues: supported.** An account can be an admin of any number of leagues.
  The admin-list reply is `repeated`, so one account can hold several (LIKELY; not live-tested,
  since the test bot administers one league).
- **Per-lobby choice.** `leagueid` is a per-lobby field set at create time (in
  `CMsgPracticeLobbyCreate.lobby_details`) or later through `SetLobbyDetails`. Nothing ties a bot to a
  single league. (VERIFIED structurally. The GC accepting *any* administered league is LIKELY.)
- **Many bots, one league.** Add every bot account as an admin of the league.
- **Limits:** a bot can host only **one lobby at a time** (one `CSODOTALobby` in its SOCache).
  Concurrency comes from more bots, not from more leagues. Any cap on admins per league or leagues per
  admin is **UNKNOWN**, and none is documented.

Recommended model for this service:

```
bots ──< bot_leagues >── leagues
           (refreshed from GC 7633 on every GC welcome + on demand)
POST /lobby/create {league_id?, league_node_id?, ...}
  → pick a free bot whose bot_leagues contains league_id (or any bot if league_id omitted)
```

---

## 6. Listing leagues and tickets: every source compared

| Source | Auth | Returns | Tells you *which bot can host*? | Status (2026-11-06) |
|---|---|---|---|---|
| GC 7633 → 7634 `CMsgDOTALeagueInfoList` | Bot's GC session | Leagues this account administers | **Yes** | VERIFIED live 2026-11-08 ([09](09-live-probe.md) §4) |
| SOCache `CSODOTAGameAccountClient.is_league_admin` | Bot's GC session | bool | Only "any or none" | Field present in current proto |
| GC `RequestLeagueAvailableLobbyNodes(league)` | Bot's GC session | Hostable nodes (bracket slots) with team IDs | Per league | go-dota2 wrapper exists |
| GC `GetProfileTickets(accountID)` | Bot's GC session | Owned league **passes** (`league_id`, `item_def`) | **No** | wrapper exists |
| SOCache `CSOEconItem` (type 1) | Bot's GC session | Inventory items. League passes would appear here with a league attribute | No | `ItemTournamentPassport` (type 42) and `EventTicket` (40) are declared but **have no constructor** in `go-dota2@532cb1851217/cso/cso_types.go:55-86`, so go-dota2 won't decode them |
| `GET https://www.dota2.com/webapi/IDOTA2League/GetLeagueInfoList/v001` | none | JSON form of `CMsgDOTALeagueInfoList`, all ACCEPTED+CONCLUDED leagues | No | **Works.** 10,166 infos, IDs up to ~20.3k (one outlier 65019) |
| `GET .../IDOTA2League/GetLeagueData/v001?league_id=N` | none | JSON `CMsgDOTALeague` (info, prize_pool, **admins = []**, streams, node_groups, series_infos, registered_players) | **No** (admins redacted) | Works. Verified for 15728 (TI 2023) and 20310 (amateur) |
| `GET .../IDOTA2League/GetLeaguesData/v001?league_ids=N,...` | none | `{leagues:[CMsgDOTALeague...]}` | No | Works |
| `GET .../IDOTA2League/GetLiveGames/v001` | none | `CMsgDOTALeagueLiveGames` (league_id, league_node_id, series_id, match_id, server_steam_id...) | No | Works. Handy to confirm *your* league lobby went live |
| `GET .../IDOTA2League/GetPrizePool/v001?league_id=N` | none | `{prize_pool, increment_per_second}` | No | Works |
| `GET .../IDOTA2DPC/GetLeagueNodeData/v001?league_id=&node_id=`, `IDOTA2DPC/GetLeagueNodeGroupData/v001?league_id=&node_group_id=` | none | Node / node-group data | No | **Works** ([gap-03](gap-03-league-portal-webapi.md) §4). `GetLeagueNodes` does not exist. On `IDOTA2League`, an empty `text/html` 200 means "unknown method", which is what the earlier tries hit |
| `api.steampowered.com/IDOTA2Match_570/GetLeagueListing/v1` | key | (was: league list) | No | **404, removed** |
| `api.steampowered.com/IDOTA2Match_570/GetLiveLeagueGames/v1` | key | live league games | No | 403 without key (exists) |
| `api.steampowered.com/IDOTA2Match_570/GetMatchHistory?league_id=` | key | league matches | No | 403 without key (exists) |

Sample (truncated) live output:

```json
// GetLeagueInfoList
{"infos":[{"league_id":20310,"name":"NEVA Season 2027","tier":1,"region":4,
  "most_recent_activity":1789477844,"total_prize_pool":0,
  "start_timestamp":1798664400,"end_timestamp":1830200400,"status":3}, ...]}
// GetLeagueData?league_id=20310
{"info":{...,"status":3,"registration_period":11},"prize_pool":{...},"admins":[],
 "streams":[...],"node_groups":[],"series_infos":[],"registered_players":[]}
```

The web JSON for node groups includes a `win_loss_limit` field that is **not** in the go-dota2
proto, so the web schema is slightly ahead of the module. Decode web JSON with lenient
`encoding/json` structs, not `protojson` with `DiscardUnknown=false`.

---

## 7. Go implementation sketches (compile-checked)

The snippets below compile against the pinned `go.mod`. The 7633 call, the `is_league_admin` check,
the node request and a league create were run live on go-dota2 master ([09](09-live-probe.md)).

### 7.1 List the leagues a bot administers (the drop-down)

```go
import (
    dota2 "github.com/paralin/go-dota2"
    "github.com/paralin/go-dota2/protocol"
)

// ListAdminLeagues asks the GC which leagues this account administers.
// go-dota2 has no generated wrapper (apigen marks 7633/7634 MsgSenderNone),
// so we pair request/response IDs by hand. The response type is
// CMsgDOTALeagueInfoList, as decoded by node-dota2 and the Java GC client.
func ListAdminLeagues(ctx context.Context, d *dota2.Dota2) ([]*protocol.CMsgDOTALeagueInfo, error) {
    resp := &protocol.CMsgDOTALeagueInfoList{}
    err := d.MakeRequest(ctx,
        uint32(protocol.EDOTAGCMsg_k_EMsgDOTALeagueInfoListAdminsRequest),
        &protocol.CMsgDOTALeagueInfoListAdminsRequest{},
        uint32(protocol.EDOTAGCMsg_k_EMsgDOTALeagueInfoListAdminsReponse), // sic
        resp,
    )
    if err != nil {
        return nil, fmt.Errorf("league admin list: %w", err)
    }
    return resp.GetInfos(), nil
}
```

Caveats:

- On the pinned version `MakeRequest` (`go-dota2@532cb1851217/client_request.go:17`) matches on
  **response message ID only**, so serialize 7633 calls per bot. On master it matches by job ID,
  and the GC echoes it on 7634 (VERIFIED live; the reply took about 0.5 s).
- Always pass a timeout `ctx` (5 s is ample). If the account administers nothing, the GC may not
  reply at all (**UNCERTAIN**, not tested). Treat a timeout as an empty list, but log it.
- Call it after the GC session is ready (after `ClientWelcomed`) and again when an admin is added
  on the website. Expose a `POST /bots/{name}/leagues/refresh`.

### 7.2 Cheap pre-check from SOCache

```go
c, err := d.GetCache().GetContainerForTypeID(uint32(cso.GameAccountClient))
if err != nil { return false, err }
if o := c.GetOne(); o != nil {
    return o.(*protocol.CSODOTAGameAccountClient).GetIsLeagueAdmin(), nil
}
```

### 7.3 Bracket nodes for a league

```go
r, err := d.RequestLeagueAvailableLobbyNodes(ctx, leagueID)
for _, n := range r.GetNodeInfos() {
    // n.GetNodeId(), n.GetNodeName(), n.GetNodeGroupName(), n.GetTeamId_1(), n.GetTeamId_2()
}
```

Leagues without a configured bracket (most amateur ones: `node_groups: []`) return no nodes
(VERIFIED live: an empty 7651 for the test league). Leave `league_node_id` unset or 0 in that case.

### 7.4 Creating a league lobby

```go
details := &protocol.CMsgPracticeLobbySetDetails{
    GameName:     proto.String(name),
    PassKey:      proto.String(pass),
    Leagueid:     proto.Uint32(leagueID),     // 0 / unset = "None"
    LeagueNodeId: proto.Uint32(nodeID),       // optional
    DotaTvDelay:  protocol.LobbyDotaTVDelay(2).Enum(), // 120 s, current numbering (vendored _120 = 1 = 60 s now); 10 s league-only
    Visibility:   protocol.DOTALobbyVisibility_DOTALobbyVisibility_Unlisted.Enum(),
    // SeriesType / RadiantSeriesWins / DireSeriesWins for Bo3 etc.
}
err := d.LeaveCreateLobby(ctx, details, true)
```

Then **verify**: read back the lobby from the SOCache (`cso.Lobby`) and check
`CSODOTALobby.GetLeagueid() == leagueID`. Also watch `events.Popup` for
`CMsgDOTAPopup_NOT_LEAGUE_ADMIN`. Create and set-details are fire-and-forget, so a rejected league
shows up only as one of those two signals (or a non-OK 7055). Which one the GC produces is
**UNCERTAIN** (Q4). Handle all of them. The accepted case is VERIFIED live: the read-back matched
and 7055 returned `eresult` 1 ([09](09-live-probe.md) §5).

---

## 8. League lobby behaviour and requirements

| Topic | Finding | Confidence |
|---|---|---|
| Host must be admin | See section 3.3 | VERIFIED |
| League must be approved | IESF: approval email the week before the start | LIKELY |
| Players need a ticket to **play**? | No. Since 2017 free-to-spectate leagues need no item, and nothing in the guides asks players for passes | LIKELY |
| Spectators need a ticket? | Only for **paid** leagues. `MISSINGLEAGUESUBSCRIPTION` watch result, `DOTA_LeagueNoOwnership_Desc` | VERIFIED (mechanism); which leagues are paid today UNCERTAIN |
| Live-spectate a friend in a league game | Blocked: `ERROR_LOBBY_IS_LEAGUE = 14` (`client_watch.proto:118`), "You cannot live spectate a league game." (`loc:34167`) | VERIFIED |
| Coaching | `k_eLobbyIsLeague` error in coaching protos (`client_coaching.proto:186,313,348`) | VERIFIED |
| DotaTV delay | Non-league lobbies have a **minimum** of 120 s (120/300/900 s allowed); only league lobbies may choose 10 s (wire 0; note the proto default for `dota_tv_delay` is 0 = 10 s, `:272`). A 60 s option (wire 1) exists since 2026-08-12; whether non-league lobbies may use it is UNCERTAIN ([gap-04](gap-04-proto-freshness-audit.md)) | League default VERIFIED live: unset reads back wire 2 = 120 s ([09](09-live-probe.md) §6). Non-league minimum VERIFIED as client UI text (`loc:20606`). UNCERTAIN whether the GC enforces it server-side (Q19) (would be resolved by creating non-league lobbies with wire `dota_tv_delay` 0 and 1 and reading back `CSODOTALobby.dota_tv_delay`, field 53) |
| Match recorded to league | `CMsgDOTAMatch.leagueid`, `CMsgGameMatchSignoutResponse.leagueid` (`server.proto:464`). Matches appear in `GetMatchHistory?league_id=` and in `GetLiveGames` while live | VERIFIED (fields); the listing behaviour is LIKELY |
| Lobby visibility | Independent of league (`visibility` field). Use Unlisted plus a pass key for inhouse play | VERIFIED |
| Steam tourney licence | Separate legal step (store.steampowered.com/tourney): free, ≤180 days, ≤1,000 participants, free streaming only. Not enforced by the GC | VERIFIED (IESF) |

---

## 9. Recommendations for this service

1. **Ops runbook:** create the league at dota2.com/league (Amateur tier is fine), add every
   bot's Steam profile under **Admins**, submit, and wait for acceptance. No item purchase.
2. **Code (later track):** add `Bot.Leagues []LeagueInfo` and refresh it with section 7.1 on GC welcome
   and on demand. Add `GET /leagues` (union across bots, with which bots can host each) and
   `GET /bots/{name}/leagues`. Accept `league_id` and `league_node_id` in `POST /lobby/create`
   and make bot selection league-aware.
3. **Enrichment:** optionally join the GC list with `GetLeagueData` (public, cache it) for
   streams, node groups and prize pool.
4. **Validation:** reject a `league_id` not in the chosen bot's list *before* calling the GC.
   After creation, verify `CSODOTALobby.leagueid`.
5. **Do not** use `GetProfileTickets` or inventory to decide hostability.

---

## 10. Verified vs Uncertain

### Verified (seen in source, docs or live calls)

- Message IDs 7633/7634/7650/7651/7434/8073/8074 and the message definitions cited above
  (`protocol/`, matching upstream GameTracking-Dota2 on 2026-11-06).
- go-dota2 wrappers: `RequestLeagueAvailableLobbyNodes` (`client_generated.go:3519`),
  `GetProfileTickets` (`:1371`), `SetLobbyDetails` (`:6841`), event `LeagueAdminList` (`:7671`).
  **No** wrapper for 7633/7634 (`apigen/msg_overrides.go:98-99`). `MakeRequest` is exported.
- 7634 decodes as `CMsgDOTALeagueInfoList`, and the GC echoes the job ID on it (live, [09](09-live-probe.md)).
- Hosting a league lobby requires the account to be a league admin (node-dota2 README, IESF
  guide, `NOT_LEAGUE_ADMIN` popup). An admin bot's league create is accepted and read back (live).
- July 6 2017: free-to-spectate Amateur/Professional leagues stopped needing a ticket item.
- `CMsgPracticeLobbySetDetails.leagueid` (16) and `league_node_id` (49). There is no
  `league_selection_priority` field.
- `CSODOTAGameAccountClient.is_league_admin` exists and is `true` for an admin (live). go-dota2 decodes that SO type but not
  `CSOEconItemTournamentPassport` (type 42).
- Public web API behaviour (`GetLeagueInfoList`, `GetLeagueData` with empty admins,
  `GetLeaguesData`, `GetLiveGames`, `GetPrizePool`). `GetLeagueListing` returns 404.

### Likely / inferred

- The in-client League drop-down is populated from the 7633 response.
- An account can administer many leagues and choose any of them per lobby.
- The league must be ACCEPTED (and possibly inside its date window) to be usable.
- Players need no item to play in a free league lobby.

### Uncertain / needs a live test with a real bot

- Whether the 7633 list includes unsubmitted, rejected or concluded leagues, and whether it is filtered by date.
- Whether the GC replies to 7633 at all when the account is admin of nothing (an empty list or a timeout).
- How a bad `leagueid` is rejected: a `NOT_LEAGUE_ADMIN` popup, silent reset to 0, or a
  `CMsgPracticeLobbyJoinResponse`/lobby-create result code.
- When the GC sends `k_EMsgGCLeagueAdminList` (7434) and what its `account_ids` mean.
- Whether "Admin Pass" activation is still needed for anything.
- ~~Correct parameters for `GetLeagueNodeData` / `GetLeagueNodes`.~~ Resolved in [gap-03](gap-03-league-portal-webapi.md): they are `IDOTA2DPC` methods.
- Caps on the number of admins per league or leagues per account.
- How long it takes for a newly added admin to show up in the GC list (cache or relog needed?).

Still to test live: a second league (one unsubmitted), a non-admin and a bogus `leagueid` on
create, and a bot that administers nothing.

---

## 11. Sources

Local:

- `protocol/dota_gcmessages_common_league.proto` (lines 6-293)
- `protocol/dota_gcmessages_msgid.proto` (lines 76-451)
- `protocol/dota_gcmessages_client.proto` (171, 338-350, 443-449)
- `protocol/dota_gcmessages_common.proto` (133, 164, 377-379, 1063, 1631-1640)
- `protocol/dota_gcmessages_client_match_management.proto` (121-175, 245)
- `protocol/dota_gcmessages_common_lobby.proto` (14-19, 208, 265, 314, 316)
- `protocol/dota_gcmessages_client_watch.proto` (18, 74, 118, 158)
- `protocol/dota_shared_enums.proto` (361-365, 509-583)
- `go-dota2@532cb1851217/client_generated.go` (1366-1387, 3514-3535, 5488-5498, 6838-6845, 7671)
- `go-dota2@532cb1851217/client_request.go:17`, `go-dota2@532cb1851217/client_lobby.go:13-40`, `go-dota2@532cb1851217/cso/cso_types.go:17-86`
- `go-dota2@532cb1851217/apigen/msg_overrides.go:67,98-99`, `go-dota2@532cb1851217/apigen/snapshot-apigen.txt:501-502,638-644,807-809`
- `go-dota2@532cb1851217/events/generated.go:659-672,1058-1065`

Web:

- SteamDatabase GameTracking-Dota2 protobufs: <https://github.com/SteamDatabase/GameTracking-Dota2/tree/master/Protobufs>
- GameTracking-Dota2 localization: <https://raw.githubusercontent.com/SteamDatabase/GameTracking-Dota2/master/game/dota/pak01_dir/resource/localization/dota_english.txt>
- Dota 2 Update, July 6th 2017 (Steam News): <https://store.steampowered.com/news/posts/?appids=570/&enddate=1499383132>
- Dota 2 League signup page: <https://www.dota2.com/league/0/list>
- Steam Support "Dota 2 - League Tickets" (legacy, body now empty): <https://support.steampowered.com/kb/1719-OPHL-1139/dota-2-league-tickets>
- IESF *DOTA 2 Organizer's Guideline* (Feb 2024): <https://nf.iesf.org/ynopsifa/2024/02/DOTA2-Organizers-Guideline.pdf>
- node-dota2 README (leagueid / league admin): <https://github.com/Arcana/node-dota2>
- hudi1/dota2-java-game-coordiator `Dota2League.java`: <https://github.com/hudi1/dota2-java-game-coordiator/blob/master/dota-gc/src/main/java/org/tomass/dota/gc/handlers/features/Dota2League.java>
- ValvePython dota2 docs (EMsg values): <https://dota2.readthedocs.io/en/stable/genindex.html>
- paralin/go-dota2 on pkg.go.dev: <https://pkg.go.dev/github.com/paralin/go-dota2>
- datdota league tiers (community "Premium" label): <https://github.com/datdota/datdota/wiki/League-Tiers>
- Live endpoints (queried 2026-11-06):
  <https://www.dota2.com/webapi/IDOTA2League/GetLeagueInfoList/v001>,
  <https://www.dota2.com/webapi/IDOTA2League/GetLeagueData/v001?league_id=15728>,
  <https://www.dota2.com/webapi/IDOTA2League/GetLeaguesData/v001?league_ids=15728>,
  <https://www.dota2.com/webapi/IDOTA2League/GetLiveGames/v001>,
  <https://www.dota2.com/webapi/IDOTA2League/GetPrizePool/v001?league_id=15728>,
  <https://api.steampowered.com/IDOTA2Match_570/GetLeagueListing/v1/> (404)

