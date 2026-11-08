# 03: go-dota2 client internals, SOCache, and protobuf staleness

**Track:** go-dota2 used as a library: client lifecycle, events, state, SOCache, request/response matching, generated API, raw GC messages, library health, and how stale the protobufs are.
**Pinned versions in this repo** (`go.mod`):
`github.com/paralin/go-dota2 v0.0.0-20250623204622-532cb1851217` (tag `7.39b` era) and
`github.com/paralin/go-steam v0.0.0-20250502043548-f167ff28a93a`.
**Research date:** 2026-11-06.

Path shorthands used below:

- `$D2` = `/home/kettle/.local/share/go/pkg/mod/github.com/paralin/go-dota2@v0.0.0-20250623204622-532cb1851217`
- `$GS` = `/home/kettle/.local/share/go/pkg/mod/github.com/paralin/go-steam@v0.0.0-20250502043548-f167ff28a93a`
- "upstream HEAD" = `paralin/go-dota2@master`, commit `6d32dc38d075` (2026-09-22), read through the GitHub API.

---

## 0. TL;DR

1. **The pinned go-dota2 is 16 months and about 50 commits behind upstream.** Upstream was rewritten in 2026: new protobuf generator, a switch from `golang/protobuf` to `aperturerobotics/protobuf-go-lite`, GC job-ID request correlation, session-cancellation fixes, and Go 1.26. Moving to it means a **breaking API migration**, not a version bump.
2. **The pinned go-steam probably cannot log in any more.** It uses the legacy `CMsgClientLogon` username+password flow (`$GS/auth.go:56-62`). In June 2026 users reported that this flow fails with `EResult_InvalidPassword` ([go-dota2#78](https://github.com/paralin/go-dota2/issues/78), [go-steam#11](https://github.com/paralin/go-steam/pull/11)). Upstream go-steam fixed it in `6b06e91640b1` (2026-06-27, "new token authentication method"). **This is the most urgent dependency problem for the service.**
3. **The vendored protos are stale for lobby messages, and so are upstream HEAD's protos.** Two changes matter on the wire:
   - `LobbyDotaTVDelay` was **renumbered** in SteamDatabase on 2026-08-12. Vendored and upstream-HEAD protos both say `LobbyDotaTV_120 = 1`, but current Valve protos say `LobbyDotaTV_60 = 1`, `LobbyDotaTV_120 = 2`, and so on. Setting `dota_tv_delay` from the vendored constants gives the wrong delay.
   - `CMsgPracticeLobbyLaunch` gained `fixed64 nonce = 6` on 2026-09-25.

   Upstream's automatic proto-refresh workflow has **failed on every run since 2026-04-19** (last success 2026-04-16), so upstream will not pick these up on its own. The `lstat src/google/protobuf` error is confirmed only in the readable logs (2026-08-22 onward); older logs have expired.
4. **The SOCache has structural limits that matter for a lobby bot.** It stores objects under the **owner SOID**, not the object's key field, and keeps only `object_data[0]` from each subscribed type. It is **never cleared** when the GC session ends, and subscriber channels are lossy (buffer of 10, oldest event dropped first). Upstream HEAD made it thread-safe but kept all of these limits.
5. **`MakeRequest` in the pinned version matches responses by message type only.** It sends no job ID, so two concurrent requests that expect the same response type can get each other's replies. Upstream HEAD fixes this with `jobid_source`/`jobid_target` correlation.
6. **The event pipeline can deadlock.** go-steam calls GC handlers synchronously on its network read goroutine. `Emit` blocks on a channel with a buffer of 30 (`$GS/client.go:65,89-91`). If the goroutine that drains `client.Events()` also blocks in `MakeRequest`, the read loop stops and the response never arrives.
7. **Lobby creation is fire-and-forget in the library.** The GC's `k_EMsgGCPracticeLobbyResponse` (7055) has no handler, so it surfaces only as `events.UnhandledGCPacket`. Section 6.3 shows how to capture the raw reply (the snippet was compile-checked), but the reply's decode type is UNCERTAIN; the SOCache lobby Create event is the reliable success signal.
8. **The skill notes at `~/.claude/skills/go-dota2-steam/references/go-dota2*.md` contain API calls that do not exist:** `client.On(...)`, `d.GetState()` and `e.Msg`. They also call `KickLobbyMember(memberId, team)` with two arguments; the method exists, but its real signature takes one argument, `KickLobbyMember(accountID uint32)` (`$D2/client_generated.go:1967`). See section 11.

---

## 1. Package map

| Package | Purpose | Key files |
|---|---|---|
| `dota2` (root) | `Dota2` client struct, session handshake, request tracking, hand-written lobby/party/chat helpers, ~355 generated methods | `client.go`, `client_session.go`, `client_request.go`, `client_cache.go`, `client_lobby*.go`, `client_party.go`, `client_chat.go`, `client_generated.go` (7,791 lines) |
| `events` | Event structs pushed onto the go-steam event channel | `session.go`, `state.go`, `invite.go`, `chat.go`, `generated.go` (71 generated event types) |
| `state` | `Dota2State` snapshot type | `state/state.go` |
| `socache` | Shared Object cache: containers per type ID, subscriptions | `socache.go`, `socache_container.go`, `socache_sub.go` |
| `cso` | SO type IDs and type-ID → protobuf constructor map | `cso/cso_types.go` |
| `protocol` | Generated `.pb.go` plus the patched `.proto` sources (package `protocol`, proto2) | 40 `.proto` files (and 40 `.pb.go`); the header says `protoc-gen-go v1.36.6`, `protoc v5.29.3` (`$D2/protocol/dota_gcmessages_client.pb.go:1-4`) |
| `apigen` | Code generator: classifies `EDOTAGCMsg` into client requests, GC responses and events, and emits `client_generated.go` and `events/generated.go` | `msg_overrides.go`, `msg_response.go`, `snapshot-*.txt` |
| `generator` | `update_protos.bash`: copies protos from the `SteamDatabase/Protobufs` submodule and patches them | `generator/update_protos.bash` |

The project's [README](https://github.com/paralin/go-dota2) says the protos come from **SteamDatabase/Protobufs**, not GameTracking-Dota2. Upstream `.gitmodules` confirms it: `generator/Protobufs → https://github.com/SteamDatabase/Protobufs`. GameTracking-Dota2 holds the raw dumps that feed SteamDatabase/Protobufs.

---

## 2. The `Dota2` client struct and lifecycle

### 2.1 Struct and constructor (verified, `$D2/client.go:32-66`)

```go
type Dota2 struct {
    le     logrus.FieldLogger
    client *steam.Client
    cache  *socache.SOCache
    connectionCtxMtx    sync.Mutex
    connectionCtx       context.Context      // non-nil only while HAVE_SESSION
    connectionCtxCancel context.CancelFunc
    stateMtx sync.Mutex
    state    state.Dota2State
    handlers handlerMap                      // map[uint32]func(*GCPacket) error
    pendReqMtx sync.Mutex
    pendReqID  uint32
    pendReq    map[uint32]map[uint32]responseHandler // respMsgID -> reqID -> handler
}

func New(client *steam.Client, le logrus.FieldLogger) *Dota2 // registers itself via client.GC.RegisterPacketHandler
```

- `New` builds the handler map, then calls `client.GC.RegisterPacketHandler(c)` (`client.go:64`). go-steam **appends** to a plain slice and has no unregister function (`$GS/gamecoordinator.go:28-30`). Every call to `dota2.New` on the same `steam.Client` therefore adds another live handler.
  - **This affects our code today.** `pkg/bot/manager.go:95` calls `dota2.New` on **every** `LoggedOnEvent`. After N reconnects, N `Dota2` instances all decode every GC packet and emit duplicate events (verified by reading). The fix is to create one `Dota2` per `steam.Client`, once, before `Connect()`.
- `Close()` (`client.go:74-80`) only cancels the connection context. It does not unregister the handler or clear the cache.
- `GetCache()` is the only public accessor for internal state. **There is no `GetState()`** (grep shows no `func (d *Dota2) GetState`). State is visible only through `events.ClientStateChanged`.

### 2.2 Handshake: `SetPlaying` → `SayHello` → `ClientWelcome`

Verified in `$D2/client_session.go`:

| Step | Code | What it does |
|---|---|---|
| `SetPlaying(true)` | `:14-17` | `client.GC.SetGamesPlayed(570)` sends `EMsg_ClientGamesPlayed` (`$GS/gamecoordinator.go:68-79`) |
| `SayHello(haveVersions...)` | `:27-38` | Sends `k_EMsgGCClientHello` with `Engine=k_ESE_Source2`, `ClientLauncher=PARTNER_NONE`, `ClientSessionNeed=104`, and optional `SocacheHaveVersions`. It sends **exactly once**, with no retry. |
| GC → `k_EMsgGCClientWelcome` | `:59-79` | For each `uptodate_subscribed_caches` entry it sends `k_ESOMsg_CacheSubscriptionRefresh` (`client_cache.go:10-14`). Each `outofdate_subscribed_caches` entry is fed to `cache.HandleSubscribed`. Then it sets `HAVE_SESSION` and emits `*events.ClientWelcomed{Welcome}`. |
| GC → `k_EMsgGCClientConnectionStatus` | `:82-94` | Calls `setConnectionStatus(*stat.Status, stat)` |
| `SetPlaying(false)` | `:18-22` | Clears games played, then `ClearState()` directly **without cancelling `connectionCtx`**. Pending `MakeRequest` calls are not woken, and `validateConnectionContext` still says the client is ready until the next status change (verified; upstream f93daab5 fixes this: "Cancel pending calls when play stops"). |

**Hello retry is the caller's job.** The library sends one hello. Other mature clients keep sending it until the welcome arrives:

- ValvePython `_knock_on_gc` resends with backoff `3 + 2^n` seconds, capping `n` at 4 ([dota2/client.py](https://github.com/ValvePython/dota2/blob/master/dota2/client.py)).
- node-dota2 resends on an interval and fires `hellotimeout` after about 10 tries ([node-dota2/index.js](https://github.com/Arcana/node-dota2/blob/master/index.js)).

Our bot manager should run a loop: `SayHello()` every 5–30 s until `ClientWelcomed` arrives or `GCConnectionStatusChanged{NewState: HAVE_SESSION}` is seen. It should restart the loop whenever the state drops away from `HAVE_SESSION`.

> Current `pkg/bot/manager.go` never calls `SetPlaying(true)` or `SayHello()` (lines 86-99), so the GC session is never established today (verified by reading).

### 2.3 Connection status and state

`state.Dota2State` (`$D2/state/state.go:8-29`):

| Field | Populated? |
|---|---|
| `ConnectionStatus GCConnectionStatus` | Yes, by `setConnectionStatus` |
| `Lobby *CSODOTALobby` | **Never.** No assignment exists anywhere in the module (grep `\.Lobby =` returns nothing). |
| `Party`, `PartyInvite` | **Never** (same grep) |
| `LastConnectionStatusUpdate` | **Never** in the pinned version (upstream HEAD sets it) |

`IsReady()` means `ConnectionStatus == GCConnectionStatus_HAVE_SESSION`.

**Use the SOCache for lobby and party state, never `Dota2State`.**

`setConnectionStatus` (`client_session.go:98-131`) does the following when the status actually changes:

1. Emits `*events.GCConnectionStatusChanged{OldState, NewState, Update}`. This happens **while `stateMtx` is held**, inside `accessState`.
2. Calls `ClearState()` and stores the new status.
3. Cancels the old `connectionCtx`, which fails all in-flight `MakeRequest` calls with `ErrNotReady`. On `HAVE_SESSION` it creates a fresh context.
4. `accessState` then emits `events.ClientStateChanged{OldState, NewState}`. This is a **value type, not a pointer**. Type-switch on `devents.ClientStateChanged`, not `*devents.ClientStateChanged` (`client.go:136`).

**The SOCache is not touched on any status change.** See section 3.4.

### 2.4 Events, and how they are delivered

All events go through `d.client.Emit(event)` (`client.go:121-123`), which is `c.events <- event` on a channel with a buffer of 30 (`$GS/client.go:65,89-91`). There is **no** `client.On(...)` API. You consume with `for ev := range steamClient.Events() { switch e := ev.(type) {...} }`.

| Event (package `events`) | Emitted when | Ptr? |
|---|---|---|
| `GCConnectionStatusChanged{OldState, NewState, Update}` | Any status transition | `*` |
| `ClientWelcomed{Welcome *CMsgClientWelcome}` | Welcome processed | `*` |
| `ClientStateChanged{OldState, NewState state.Dota2State}` | Any state mutation | value |
| `UnhandledGCPacket{Packet *GCPacket}` | No static handler and no pending request matched | `*` |
| `ChatMessage`, `JoinedChatChannel`, `InvitationCreated` | Hand-registered (`client.go:98-109`) | `*` |
| 71 generated events, e.g. `LeagueAdminList`, `LobbyMVPAwarded`, `MatchSignedOut`, `TeamInfo`, `PartySearchInvite` | `registerGeneratedHandlers` (`client_generated.go` tail) | `*` |

**Deadlock hazard (verified by reading the code paths).** go-steam calls `GameCoordinator.HandlePacket` → every `GCPacketHandler.HandleGCPacket` synchronously on the TCP read goroutine (`$GS/gamecoordinator.go:32-49`, `$GS/client.go:206-222`). `HandleGCPacket` emits events *before* it delivers responses to pending requests (`client.go:162-176`). So if the single goroutine draining `Events()` is blocked in `MakeRequest` (or in anything that waits on GC traffic), the following happens:

1. More than 30 events pile up.
2. `Emit` blocks.
3. The read loop stops.
4. The response is never read.
5. Steam heartbeats eventually fail too.

**Rule:** drain `Events()` on a dedicated goroutine that never blocks on GC round-trips. Hand work to worker goroutines through your own buffered channels.

---

## 3. SOCache internals

### 3.1 Data model (verified, `$D2/socache/*.go`)

```
SOCache
 └─ containerMap sync.Map: typeID(uint32) -> *SOCacheContainer   (created lazily, only for types known to cso)
      SOCacheContainer
       ├─ objects       sync.Map: uint64 -> proto.Message   // KEY = owner SOID id (!)
       └─ subscriptions sync.Map: uint32 -> chan *CacheEvent (cap 10)
```

`CacheEvent{EventType: Create|Update|Destroy, Object proto.Message}` (`socache_sub.go:8-27`).

Public API:

| Call | Notes |
|---|---|
| `cache.SubscribeType(cso.Lobby) (<-chan *CacheEvent, CacheUnsubscribeFunc, error)` | Channel buffer is 10. The unsubscribe function deletes the map entry but **never closes the channel** (`socache_container.go:150-157`), so `for range ch` never ends on its own. Select on a done channel. |
| `cache.GetContainerForTypeID(uint32(cso.Lobby))` | Returns an error for type IDs with no constructor |
| `ctr.Get(id)`, `ctr.GetOne()`, `ctr.Range(cb)` | `GetOne` returns whatever `sync.Map.Range` yields first, which is arbitrary when more than one object exists |

### 3.2 Known SO types (`$D2/cso/cso_types.go:17-86`)

| CSOType | ID | Constructor registered? | Proto |
|---|---|---|---|
| EconItem | 1 | yes | `CSOEconItem` |
| DropRateBonus | 38 | yes | `CSOEconItemDropRateBonus` |
| EventTicket | 40 | **no** | n/a |
| ItemTournamentPassport | 42 | **no** (the `CSOEconItemTournamentPassport` message exists in protos) | n/a |
| GameAccountClient | 2002 | yes | `CSODOTAGameAccountClient` |
| Party | 2003 | yes | `CSODOTAParty` |
| Lobby | 2004 | yes | `CSODOTALobby` |
| PartyInvite | 2006 | yes | `CSODOTAPartyInvite` |
| GameHeroFavorites | 2007 | yes | `CSODOTAGameHeroFavorites` |
| MapLocationState | 2008 | yes | `CSODOTAMapLocationState` |
| Tournament | 2009 | **no** | n/a |
| PlayerChallenge | 2010 | yes | `CSODOTAPlayerChallenge` |
| LobbyInvite | 2011 | yes | `CSODOTALobbyInvite` |
| GameAccountPlus | ? | Added upstream in PR [#70](https://github.com/paralin/go-dota2/pull/70) (merged as `958f2d4a`), **not in our pin** | `CSODOTAGameAccountPlus` |

SO types with no constructor are silently dropped: `GetContainerForTypeID` returns an error, which is logged at Debug and ignored (`client_cache.go:23-25`). The protos also define `CSODOTAStaticLobby`, `CSODOTAServerLobby` and `CSODOTAServerStaticLobby` (`dota_gcmessages_common_lobby.proto:330-342`). These are server-side or static lobby parts that the client cache never decodes.

**For goal (4), listing tickets and leagues:** if any league or ticket data reaches the bot through the SOCache (EventTicket 40, ItemTournamentPassport 42, or league-ticket items inside the `CSOEconItem` inventory), the pinned library **cannot see most of it**. Types 40 and 42 have no constructor, and EconItems collapse to one entry (next section). Other research tracks cover the actual league or ticket message flow. This track's conclusion is that the SOCache needs patching or a side-channel `GCPacketHandler` to inspect raw `k_ESOMsg_CacheSubscribed` payloads.

### 3.3 Population and update flow

| GC message | Handler | Behaviour |
|---|---|---|
| `k_EMsgGCClientWelcome.outofdate_subscribed_caches[]` | `cache.HandleSubscribed` | Initial population at login |
| `k_ESOMsg_CacheSubscribed` (24) | `handleCacheSubscribed` → `SOCache.HandleSubscribed` (`socache.go:42-60`) | For each `objects[]` (SubscribedType), container `addUpdateObject(owner_soid, obj)`. **Only `object_data[0]` is parsed** (`socache_container.go:41`). |
| `k_ESOMsg_UpdateMultiple` (26) | `HandleUpdateMultiple` (`socache.go:84-127`) | added/modified → `addUpdateObject(owner_soid, ...)`. removed → `removeObject(owner_soid)`. |
| `k_ESOMsg_Destroy` (23) | `HandleDestroy` | `removeObject(owner_soid)` |
| `k_ESOMsg_CacheUnsubscribed` (25) | `HandleUnsubscribed` (`socache.go:63-81`) | Finds the **first** container holding key `owner_soid.id`, removes from that one only, then stops ranging |
| `k_ESOMsg_Create` (21), `Update` (22), `CacheSubscribedUpToDate` (29) | **no handler** | These become `UnhandledGCPacket` |

A lobby lives in its own cache, whose owner is the lobby SOID. The bot account's own cache (owner = SteamID) carries `CSODOTAGameAccountClient`, `CSOEconItem`, `CSODOTALobbyInvite`, `CSODOTAPartyInvite`, and others. This is inferred from the structure of the code and is consistent with the SteamKit/node-dota2 design, but not traced packet by packet.

### 3.4 Structural defects, and what they mean for us

| # | Defect (verified in source) | Impact on a lobby bot |
|---|---|---|
| S1 | **Objects keyed by owner SOID, not by the object's `key_field`** (`socache_container.go:81-100`: `soID := soid.GetId()`) | One object per (type, owner). Fine for a single `CSODOTALobby` (owner = lobby). **Broken** for multi-object types in the user cache: many `CSOEconItem`, or several `CSODOTALobbyInvite`/`CSODOTAPartyInvite` at once. Only the last one survives. |
| S2 | Only `object_data[0]` decoded per SubscribedType (`socache_container.go:41`) | On initial subscribe, the 2nd and later objects of a type are lost (inventory items, multiple invites) |
| S3 | **Cache never cleared on GC session loss or `SetPlaying(false)`** (`setConnectionStatus` does not touch `d.cache`) | After a GC drop, a stale `CSODOTALobby` stays. `LeaveCreateLobby` then sees it through `GetOne()` (`client_lobby.go:35`) and tries to leave or destroy a lobby that may already be gone. The API's `/lobby/info` could serve dead data. |
| S4 | A re-subscribe does not prune objects missing from the new snapshot | Same as S3: stale entries survive a reconnect |
| S5 | `HandleUnsubscribed` stops at the first container with a matching key (`socache.go:69-78`) | Other types under the same owner are not removed |
| S6 | Lossy subscriptions: when the 10-slot channel is full, the oldest event is dropped, with up to 3 tries (`socache_container.go:57-78`) | A slow consumer can miss Create or Destroy. Always re-read `ctr.Get/GetOne` after an event; do not treat the stream as the source of truth. |
| S7 | `GetOne()` is arbitrary with more than one object; no snapshot isolation in pinned `Range` | Matters only if S3 leaves two lobbies cached |
| S8 | Unsubscribe never closes the channel | Goroutine leak if the consumer ranges over it |

Upstream HEAD (`socache/socache_container.go@master`) replaced `sync.Map` with a mutex-guarded map, made `Range` operate on a cloned snapshot, and made publication atomic. **S1, S2, S3, S4, S6 and S8 are unchanged.** It still uses `soid.GetId()` as the key, still parses `GetObjectData()[0]`, and `client-session.go` still does not clear the cache.

**What to do in our service, either way:**

1. On every `GCConnectionStatusChanged` where `NewState != HAVE_SESSION`, treat all cached lobby data as **unknown**. Mark bots "not ready", and do not trust `GetOne()` until a fresh `CacheSubscribed` arrives after the next welcome.
2. Track "my lobby" by `lobby_id` taken from the create flow and `Get(lobbyID)`, instead of `GetOne()`.
3. If we need inventory, tickets or multiple invites, use a fork patch: key by the object's key field, decode every `object_data[i]`, clear per-owner on resubscribe and on session loss.

---

## 4. `MakeRequest`: request/response matching

### 4.1 Pinned behaviour (verified, `$D2/client_request.go:17-106`)

```go
func (d *Dota2) MakeRequest(ctx context.Context, reqMsgID uint32, request proto.Message,
    respMsgID uint32, response proto.Message, matchesRequest ...func(proto.Message) bool) error
```

1. `validateConnectionContext()` returns `ErrNotReady` unless the client is `HAVE_SESSION` (`client_session.go:41-56`).
2. A closure is registered under `pendReq[respMsgID][reqID]`. `reqID` is a **local counter only**. It is never put on the wire.
3. `d.write(reqMsgID, request)` builds `NewGCMsgProtobuf` with **no `jobid_source`** (`client.go:116-118`; `$GS/protocol/gamecoordinator/msg.go:30-38`).
4. Every incoming packet goes through `handleResponsePacket` (`:93-106`), which iterates *every* handler waiting on that `MsgType` in random map order. Each closure **unmarshals into the caller's shared `response` object**, then runs the `matchesRequest` predicates. The first predicate that returns true wins.
5. `select` on `ctx.Done()` (returns `ctx.Err()`), `connectionCtx.Done()` (returns `ErrNotReady`), or the response channel.
6. On error, a deferred cleanup removes the pending entry.

Consequences:

| Issue | Detail |
|---|---|
| Cross-talk | Two concurrent requests with the same `respMsgID` and no predicate: whichever handler the map yields first takes the next reply. |
| Mutation after return | When a predicate rejects a packet, the closure has already unmarshalled into `response`. A late packet for a cancelled request still runs the closure until the deferred cleanup takes the lock, and so can mutate an object the caller is reading. |
| Static handlers too | If the `MsgType` also has a static or generated event handler, the event is emitted **and** the request is satisfied (`client.go:162-176`). |
| Timeouts | Only via `ctx`. Always use `context.WithTimeout`, because the GC can silently drop requests (for example [#61](https://github.com/paralin/go-dota2/issues/61) "haven't received any task callback information"). |

**Practical rule for the pinned version:** serialize GC requests **per bot** (one in-flight request per response message type), or always pass a `matchesRequest` predicate that checks an identifying field such as `lobby_id` or `league_id`.

### 4.2 Upstream HEAD behaviour (verified from `client-request.go@master`, commit `f93daab554a5`)

- Allocates `jobID := ++d.nextJobID` (monotonic across sessions) and calls `message.SetSourceJobId(jobID)`.
- `handleResponsePacket` routes **only** when `pending[packet.TargetJobId]` exists and `messageType == packet.MsgType`. go-steam already parses `TargetJobId` from the header (`$GS/protocol/gamecoordinator/packet.go:12-19,38`).
- Decodes on the caller's goroutine, so late replies cannot mutate a returned result. Calls `msg.Reset()` before unmarshal.
- `setConnectionStatus` now emits events **after** releasing the mutex, and `SetPlaying(false)` cancels pending calls.

**Uncertain:** HEAD's model depends on the GC echoing `jobid_source` back as `jobid_target` for every reply type a caller uses. node-dota2 routes practice-lobby replies through job callbacks (`sendToGC(type, payload, handler, callback)`, [handlers/lobbies.js](https://github.com/Arcana/node-dota2/blob/master/handlers/lobbies.js)), which suggests the GC does echo for those. Any reply type that is *broadcast* rather than job-addressed would time out under HEAD. Test each message we rely on.

---

## 5. Generated API (apigen)

- `apigen` runs heuristics over every `EDOTAGCMsg` value. It classifies the sender (CLIENT, GC, N/A), pairs requests with responses by naming, and picks a verb for the method name. Hand-written overrides live in `apigen/msg_overrides.go`. The output is snapshotted in `apigen/snapshot-messages.txt` (the list of computed client methods is at line 960).
- Result in the pin: **355** `func (d *Dota2)` in `client_generated.go`. Of these, **270** are request/response (`ctx` plus a `MakeRequest` return) and the rest are fire-and-forget `d.write`. There are 71 generated events.
- Shapes:
  - Fire-and-forget: `func (d *Dota2) LaunchLobby()`, `KickLobbyMember(accountID uint32)`, `SetLobbyDetails(req *CMsgPracticeLobbySetDetails)`, `LeaveLobby()`, `AbandonLobby()`, `ApplyTeamToLobby(teamID)`.
  - Request/response: `DestroyLobby(ctx) (*CMsgDOTADestroyLobbyResponse, error)`, `ListLobbies(ctx, passKey, region, gameMode)`, `JoinLobby(ctx, lobbyID, passKey, crc, ts)`, `RequestLeagueAvailableLobbyNodes(ctx, leagueID) (*CMsgDOTALeagueAvailableLobbyNodes, error)` (`client_generated.go:3519`).
- **Overridden to "no wrapper" (`MsgSenderNone`)**, relevant to us (`msg_overrides.go`):
  - `k_EMsgGCPracticeLobbyResponse` (7055), the reply to create, launch, kick and similar (`:33`)
  - `k_EMsgGCPracticeLobbyCreate`, `k_EMsgGCPracticeLobbySetTeamSlot`, `k_EMsgGCClearPracticeLobbyTeam`, which are hand-written instead (`:70-72`)
  - `k_EMsgDOTALeagueInfoListAdminsRequest/Reponse` (7633/7634) (`:97-98`)
  - `k_EMsgGCLeagueAdminList` (7434) is forced to `MsgSenderGC`, so it is **an event only**: `events.LeagueAdminList{CMsgLeagueAdminList{account_ids}}`
- Hand-written lobby helpers (`client_lobby*.go`):
  - `CreateLobby(details)`: fire-and-forget `CMsgPracticeLobbyCreate{PassKey, LobbyDetails}`. **It does not set `client_version`** (field 6) or `search_key`.
  - `LeaveCreateLobby(ctx, details, destroyOld)`: a loop driven by the SOCache that leaves or destroys any existing lobby and then creates one. It inherits the SOCache staleness (S3) and `GetOne` arbitrariness (S7).
  - `JoinLobbyTeam`, `SetLobbyBotSlotDifficulty`, `InviteLobbyMember(steamid)`, `RespondLobbyInvite`, `ClearTeamFromLobby` (sends an empty `CMsgFlipLobbyTeams` because the real proto is unknown, `client_lobby.go:72-78`).

Lobby fields relevant to leagues exist in the pinned protos: `CMsgPracticeLobbySetDetails.leagueid = 16` and `league_node_id = 49` (`dota_gcmessages_client_match_management.proto:137,163`), and `CSODOTALobby.leagueid = 42` (`dota_gcmessages_common_lobby.proto:265`).

---

## 6. Calling a GC message that has no wrapper

### 6.1 Sending (any message, pinned version)

`d.write` is unexported. Use go-steam directly. `GC.Write` sets the protobuf mask bit (`0x80000000`) for you (`$GS/gamecoordinator.go:51-65`).

```go
import (
    "github.com/golang/protobuf/proto"
    dota2 "github.com/paralin/go-dota2"
    "github.com/paralin/go-dota2/protocol"
    "github.com/paralin/go-steam"
    "github.com/paralin/go-steam/protocol/gamecoordinator"
)

func rawSend(sc *steam.Client, msgID protocol.EDOTAGCMsg, body proto.Message) {
    sc.GC.Write(gamecoordinator.NewGCMsgProtobuf(dota2.AppID, uint32(msgID), body))
}

// e.g. a message apigen marked N/A:
rawSend(sc, protocol.EDOTAGCMsg_k_EMsgDOTALeagueInfoListAdminsRequest,
    &protocol.CMsgDOTALeagueInfoListAdminsRequest{})
```

For base-SDK messages (`EGCBaseClientMsg`, `EGCBaseMsg`, `ESOMsg`), cast those enums to `uint32` the same way. Whether the GC accepts a given client-originated message (permission, rate limits) is a separate question for the protocol tracks.

### 6.2 Receiving: three options

| Option | How | When |
|---|---|---|
| A. `UnhandledGCPacket` event | `case *devents.UnhandledGCPacket:` then `proto.Unmarshal(e.Packet.Body, &protocol.X{})` | Response types with no static handler and no pending request, e.g. 7055 |
| B. `MakeRequest` with explicit IDs | `d.MakeRequest(ctx, reqID, req, respID, resp, pred...)` | Any request/reply pair. It is exported, so no wrapper is needed. |
| C. Your own `GCPacketHandler` | `sc.GC.RegisterPacketHandler(myTap)` **before** `Connect()` (the slice is not mutex-protected) | Inspect *every* packet, including ones go-dota2 handles (e.g. raw `k_ESOMsg_CacheSubscribed` to recover data the SOCache drops). Runs on the read goroutine, so never block in it. |

### 6.3 Example: getting a result for lobby creation (compile-checked against the pinned modules)

```go
// The reply to k_EMsgGCPracticeLobbyCreate is k_EMsgGCPracticeLobbyResponse (7055).
// Decode as CMsgGenericResult (eresult = EResult, 1 = OK; debug_message = field 2). See gap-05.
// NOTE: go-dota2 sends no source job ID, and on upstream master MakeRequest matches by job ID,
// so prefer a job-addressed send + GCPacketHandler tap (gap-05 §5.2).
func createAndWait(ctx context.Context, d *dota2.Dota2,
    details *protocol.CMsgPracticeLobbySetDetails) (*protocol.CMsgGenericResult, error) {
    res := &protocol.CMsgGenericResult{}
    err := d.MakeRequest(ctx,
        uint32(protocol.EDOTAGCMsg_k_EMsgGCPracticeLobbyCreate),
        &protocol.CMsgPracticeLobbyCreate{PassKey: details.PassKey, LobbyDetails: details},
        uint32(protocol.EDOTAGCMsg_k_EMsgGCPracticeLobbyResponse),
        res)
    return res, err
}
```

Caveats:

- 7055 is shared by create, flip, shuffle, kick, launch and leave replies. The source is the node-dota2 comment "Received create/flip/shuffle/kick/launch/leave response" in `handlers/lobbies.js` `onPracticeLobbyResponse`. With the pinned type-only matching, **serialize lobby operations per bot**.
- **Decode type: settled enough to implement ([gap-05](gap-05-7055-decode-and-league-rejection.md)).** 7055 is `CMsgGenericResult{eresult = 1 [default = 2], debug_message = 2}` (`$D2/protocol/econ_shared_enums.proto:37-40`), and `eresult` is a Steam EResult (1 = OK, 2 = Fail). node-dota2's own `.result === 1` check is broken (the field does not exist), but dota-ihl-bot checks `eresult === EResult.OK` in production, and node-dota2-user and under-5k also decode it as `CMsgGenericResult`. The GC addresses the create reply by job ID. Not every lobby op produces a 7055 (flip/shuffle/abandon do not, node-dota2 #654). A live capture is still wanted for golden tests.
- Because pinned `MakeRequest` matches by message type only, this call also accepts any other 7055 reply (launch, kick, leave and so on) that arrives first.
- Treat a `CSODOTALobby` Create event in the SOCache as the authoritative success signal. Use the 7055 result to fail fast.

The snippet, together with a custom tap handler, `SubscribeType(cso.Lobby)`, an event-switch over `GCConnectionStatusChanged`, `ClientWelcomed`, `ClientStateChanged`, `UnhandledGCPacket` and `LeagueAdminList`, and `RequestLeagueAvailableLobbyNodes`, passed `go vet` in a scratch module pinned to our `go.mod` versions (Go 1.27.1, `GOPROXY=off`).

---

## 7. Library health

### 7.1 go-dota2 (github.com/paralin/go-dota2)

| Metric (GitHub API, 2026-11-06) | Value |
|---|---|
| Archived | no |
| Last push | 2026-11-04; last code commit `6d32dc38d075` on 2026-09-22 |
| Stars / forks | 184 / 48 |
| Open issues | 2: one Renovate PR ([#81](https://github.com/paralin/go-dota2/pull/81) "fix(deps): update all dependencies") and one human-filed issue ([#21](https://github.com/paralin/go-dota2/issues/21) "user guide document") |
| Tags | `7.39b`, `7.40c`, `7.41b` (2026-04-15), `7.41d` and `7.41d-r2` (2026-06-27) |
| Commits since our pin (since 2025-06-23) | about 50 |
| Maintainer | Christian Stewart (paralin), solo. Responsive to issues with short answers. |

Major upstream changes after our pin, by commit:

| Commit | Date | Change | Breaking for us? |
|---|---|---|---|
| `958f2d4a` | 2026-02-24 | GameAccountPlus SO (#70) | no |
| `8a9b8a92`, `711ff66c` | 2026-02-24 | Protos and apigen updated to the then-current API | Possibly: renamed or removed generated methods |
| `3003d54c` | 2026-02-24 | Pure-Go generate pipeline (`cmd/generate`) and the `update-protos.yml` workflow (cron every 3 days) | no |
| `47364178` | 2026-06-27 | New protobuf generator (about 600k lines changed) | yes (generated types) |
| `f3f02e3a` | 2026-08-22 | Generate with `aperturerobotics/common` (`aptre`) | build only |
| `f93daab5` | 2026-09-22 | **GC job-ID correlation, session cancellation, `Coordinator` interface, `NewWithCoordinator`** | `MakeRequest` semantics |
| `e63ba66e` | 2026-09-22 | **Drops `github.com/golang/protobuf`; uses `protobuf-go-lite` (`UnmarshalVT`, `protobuf.Message`)** | **Yes.** `proto.String`, `proto.Unmarshal` and the `proto.Message` interface change. |
| HEAD `go.mod` | n/a | `go 1.26.0`; code uses `new(uint32(104))` (Go 1.26 syntax) | Our `go.mod` says `go 1.24.11`, so it needs a bump. Our toolchain is 1.27.1, which is fine. |

**Upstream proto automation is broken.** The `update-protos.yml` runs show 70 failures and 20 successes in the last 90 runs. The last success was 2026-04-16 and every run since 2026-04-19 has failed. The current failure is in step "Generate protobufs" (this exact error is confirmed in every readable run log from 2026-08-22 through 2026-11-04; logs before about August return HTTP 410, so the cause of the April-August failures is unknown):
`vendor protobuf includes: lstat src/google/protobuf: no such file or directory` → `make: *** [Makefile:29: gen] Error 1` (run log for 2026-11-04).

SteamDatabase/Protobufs commit `bd8f82df2781` (2026-10-04, per-folder `descriptor.proto`) is **not** the cause: the same `lstat` error appears in runs from 2026-08-22 to 2026-10-01, before that commit. The cause is UNCERTAIN. One guess, not verified, is a change in the generator's include-path logic around `f3f02e3a` (2026-08-22, "generate protobufs with aperturerobotics/common"), which matches the first readable failing log; resolving it needs reading `cmd/generate`'s vendoring code at HEAD. Upstream HEAD's `generator/Protobufs` submodule is pinned at `e9809b2f954f` (2026-07-10).

Relevant issues:

| Issue | Takeaway |
|---|---|
| [#78](https://github.com/paralin/go-dota2/issues/78) (2026-06) "Login Issues?" | Legacy Steam password login broke for all libraries. Fixed in go-steam master (see 7.2). |
| [#77](https://github.com/paralin/go-dota2/issues/77) (2026-05) | A headless bot calling `LaunchLobby()` while in a player or broadcast slot gets stuck at loading. Maintainer: "Join the caster slot and then start. Then once game starts you can leave." |
| [#69](https://github.com/paralin/go-dota2/issues/69) (2025-08) | Same loading problem. Advice: keep the bot in the unassigned/player pool, or "join spectator slot". |
| [#72](https://github.com/paralin/go-dota2/issues/72) (2025-11) | "Migrating from ValvePython/dota2 - Question about Game Coordinator disconnects after 60 min". A question, not a go-dota2 bug: the drop was seen with ValvePython, and the author later said go-dota2's keepalives seem fine (N=2). Anecdotal. |
| [#71](https://github.com/paralin/go-dota2/issues/71) (2025-10) | `CMsgReadyUp` got no server response (`FAILED_TO_READY_UP`). Relevant only to matchmaking. |
| [#61](https://github.com/paralin/go-dota2/issues/61) (2024-12) | `CreateLobby` gives "no callback". Confirms the fire-and-forget design (section 6.3). |
| [#67](https://github.com/paralin/go-dota2/issues/67), [#68](https://github.com/paralin/go-dota2/pull/68) (2025-06) | Test and build breakage fixed in our exact pin (`532cb185`, "rename AcceptInvite") |

**Forks:** none is maintained ahead of upstream. The most recently pushed forks are even with or behind master: `JasonSecurity` 0/0, `unspectd` 0 ahead/10 behind, `VandersonTavares` 0/11, `Robbejj-PGL` 0/13. Some forks do carry commits master lacks, but nothing substantive: `gg-cr` is 2 ahead/23 behind (a module-path rename to gg-cr, plus a `cso` `ErrUnknownType` sentinel and a quieter unknown-type welcome-cache log), and `marceloarc` is 1 ahead/36 behind ("fix internal imports"). `amandladev` is 15 ahead/46 behind; most of its diff is `examples/basic/**` (including the README files), but it also changes `client_party.go`, `go.mod`, `go.sum` and `.gitignore`. **Upstream is the only live line.** If we need fixes now (SOCache, current protos), we must run **our own fork**.

### 7.2 go-steam (github.com/paralin/go-steam)

| | Pinned `f167ff28a93a` (2025-05-02) | Upstream master `336e824f8540` (2026-09-22) |
|---|---|---|
| Login | Legacy `CMsgClientLogon` with `Password`, `LoginKey`, `SentryFileHash`, `ShouldRememberPassword` (`$GS/auth.go:22-39,56-76`) | `IAuthenticationService` token flow (`6b06e91640b1`, 2026-06-27). PR #11's description says legacy logon returns `EResult_InvalidPassword`. |
| Protobuf runtime | `github.com/golang/protobuf v1.5.4` | `protobuf-go-lite` (`c82323035020`) |
| Heartbeat / reconnect | basic | `e882f1724962` "reconnect after unanswered heartbeats", `8c23cbb74e27` "bind transport work to connection lifetime", `908c060a` "expose playing session availability" |
| CM list | static/random | Directory cache auto-init and HTTP error handling (`4263b220`, `9a903700`) |

**Inference, high confidence:** our pinned go-steam cannot log in bot accounts with username and password today. Evidence: multiple users in #78 in June 2026, the PR #11 author's live verification, and the maintainer's "Fixed!". `secrets.yaml` support for `sentry_hash` is obsolete under the new flow. This needs its own verification run before any other lobby work.

### 7.3 "Deprecated golang/protobuf v1 usage"

- In our pin, the generated `.pb.go` files are **APIv2** (`protoimpl`, `protoc-gen-go v1.36.6`). Hand-written code (`client.go:8`, `socache`, `cso`, `events`) imports `github.com/golang/protobuf/proto` v1.5.4. That module is a thin, deprecated shim over `google.golang.org/protobuf`. It works, but linters flag SA1019.
- Upstream HEAD removed it completely in favour of `protobuf-go-lite`. That is a **different message interface**: generated `MarshalVT`/`UnmarshalVT`, no reflection-based `proto.Message`. Any of our code that uses `proto.String`, `proto.Unmarshal`, `protojson`, or passes messages to generic protobuf tooling will need changes when we upgrade.

---

## 8. How stale are the vendored protos?

### 8.1 Method

I downloaded current `SteamDatabase/Protobufs@master/dota2/*.proto` for six files. I normalized both sides the way `update_protos.bash` does (strip leading `.` on type names and the `syntax`/`package`/`import`/`option` lines) and diffed them against `$D2/protocol/*.proto`. A second pass stripped the new Valve `boxed_type="..."` and `synthetic_default` field options.

| File | Changed lines (raw) | Real semantic changes (after stripping options) |
|---|---|---|
| `dota_gcmessages_client_match_management.proto` | 15 | `CMsgPracticeLobbyLaunch` **+ `fixed64 nonce = 6`** |
| `dota_gcmessages_common_lobby.proto` | 55 | **`LobbyDotaTVDelay` renumbered**; `LobbyType` **+ `AUTOMATED_BOT_ONLY_MATCH = 16`**; `CSODOTALobby` + `current_primary_event_for_display = 104` |
| `dota_gcmessages_msgid.proto` | 72 | + `k_EMsgClientToGCWatchingBroadcast = 8077`, `k_EMsgLobbyOverworldFortuneList = 8185`, about 30 Monster Hunter / Fantasy Crafting IDs (9023-9055+); − `k_EMsgDevGrantEventPoints*`, `k_EMsgDevResetEventState*`, `k_EMsgGCToClientPostGameItemAwardNotification` |
| `dota_gcmessages_common_league.proto` | 70 | Node/group tiebreak fields replaced (`score_tiebreak_*` → `tiebreak_game_win_pct`, …); + `win_loss_limit = 27`; + `duration = 3` in a nested message. **League admin and info messages are unchanged.** |
| `gcsdk_gcmessages.proto` | 12 | Mostly options; + `trusted_servers_only = 6` in `CMsgGCToGCMasterBroadcastMessage`. **`CMsgClientHello` keeps the fields we send.** |
| `dota_gcmessages_client.proto` | 355 | Event, overworld and claim-action churn; nothing lobby or league critical in the diff sample |

### 8.2 The `LobbyDotaTVDelay` renumbering (verified)

| Name | Vendored (and upstream HEAD) | Current Valve (since SteamDatabase/Protobufs `c983b3e16435` = GameTracking-Dota2 `c592bf50`, 2026-08-12) |
|---|---|---|
| `LobbyDotaTV_10` | 0 | 0 |
| `LobbyDotaTV_60` | (absent) | **1** |
| `LobbyDotaTV_120` | **1** | 2 |
| `LobbyDotaTV_300` | **2** | 3 |
| `LobbyDotaTV_900` | **3** | 4 |

The wire carries integers. Sending `protocol.LobbyDotaTVDelay_LobbyDotaTV_300` from our pin (value 2) is now interpreted as **120 s**, and the vendored `_120` (value 1) means **60 s**. This hits `CMsgPracticeLobbySetDetails.dota_tv_delay = 24` and its decode in `CSODOTALobby.dota_tv_delay = 53`. **Workaround without regenerating:** send raw integers using the current mapping, e.g. `protocol.LobbyDotaTVDelay(3)` for 300 s, and document it. Our code does not set `dota_tv_delay` today (grep finds nothing). The proto default `LobbyDotaTV_120` is an integer (1) on the client side but is not sent unless set, because proto2 optional fields are omitted when unset.

### 8.3 `CMsgPracticeLobbyLaunch.nonce` (verified present upstream, effect uncertain)

The field was added in SteamDatabase `623ebf57b33a` (2026-09-25). It is absent from the pin and from upstream HEAD. We do not know whether the GC **requires** it, which would make `LaunchLobby()` silently fail, or whether it is optional anti-replay. Test `LaunchLobby()` on a live bot. If it fails, regenerate or hand-add the field (proto2 unknown fields are fine to add).

### 8.4 Overall risk assessment

| Area | Risk | Reason |
|---|---|---|
| Session handshake (`Hello`/`Welcome`/`ConnectionStatus`) | Low | Fields unchanged; ClientSessionNeed 104 matches ValvePython's `UserInUINeverConnected` |
| SOCache messages (`CMsgSOCacheSubscribed`, …) | Low | Only option annotations changed |
| Lobby create and set details | **Medium** | Enum renumbering (DotaTV). New `LobbyType` value. Message IDs unchanged. |
| Lobby launch | **Medium, unknown** | New `nonce` |
| League lobby fields (`leagueid`, `league_node_id`) | Low | Unchanged apart from `boxed_type` options |
| League admin, info and node messages | Low–Medium | IDs 7434/7633/7634/7650/7651 unchanged; node/group schema churn |
| Steam login (go-steam) | **High** | Legacy flow reportedly dead |

General rule: proto2 is tolerant. Unknown fields are skipped and missing fields are unset. **Renumbered enums and new required semantics are the dangerous drifts**, and diff review will not catch them unless someone looks for them.

---

## 9. Regenerating and updating the protos

### 9.1 Option A: upgrade to upstream HEAD, then patch

1. `go get github.com/paralin/go-dota2@master github.com/paralin/go-steam@master` and bump `go` in `go.mod` to `1.26`.
2. Port our code:
   - Replace `github.com/golang/protobuf/proto` helpers with pointer helpers or Go 1.26 `new(expr)`.
   - Messages become `protobuf-go-lite`.
   - Logon moves to the new auth flow (see go-steam `auth_session.go`).
3. You still get stale DotaTV and Launch protos (HEAD's submodule is at 2026-07-10). That requires Option B or a PR upstream.

### 9.2 Option B: run our own fork (recommended while upstream automation is broken)

```bash
# 1. Fork paralin/go-dota2 -> kettleofketchup/go-dota2, clone with submodules
git clone --recurse-submodules git@github.com:kettleofketchup/go-dota2.git
# 2. Advance the SteamDatabase/Protobufs submodule
git -C go-dota2/generator/Protobufs fetch origin && git -C go-dota2/generator/Protobufs checkout origin/master
# 3. Regenerate (HEAD layout: Makefile -> `go run ./cmd/generate`, which runs the
#    update step, then aptre/protoc-gen-go-lite, then apigen)
make -C go-dota2 gen
#    If it fails with "vendor protobuf includes: lstat src/google/protobuf" (upstream
#    CI has failed this way since at least 2026-08-22; cause UNCERTAIN, not
#    SteamDatabase bd8f82df2781, which is later), inspect the include-path
#    discovery in cmd/generate and point it at wherever google/protobuf/*.proto
#    actually lives in the submodule checkout.
# 4. Review drift explicitly:
git -C go-dota2 diff --stat -- protocol/
git -C go-dota2 diff -- protocol/*.proto | grep -E '^[-+].*= [0-9]+;' | grep -E 'enum|^[-+]\s+[A-Za-z_]+ = '   # enum renumbering
git -C go-dota2 diff -- apigen/snapshot-messages.txt                                                   # API surface changes
# 5. go build ./... && go test ./...  in the fork; tag e.g. v0.0.0-kettle.YYYYMMDD
# 6. In dota_lobby/go.mod:
#    replace github.com/paralin/go-dota2 => github.com/kettleofketchup/go-dota2 vX
```

Apply our own patches in the same fork:

- SOCache keyed by the object's key field, decoding every `object_data[i]`, cleared per-owner on resubscribe and on session loss.
- Constructors for SO types 40 and 42 if they turn out to matter for tickets.

Upstream the patches where possible.

**For the pinned (pre-2026) layout**, regeneration is `make updateproto genproto apigen`:

- `generator/update_protos.bash` copies `dota_gcmessages_*`, `dota_client_enums`, `network_connection`, `base_gcmessages`, `gcsdk_gcmessages`, `econ_*`, `dota_match_metadata`, `dota_shared_enums`, `steammessages*`, `valveextensions` and `gcsystemmsgs`.
- It prepends `syntax="proto2"; package protocol;` and strips leading dots.
- It deletes `CDotaMsgStructuredTooltipProperties` and `rsync`s into `protocol/`.
- `genproto` runs `protoc --go_out` with an `M` mapping per file.
- `apigen.bash` rebuilds `client_generated.go` and `events/generated.go` (`$D2/Makefile:58-82`, `$D2/generator/update_protos.bash:1-68`).

Requires `protoc` and the tools under `tools/`, which are **not** included in the module zip. You need the git checkout.

### 9.3 Option C: stay pinned and work around (short-term only)

- Raw integers for `LobbyDotaTVDelay`.
- Raw `GC.Write` for any message whose schema changed, using a locally defined proto struct.
- **Does not fix login.** go-steam must move regardless.

### 9.4 Keeping up

Mirror upstream's `update-protos.yml` idea in our fork, but **fail loudly**: add a CI check that diffs enum values in lobby, league and gcsdk protos and opens an issue. Dota patches arrive often (the 7.39b → 7.41d tags span about 12 months). SteamDatabase updated `dota2/` 10 times between 2026-07-31 and 2026-10-07.

---

## 10. Recommended usage pattern for our multi-bot manager

These recommendations follow from the verified internals above.

1. Per bot:
   1. `sc := steam.NewClient()`.
   2. Register any tap `GCPacketHandler`.
   3. `d := dota2.New(sc, le)` **once**.
   4. `sc.Connect()`.
2. Run one goroutine per bot that **only** drains `sc.Events()` and forwards to a per-bot state machine through buffered channels. Never block it on `MakeRequest`.
3. On `LoggedOnEvent`: `d.SetPlaying(true)`, then start a hello loop (`d.SayHello()` every 5–30 s with backoff) until `HAVE_SESSION`.
4. On `GCConnectionStatusChanged` away from `HAVE_SESSION`: mark the bot not ready, consider cached lobby data stale, and restart the hello loop.
5. Per-bot GC request mutex in the pinned version: one in-flight request per response type. Always `context.WithTimeout`.
6. Lobby truth comes from `GetCache()` `cso.Lobby` container `Get(lobbyID)` after a Create or Update event. Do not use `Dota2State`.
7. Before going headless in production, decide the bot's in-lobby slot (caster or spectator, per #69 and #77) so `LaunchLobby()` does not hang in loading.

---

## 11. Corrections to the local skill notes

The notes are `~/.claude/skills/go-dota2-steam/references/go-dota2.md` and `go-dota2-client.md`.

| Note says | Reality (pinned and HEAD) |
|---|---|
| `client.On(func(e *events.GCConnectionStatusChanged){...})` | No `On` method in go-steam. Use `for ev := range client.Events()` with a type switch. |
| `ClientWelcomed` has `e.Msg` | The field is `e.Welcome` (`events/session.go:19-22`) |
| `state := d.GetState()`, `state.Lobby` | No `GetState()`. `Lobby`, `Party` and `PartyInvite` are never populated. |
| `d.KickLobbyMember(memberId, team)` | `KickLobbyMember(accountID uint32)`. `KickLobbyMemberFromTeam(accountID)` is separate. |
| `d.InviteToLobby`, `SetLobbyTeamSlot` | The actual names are `InviteLobbyMember(steamid.SteamId)` and `JoinLobbyTeam(team, slot)` |
| `GameMode: proto.Uint32(uint32(DOTA_GameMode_...))` | Pinned: OK (`game_mode` is uint32). HEAD: there is no `proto` package, so use a pointer helper. |

---

## 12. Verified vs Uncertain

### Verified (read in source or API output)

- Struct, `New`, handler registration, and the event set: `$D2/client.go:32-113`, `events/*.go`.
- `SayHello` is a single send with `ClientSessionNeed=104` and the Source2 engine: `client_session.go:27-38`.
- `SetPlaying(false)` does not cancel `connectionCtx` (pinned): `client_session.go:18-22`.
- `Dota2State.Lobby/Party/PartyInvite/LastConnectionStatusUpdate` are never assigned (pinned). There is no `GetState`.
- SOCache is keyed by owner SOID and decodes only `object_data[0]`. It is never cleared on a session change, has lossy 10-slot channels, and its channels never close. This holds in the pin and at upstream HEAD.
- Pinned `MakeRequest` matches by `MsgType` only, with no job ID. HEAD uses `SetSourceJobId` and `TargetJobId` routing.
- go-steam's GC dispatch is synchronous on the read loop, and `Emit` blocks with a buffer of 30.
- `k_EMsgGCPracticeLobbyResponse` is overridden to no wrapper. `LeagueAdminList` is event-only.
- Upstream: about 50 commits since the pin, the protobuf-go-lite migration, Go 1.26, job correlation, and proto-update workflow failures since 2026-04-19 (the quoted error is confirmed from 2026-08-22 on; earlier logs expired).
- The `LobbyDotaTVDelay` renumbering (2026-08-12) and `CMsgPracticeLobbyLaunch.nonce` (2026-09-25) exist in current SteamDatabase protos and are missing from both the pin and upstream HEAD.
- `pkg/bot/manager.go` calls `dota2.New` on every logon and never calls `SetPlaying` or `SayHello`.
- The snippets in section 6 compile against the pinned modules.

### Likely (strong secondary evidence)

- The pinned go-steam legacy logon no longer works. Source: issue #78 and PR #11. Not tested live here.
- 7055 replies are job-addressed. Source: node-dota2 registers them as job callbacks on the create path.
- The bot's own SO cache (owner = SteamID) holds invites and inventory, while the lobby is a separate owner cache.

### Uncertain (needs a live test)

- Whether the GC requires `CMsgPracticeLobbyLaunch.nonce`.
- Whether every reply type we need echoes `jobid_target`, which HEAD's `MakeRequest` depends on.
- The root cause of upstream's proto-workflow failures. `bd8f82df` is ruled out (the error predates it); April-August logs have expired.
- The decode type of 7055 replies (`CMsgGenericResult` per node-dota2, whose own success check reads a `result` field the Valve proto lacks).
- Whether league or ticket data relevant to goal (4) arrives through SO types 40 or 42, or through EconItems that the SOCache currently drops.
- The about-60-minute lobby-update drop reported for other libraries (#72).

---

## Sources

Local (pinned modules):

- `$D2/client.go`, `client_session.go`, `client_request.go`, `client_cache.go`, `client_lobby.go`, `client_lobby_members.go`, `client_lobby_slot.go`, `client_party.go`, `client_generated.go`, `events/{event,session,state,invite,generated}.go`, `state/state.go`, `socache/{socache,socache_container,socache_sub}.go`, `cso/cso_types.go`, `apigen/{msg_overrides.go,apigen.bash,snapshot-messages.txt}`, `generator/update_protos.bash`, `Makefile`, `go.mod`, `protocol/*.proto`
- `$GS/gamecoordinator.go`, `$GS/client.go`, `$GS/auth.go`, `$GS/protocol/gamecoordinator/{msg,packet}.go`
- `/home/kettle/git_repos/dota_lobby/pkg/bot/manager.go`, `go.mod`
- `~/.claude/skills/go-dota2-steam/references/go-dota2.md`, `go-dota2-client.md`

Upstream and web:

- go-dota2 repo and commits: https://github.com/paralin/go-dota2, https://github.com/paralin/go-dota2/commits/master
- Commits: https://github.com/paralin/go-dota2/commit/f93daab554a5, https://github.com/paralin/go-dota2/commit/e63ba66eb27b, https://github.com/paralin/go-dota2/commit/473641785124, https://github.com/paralin/go-dota2/commit/3003d54cf8a8
- HEAD files: https://github.com/paralin/go-dota2/blob/master/client-request.go, https://github.com/paralin/go-dota2/blob/master/client.go, https://github.com/paralin/go-dota2/blob/master/client-session.go, https://github.com/paralin/go-dota2/blob/master/socache/socache_container.go, https://github.com/paralin/go-dota2/blob/master/go.mod, https://github.com/paralin/go-dota2/blob/master/Makefile, https://github.com/paralin/go-dota2/blob/master/.github/workflows/update-protos.yml, https://github.com/paralin/go-dota2/blob/master/.gitmodules
- Workflow runs: https://github.com/paralin/go-dota2/actions/workflows/update-protos.yml
- Issues: https://github.com/paralin/go-dota2/issues/61, /69, /71, /72, /77, /78; PR https://github.com/paralin/go-dota2/pull/70
- go-steam: https://github.com/paralin/go-steam, https://github.com/paralin/go-steam/pull/11, https://github.com/paralin/go-steam/commit/6b06e91640b1
- SteamDatabase/Protobufs: https://github.com/SteamDatabase/Protobufs/tree/master/dota2, https://github.com/SteamDatabase/Protobufs/commit/c983b3e16435, https://github.com/SteamDatabase/Protobufs/commit/623ebf57b33a, https://github.com/SteamDatabase/Protobufs/commit/bd8f82df2781
- ValvePython dota2 hello loop: https://github.com/ValvePython/dota2/blob/master/dota2/client.py
- node-dota2 hello loop and lobby response: https://github.com/Arcana/node-dota2/blob/master/index.js, https://github.com/Arcana/node-dota2/blob/master/handlers/lobbies.js

---

## Verification notes

Changes after verifier review (2026-11-06):

- Package map: `protocol` has 40 `.proto` files, not 36 (`ls $D2/protocol/*.proto | wc -l`).
- TL;DR 8: `KickLobbyMember` exists (`$D2/client_generated.go:1967`); only the skill notes' two-argument call is wrong. It is no longer listed with the truly missing `client.On`, `GetState` and `e.Msg`.
- Section 5: the `k_EMsgGCPracticeLobbyResponse` override is at `apigen/msg_overrides.go:33`, not `:30`.
- Section 6.3 and the evidence summary: the `CMsgGenericResult` decode for 7055 is now UNCERTAIN, because node-dota2 checks `.result` while the Valve proto field is `eresult`. Added that type-only `MakeRequest` also accepts other 7055 replies.
- Section 7.1: open issues are #81 (Renovate PR) and #21 (human-filed user guide request), not two bot PRs.
- Section 7.1 and 9.2: removed `bd8f82df2781` as a possible cause of the `lstat src/google/protobuf` failure (the error appears from 2026-08-22, six weeks earlier). The error is confirmed only in readable logs from 2026-08-22 to 2026-11-04; April-August causes are unknown.
- Section 7.1 #72: reworded as a ValvePython question, not a go-dota2 bug.
- Section 7.1 forks: added `gg-cr` (2 ahead) and `marceloarc` (1 ahead), both trivial and behind master. `amandladev` also changes `client_party.go`, `go.mod`, `go.sum` and `.gitignore`; its READMEs are under `examples/`.
