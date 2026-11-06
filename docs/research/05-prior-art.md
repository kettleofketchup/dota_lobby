# 05 - Prior Art: Dota 2 Lobby Bots, Libraries and League Hosting

> Research track: how existing automated Dota 2 lobby bots handle multiple bot accounts,
> league IDs and tickets, invites and team assignment, match-end detection, and the
> known pitfalls. The aim is to pull out reusable patterns for this Go service
> (`pkg/bot`, `pkg/api`).
>
> Researched 2026-11-06. **VERIFIED** means seen in source code or official docs, with a
> link or `file:line`. **INFERRED** means reasoned from evidence but not observed
> directly. **UNCERTAIN** means conflicting or missing evidence.

Local paths used below:

- `GD2` = `/home/kettle/.local/share/go/pkg/mod/github.com/paralin/go-dota2@v0.0.0-20250623204622-532cb1851217`
- `REPO` = `/home/kettle/git_repos/dota_lobby`

---

## 1. Executive summary

1. **One lobby per bot account.** Every project surveyed treats a Steam/Dota account as
   able to host one practice lobby at a time. To run games concurrently they either
   (a) pool N accounts, with one in-flight lobby each, or (b) leave the lobby once the
   game is running so the bot is free again. Option (b) is unreliable: the GC ignores
   `PracticeLobbyLeave` while the lobby is in `RUN`, and abandon-then-leave is only
   partly confirmed. For a service that needs concurrency, **pooling accounts is the proven pattern.**
2. **League ID is just `CMsgPracticeLobbySetDetails.leagueid`** (field 16), passed at create
   time or in a later config call. It only works if the **bot account is an admin of that
   league on dota2.com/league** (node-dota2 docs; Challengermode; under-5k-league docs).
   No prior-art project redeems a ticket item; they all rely on league admin rights.
   **INFERRED:** that admin rights alone are what populate the in-game league dropdown.
   No source or client capture shows how the dropdown is filled (see section 4.2 and section 7).
3. **Multiple leagues per bot is supported by prior art.** devilesk/dota-ihl-bot asks the
   GC which leagues each bot administers (`k_EMsgDOTALeagueInfoListAdminsRequest` 7633,
   whose reply 7634 decodes as `CMsgDOTALeagueInfoList`). It stores these as "tickets" in a
   Bot <-> Ticket many-to-many table and picks a free bot **that holds the ticket for the
   requested league**. under-5k-league shares one account across two leagues by giving
   it admin rights on both tickets. This matches the "list tickets" plus "multiple
   league dropdowns" feature this project wants. It is a likely working method backed by
   dota-ihl-bot's production use (about 2019). It has not been live-tested against today's GC,
   and nothing confirms it is the same list the client's dropdown uses (UNCERTAIN).
4. **go-dota2 has the message types but no wrapper for the admin-league list.** The apigen
   override file deliberately marks 7633/7634 as `MsgSenderNone`
   (`GD2/apigen/msg_overrides.go:98-99`), so it has to be called by hand through
   `Dota2.MakeRequest` (`GD2/client_request.go:17`).
5. **Match end detection.** Prior art uses lobby SOCache updates: `state == POSTGAME`
   and/or `match_outcome` in 2..13 means a winner, and 64..69 means not scored. When the
   bot has left, they poll GC `MatchDetailsRequest` (7095) or the OpenDota / Steam Web API
   `IDOTA2Match_570/GetMatchDetails`.
6. **Main pitfalls.** The GC drops for routine Tuesday maintenance and for ad-hoc updates.
   The bot's lobby picture can go stale and needs a "resync" (`gamesPlayed([])` then
   `[570]`, which forces a fresh welcome). The GC ignores a Leave during `RUN`. The bot
   auto-joins a team slot and must kick itself. Limited ($5) accounts may not be able to
   chat. Per-account GC request limits apply: about 100 match-details requests per account
   per 24 h (ValvePython docs; OpenDota maintainer), plus about 500 per IP per 24 h (OpenDota). A stale lobby from a previous session blocks create, which is why
   `LeaveCreateLobby` exists.

---

## 2. Project catalogue

| # | Project | Lang / GC lib | Status | Bot pooling | League ID | Results |
|---|---------|---------------|--------|-------------|-----------|---------|
| 1 | [devilesk/dota-ihl-bot](https://github.com/devilesk/dota-ihl-bot) | JS, fork of node-dota2 (`github:devilesk/node-dota2#a71891d`) | Unmaintained (about 2019) | DB `Bot` rows, `findUnassignedBot(league)` filtered by **ticket** | `createPracticeLobby({leagueid})`; tickets loaded from GC admin list | lobby `match_outcome` + OpenDota + Steam Web API |
| 2 | [UncleVasya/Dota2-EU-Ladder](https://github.com/UncleVasya/Dota2-EU-Ladder) (mirror: [rd2l/inhouse](https://github.com/rd2l/inhouse)) | Python, ValvePython/dota2 + gevent | Active-ish | N greenlets, `BOT_LOGIN{i}` credentials, `sync_queue` loop assigns queues to free bots | `leagueid` from `LEAGUE_ID` env, one league for all bots | lobby `state == POSTGAME` + `match_outcome` |
| 3 | [timothyjjcrow/under-5k-league](https://github.com/timothyjjcrow/under-5k-league) `ops/dota-lobby-bot` | Node 22, steam-user + dota2-user (itsjfx) + hand-written `lobby.proto` | **Very active (Oct-Nov 2026)** | Single account, one active claim, `BUSY` otherwise; early leave to free the bot | Per request `leagueId`, one account with admin on **two** leagues (US/EU) | lobby POSTGAME/outcome; after early leave, GC `MatchDetailsRequest` polling; OpenDota for stats |
| 4 | [ErkoKnoll/Dota2SentinelBot](https://github.com/ErkoKnoll/Dota2SentinelBot) | C#, SteamKit2 with hand-built GC messages | Old (2017) | `LobbyBotsPool`: separate lobby-bot processes **poll an HTTP coordinator** for `CREATE_LOBBY` work | `leagueid = 0` (custom games) | Leaves at launch; `MatchCheckerJob` uses GC `MatchDetailsRequest` + player match history |
| 5 | [AraanBranco/meepow](https://github.com/AraanBranco/meepow) | **Go, paralin/go-dota2 + go-steam**, Viper, Redis, AWS ECS | 2025 | **One container (ECS Fargate task) per lobby**; REST API -> Redis -> `RunTask` | `lobbyDetails.Leagueid = ...` (also a bug: sets `LeaderId` to league id) | SOCache lobby `RUN`/`POSTGAME` + `MatchOutcome`, written to Redis |
| 6 | [sushkov/Dota2LobbyBot](https://github.com/sushkov/Dota2LobbyBot) | JS, node-dota2, MySQL binlog watcher | Old | In-memory `bots[]`, `selectFreeBot` with 10 s retry | `leagueid` from `settings` table row | Leaves on launch, no results |
| 7 | [midasclub/daas](https://github.com/midasclub/daas) ("Dota as a Service") | TypeScript, node-dota2 | WIP (2021) | DB bot pool + **warm pool of EC2 "machines"**, one bot per machine; bot `disabledUntil` cooldown | not found in public source | webhooks `GAME_STARTED/FINISHED/CANCELLED` |
| 8 | [Cienszki/dota2-lobby-bot](https://github.com/Cienszki/dota2-lobby-bot) | TS, steam-user + `dota2` 6.2.0 + steam-totp | 2026 | **One process per bot account**, Firestore command queue | unknown | event docs to orchestrator |
| 9 | [sbx320/bot](https://github.com/sbx320/bot) (RD2L) | C++ custom Steam/GC | Old (2018) | single | n/a | leaves lobby once `game_state >= HERO_SELECTION` |
| 10 | [blastorg/gamecoordinator-dota-communicator](https://github.com/blastorg/gamecoordinator-dota-communicator) | TS, steam-user (BLAST, commercial TO) | Maintained | n/a (library) | n/a | `getMatchDetails` over GC |
| 11 | Challengermode (commercial) | closed | live | Their bot accounts; you add them to your league | "Custom League ID" per tournament | closed |

Other go-dota2 importers (from [pkg.go.dev importedby](https://pkg.go.dev/github.com/paralin/go-dota2?tab=importedby)):
`13k/night-stalker` (live-match watcher), `AdmiralBulldogTv/DiscordBot`, `basskip/auleaderboard`,
`d3nd3/dota-report-timestamps`, `mikedotexe/dota-go-matchmaking` (a custom-lobby POC with a single `main.go`).
Of these, only **meepow** is a lobby-hosting service.

---

## 3. Project deep dives

### 3.1 devilesk/dota-ihl-bot (the reference for multi-bot + multi-league)

A Discord inhouse-league bot with a Postgres/Sequelize DB, a queue/draft state machine, and a
pool of Dota bot accounts.

**Bot <-> league "ticket" model (VERIFIED):**

- `lib/dotaBot.js` `loadDotaBotTickets` calls `requestLeagueInfoListAdmins()`, upserts each result
  as a `Ticket` row, and sets the bot's tickets to exactly that set:

  ```js
  const loadDotaBotTickets = async (dotaBot) => {
      const leagueInfos = await dotaBot.requestLeagueInfoListAdmins();
      const tickets = await Fp.mapPromise(Db.upsertTicket)(leagueInfos);
      const bot = await Db.findBotBySteamId64(dotaBot.steamId64);
      await Db.setTicketsOf(bot)(tickets);
      return tickets;
  };
  // requestLeagueInfoListAdmins maps body -> { leagueid: ticket.league_id, name,
  //   mostRecentActivity, startTimestamp, endTimestamp } with a 5000 ms timeout
  ```

  ([dotaBot.js](https://github.com/devilesk/dota-ihl-bot/blob/master/lib/dotaBot.js), lines ~318-325 and ~1052-1074)
- The underlying fork method ([devilesk/node-dota2@a71891d handlers/leagues.js](https://github.com/devilesk/node-dota2/blob/a71891d/handlers/leagues.js)):

  ```js
  var payload = new Dota2.schema.CMsgDOTALeagueInfoListAdminsRequest({});
  this.sendToGC(Dota2.schema.EDOTAGCMsg.k_EMsgDOTALeagueInfoListAdminsRequest, payload,
                onLeagueInfoListAdminsReponse, callback);
  // ...
  var response = Dota2.schema.CMsgDOTALeagueInfoList.decode(message);
  handlers[Dota2.schema.EDOTAGCMsg.k_EMsgDOTALeagueInfoListAdminsReponse] = onLeagueInfoListAdminsReponse;
  ```

  Upstream Arcana/node-dota2 does **not** have this method. It exists only in devilesk's fork.
- Bot selection is filtered by ticket ([lib/db.js](https://github.com/devilesk/dota-ihl-bot/blob/master/lib/db.js), lines ~188-214):

  ```js
  const findAllUnassignedBotForLeagueTicket = async league => db.Bot.findAll({
      where: { status: { [Op.in]: [BOT_OFFLINE, BOT_IDLE] }, lobbyCount: { [Op.lt]: 5 } },
      include: [{ model: db.Ticket, where: { leagueid: league.leagueid } }],
      order: [['id', 'ASC']],
  });
  const findAllUnassignedBotWithNoTicket = ... // bots with NO tickets, lobbyCount 0
  const findUnassignedBot = async league => (league.leagueid
      ? findAllUnassignedBotForLeagueTicket(league)
      : findAllUnassignedBotWithNoTicket()).then(bots => bots[0]);
  ```

  Note that **un-ticketed lobbies use bots with no tickets at all**, which keeps league-capable
  bots free for league games. Assignment and unassignment run in a DB transaction that
  increments or decrements `Bot.lobbyCount`.
- Before creating, `STATE_BOT_ASSIGNED` reloads tickets and checks the bot really has the league
  ([lib/lobbyStateHandlers.js](https://github.com/devilesk/dota-ihl-bot/blob/master/lib/lobbyStateHandlers.js) ~209-221):

  ```js
  const tickets = await DotaBot.loadDotaBotTickets(dotaBot);
  if (!lobbyState.inhouseState.leagueid || tickets.find(t => t.leagueid === lobbyState.inhouseState.leagueid)) {
      const lobbyOptions = { ...lobbyState, leagueid: lobbyState.inhouseState.leagueid };
      ... lobbyState.dotaLobbyId ? joinDotaBotLobby(...) : createDotaBotLobby(...)
  ```
- Admin commands exist for `ticket-add`, `ticket-remove`, `league-ticket`, `bot-add/remove/list/status`
  (`commands/owner/*`, `commands/admin/*`).

**Lobby lifecycle (VERIFIED, `lib/dotaBot.js`):**

- Create with `createPracticeLobby({ game_name, pass_key, leagueid, game_mode, cm_pick })`. It treats
  `EResult.OK` **or `EResult.Fail`** as success (a quirk), then `practiceLobbyKickFromTeam(self)`
  and joins lobby chat.
- `inviteToLobby(steamId64)` resolves on the `inviteCreated` event, with no timeout.
- Team enforcement: a `teamCache` holds steamId -> intended team. On every `practiceLobbyUpdate` it diffs
  members, and anyone on the wrong team gets `practiceLobbyKickFromTeam`. `isDotaLobbyReady` is true when
  every expected player is on the right side, and then `launchPracticeLobby()` runs.
- Workarounds in comments: `// flipLobbyTeams callback does not fire` (waits 1 s), `// abandonCurrentGame does not callback`.
- A rate-limited GC queue: `this._queue = new Queue(null, null, true)` (`// Block queue until GC is ready`),
  with settable `rateLimit` (ms between requests) and exponential `backoff`. Every GC call goes through `schedule(fn)`.
  GC `ready`/`unready` unblock or block the queue.
- Lobby state machine (`lib/constants.js`): `STATE_WAITING_FOR_BOT -> STATE_BOT_ASSIGNED -> STATE_BOT_CREATED/STATE_BOT_FAILED
  -> STATE_WAITING_FOR_PLAYERS -> STATE_MATCH_IN_PROGRESS -> STATE_MATCH_ENDED -> STATE_MATCH_STATS/NO_STATS -> STATE_COMPLETED`.
- Results: `match_outcome` RadVictory/DireVictory from the lobby emits `EVENT_MATCH_OUTCOME`. `matchTracker.js`
  then polls `https://api.opendota.com/api/matches/{id}` and
  `http://api.steampowered.com/IDOTA2Match_570/GetMatchDetails/v1/?key=...&match_id=...` for stats.
- Bot statuses: `BOT_UNAVAILABLE, BOT_LOADING, BOT_ONLINE, BOT_IDLE, BOT_IN_LOBBY, BOT_OFFLINE, BOT_FAILED`.

### 3.2 UncleVasya/Dota2-EU-Ladder (ValvePython, N bots in one process)

File: [`app/balancer/management/commands/dota_bot.py`](https://github.com/UncleVasya/Dota2-EU-Ladder/blob/master/app/balancer/management/commands/dota_bot.py) (1510 lines).

- **Pool (VERIFIED, lines 81-105):** credentials are generated as `BOT_LOGIN{i}`/`BOT_PASSWORD{i}` for
  `-n` bots. One gevent greenlet runs per bot plus one `sync_queue` greenlet. On shutdown it calls
  `destroy_lobby(); exit(); steam.logout()` for each bot.
- **Reconnect (lines 139-182):** on Steam `disconnected` it calls `client.reconnect(maxdelay=30)` or re-logs in
  after 30 s. On Dota `notready` it waits 30 s and calls `dota.launch()`. It auto-accepts friend invites.
- **Create (lines 247-277):** `create_practice_lobby(password=..., options={... 'leagueid': int(os.environ.get('LEAGUE_ID', -1)), 'fill_with_bots': False, 'allow_spectating': True, 'dota_tv_delay': 0, 'pause_setting': 0 ...})`.
  One league ID for every bot.
- **On `EVENT_LOBBY_NEW`:** `join_practice_lobby_team()` (moves the bot to unassigned) and join lobby chat.
- **On `EVENT_LOBBY_CHANGED`:** in `UI` state it runs kick policies (banned, not in queue, unbalanced, low MMR,
  no voice). In `SERVERSETUP` it records `server_id`. In `POSTGAME` it calls `process_game_result` (`match_outcome`
  RadVictory/DireVictory -> `MatchManager.record_balance(...)`) and **creates a new lobby on the same bot**.
- **Scheduler (lines 1466-1510):** every 5 s, if all bots are ready, it matches active DB queues to bots. Busy bots
  whose queue vanished `destroy_lobby()`, sleep 5 s, and recreate. Free bots
  (`not game_start_time and lobby and not queue`) get a queue. When a queue reaches 10 players it calls
  `invite_players` (invite only those not already in `lobby.all_members`, tracking `invited_players`).
- Pattern: **bots keep an idle lobby open at all times** and get re-purposed for a queue, which keeps latency low.

### 3.3 timothyjjcrow/under-5k-league `ops/dota-lobby-bot` (the most detailed operational notes)

Docs: [docs/DOTA-LOBBY-BOT.md](https://github.com/timothyjjcrow/under-5k-league/blob/main/docs/DOTA-LOBBY-BOT.md),
[docs/DOTA-BOT-HOSTING.md](https://github.com/timothyjjcrow/under-5k-league/blob/main/docs/DOTA-BOT-HOSTING.md),
[PR #125](https://github.com/timothyjjcrow/under-5k-league/pull/125). These are written up from logs of real games in Oct 2026,
which makes them the most current field evidence available.

- **Architecture:** a separate Node 22 worker with persistent state (`state/steam-auth.json` refresh token, `lobbies.json`
  job ledger). The control plane is `POST /lobby` on `127.0.0.1:8090` with a Bearer secret and an 8 KiB body cap, plus an
  optional outbound-WebSocket relay on Cloudflare. **Idempotency keys** look like `inhouse:<id>:1` or `eu:duel:<id>:1`.
  A durable **single active claim** returns `BUSY` to any other create.
- **Tickets:** "Give that account permission to host under the in-house ticket through
  [Dota league administration](https://www.dota2.com/league/). Confirm that this account can actually select the
  ticket when testing a lobby." League IDs were looked up via [OpenDota /api/leagues](https://api.opendota.com/api/leagues)
  (e.g. 20004). Sharing one account across US and EU leagues needs admin on **each** league's ticket. Each request carries
  its own `leagueId` and region, and the bot verifies the actual lobby settings against the request before launch.
- **Multi-bot:** "A second Steam bot account (its own Dota account, ticket permission and routing) is what two
  bot-hosted games at once would take; multiple-account routing is not implemented."
- **Lobby settings used:** CM (2), region 2/3, password, no cheats, no AI fill, DotaTV delay 2 min, unique name suffix.
- **Invites:** automatic and once per job, on the first "ready" snapshot (own lobby, settings confirmed, bot is leader).
  Each invite is recorded *before* sending so a restart never re-sends a round. Resend limits are 30 s per player and
  max 5 per job. The GC answer can mark an invite `offline` (player's Dota closed). A blocked invite ("Block party
  invites from non-friends") looks identical to a delivered one.
- **Seat help:** the bot cannot move players onto a side. It can only kick them back to unassigned
  (`KickFromTeam`) and tell them in lobby chat. It never moves members whose `team` was absent on the wire, because
  the decoder defaults a missing team to Radiant (0).
- **Start:** a manual "Start game with bot" that verifies 5 rostered players on each side. It does not auto-launch on
  the 10th player.
- **Leaving (VERIFIED from their logs):** "The Game Coordinator ignores a Leave while a game runs (lobby state RUN)."
  The bot leaves at POSTGAME (lobby state 3) or when a RUN snapshot carries `matchOutcome` 2..13. Every Leave has
  20 s to be confirmed (Destroy, removal, CacheUnsubscribed, or a welcome without a lobby). If it isn't, the bot
  **resyncs**.
- **Resync:** `gamesPlayed([])` and then, 2 s later, `gamesPlayed([570])`. That produces a full GC welcome that replays
  all caches. With no welcome within 30 s the process exits non-zero and systemd restarts it. Limits are at least
  1 min between resyncs and max 3 per job, persisted. There is no resync during create, launch or server setup
  (lobby states 1, 4, 5, 6).
- **Stale lobby incident:** on 2026-10-18 a match end never reached the bot while its GC session stayed up. The last
  snapshot still said RUN, so every Create answered `BUSY`. Only a restart's full welcome freed it.
- **Early leave (experimental, from 2026-10-28):** `AbandonCurrentGame` (7035) then Leave (7040), only once
  `game_state` is in {HERO_SELECTION 2, STRATEGY_TIME 3, PRE_GAME 4, GAME_IN_PROGRESS 5, TEAM_SHOWCASE 8}. Max 2 tries,
  1 min apart. Observation: "Dota never sends those states by itself: after `WAIT_FOR_PLAYERS_TO_LOAD` (1) the next
  lobby update is the postgame". So a resync ("early check") 2 min after Start is needed to see the real game state.
- **After an early leave:** poll `k_EMsgGCMatchDetailsRequest` 7095 -> 7096 starting 8 min after leaving, then
  1/min, one request in flight, 30 s timeout, give up after 3 h. Outcome 2..13 means `over`; 64..69 means
  not scored, which counts as a failed launch and can be retried.
- **Failed launches:** a not-scored outcome (64..69) or a return to the lobby UI without a played game releases the bot.
  Create with no answer frees the bot after 2 min. An unstarted lobby is released after 30 min (10 min for 1v1 duels).
- **Offline policy:** if offline for 5 min after having connected, exit non-zero so the supervisor restarts the process.
  This never happens before the first connect or after a deliberate stop (wrong account, session elsewhere).
- **Limited accounts:** "Steam limits an account that has spent less than US$5, and a limited account may be unable
  to chat." They log `account limited=true/false`.
- **Logging hygiene:** never log lobby names, passwords, Steam IDs or tokens. The logger whitelists field names.
  An optional `DOTA_BOT_GC_LOG=1` logs only message types.
- **Auth:** QR or password login via steam-session, with refresh token persistence. A first login from a datacenter
  IP may need Steam Guard again. "One Steam account must never run in two places."

### 3.4 ErkoKnoll/Dota2SentinelBot (C#, coordinator plus pull-based lobby workers)

- Projects: `Dota2SentinelCoordinatorBot` (a chat bot that takes requests), `Dota2SentinelHost` (ASP.NET coordinator
  API + `MatchCheckerJob`), `Dota2SentinelLobbyBot` (one process per account), and `Dota2Client`
  (hand-rolled GC on SteamKit2).
- **Work distribution (VERIFIED):** each lobby bot polls `GET {coordinator}/api/coordinator/{userName}?hostingLobby={bool}`
  every 500 ms. The coordinator dequeues one `CREATE_LOBBY` message only for bots that are not hosting
  ([CoordinatorController.cs](https://github.com/ErkoKnoll/Dota2SentinelBot/blob/master/Dota2SentinelHost/Controllers/CoordinatorController.cs)).
  Bots `POST` `LOBBY_CREATED`/`LOBBY_LEFT` back. The coordinator caps concurrent lobbies at `LobbyBotsPool.Count`.
- **Lobby timeout:** a 5 s timer leaves the lobby after `LobbyTimeout` minutes.
- **Launch:** when ready-up `accepted_ids == MaxPlayers-1` the bot **declines** its own ready-up (custom games).
  On `CacheUnsubscribed` (the lobby leaving the bot's SOCache after launch) it records an `OngoingMatch` and leaves,
  so **the bot is free as soon as the game launches.**
- **Results:** `MatchCheckerJob` timers request `GetPlayerMatchHistory` (one request per 5 s) and `GetMatchDetails`
  through the GC to find the match and detect leavers.
- **Chat:** messages go through primary/secondary queues drained by a timer, which acts as chat rate limiting.
- `leagueid = 0`: custom-game lobbies, no league use.
- The wiki documents [sniffing the Steam protocol with NetHook2](https://github.com/ErkoKnoll/Dota2SentinelBot/wiki/Sniffing-Steam-network-protocol)
  to discover GC message flows. This is a useful technique for verifying league-dropdown traffic.

### 3.5 AraanBranco/meepow (Go, go-dota2: the closest stack to ours)

`go.mod`: `paralin/go-dota2 v0.0.0-20241218071341-eca341841669`, `paralin/go-steam v0.0.0-20231025185642-e7c8d97e052a`,
viper, gorilla/mux, go-redis, aws-sdk ecs.

- **Architecture:** `POST` lobby -> `LobbyManager.CreateLobby` stores the JSON in Redis `lobby:{ref}` and calls
  `cloudecs.LaunchContainer`, which runs an **ECS Fargate task per lobby** with env `MEEPOW_REFERENCE_ID`,
  `MEEPOW_BOT_USERNAME/PASSWORD`. The container runs `start bot` and exits when the game ends. Status lives at
  `lobby:{ref}:status` (`LOBBY_CREATED/STARTED/RUNNING/FINISHED`) with a TTL of 1 h after finish.
- **Bot flow ([bot_manager.go](https://github.com/AraanBranco/meepow/blob/main/internal/core/services/bot/bot_manager.go)):**

  ```go
  case *steam.LoggedOnEvent:
      b.SteamClient.Social.SetPersonaState(steamlang.EPersonaState_LookingToPlay)
      b.DotaClient = dota2.New(b.SteamClient, logger)
      b.DotaClient.SetPlaying(true)
      time.Sleep(5 * time.Second)
      b.DotaClient.SayHello()
      ...
      time.Sleep(15 * time.Second)
      lobbyDetails := &protocol.CMsgPracticeLobbySetDetails{ GameName, PassKey, ServerRegion, GameMode,
          FillWithBots: false, CustomMaxPlayers: 10, AllowSpectating: true, Visibility, Allchat: true }
      if leagueDotaID != 0 { lobbyDetails.Leagueid = proto.Uint32(uint32(leagueDotaID)) }
      b.DotaClient.LeaveCreateLobby(context.TODO(), lobbyDetails, true)
      b.DotaClient.KickLobbyMemberFromTeam(b.SteamClient.SteamId().GetAccountId())
      for _, player := range lobbyData.Players { b.DotaClient.InviteLobbyMember(steamID) }
      eventCh, _, _ := b.DotaClient.GetCache().SubscribeType(cso.Lobby)
      // ticker: UI -> LaunchLobby when members >= maxPlayers; RUN -> store match id; POSTGAME -> outcome, Disconnect
  ```

- **Defects to avoid (VERIFIED by reading):**
  - `dota2s.Lobby.LeaderId = proto.Uint64(uint64(leagueDotaID))` sets the *leader* to the league ID. This is a
    harmless but wrong local state object.
  - `dota2s.IsReady()` is called on a locally constructed `state.Dota2State`, not the client's state. Readiness is
    really just the fixed 5 s + 15 s sleeps.
  - The ticker loop blocks on `<-eventCh`, so it is not a real 3 s poll.
  - It counts members whose name is not the bot username, rather than checking their team.
  - The `LaunchLobby()` call sits inside the per-member loop.
  - Any outcome other than DireVictory is reported as "RADIANT", including not-scored outcomes.
  - The process calls `os.Exit(1)` on Steam disconnect.
  - It has a single bot credential; there is no pool.
- Takeaway: the per-lobby-container model sidesteps concurrency inside one process, but logs in a fresh session for
  every lobby. That costs 20+ s of startup, repeats Steam Guard/login rate-limit exposure, and is risky for account
  flagging (INFERRED).

### 3.6 Others (short)

- **sushkov/Dota2LobbyBot** ([bot.js](https://github.com/sushkov/Dota2LobbyBot/blob/master/bot.js)): `selectFreeBot` polls
  `bots[i].free` every 10 s. Each lobby does `leavePracticeLobby` -> `createPracticeLobby({leagueid, game_name, pass_key})`
  -> `practiceLobbyKickFromTeam(self)` ("For some reason the bot automatically joins the first slot. Kick him.")
  -> `inviteToLobby` for each player. It auto-starts after a countdown when `>= match_users` are seated, then
  `launchPracticeLobby` -> `leavePracticeLobby` and the bot is free. It has a lobby timeout and stores status
  codes in MySQL.
- **midasclub/daas**: the worker loop (`setNonOverlappingInterval`) keeps `alwaysActiveMachines` idle and
  warm, replaces old machines, and assigns lobbies to idle machines (`sendLobbyToMachine`). `launchMachine` picks a
  random `OFFLINE` bot whose `disabledUntil` has passed, then waits for `BOOT_OK`/`DOTA_OK` (90 s timeouts). After
  `GAME_FINISHED` the bot is **disabled for 3 h**, and after `GAME_CANCELLED` for 30 min. The code gives no reason;
  the cooldown may be to let the GC fully detach the account from the finished game (UNCERTAIN). Results go out as
  webhooks.
- **Cienszki/dota2-lobby-bot**: "One process per bot account (Dota 2 limits one lobby per client)", with a Firestore
  command queue `/botCommands/{botAccountId}/queue/{commandId}`. It uses `steam-totp` for automatic 2FA codes.
- **sbx320/bot (RD2L)**: on joining a lobby, kick self from team. If `game_state >= HERO_SELECTION`, `LeaveLobby()`.
  IRC-driven `!get-team` lookups.
- **BLAST gamecoordinator-dota-communicator**: login with `refreshToken`, `gamesPlayed([570], true)`, wait for a
  `connectedToGc` event, then `getMatchDetails(matchId)` over the GC. This is a commercial tournament organizer
  using the GC for match details.
- **Challengermode** ([docs](https://support.challengermode.com/en/organizing/integrate-your-dota-2-league)): the organizer
  sends the League ID, dates and "estimated maximum number of concurrent matches" at least 7 days ahead.
  Challengermode replies with "Steam Accounts that you need to add to your Dota 2 League - these are the bots that
  will be hosting the lobbies". Then "all lobbies in your tournament will then be hosted with your League ID."
  This confirms commercially that **a pool of bots, each added as a league admin**, is the standard approach.
  INFERRED: that Challengermode sizes the number of accounts to the concurrency estimate. The page asks for the
  estimate and then sends accounts, but never states how many or why.
- **FACEIT** (historical): no public technical docs were found on how FACEIT hosted Dota lobbies. It is not used as evidence.
- **Arman681/DotA2-Inhouse-Bot** / **maxxwizard/inhouseBot**: these do not host lobbies. Players self-host; the first
  uses `!bindleague <league_id>` to track live league games through the Steam Web API.

---

## 4. Cross-cutting patterns

### 4.1 Bot pooling and assignment

| Pattern | Used by | Pros | Cons |
|---------|---------|------|------|
| In-process pool, scheduler loop matches work to free bots | EU-Ladder (gevent), sushkov, dota-ihl-bot | Simple, one deploy, shared state | One crash takes all bots down; needs careful per-bot goroutine isolation |
| Process per account, pull-based work queue | Sentinel (HTTP poll), Cienszki (Firestore) | Isolation, horizontal scale | Needs a coordinator and queue |
| Container per lobby | meepow (ECS), daas (EC2 machines, warm pool) | Strong isolation | Slow start, many logins, costly |
| Single account + durable claim + `BUSY` | under-5k-league | Very robust, idempotent | No concurrency |

Recommended for this service (INFERRED): an **in-process pool** (`pkg/bot.Manager` already holds N bots and has
`GetAvailableBot()` at `REPO/pkg/bot/manager.go:164`, which today takes no arguments and returns the first connected
free bot; a league-aware variant would be a new change). Give each bot its own goroutine and its own
`steam.Client`/`dota2.Dota2`. Selection is **ticket-aware**, as in dota-ihl-bot: choose a free bot that holds the
requested `leagueid`, and prefer un-ticketed bots for non-league lobbies. Bot states should follow dota-ihl-bot
(`OFFLINE, CONNECTING, IDLE, IN_LOBBY, IN_GAME, COOLDOWN, FAILED`). Persist the claim (bot -> lobby key) so a restart
does not double-assign, as under-5k-league's `lobbies.json` does.

### 4.2 League ID / tickets

- Set `CMsgPracticeLobbySetDetails.Leagueid` (`GD2/protocol/dota_gcmessages_client_match_management.pb.go:874`, field 16).
  Related optional fields: `LeagueNodeId` (field 49, line 900), `SeriesType`, `RadiantSeriesWins`/`DireSeriesWins`,
  `SelectionPriorityRules`. The lobby SO echoes `CSODOTALobby.Leagueid` (`GD2/protocol/dota_gcmessages_common_lobby.pb.go:1858`,
  field 42). Reading it back verifies the ticket was applied, as under-5k-league does before launch.
- **Admin requirement (VERIFIED in docs):** "The bot should be a league admin for this to work."
  ([node-dota2 README](https://github.com/Arcana/node-dota2#readme), the `leagueid` option). League admins are added on
  [dota2.com/league](https://www.dota2.com/league/) ("Admins" section of the tournament submission, per
  [IESF organizer guideline PDF](https://nf.iesf.org/ynopsifa/2024/02/DOTA2-Organizers-Guideline.pdf); older flow in
  [Steam Support KB](https://support.steampowered.com/kb/1719-OPHL-1139/dota-2-league-tickets)).
- **Listing the leagues a bot can host:** prior art uses
  `k_EMsgDOTALeagueInfoListAdminsRequest = 7633` -> `k_EMsgDOTALeagueInfoListAdminsReponse = 7634` (sic, "Reponse")
  (`GD2/protocol/dota_gcmessages_msgid.pb.go:296-297`). The request is empty: `CMsgDOTALeagueInfoListAdminsRequest`
  (`GD2/protocol/dota_gcmessages_common_league.pb.go:1057`). devilesk decodes the reply as `CMsgDOTALeagueInfoList{ infos: []CMsgDOTALeagueInfo }`
  (`...common_league.pb.go:873`; `CMsgDOTALeagueInfo` at `:759` has `league_id, name, tier, region, most_recent_activity,
  total_prize_pool, start_timestamp, end_timestamp, status`).
- go-dota2 does **not** generate a method for it: `GD2/apigen/msg_overrides.go:98-99` marks both IDs `MsgSenderNone`.
  The closest generated helper is `RequestLeagueAvailableLobbyNodes(ctx, leagueID)` (`GD2/client_generated.go:3519`),
  which is for league bracket nodes, not admin lists. `k_EMsgGCLeagueAdminList` produces an `events.LeagueAdminList`
  (`CMsgLeagueAdminList{account_ids}`, `GD2/client_generated.go:7671`). That is a GC push listing the admins *of* a
  league, not the leagues of a bot.
- **Go sketch (INFERRED, untested against the live GC):**

  ```go
  // ListAdminLeagues asks the GC which leagues this account administers ("tickets").
  func ListAdminLeagues(ctx context.Context, d *dota2.Dota2) ([]*protocol.CMsgDOTALeagueInfo, error) {
      req := &protocol.CMsgDOTALeagueInfoListAdminsRequest{}
      resp := &protocol.CMsgDOTALeagueInfoList{}
      err := d.MakeRequest(ctx,
          uint32(protocol.EDOTAGCMsg_k_EMsgDOTALeagueInfoListAdminsRequest),
          req,
          uint32(protocol.EDOTAGCMsg_k_EMsgDOTALeagueInfoListAdminsReponse),
          resp)
      if err != nil {
          return nil, fmt.Errorf("league admin list: %w", err)
      }
      return resp.GetInfos(), nil
  }
  ```

  `MakeRequest` (`GD2/client_request.go:17`) has the signature
  `(ctx, reqMsgID uint32, request proto.Message, respMsgID uint32, response proto.Message, matchesRequest ...func(proto.Message) bool) error`.
  Always use a context timeout: devilesk used 5 s.
- **Multiple leagues:** one account can be an admin on several leagues (under-5k-league's US+EU sharing), so a single bot
  can create lobbies for any league in its admin list. Selecting which league per lobby is just a different `Leagueid`.
  There is no evidence of any per-account cap on admin leagues (UNCERTAIN).

### 4.3 Lobby creation sequence (consensus)

1. Steam logon -> `SetPlaying(true)` (gamesPlayed 570) -> short wait -> `SayHello()` repeated every few seconds until
   the GC session is ready (`IsReady()`): paralin's guidance in
   [go-dota2 #21](https://github.com/paralin/go-dota2/issues/21). Sending GC messages before `GamesPlayed` + `ClientHello`
   makes lobbies hang at "FINDING SERVER" ([SteamKit #791](https://github.com/SteamRE/SteamKit/issues/791)).
2. Clear any leftover lobby: `LeaveCreateLobby(ctx, details, destroyOldLobby=true)` (`GD2/client_lobby.go:21`).
   It destroys the old lobby if this bot leads it, abandons it if the state is not `UI`, leaves, then creates. The
   fire-and-forget form is `CreateLobby` (`GD2/client_lobby.go:13`).
3. Kick self from the team slot: `KickLobbyMemberFromTeam(selfAccountID)` (`GD2/client_generated.go:1979`), or
   `JoinLobbyTeam(DOTA_GC_TEAM_PLAYER_POOL, ...)`. Every project does this because the creator lands in a slot.
4. Invite: `InviteLobbyMember(steamid)` (`GD2/client_lobby_members.go:9`). Invite only players not already present,
   with per-player resend limits.
5. Watch the SOCache (`GetCache().SubscribeType(cso.Lobby)`). Enforce teams by kicking misplaced members from their team.
6. Launch: `LaunchLobby()` (`GD2/client_generated.go:2030`) once the roster is verified (all 10 on the correct sides).
7. Track `state` (UI -> SERVERSETUP -> RUN -> POSTGAME), `match_id`, `game_state`, `match_outcome`.
8. Finish: at POSTGAME, `LeaveLobby()` (`:2073`) or `DestroyLobby(ctx)` (`:516`), then mark the bot free.

### 4.4 Team assignment

- Bots cannot place players on a side. They can only kick a member back to unassigned (`KickLobbyMemberFromTeam`)
  and tell them where to go (lobby chat). Some can `FlipLobbyTeams` and `SendBalancedShuffleLobby`. All projects use
  the "intended team map + kick misplaced" loop (dota-ihl-bot `teamCache`, EU-Ladder `check_teams_setup`,
  under-5k "seat help").
- `cm_pick` (`DOTA_CM_GOOD_GUYS/BAD_GUYS/RANDOM`) is used to give first pick to a side.

### 4.5 Match end and results

| Signal | Source | Notes |
|--------|--------|-------|
| `CSODOTALobby.state == POSTGAME` | SOCache | EU-Ladder, meepow, under-5k |
| `match_outcome` 2..13 (winner) / 64..69 (not scored) | SOCache or GC match details | under-5k distinguishes them; meepow wrongly treats everything non-Dire as Radiant |
| `CacheUnsubscribed` for the lobby | SOCache | Sentinel uses it as the "game launched, I'm out" signal |
| GC `MatchDetailsRequest` 7095/7096 | `RequestMatchDetails(ctx, matchID)` `GD2/client_generated.go:3562` | Rate-limited per account (ValvePython docs: 100/day, may be stale) |
| OpenDota `/api/matches/{id}` | HTTP | Needs time to appear; no key needed for low volume |
| Steam Web API `IDOTA2Match_570/GetMatchDetails` | HTTP + API key | Used by dota-ihl-bot |

### 4.6 Concurrency inside one account (early leave)

- A plain leave during RUN is ignored: under-5k logs and node-dota2 #179.
- Abandon-then-leave (`AbandonLobby()` = `k_EMsgGCAbandonCurrentGame`, `GD2/client_generated.go:14`) has **mixed
  reports**. One 2017 user "get[s] a red screen in the match and trying to create a new lobby - I get into the old
  lobby" ([node-dota2 #179](https://github.com/Arcana/node-dota2/issues/179)). under-5k enabled it on 2026-10-28;
  results were not yet published at the time of research.
- Sentinel and sushkov leave right after launch, but they were hosting **custom games and unticketed lobbies** in an
  older protocol era.
- Conclusion: **do not depend on early leave.** Size the pool to the max number of concurrent games.

### 4.7 Rate limiting and backoff

- dota-ihl-bot puts every GC call through a queue with `rateLimit` (ms) and exponential `backoff`. That queue is
  blocked whenever the GC is not ready.
- paralin: "there are definitely rate limits", with risk of being "IP banned, steam account banned, or rate limited
  if abusing the privilege" ([go-dota2 #21](https://github.com/paralin/go-dota2/issues/21)).
- Per-account GC limits: ValvePython docs say `request_match_details` is "Rate limited to 100 requests/day" and
  `request_matches` 50/day ([docs](https://dota2.readthedocs.io/en/stable/dota2.features.match.html)). The OpenDota
  maintainer gives "100 requests/accounts/24 hours and 500 requests/IP/24 hours"
  ([odota/core #1482](https://github.com/odota/core/issues/1482)) and scales the account count to load.
  OpenDota's `svc/retriever.ts` sets `matchesPerAccount = 100` and calls `selfDestruct()` (process exit, then a
  restart with fresh accounts) once `matchRequests >= clientMap.size * matchesPerAccount`
  ([retriever.ts](https://github.com/odota/core/blob/master/svc/retriever.ts)).
- under-5k: one match-details request in flight, 1/min; invite resend at least 30 s apart; at least 1 min between resyncs.
- Sentinel: queued chat messages drained by a timer.

---

## 5. Known pitfalls (consolidated)

| Pitfall | Evidence | Mitigation seen |
|---------|----------|-----------------|
| GC not ready / drops (Tuesday Steam maintenance, ad-hoc GC restarts) | [esports.net GC article](https://www.esports.net/news/dota/dota-2-game-coordinator/); node-dota2 "GC not ready" | Block the work queue on `unready`; reconnect after 30 s (EU-Ladder); exit and let the supervisor restart after 5 min offline (under-5k) |
| Stale lobby state (missed POSTGAME) | under-5k 2026-10-18 incident | Resync with `gamesPlayed([])` -> `[570]`; 3 h backstop; bounded retries |
| Leave ignored during RUN | under-5k logs; node-dota2 #179 | Leave at POSTGAME; pool more bots |
| Bot auto-seated in a team slot | sushkov comment; all projects | `KickLobbyMemberFromTeam(self)` right after create |
| Leftover lobby from a previous session blocks create / rejoins old lobby | node-dota2 #179; Steam forum; `LeaveCreateLobby(destroyOldLobby)` | Leave/destroy before create; read the welcome cache first |
| League ID silently not applied | node-dota2 docs ("bot should be a league admin") | Check `CSODOTALobby.Leagueid` after create; refresh the admin list before assigning (dota-ihl-bot) |
| `create_tournament_lobby` broken in ValvePython | [ValvePython/dota2 #52](https://github.com/ValvePython/dota2/issues/52) | Use practice lobby + `leagueid` |
| Callbacks that never fire (`flipLobbyTeams`, `abandonCurrentGame`, `inviteCreated`) | dota-ihl-bot comments | Always use timeouts and confirm via SOCache |
| Limited account ($5) can't chat | under-5k docs; node-dota2 fork docs | Fund bot accounts; detect and log `limited` |
| Invites blocked by player setting | under-5k docs | Fall back to lobby name/password; tell users to untick "Block party invites from non-friends" |
| Not-scored outcomes (64..69) treated as wins | meepow bug | Map outcomes explicitly |
| Steam Guard / login from datacenter IP | under-5k hosting doc | Persist refresh tokens / sentry; TOTP via shared secret (Cienszki uses steam-totp) |
| Same account logged in twice | under-5k ("One Steam account must never run in two places") | Process lock; single-instance claim per account |
| Cannot matchmake with a bot | paralin in go-dota2 #21 | n/a; lobbies only |
| Per-account GC rate limits | ValvePython docs; OpenDota rotation | Prefer OpenDota / Web API for results; spread GC calls across bots |
| Lobby create with a failure `EResult` treated as OK | dota-ihl-bot accepts `Fail` | Use the `k_EMsgGCPracticeLobbyResponse` (7055) result and confirm via SOCache |

---

## 6. Reusable patterns for this Go service

1. **Ticket-aware bot pool (from dota-ihl-bot).** On each bot's GC-ready event, and periodically (for example every
   30 min), call `ListAdminLeagues` (section 4.2) and cache `bot -> []leagueInfo`. Expose `GET /leagues`, which
   aggregates across bots: league id, name, start/end, and which bots hold it. Expose `GET /bots/{id}/leagues`.
   PROPOSED (not existing code): change `GetAvailableBot()` (`REPO/pkg/bot/manager.go:164`, currently no arguments)
   to `GetAvailableBot(leagueID)`, returning a free bot holding that ticket. For `leagueID == 0`, prefer bots with no tickets.
2. **Durable claim + idempotency key (from under-5k).** `POST /lobby/create` takes a client key. Re-posting the same key
   returns the same job. A bot holds at most one claim, persisted so restarts are safe.
3. **Explicit lobby job state machine** (from dota-ihl-bot and under-5k):
   `pending -> assigned -> creating -> ready -> starting -> running -> over | failed | released`, with timeouts
   (create 2 min, launch 2 min, idle 30 min, leave-confirm 20 s, running backstop 3 h).
4. **Per-bot GC command queue** with a rate limit and backoff that blocks when the GC is not ready
   (dota-ihl-bot `schedule()`).
5. **Verify after create.** Read back `CSODOTALobby.Leagueid`, `ServerRegion`, `GameMode`, and leader = bot before
   inviting or launching. Report `blocked`/`SETTINGS` if they don't match.
6. **Invite policy.** Invite once per job automatically, record before sending, skip present members, 30 s per-player
   resend gap, max 5.
7. **Seat enforcement.** Map intended teams and kick misplaced members to unassigned. Never act on members with an
   unknown team.
8. **Results.** Primary: lobby POSTGAME + `match_outcome`. Secondary: `RequestMatchDetails` with low frequency and one
   in flight. Tertiary: OpenDota or the Steam Web API.
9. **Resilience.** Reconnect loop. A forced resync path (`SetPlaying(false)` -> 2 s -> `SetPlaying(true)` -> `SayHello`,
   the Go equivalent of `gamesPlayed([])`/`[570]`. VERIFIED that `SetPlaying(false)` calls `GC.SetGamesPlayed()` and
   clears state (`GD2/client_session.go:14-22`); INFERRED that this triggers a full re-welcome). Exit
   with a non-zero status for a supervisor such as Docker restart after a long offline period.
10. **Logging hygiene.** Use the existing `config.Secret` type. Never log lobby passwords, refresh tokens or Steam IDs
    by default.

---

## 7. Verified vs Uncertain

### VERIFIED (seen in source/docs)

- go-dota2 has `CMsgDOTALeagueInfoListAdminsRequest` and msg IDs 7633/7634 but no generated wrapper (`GD2/apigen/msg_overrides.go:98-99`,
  `GD2/protocol/dota_gcmessages_msgid.pb.go:296-297`, `GD2/protocol/dota_gcmessages_common_league.pb.go:1057`).
- devilesk's node-dota2 fork sends 7633 and decodes 7634 as `CMsgDOTALeagueInfoList` (handlers/leagues.js @ a71891d),
  and dota-ihl-bot uses it to populate per-bot tickets and to filter bot selection by league.
- `leagueid` is field 16 of `CMsgPracticeLobbySetDetails` (`GD2/.../client_match_management.pb.go:874`) and field 42
  of `CSODOTALobby` (`GD2/.../common_lobby.pb.go:1858`).
- node-dota2 docs: "The bot should be a league admin for this to work."
- Challengermode asks for the estimated max concurrent matches, then sends bot Steam accounts that you add to your
  league to host lobbies with your League ID. (How many accounts it sends relative to that estimate is not stated.)
- EU-Ladder runs N ValvePython bots in one process with a 5 s scheduler and one `LEAGUE_ID` for all bots.
- meepow is a Go/go-dota2 service using `LeaveCreateLobby` + `Leagueid`, one ECS task per lobby.
- Sentinel lobby bots pull work over HTTP and free themselves at launch via `CacheUnsubscribed`.
- under-5k-league field logs: the GC ignores Leave in RUN; a stale RUN snapshot happened once; a full welcome after
  `gamesPlayed` cycling clears it.
- go-dota2 `LeaveCreateLobby` destroys (if leader), abandons (if not UI), leaves, then creates (`GD2/client_lobby.go:21-60`).

### INFERRED

- Calling 7633 through `Dota2.MakeRequest` will work with go-dota2. The request is empty and the reply type matches
  devilesk's decoder, and dota-ihl-bot used it in production as each bot's ticket list. It has not been run against
  the current GC (dota-ihl-bot is about 2019 vintage). Resolve with a live `MakeRequest(7633 -> 7634)` test on a
  bot that is a known admin of a league.
- The in-game "league" dropdown in the Dota client lobby settings is populated from the same admin-league list (7633/7634).
  This is consistent with tickets appearing only for admins, but no Valve doc, client sniff or live test confirms it.
  Resolve by sniffing the Dota client's GC traffic while opening the lobby league dropdown (Sentinel wiki technique)
  and comparing it with the 7634 reply.
- Admin rights alone (no ticket item or pass) are enough for a league to appear in the dropdown. The sources only say
  the bot must be an admin to host league lobbies.
- In-process pooling with goroutines per bot is the best fit for this repo, given `pkg/bot.Manager`.
- A per-lobby container (meepow) increases login frequency and account-flag risk.

### UNCERTAIN

- Whether abandon-then-leave reliably frees a bot in a ticketed lobby without penalising it (under-5k's experiment is ongoing).
- Whether the current per-account GC limits are still about 100 match-details/24 h per account and 500/24 h per IP
  (ValvePython docs and OpenDota maintainer/`retriever.ts`); not measured by us. These limits concern match data, not
  lobby or league calls.
- Whether there is a cap on how many leagues one account can administer, or on how many lobbies a single league can
  host at once (Challengermode asks for max concurrency but does not state a cap).
- Whether limited ($5) accounts can create league lobbies or send lobby invites. They are only documented as unable to chat.
- Why daas puts bots on a 3 h cooldown after a finished game.
- What FACEIT and other commercial platforms do internally (no public docs).
- Whether `CMsgDOTALeagueInfo.status` or the timestamps can tell active tickets from expired ones (fields exist; semantics unverified).

---

## 8. Sources

Projects and code:

- devilesk/dota-ihl-bot: https://github.com/devilesk/dota-ihl-bot ; docs https://devilesk.github.io/dota-ihl-bot/lib_dotaBot.js.html ; `lib/db.js`, `lib/dotaBot.js`, `lib/lobbyStateHandlers.js`, `lib/matchTracker.js`, `lib/constants.js`
- devilesk/node-dota2 fork (league admin list): https://github.com/devilesk/node-dota2/blob/a71891d/handlers/leagues.js
- Arcana/node-dota2 (deprecated 2022, points to go-dota2): https://github.com/Arcana/node-dota2 ; issue #179 https://github.com/Arcana/node-dota2/issues/179 ; issue #9 https://github.com/Arcana/node-dota2/issues/9 ; issue #388 https://github.com/Arcana/node-dota2/issues/388
- dotabod/node-dota2-fork: https://github.com/dotabod/node-dota2-fork
- UncleVasya/Dota2-EU-Ladder: https://github.com/UncleVasya/Dota2-EU-Ladder/blob/master/app/balancer/management/commands/dota_bot.py ; mirror https://github.com/rd2l/inhouse
- timothyjjcrow/under-5k-league: https://github.com/timothyjjcrow/under-5k-league/blob/main/docs/DOTA-LOBBY-BOT.md ; https://github.com/timothyjjcrow/under-5k-league/blob/main/docs/DOTA-BOT-HOSTING.md ; PR #125 https://github.com/timothyjjcrow/under-5k-league/pull/125
- ErkoKnoll/Dota2SentinelBot: https://github.com/ErkoKnoll/Dota2SentinelBot ; wiki https://github.com/ErkoKnoll/Dota2SentinelBot/wiki/Sniffing-Steam-network-protocol
- AraanBranco/meepow: https://github.com/AraanBranco/meepow (`internal/core/services/bot/bot_manager.go`, `internal/core/infrastructure/cloudecs/cloudecs.go`)
- sushkov/Dota2LobbyBot: https://github.com/sushkov/Dota2LobbyBot/blob/master/bot.js
- midasclub/daas: https://github.com/midasclub/daas (`src/worker/*.ts`, `src/support/disableBot.ts`)
- Cienszki/dota2-lobby-bot: https://github.com/Cienszki/dota2-lobby-bot
- sbx320/bot (RD2L): https://github.com/sbx320/bot/blob/master/src/dota/Dota.cpp
- blastorg/gamecoordinator-dota-communicator: https://github.com/blastorg/gamecoordinator-dota-communicator
- paralin/go-dota2: https://github.com/paralin/go-dota2 ; issue #21 https://github.com/paralin/go-dota2/issues/21 ; importers https://pkg.go.dev/github.com/paralin/go-dota2?tab=importedby
- ValvePython/dota2 issue #52: https://github.com/ValvePython/dota2/issues/52 ; match feature docs https://dota2.readthedocs.io/en/stable/dota2.features.match.html
- SteamKit issue #791: https://github.com/SteamRE/SteamKit/issues/791
- odota/core issue #1482: https://github.com/odota/core/issues/1482
- Protobuf trackers: https://github.com/SteamDatabase/Protobufs/blob/master/dota2/dota_gcmessages_client_match_management.proto , https://github.com/SteamTracking/GameTracking-Dota2

League administration / commercial:

- Challengermode league integration: https://support.challengermode.com/en/organizing/integrate-your-dota-2-league
- Steam Support, Dota 2 League Tickets: https://support.steampowered.com/kb/1719-OPHL-1139/dota-2-league-tickets
- IESF Dota 2 Organizer's Guideline (2024): https://nf.iesf.org/ynopsifa/2024/02/DOTA2-Organizers-Guideline.pdf
- Dota 2 league admin portal: https://www.dota2.com/league/
- OpenDota leagues list: https://api.opendota.com/api/leagues
- GC downtime overview: https://www.esports.net/news/dota/dota-2-game-coordinator/

Local (go-dota2 @ 20250623204622-532cb1851217, `GD2/`):

- `client_lobby.go:13` CreateLobby, `:21` LeaveCreateLobby
- `client_lobby_members.go:9` InviteLobbyMember
- `client_generated.go:14` AbandonLobby, `:516` DestroyLobby, `:1979` KickLobbyMemberFromTeam, `:2030` LaunchLobby, `:2073` LeaveLobby, `:3519` RequestLeagueAvailableLobbyNodes, `:3562` RequestMatchDetails, `:7671` LeagueAdminList event
- `client_request.go:17` MakeRequest
- `apigen/msg_overrides.go:67,98-99`
- `protocol/dota_gcmessages_msgid.pb.go:38,40,41,42,49,77,78,296,297,301,302`
- `protocol/dota_gcmessages_common_league.pb.go:759,873,1057,1093,1137`
- `protocol/dota_gcmessages_client_match_management.pb.go:861,874,900`
- `protocol/dota_gcmessages_common_lobby.pb.go:1858`
- This repo: `REPO/pkg/bot/manager.go:164` GetAvailableBot

---

## Verification notes

Changes after verifier review (2026-11-06):

- Executive summary item 2: the claim that admin rights populate the in-game league dropdown is now marked INFERRED,
  matching section 4.2 and section 7.
- Executive summary item 3 and section 7: the 7633 -> 7634 admin-league list is described as a likely working method
  backed by dota-ihl-bot's production use. Neither the link to the dropdown nor the current GC behaviour is verified,
  and the doc now says what test would resolve each. Re-checked `GD2/client_request.go:17` (`MakeRequest` signature fits).
- Rate limits (summary item 6, section 4.7, section 7): removed the unsupported "OpenDota filters near 180" figure.
  It is replaced with the maintainer's 100/account/24 h and 500/IP/24 h, plus `retriever.ts`
  (`matchesPerAccount = 100`, self-destruct and restart when the pool budget is used up). Both were re-fetched and
  neither contains "180".
- Challengermode (section 3, section 7): the claim that accounts are "sized to max concurrency" is now INFERRED. The
  page only asks for the estimate.
- `GetAvailableBot` (section 4.1, section 6): `REPO/pkg/bot/manager.go:164` is `GetAvailableBot()` with no arguments.
  `GetAvailableBot(leagueID)` is now labelled as a proposed change.
