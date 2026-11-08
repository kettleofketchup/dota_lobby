# Gap 03: the dota2.com league admin portal as a second source for "leagues this bot administers"

> Research date: 2026-11-06. Everything below was fetched without logging in. No credentials,
> cookies from a real session, or tokens were used.
> Labels: **VERIFIED** means seen in fetched source or a live HTTP response. **INFERRED** means
> reasoned from verified facts. **UNCERTAIN** means not testable without logging in.

## TL;DR

1. **The league admin portal (`www.dota2.com/league/`) has no JS bundle and no webpack app.**
   It is a server-rendered PHP/jQuery site. Its "my leagues" list and its per-league tabs
   (overview, admins, graphics, streams, prizepool, messages, summary) are HTML pages behind a
   login. None of them calls `IDOTA2League/*`. The only XHRs in the portal are form posts to
   `/league/<id>/post_*` and the autocomplete call `/league/0/findleaguesjson/`. **VERIFIED**
2. **No `IDOTA2League` (or other `webapi/`) method that lists leagues for an admin exists in any
   dota2.com bundle.** All three current webpack bundles were grepped: `dota_react/main.js`,
   `dota_react/AdminPages.js` and `applications/dpc/main.js`. They reference only public read
   methods, plus `IDOTA2Teams` write methods for Valve-internal team admin. A guessed
   `IDOTA2League/GetLeagueAdmins` returns the "no such method" signature (see 4). **VERIFIED**
3. **Portal auth is a cookie, not a WebAPI key.** `steamcommunity.com/oauth/login?client_id=9B2C1229&response_type=token`
   (implicit grant) redirects to `www.dota2.com/oauth/...`. That page's JS writes the
   `access_token` from the URL fragment into a `dota_oauth_token` cookie that lasts 30 days.
   State-changing posts also send a server-rendered `sessionid` form field. **VERIFIED**
   Whether a SteamClient-platform access token (from the bot's refresh token) would be
   accepted in that cookie is **UNCERTAIN**. It was not tested because testing it means logging in.
4. **02 §6 asked for the parameters of `GetLeagueNodeData` and `GetLeagueNodes`.** Those methods
   are under **`IDOTA2DPC`**, not `IDOTA2League`. The working calls are
   `IDOTA2DPC/GetLeagueNodeData/v001?league_id=&node_id=` and
   `IDOTA2DPC/GetLeagueNodeGroupData/v001?league_id=&node_group_id=`. `GetLeagueNodes` is not
   referenced anywhere. An unknown `IDOTA2League/<method>` returns **HTTP 200 with 0 bytes and
   `text/html`**. That is why 02 saw "empty 200" responses. **VERIFIED**
5. **Status filtering.** Every public league list returns only `status` 3 (ACCEPTED) and
   5 (CONCLUDED). No 1/2/4/6 entries appear among 10,166 leagues. `IDOTA2DPC/GetLeagueInfoList`
   honours `start_timestamp`, filtering on each league's `start_timestamp`.
   `IDOTA2League/GetLeagueInfoList` ignores it. Valve's own esports app filters further on the
   client side (`tier >= 3`, `start_timestamp: 1608883200`). **VERIFIED** We could not see how
   the logged-in "my leagues" page filters. **UNCERTAIN**

**Gap status: NOT resolved for goal 4.** No second programmatic, JSON, admin-scoped source
exists in the shipped bundles. The only other route is to scrape the logged-in portal HTML
(`/league/0/list`) with a `dota_oauth_token` cookie. That route depends on a token type we
could not verify. The sub-question on node parameters (02 §6) **is resolved**.

---

## 1. What the portal actually is

| Fact | Evidence | Label |
|---|---|---|
| `GET https://www.dota2.com/league/` returns 302 to `/league/0/list` and serves a 7.6 KB HTML page titled "Dota 2 - League Signup" | live fetch | VERIFIED |
| Scripts loaded: `jquery-1.7.1`, `shared_global.js`, `public/javascript/leagues.js` (101 bytes, an empty click handler), `modal.js`, `viewbracket.js`, `tournamentbracket.js`, `jquery.numeric`, jQuery UI | `league.html` lines 15-24 | VERIFIED |
| `viewbracket.js` and `tournamentbracket.js` contain no `$.get`, `$.post`, `ajax` or `url:` calls, and no `webapi` or `IDOTA2` strings | grep over the downloaded files | VERIFIED |
| Login link: `https://steamcommunity.com/oauth/login?client_id=9B2C1229&response_type=token&state=league%2F0%2Flist` | `league.html:135,145` | VERIFIED |
| SteamDatabase/SteamTracking tracks only these dota2.com JS files: `dota_react/{main,AdminPages,manifest}.js`, `dota_react/shared_english-json.js`, `applications/dpc/main.js`, plus the dpc localization. **It does not track the league portal.** | [SteamTracking urls.txt](https://github.com/SteamDatabase/SteamTracking/blob/master/urls.txt), tree listing | VERIFIED |

### 1.1 Portal endpoints we found

Logged out, every per-league page returns **302 to `/league/0/list`**. The Wayback Machine shows
the same behaviour back to 2018. Its archive of every `/league/<id>/<tab>` URL is 301/302 only,
so no logged-in snapshot exists
([CDX query](https://web.archive.org/cdx/search/cdx?url=dota2.com/league/*&fl=timestamp,original,statuscode,length)).

| Endpoint | Verb | Params | Auth | Source | Logged-out result |
|---|---|---|---|---|---|
| `/league/0/list` | GET | none | `dota_oauth_token` cookie | live | 200, logged-out landing page. With a garbage cookie the body is byte-identical (except sessionid) |
| `/league/0/overview` (create form, `leagueID = 0`) | GET | none | none needed to view | live; Wayback 2025-10, 2026-02, 2026-06 | **200**, the form is public |
| `/league/0/create` | GET | none | cookie | live | 302 to list |
| `/league/<id>/overview`, `/admins`, `/graphics`, `/streams`, `/prizepool`, `/messages`, `/summary` | GET | path id | cookie | Wayback CDX (2018), live probe of `15728/overview` and `15728/admins` | 302 to list |
| `/league/0/post_createleague` | POST | `sessionid, league_name, tier, region, league_url, description, notes, start_date, end_date` (unix seconds) | cookie + `sessionid` | `/league/0/overview` inline script, `ov.html:105-115` | not called (it writes data) |
| `/league/<id>/post_editleague` | POST | same as create | cookie + `sessionid` | `ov.html:105` | not called |
| `/league/<id>/post_deleteleague` | POST | `sessionid` | cookie + `sessionid` | `ov.html:138-139` | not called |
| `/league/0/post_createaccount`, `/league/0/post_editaccount` | POST | `sessionid` plus account fields | cookie + `sessionid` | Wayback [2018-10-06 editaccount](https://web.archive.org/web/20181006194623id_/http://www.dota2.com:80/league/0/editaccount) line 80 | not called |
| `/league/0/editaccount` | GET | none | cookie | `league.html` (`redirectToEditAccount`), Wayback | 302 to list |
| `/league/0/findleaguesjson/` | GET (`$.get`) | `search_value`, `search_by` = `league_id` or `league_name`. The UI also has an email radio button that sends an empty `search_by` | cookie | `league.html:50-60` | **302 to list**, so it is login-gated. Response: a JSON array of `{league_id, league_name}`, or `0` |

Notes:
- **No admin-add endpoint is visible.** The FAQ says admins are added "under the League
  Administrators portion" of the admin page by Steam Community URL
  ([Valve KB 1719-OPHL-1139](https://support.steampowered.com/kb/1719-OPHL-1139/dota-2-league-tickets)).
  That form is rendered only on the logged-in `/league/<id>/admins` page, so we could not see its
  post target or parameters. **UNCERTAIN**: by the naming pattern it is probably `post_addadmin`
  or similar. That is a guess, not a finding.
- **Status lives on the server side.** The create form has a "Status" row that is empty for a
  new league (`ov.html`, "Status" label just before "League Name"). So the edit view shows
  per-league status. **INFERRED**: the logged-in list probably shows status for each league.
  We could not see whether it hides any statuses.
- **Tier radio values on the create form:** 1 Amateur, 2 Professional, 7 DPC League Qualifier,
  8 DPC League, 9 DPC League Finals, 4 DPC Major. The script also has text for 5 (The International).
  These match `ELeagueTier` (`go-dota2/protocol/dota_shared_enums.proto:529-540`). Region
  options 0-6 match `ELeagueRegion` (`:519`). **VERIFIED**

### 1.2 The auth mechanism

The OAuth redirect lands on `https://www.dota2.com/oauth/`. Its body is the same for
`/oauth/callback` and `/oauth/login`, 4,959 bytes. Its inline script (saved as `oauth_landing.html`, lines ~69-123):

```js
var params = parseURIFragment( document.location );           // #access_token=...&state=...
...
var tokenLifetime = (30*24*60*60);
if ( params.hasOwnProperty( "access_token" ) ) {
    document.cookie = 'dota_oauth_token=' + params.access_token + "; max-age=" + tokenLifetime + "; path=/";
} else if ( params.hasOwnProperty( "oauth_token" ) ) { ... same cookie ... }
...
document.location = 'https://www.dota2.com/' + decodedURI;      // state = "league/0/list"
```

- **VERIFIED:** the dota2.com login session is just a `dota_oauth_token` cookie. It holds the
  Steam OAuth access token issued to client `9B2C1229`. No bundled JS reads that cookie
  (grep over all three bundles finds no match), so it is consumed only on the server.
- **VERIFIED:** POSTs also need the per-page `sessionid` value that the server writes into the
  inline script, e.g. `sessionid: "fd45d8097935896d9870214e"` in an anonymous render. The only
  cookie set on an anonymous response is `steamCountry`.
- **VERIFIED:** a garbage `dota_oauth_token` cookie does not change `/league/0/list`. It still
  serves the login page. `/league/15728/admins` still returns 302. `GetLeagueData.admins` stays `[]`.
- **UNCERTAIN:** we do not know whether a JWT access token from `IAuthenticationService`
  (the kind a bot gets from its SteamClient refresh token, see 04) would pass the server-side
  check in place of the token from the `9B2C1229` implicit grant. Both are Steam access tokens,
  but the portal may check audience or client. **Not tested**, because testing means presenting
  real credentials to the portal, which this task excluded.
- **Related, VERIFIED:** `api.steampowered.com/IDOTA2League_570/*` (listed by
  [xPaw's API docs](https://github.com/xPaw/SteamWebAPIDocumentation/blob/master/api.json):
  GetLeagueData, GetLeagueInfoList, GetLeagueNodeResults, GetLeaguesData, GetLiveGames,
  GetMessages, GetPredictionResults, GetPredictions, GetPrizePool, none with documented params)
  returns **403 "verify your key= parameter"** without a key. SteamTracking's `API/` folder has
  no `IDOTA2League_570.json`, which means it is not in the public key's interface list. None of
  these methods is admin-scoped by name.

## 2. The webpack bundles: every `webapi/` method

Sources: live `https://www.dota2.com/public/javascript/{dota_react/main.js, dota_react/AdminPages.js, applications/dpc/main.js}`
(3.69 MB / 97 KB / 282 KB), cross-checked against the SteamTracking prettified copies
[`c/www.dota2.com/public/javascript/dota_react/main.js`](https://github.com/SteamDatabase/SteamTracking/blob/master/c/www.dota2.com/public/javascript/dota_react/main.js)
(commit `8423f011b3`, 2026-10-07),
[`.../dota_react/AdminPages.js`](https://github.com/SteamDatabase/SteamTracking/blob/master/c/www.dota2.com/public/javascript/dota_react/AdminPages.js)
(`8423f011b3`) and
[`.../applications/dpc/main.js`](https://github.com/SteamDatabase/SteamTracking/blob/master/c/www.dota2.com/public/javascript/applications/dpc/main.js)
(`6db5c82d0c`, 2025-05-07). Line numbers below refer to those prettified `c/` copies. The live
and tracked files give the same method set.

Search patterns used: `I[A-Z][A-Za-z0-9]+/[A-Z]...`, `webapi`, `IDOTA2League`, `GetLeagueNode`,
`GetLeagueInfoList`, `LeagueAdmin`, `MyLeague`, `ELeagueStatus`, `league_status`, `access_token`,
`dota_oauth_token`, `Admin`, `Edit`, `Submit`.

### 2.1 `IDOTA2League` (all GET, all anonymous; axios sends the dota2.com cookies same-origin)

| Method | Params seen in bundle | Where | Response (proto) | Live result |
|---|---|---|---|---|
| `GetLeagueData/v001` | `league_id`, `delay_seconds` | main.js:77323-77330 | `CMsgDOTALeague` (`common_league.proto:107`) | 200, 140 KB for 15728. `admins: []` even with a cookie present |
| `GetLeaguesData/v001` | `league_ids` (comma list), `delay_seconds` | main.js:77578-77583; AdminPages.js:5857 | `{leagues:[CMsgDOTALeague]}` | 200 |
| `GetPrizePool/v001` | `league_id` | main.js:75607-75611 | `{prize_pool, increment_per_second}` | 200 (02 §6) |

These three are the only `IDOTA2League` calls in any bundle. **None is admin-scoped.** No
method name in any bundle contains `Admin` or `MyLeague` in a league context. `ELeagueStatus`
and `league_status` do not appear.

### 2.2 `IDOTA2DPC` (the esports and DPC app; all GET, anonymous)

`applications/dpc/main.js:14613-14630` defines
`doAnonymousGet(m, p) = GET BASE_URL + "webapi/IDOTA2DPC/" + m + "/v001" ? p`.

| Method | Params | Line | Live result (2026-11-06) |
|---|---|---|---|
| `GetLeagueInfoList` | `start_timestamp: 1608883200, dpc_only: true` | 14713-14716 | 200, 1.26 MB, 6,334 infos. `start_timestamp` works. `dpc_only` has **no effect** (tier-1 leagues are still returned, and `?dpc_only=1` alone returns all 10,166) |
| **`GetLeagueNodeData`** | **`league_id`, `node_id`** | 14783-14786 | 200 JSON = `CMsgDOTALeagueNode` (`common_league.proto:27`). Example: `league_id=15728&node_id=1` gives node_group_id 3, series_id 815178, matches[] |
| **`GetLeagueNodeGroupData`** | **`league_id`, `node_group_id`** | 14797-14800 | 200, 78 KB JSON = `CMsgDOTALeagueNodeGroup` (`:61`) plus `win_loss_limit`, a field missing from the vendored go-dota2 proto (schema drift) |
| `GetLeagueSeries` | `league_id`, `series_id` | 14812 | 200 `{series_id, series_type, match_minimal:[]}` |
| `GetLeagueMatchMinimal` | `league_id`, `match_id` | 14826 | (also main.js:77608) |
| `GetLeagueData` | `league_id` | 14771 | 200, same body as the IDOTA2League version |
| `GetLeagueResults` | `league_id` | 14759 | 200 standings |
| `GetLeaguePlayers` | `league_id`, `role` | 14884 | 200 `{player_infos:[], retry_time:0}` |
| `GetLiveGames` | `league_id`, `string_server_steam_id: 1` | 14688 | |
| `GetRealtimeMatchStats` | `server_steam_id` | 14680 | |
| `GetDPCMatch`, `GetDPCStandings`, `GetSingleTeamInfo` (`team_id`, `get_dpc_info`), `GetPlayerInfo` (`account_id`), `GetDPCSearchResults` (`search`), `GetRecentAndUpcomingMatches` | as named | 14841-14953 | not needed |

The DPC app also has an **authenticated** channel. It is not `webapi`:
`doUserGet(m, p)` sends `GET applications/dpc_get` with `requesting_method`,
`requesting_account_id`, `requesting_session_id` (14590-14605), and `doPost` sends
`POST applications/dpc_post` (14636-14646). Its methods are fantasy, predictions, favourites
and profile only: `GetSteamPlayerInfo`, `GetProfileInfo`, `GetDPCFavorites`, `GetDPCFeedData`,
`GetFantasyDPC*`, `GetPredictionStatus`, `SetNotificationDisabled`, `SetUpcomingMatchPredictions`,
`ToggleDPCFavorite`, `SetFantasyDPCLineup`. **None is about league admin.** **VERIFIED**

### 2.3 `IDOTA2Teams` and `IDOTA2Fantasy` (`AdminPages.js`, Valve-internal team admin)

`AdminPages.js:4859-4864`: `EditTeamName/v0001`, `AddTeamMember/v0001`, `RemoveTeamMember/v0001`,
`SetTeamAdmin/v0001`, `UpdateRegisteredTeamData/v0001?u=&appid=`. Also `GetFanContentStatus`
(5655), `GetRegisteredTeams` (5687), `GetSingleTeamInfo` (5715), `IDOTA2Fantasy/GetPlayerInfo`
(5736) and `GetTeamAuditInformation` (5799). The page also shows behaviour score, smurf
category and VAC fields (lines ~2042-2690), so it is a Valve support tool. Posts send params
such as `admin_account_id`, `admin_email`, `registration_period` (5590-5598) and rely on the
cookie. These are pro-team registration tools, **not league admin**. **VERIFIED**

### 2.4 Other dota_react calls that use `sessionid`

These are `react/setspoilerblock`, `react/setwatchedgame`, `react/togglefavoriteteam`,
`proregistration/*` and `filmcontest/*`. Each posts `sessionid: SESSION_ID` from
`application_config` (main.js:500-535, 68546, 77352, 77416, 77925). This confirms the site-wide
pattern: **cookie plus a `sessionid` field, never `access_token` or `key`**. No bundle contains
the string `access_token`. **VERIFIED**

## 3. Live calls made (unauthenticated)

All calls went to `https://www.dota2.com/webapi/...`.

| Call | HTTP | Bytes / type | Meaning |
|---|---|---|---|
| `IDOTA2League/ThisMethodDoesNotExist/v001` | 200 | 0, `text/html` | **The "unknown method" signature for IDOTA2League** |
| `IDOTA2DPC/ThisMethodDoesNotExist/v001` | 200 | 4 (`null`), JSON | Unknown-method signature for IDOTA2DPC |
| `IDOTA2Nonexistent/Foo/v001` | 404 | HTML error | Unknown interface |
| `IDOTA2League/GetLeagueNodeData?league_id=15728&node_id=0` and `=1` | 200 | 0, `text/html` | Not an IDOTA2League method, which explains 02 §6 |
| `IDOTA2League/GetLeagueNodes?league_id=15728` | 200 | 0, `text/html` | Not a method |
| `IDOTA2League/GetLeagueNodeGroupData?...` | 200 | 0, `text/html` | Not a method |
| `IDOTA2League/GetMessages?league_id=15728` | 200 | 0, `text/html` | Not routed on dota2.com, though xPaw lists it on api.steampowered.com |
| `IDOTA2League/GetLeagueAdmins?league_id=15728`, `IDOTA2League/GetLeagueInfoListAdmins` (guesses) | 200 | 0, `text/html` | Do not exist |
| `IDOTA2League/GetLeagueNodeResults?league_id=15728[&node_id=1]` | 200 | `{"node_results":[]}` | Exists. Correct params unknown, and not needed |
| `IDOTA2League/GetPredictionResults?league_id=15728` | 200 | `{"result":{"status":8}}` | Exists |
| `IDOTA2DPC/GetLeagueNodeData?league_id=15728&node_id=1` | 200 | 829 B JSON | **Works** |
| `IDOTA2DPC/GetLeagueNodeData?league_id=20310&node_id=0` / `league_id=999999&node_id=1` | 200 | `null` | null means "no such node" |
| `IDOTA2DPC/GetLeagueNodeGroupData?league_id=15728&node_group_id=1` | 200 | 78 KB JSON | **Works** |
| `IDOTA2DPC/GetLeagueNodes?league_id=15728` | 200 | `null` | Looks the same as an unknown method |
| `IDOTA2League/GetLeagueInfoList` (no params) | 200 | 10,166 infos | status {5: 9375, 3: 791}. Tiers 1-9. Max id 65019 |
| `IDOTA2League/GetLeagueInfoList?start_timestamp=1608883200` | 200 | 10,166 infos | param ignored |
| `IDOTA2DPC/GetLeagueInfoList?start_timestamp=1608883200[&dpc_only=1]` | 200 | 6,334 infos | status {5: 5587, 3: 747}. min `start_timestamp` = 1609106400, so it filters on start time. `most_recent_activity` and `end` are not used as the filter |
| `IDOTA2DPC/GetLeagueInfoList?dpc_only=1` | 200 | 10,166 | `dpc_only` ignored |
| `api.steampowered.com/IDOTA2League_570/GetLeagueInfoList/v1/` (no key) | 403 | | Needs a key |
| `/league/0/findleaguesjson/?search_value=15728&search_by=league_id` | 302 | | Login-gated |

`status` in these lists is `CMsgDOTALeagueInfo.status` (`common_league.proto:191`, a uint32
that carries `ELeagueStatus`, `dota_shared_enums.proto:509-517`: 1 UNSUBMITTED, 2 SUBMITTED,
3 ACCEPTED, 4 REJECTED, 5 CONCLUDED, 6 DELETED).

## 4. What this means for Q3 (the drop-down filter) and goal 4

- **VERIFIED:** Valve's public league catalogue contains only ACCEPTED and CONCLUDED leagues.
  The filtering happens on the server, since none of the 10,166 infos has status 1, 2, 4 or 6.
  So the public catalogue cannot show a newly created, still-unsubmitted or submitted league that
  a bot administers.
- **VERIFIED:** Valve's own UI applies a start-date cutoff (`start_timestamp` server-side on
  IDOTA2DPC) and a tier filter (`tier >= 3`, client-side, `dpc/main.js:14727`) to build *its*
  list. This is a UI choice for the esports app. It says nothing direct about the in-client
  lobby drop-down. **INFERRED (weak):** Valve does filter league lists by date and tier when
  presenting them, so the 7634 list or the drop-down could be filtered as well. That still has
  to be tested in a real client (README open question 3).
- **UNCERTAIN:** whether the logged-in portal `/league/0/list` lists leagues of every status
  for the admin. A "Status" row on the edit form suggests it shows status, but we cannot see it.
- **Practical recommendation (INFERRED):**
  1. Keep GC 7633→7634 as the primary goal-4 source (02 §7).
  2. Use the public `GetLeagueInfoList`/`GetLeaguesData` only to *enrich* or *validate* a
     league id from 7634 or config (name, tier, status, dates). A league id that is missing
     from the public list is not necessarily invalid. It may be unsubmitted or submitted.
  3. A third, fallback source would be an HTML scrape of `GET /league/0/list` with
     `Cookie: dota_oauth_token=<token>`. It is unproven, and it would put a bot's Steam access
     token into a dota2.com cookie and parse unversioned HTML. Do not build it unless 7634 turns
     out to be broken. If it is tried, first test one bot by hand with a token from
     `GenerateAccessTokenForApp` (04) and see whether `/league/0/list` stops serving the
     "Login" button. That test needs explicit approval from the operator because it uses a
     real credential.
- **For 02 §6:** replace "`GetLeagueNodeData`, `GetLeagueNodes`: param names unknown,
  UNCERTAIN" with "`IDOTA2DPC/GetLeagueNodeData?league_id&node_id` and
  `IDOTA2DPC/GetLeagueNodeGroupData?league_id&node_group_id` work. `GetLeagueNodes` does not
  exist. On `IDOTA2League`, an empty `text/html` 200 means unknown method."

## 5. Not confused with "tickets"

`IDOTA2Ticket_570` on api.steampowered.com (`GetSteamIDForBadgeID`, `SetSteamAccountPurchased`,
`SteamAccountValidForBadgeType`) is about TI event **badges** or physical tickets
([SteamTracking API/IDOTA2Ticket_570.json](https://github.com/SteamDatabase/SteamTracking/blob/master/API/IDOTA2Ticket_570.json)).
It has nothing to do with league "tickets" (DotaTV league passes) or league admin. **VERIFIED**
from its method and parameter names.

## 6. Prior-art search

GitHub code search for `post_editleague`, `post_createleague`, `league/0/findleaguesjson` and
`post_addadmin dota2` found no hits. `IDOTA2League/GetLeagueInfoList` turns up only public read
clients (odota/core `svc/leagues.ts`, 27-Nerds/dota-leagues-go, Aluerie/Shared-Bot-Utilities,
nlavalle/AghanimsFantasy and others). None of them calls an admin-scoped league endpoint.
**VERIFIED**, as of the search date.

## Sources

- Portal: https://www.dota2.com/league/ (302 to `/league/0/list`), https://www.dota2.com/league/0/overview, https://www.dota2.com/oauth/
- Bundles (live): https://www.dota2.com/public/javascript/dota_react/main.js, https://www.dota2.com/public/javascript/dota_react/AdminPages.js, https://www.dota2.com/public/javascript/applications/dpc/main.js, https://www.dota2.com/public/javascript/leagues.js
- SteamTracking copies: https://github.com/SteamDatabase/SteamTracking/tree/master/c/www.dota2.com/public/javascript and https://github.com/SteamDatabase/SteamTracking/blob/master/urls.txt
- Wayback: https://web.archive.org/cdx/search/cdx?url=dota2.com/league/* and https://web.archive.org/web/20181006194623id_/http://www.dota2.com:80/league/0/editaccount
- xPaw API list: https://github.com/xPaw/SteamWebAPIDocumentation/blob/master/api.json
- Valve KB, league tickets: https://support.steampowered.com/kb/1719-OPHL-1139/dota-2-league-tickets
- go-dota2 protos (`go-dota2@532cb1851217/protocol/`, pinned `v0.0.0-20250623204622-532cb1851217`): `dota_gcmessages_common_league.proto:27,61,107,125-129,182-196,233`; `dota_shared_enums.proto:509-540`
- Related project docs: `docs/research/02-league-tickets.md` §6-7, `04-steam-auth-headless.md`, `README.md` open questions 2, 3 and 16
