# Gap 01: Do upstream go-steam and go-dota2 `master` build together for headless token login?

Date: 2026-11-06. Scope: research question Q1 (auth). No repo Go code was changed. All compile work was done in
throwaway modules at `/tmp/gap01` (ported snippets), `/tmp/gap01b` (unported snippets, to record the breaks) and
`/tmp/gap01c` (go-directive test), built with `GOROOT=/home/kettle/.local/share/mise/installs/go/1.27.1`.

Labels:
- **VERIFIED**: compiled, ran a unit test, or read in the master source (file:line given).
- **REPORTED**: someone else said it (issue, PR or commit text), or it is inferred from another library's code.
- **UNCERTAIN**: still open, and needs a live account to settle.

## TL;DR

1. **The two masters build together, and every snippet the plan depends on compiles after mechanical porting.**
   This is VERIFIED. `go get github.com/paralin/go-steam@master github.com/paralin/go-dota2@master` resolves to
   go-steam `v0.0.0-20260922075330-336e824f8540` and go-dota2 `v0.0.0-20260922075533-6d32dc38d075`.
   go-dota2 master's `go.mod` already pins that same go-steam commit, so the graph has no version conflict.
   The ported versions of 02 §7.1 to §7.4, 03 §6.1 to §6.3, a credentials+TOTP `Authenticate` bootstrap, a
   token `LogOn(ctx)` flow and an event loop all pass `go build` and `go vet`. The upstream unit tests for
   `go-steam`, `go-dota2` and `go-dota2/socache` pass on this toolchain.
2. **The API breaks are all mechanical, with one exception: the meaning of `MakeRequest` changed.** On master it
   matches replies by **GC job ID**, not by message type. A reply whose `job_id_target` does not echo our
   `job_id_source` never completes the call; it ends in a ctx timeout. Code in two node-dota2-based bots relies on
   the GC echoing job IDs for **7634 (league admin list)** and **7055 (practice lobby response)**. That is
   REPORTED, not live-tested.
3. **Headless refresh-token login is implemented on master.** `Auth.Authenticate` does credentials plus
   `TwoFactorCode`, `Auth.LogOn(ctx, {AccessToken})` writes `CMsgClientLogon.access_token` (field 108), and
   `PlayingSessionStateEvent` is emitted. All VERIFIED in source. **No one has publicly reported a token or TOTP login on
   the 2026-09-22 master.** The only success reports are for PR #11's password-only flow (2026-06-26) and the
   maintainer's 2026-06-27 rewrite ("Fixed!"). Both are REPORTED, and both predate the lite and job-ID refactors.
4. **Token renewal is not in go-steam.** We can build it ourselves from exported APIs only: a CM call to
   `Authentication.GenerateAccessTokenForApp#1` over EMsg 151 compiles against master (VERIFIED compile). Whether
   Steam accepts it this way is UNCERTAIN.
5. **Gap status: resolved for "does it build / what breaks". Still open for "does it log in".** The one remaining
   step is a live smoke test with one bot account. §7 gives a ready-made procedure.

## 1. Resolved modules and Go directives (VERIFIED)

| Item | Value | Source |
|---|---|---|
| go-steam master | `v0.0.0-20260922075330-336e824f8540` ("docs(generator): include the complete retained definition license") | `go get` output; `gh api repos/paralin/go-steam/commits` |
| go-dota2 master | `v0.0.0-20260922075533-6d32dc38d075` ("chore(deps): adopt standalone Steam protocol generation") | `go get` output |
| go-dota2 → go-steam | `github.com/paralin/go-steam v0.0.0-20260922075330-336e824f8540 // master`, the **same commit** as go-steam master | `$GD/go.mod:7` |
| protobuf runtime (both) | `github.com/aperturerobotics/protobuf-go-lite v0.18.1-0.20260922073011-561b6c171b0f` | `$GS/go.mod:7`, `$GD/go.mod:6` |
| `go` directive | `go 1.26.0` in both; **no `toolchain` directive** in either | `$GS/go.mod:3`, `$GD/go.mod:3` |
| Our repo | `go 1.24.11` (`go.mod:3`). With the masters required, a 1.24.11 directive fails with `go: updates to go.mod needed`, and 1.26.0 builds. We must bump to at least `go 1.26.0`. The code also uses `new(expr)` (Go 1.26), e.g. `$GD/client-session.go:37`. | `/tmp/gap01c` test |
| Our `golang/protobuf` dependency | No longer needed by either library. | `$GD/go.mod`, `$GS/go.mod` |
| Upstream tests | `go test github.com/paralin/go-steam github.com/paralin/go-dota2 github.com/paralin/go-dota2/socache`: all `ok` on Go 1.27.1. `TestLiveSteamCredentialSmoke` skips because no credentials are set. | `/tmp/gap01` |

`$GS` = `~/.local/share/go/pkg/mod/github.com/paralin/go-steam@v0.0.0-20260922075330-336e824f8540`,
`$GD` = `~/.local/share/go/pkg/mod/github.com/paralin/go-dota2@v0.0.0-20260922075533-6d32dc38d075`,
`$LITE` = `~/.local/share/go/pkg/mod/github.com/aperturerobotics/protobuf-go-lite@v0.18.1-0.20260922073011-561b6c171b0f`.

## 2. API breaks vs. the pinned modules (VERIFIED, file:line in master)

| # | Area | Pinned | Master | Impact on our snippets |
|---|---|---|---|---|
| B1 | `MakeRequest` signature | `(ctx, reqMsgID uint32, request proto.Message, respMsgID uint32, response proto.Message, matchesRequest ...func(proto.Message) bool) error` (pinned `client_request.go:17-24`) | Same shape, but `protobuf.Message` is the lite interface: `(ctx, reqMsgID uint32, request protobuf.Message, respMsgID uint32, response protobuf.Message, matchesRequest ...func(protobuf.Message) bool) error` (`$GD/client-request.go:21-28`) | Call sites compile unchanged (02 §7.1, 03 §6.3). The `matchesRequest` predicate now takes `protobuf.Message`. |
| B2 | `MakeRequest` semantics | Matches the response by **message type only** | Allocates `jobID`, calls `message.SetSourceJobId(jobID)` (`$GD/client-request.go:39-53`). The reply is accepted only if `pending[packet.TargetJobId]` exists **and** `MsgType == respMsgID` (`$GD/client-request.go:87-98`). Returns `ErrNotReady` when there is no session or the session ends (`:34-36`, `:60-61`). | Concurrent 7055s are no longer confused. **But a reply without an echoed job ID never completes `MakeRequest`.** It falls through to `UnhandledGCPacket`, or to a static handler, and the caller times out (see §4). |
| B3 | Message types | `github.com/golang/protobuf` | `protobuf-go-lite`: `Message{SizeVT, MarshalToSizedBufferVT, MarshalVT, UnmarshalVT, Reset}` (`$LITE/protobuf-go-lite.go:29-40`). Generated types keep `Reset/String/ProtoMessage` and `GetX()` getters (e.g. `$GD/protocol/econ_shared_enums.pb.go:189-205,331`). | `proto.String(x)`/`proto.Uint32(x)` → `new(x)` (Go 1.26). Unmarshal → `msg.UnmarshalVT(body)`. **Compile error** if a `golang/protobuf` `proto.Message` is passed to `gamecoordinator.NewGCMsgProtobuf`: `protoiface.MessageV1 does not implement protobuf_go_lite.Message (missing method MarshalToSizedBufferVT)` (from `/tmp/gap01b`). `proto.Unmarshal(b, liteMsg)` still compiles and worked at runtime through legacy reflection (tested on `CMsgGenericResult`), but it drags `google.golang.org/protobuf` back in. Don't use it. |
| B4 | GC send | `NewGCMsgProtobuf(appId, msgType uint32, body proto.Message)` | `NewGCMsgProtobuf(appId, msgType uint32, body protobuf.Message) *GCMsgProtobuf` with `SetSourceJobId` and `SetTargetJobId` (`$GS/protocol/gamecoordinator/msg.go:35,66,76`). `GC.Write` serializes first, then reports encode errors through `client.Errorf` instead of panicking (`$GS/gamecoordinator.go:64-83`). | 03 §6.1 `rawSend` compiles once the body is a lite message. |
| B5 | `events.UnhandledGCPacket` | `{Packet *gamecoordinator.GCPacket}` | Unchanged shape: `{Packet *sprotocol.GCPacket}` (`$GD/events/session.go:24-28`). `GCPacket{AppId, MsgType, IsProto, GCName, Body, TargetJobId}`; **no SourceJobId** (`$GS/protocol/gamecoordinator/packet.go:13-20`). It is emitted only when no static handler succeeded **and** no pending job claimed the packet (`$GD/client.go:164-179`). | The 03 §6.3 7055 capture works: `e.Packet.Body` → `(&protocol.CMsgGenericResult{}).UnmarshalVT(...)`. `TargetJobId` is available for diagnostics. |
| B6 | `events.Event` | `GetEventBody() proto.Message` | `GetEventBody() protobuf.Message` (`$GD/events/event.go:9-16`). Generated events embed the message, e.g. `LeagueAdminList{protocol.CMsgLeagueAdminList}` for 7434 (`$GD/events/generated.go:733-749`). | No code change needed when switching on the concrete type. |
| B7 | `cso` type registry | ctor map returning `proto.Message` | `csoTypeCtors map[CSOType]func() protobuf.Message` (`$GD/cso/cso_types.go:54-88`), `NewSharedObject(typ) (protobuf.Message, error)` (`:91-98`). Registered: EconItem, GameAccountClient, Party, Lobby, PartyInvite, GameHeroFavorites, MapLocationState, PlayerChallenge, LobbyInvite, DropRateBonus, GameAccountPlus. **Constants without ctors**: ItemRecipe 5, EconGameAccountClient 7, SelectedItemPreset 35, ItemPresetInstance 36, **EventTicket 40, ItemTournamentPassport 42, Tournament 2009**. | 02 §7.2 (`GameAccountClient` → `CSODOTAGameAccountClient.GetIsLeagueAdmin()`) compiles unchanged. `GetContainerForTypeID` is unchanged (`$GD/socache/socache.go:24`). The cache cannot decode ticket or tournament SOs; this agrees with 02 §9.5 ("don't use tickets/inventory"). |
| B8 | Construction | `dota2.New(client, le)` | `New(client, le)` = `NewWithCoordinator(NewSteamCoordinator(client), client.Emit, le)` (`$GD/client.go:56-58`). The doc says *"registers a Dota2 handler on a Steam client before it connects"*. `Close()` is **terminal** (`closed = true`, `$GD/client.go:86-91`). | See §5, D1/D2: the current `manager.go:95` creates `dota2.New` inside `LoggedOnEvent`, which breaks the master contract. |
| B9 | `LeaveCreateLobby` | `(ctx, details, destroyOldLobby bool) error` | Same (`$GD/client_lobby.go:19`). | 02 §7.4 compiles after `proto.X` → `new(x)`. |
| B10 | `RequestLeagueAvailableLobbyNodes` | generated wrapper | `(ctx, leagueID uint32) (*CMsgDOTALeagueAvailableLobbyNodes, error)`, built on `MakeRequest` (`$GD/client_generated.go:3800-3816`) | 02 §7.3 compiles. Subject to B2. |
| B11 | Steam logon | `Auth.LogOn(*LogOnDetails)` | `Auth.LogOn(ctx, *LogOnDetails) error` (`$GS/auth.go:27`). Compile error otherwise: `not enough arguments in call to sc.Auth.LogOn` (`/tmp/gap01b`). | `pkg/bot/manager.go:81` must change. |
| B12 | 7633 constants | present | `EDOTAGCMsg_k_EMsgDOTALeagueInfoListAdminsRequest = 7633`, `..._Reponse = 7634` (`$GD/protocol/dota_gcmessages_msgid.pb.go:282-283`). `CMsgDOTALeagueInfoList.GetInfos()` (`$GD/protocol/dota_gcmessages_common_league.pb.go:697,708`), `CMsgDOTALeagueInfoListAdminsRequest` (`:777`). | 02 §7.1 compiles unchanged. |

The full ported file is `/tmp/gap01/main.go`. The relevant ported calls:

```go
// 02 §7.4: only the pointer helpers change
details := &protocol.CMsgPracticeLobbySetDetails{
    GameName: new(name), PassKey: new(pass), Leagueid: new(leagueID), LeagueNodeId: new(nodeID),
    DotaTvDelay: protocol.LobbyDotaTVDelay_LobbyDotaTV_120.Enum(),
    Visibility:  protocol.DOTALobbyVisibility_DOTALobbyVisibility_Unlisted.Enum(),
}
// 03 §6.3: UnhandledGCPacket capture
case *devents.UnhandledGCPacket:
    if e.Packet.MsgType == uint32(protocol.EDOTAGCMsg_k_EMsgGCPracticeLobbyResponse) {
        r := &protocol.CMsgGenericResult{}
        _ = r.UnmarshalVT(e.Packet.Body) // was proto.Unmarshal
    }
```

## 3. Auth surface on go-steam master (VERIFIED in source)

| Question | Finding | Ref |
|---|---|---|
| `LogOnDetails` | `Username, Password, AccessToken, DeviceFriendlyName, AuthCode, TwoFactorCode, ConfirmSteamGuard func(ctx, []SteamGuardConfirmation) (string, unified.EAuthSessionGuardType, error), ShouldRememberPassword`. There is no `GuardData` field. | `$GS/logon-details.go:10-39` |
| `Authenticate` | `(a *Auth) Authenticate(ctx, *LogOnDetails) (string, error)`. **Requires `Password`** even when you only want to renew. It opens no CM connection. | `$GS/auth-token.go:12-17` |
| Credential flow | WebAPI `https://api.steampowered.com/IAuthenticationService/` (`$GS/auth_session.go:21`). Platform is `SteamClient`, persistence is `Persistent`, the default device name is `"go-steam"` (`:51-56`), and `RememberLogin` = `ShouldRememberPassword` (`:63`). It returns `refresh_token`, falling back to `access_token` (`:94-99`). `maxPollAttempts = 10` (`:22`). It uses `http.DefaultClient` unless a test hook sets another client (`:252-257`). | |
| TOTP (`TwoFactorCode`) | If `DeviceCode` is allowed and `TwoFactorCode != ""`, it calls `UpdateAuthSessionWithSteamGuardCode` with `CodeType=DeviceCode` before polling. `ConfirmSteamGuard` is a fallback inside the same session. `DeviceConfirmation`/`EmailConfirmation` lead to polling without error. | `$GS/auth_session.go:113-149, 175-189` |
| Token logon | `LogOn` with `AccessToken` skips HTTP and writes `CMsgClientLogon{AccountName, AccessToken, ClientLanguage, ProtocolVersion, [ShouldRememberPassword]}`. `ctx` is unused on this path. | `$GS/auth.go:38-73` |
| Field 108 really serialized | `CMsgClientLogon.AccessToken` has tag `bytes,108` and is emitted by `MarshalVT` (bytes `0xe2 0x06`). This removes the gg-cr "proto.Marshal drops field 108" concern for this library. | `$GS/protocol/protobuf/client_server_login.pb.go:199, 2305-2311` |
| Failure reporting | `OK` → `LoggedOnEvent` + WebAPI nonce request. `Fail/ServiceUnavailable/TryAnotherCM` → `SteamFailureEvent`. Anything else → `LogOnFailedEvent{Result}` **and `Disconnect()`**. Auth-stage errors → `LogOnFailedEvent{Err, AuthSessionState, Confirmations}` and are also returned from `LogOn`. | `$GS/auth.go:99-134, 38-52`; `$GS/auth_events.go:19-61` |
| `PlayingSessionState` | `EMsg_ClientPlayingSessionState` → `PlayingSessionStateEvent{PlayingBlocked, PlayingApp}`. The comment says *"Announcing playback while blocked can end the login with LoggedInElsewhere."* The library itself takes no action. | `$GS/auth.go:83-89`; `$GS/playing-session-event.go:3-11` |
| `LoggedInElsewhere` | No special handling. It only arrives as `LoggedOffEvent{Result}` (`$GS/auth.go:137-149`). The enum values exist: `EResult_LoggedInElsewhere = 6`, `AlreadyLoggedInElsewhere = 50` (`$GS/protocol/steamlang/enums.go:3791,3835`). | |
| `InvalidSignature` | **Absent.** The EResult enum stops at `NeedCaptcha = 101` (`$GS/protocol/steamlang/enums.go:3904`). SteamKit has `InvalidSignature = 121` ([eresult.steamd:124](https://github.com/SteamRE/SteamKit/blob/master/Resources/SteamLanguage/eresult.steamd)). `EResult.String()` falls back to the number (`enums.go:4013-4019`), so we must compare against `steamlang.EResult(121)` (compiled in `/tmp/gap01/main.go`). | |
| Renewal / `GenerateAccessTokenForApp` | **No helper.** A grep for `GenerateAccessToken` in `$GS` finds no hits. The messages exist: `CAuthentication_AccessToken_GenerateForApp_Request{refresh_token=1, steamid=2, renewal_type=3}` and `_Response{access_token=1, refresh_token=2}` (`$GS/protocol/protobuf/unified/auth.proto:227-236`; Go types `auth.pb.go:1316,1355`), plus `ETokenRenewalType_k_ETokenRenewalType_Allow` (`auth.pb.go:180-184`). `authServiceCall` is unexported (`auth_session.go:191`). The steamlang EMsg table lacks `ServiceMethodCallFromClient = 151` ([emsg.steamd:40](https://github.com/SteamRE/SteamKit/blob/master/Resources/SteamLanguage/emsg.steamd)) but has `EMsg_ServiceMethodResponse = 147` (`enums.go:50`). | |
| Event channel | `Emit` is a **blocking** send on a 30-slot channel (`$GS/client.go:101-103`, `:76`). The GC reader goroutine emits too, so the event loop must never block on a `MakeRequest`. Run it in a goroutine, as `/tmp/gap01/main.go` does. | |
| Reconnect | None in the library. Heartbeat expiry → `failSession` → `FatalErrorEvent` + disconnect (`$GS/client.go:358-391`). This confirms the 04 §3.1 note. | |

### 3.1 Token renewal from Go: compile-checked sketch (VERIFIED compiles; live behaviour UNCERTAIN)

`/tmp/gap01/renew.go` registers a `steam.PacketHandler` and sends
`NewClientMsgProtobuf(steamlang.EMsg(151), &unified.CAuthentication_AccessToken_GenerateForApp_Request{RefreshToken, Steamid, RenewalType: Allow})`
with `SetSourceJobId(sc.GetNextJobId())` and `Header.Proto.TargetJobName = "Authentication.GenerateAccessTokenForApp#1"`.
It matches `EMsg_ServiceMethodResponse` by `packet.TargetJobId` and reads `Header.Proto.GetEresult()`.
This mirrors SteamKit's `SteamAuthentication.GenerateAccessTokenForAppAsync`, which calls the unified
`AuthenticationService.GenerateAccessTokenForApp` over the logged-on CM connection
([SteamAuthentication.cs:63-84](https://github.com/SteamRE/SteamKit/blob/master/SteamKit2/SteamKit2/Steam/Authentication/SteamAuthentication.cs)).

- REPORTED: node-steam-session says WebAPI renewal works only for MobileApp tokens and that SteamClient tokens are
  renewed over the CM (see 04 §2.2).
- UNCERTAIN: whether Steam returns a new `refresh_token` this way for a go-steam token, and how close to expiry it
  has to be.
- Fallback that needs no renewal: re-run `Authenticate` with password + TOTP when the token JWT `exp` is near, or
  when a token logon fails with a dead-token EResult. That works headless for TOTP bots.

## 4. The job-ID dependency of the 7633 and 7055 snippets

Master `MakeRequest` completes only when the GC copies our `job_id_source` into the reply's `job_id_target`
(`$GD/client-request.go:50-53, 87-98`).

- **REPORTED (code-inferred) for 7634:** devilesk/dota-ihl-bot's `requestLeagueInfoListAdmins` resolves its promise
  only inside the callback ([dotaBot.js:1052-1073](https://github.com/devilesk/dota-ihl-bot/blob/master/lib/dotaBot.js)).
  Its node-dota2 fork (`devilesk/node-dota2#a71891d`, `handlers/leagues.js:20-31,79-88`) passes that callback
  through `_convertCallback` (`handlers/helper.js:101-110`) to node-steam's `SteamGameCoordinator._send`. That
  function stores it in `_jobs[sourceJobID]` and invokes it only when an incoming `job_id_target` matches
  ([node-steam game_coordinator/index.js:33-42,104-107](https://github.com/seishun/node-steam/blob/master/lib/handlers/game_coordinator/index.js)).
  If the GC did not echo the job ID, dota-ihl-bot's ticket loading would always hit its 5 s timeout. Its wiki shows
  tickets loaded (02 §1), so the GC very likely echoes on 7634.
- **REPORTED for 7055:** Arcana/node-dota2 `createPracticeLobby` uses the same callback mechanism
  (`handlers/lobbies.js:139-141`, `index.js:274-285`).
- **Master already ships 7633 and 7055 as plain `MakeRequest` users?** No. apigen still marks 7633 as N/A (03),
  and the lobby wrappers are fire-and-forget (`$GD/client_lobby.go:11-16`). So upstream has no test that exercises
  these two replies live. **UNCERTAIN until we capture one.**
- **Design consequence (no plan change needed):** keep the 03 §6.2 option-A fallback. If `MakeRequest` times out,
  look at the `UnhandledGCPacket` stream (or a registered `GCPacketHandler` tap) for 7634/7055 and log
  `TargetJobId`. A tap sees every packet, even ones a pending job claims (`$GS/gamecoordinator.go:58-60`).

## 5. Design deltas for the `pkg/gc` adapter

These are VERIFIED from source. Recommendations are marked as such.

- **D1: register before connect.** `GameCoordinator.RegisterPacketHandler` is a plain `append` with no lock
  (`$GS/gamecoordinator.go:36-38`), and the reader iterates over it (`:58-60`). Today's `manager.go:95` calls
  `dota2.New` after `LoggedOnEvent`, which is a data race on master and adds a duplicate handler on every
  re-logon. *Recommendation:* create the `steam.Client` and `dota2.Dota2` (and any tap) together before
  `ConnectContext`.
- **D2: one pair per connection.** `Dota2.Close()` is permanent (`$GD/client.go:86-91`), and go-dota2 does not
  watch Steam's `DisconnectedEvent`. The caller must `Close` on disconnect (`$GD/client.go:55`). *Recommendation:* on
  disconnect, `Close()` the Dota2, drop both objects, and build a fresh `steam.Client` + `Dota2` for the next attempt.
  Alternatively, keep the pair and call `SetPlaying(false)`, which cancels requests (`$GD/client-session.go:13-30`).
- **D3: playing session.** On `PlayingSessionStateEvent{PlayingBlocked:true}`, call `SetPlaying(false)` and
  back off. Do not `SayHello`. `LoggedOffEvent{LoggedInElsewhere}` → mark the bot busy, and do not auto-retry in
  a tight loop.
- **D4: dead token.** Treat `LogOnFailedEvent.Result` ∈ {InvalidPassword 5, AccessDenied 15, Revoked 26,
  Expired 27, EResult(121)} as "re-authenticate". Note that go-steam has already called `Disconnect()` (`$GS/auth.go:129-132`).
- **D5: the hello retry** is still the caller's job. `SayHello` sends once (`$GD/client-session.go:33-40`).
- **D6: event loop.** Never call `MakeRequest` or `LeaveCreateLobby` on the goroutine that drains
  `client.Events()` (see §3, Event channel).

## 6. Upstream reports (PR #11, and every issue/PR after 2026-06)

Read via `gh api` on 2026-11-06.

| Item | Date | Content | Label |
|---|---|---|---|
| [go-steam PR #11](https://github.com/paralin/go-steam/pull/11) (markus-wa), closed unmerged 2026-06-27 | 2026-06-10 | Body: *"Verified end-to-end against live Steam: a password login now completes with `EResult_OK`"*. It notes the flow *"targets accounts without Steam Guard"*. | REPORTED (password, no Guard, PR branch, not master) |
| PR #11 comments | 2026-06-24 to 06-27 | Peadar-Kenny: *"As of last night steam fully deprecated the old logon method."* TexHik620953: the PR fails with `steam guard confirmation required (k_EAuthSessionGuardType_DeviceCode)`. markus-wa: *"master does not work in any case right now."* crowedev (review) asks for token reuse. paralin: *"I wrote a new commit from scratch + released it."* | REPORTED |
| [go-steam PR #12](https://github.com/paralin/go-steam/pull/12) (TexHik620953) | 2026-06-26 | "Same as markus-wa fork but with steamguard support"; closed by paralin's rewrite. | REPORTED |
| go-steam `6b06e91640b1` | 2026-06-27 | "new token authentication method … Remove deprecated auth method, add steam guard + email. Closes #12, #11." | VERIFIED (commit text) |
| [go-dota2 #78](https://github.com/paralin/go-dota2/issues/78) "Login Issues?" | 2026-06-25 | eQuaL322 points to PR #11. crowedev: *"I can confirm the unmerged proto + token update in PR #11 does in fact fix the login problem."* paralin closes it on 06-27: *"Fixed!"* | REPORTED (PR #11 branch) |
| go-steam PRs #13-#17, go-dota2 #79-#81 | 2026-06-30 to 2026-09-22 | Renovate dependency bumps only | n/a |
| go-dota2 #77, #76, #21 (recent comments) | 2026-05 to 07 | Lobby launch usage questions, no auth reports | n/a |
| go-steam issues | n/a | Issues are enabled, but no human-filed issues exist at all | VERIFIED (empty) |
| go-steam `live_smoke_test.go` | 2026-09-22 tree | Opt-in live test: `ConnectedEvent` → `Auth.LogOn(ctx, {Username, Password, AuthCode, TwoFactorCode})` → expects `LoggedOnEvent OK` + `AccountInfoEvent` (`$GS/live_smoke_test.go:15-97`). It exists, but nothing published says it was run on the lite or job-ID commits, and it does **not** exercise `AccessToken`. | VERIFIED (exists); run status UNCERTAIN |

**No public report, success or failure, exists for (a) `AccessToken` (refresh-token) logon, (b) TOTP via
`TwoFactorCode`, or (c) anything on the 2026-09-22 master** (protobuf-go-lite and job-ID correlation). All
success reports predate those refactors.

## 7. What still needs a live run (to close the gap fully)

With one bot account that has a TOTP `shared_secret` (all steps use `/tmp/gap01/main.go` as written):

1. `STEAM_USER`, `STEAM_PASS`, `STEAM_SHARED_SECRET` set, `STEAM_REFRESH_TOKEN` empty. Expect `Authenticate` to
   return a JWT. Decode the payload and record `aud` (should include `client`) and `exp`.
2. Rerun with `STEAM_REFRESH_TOKEN` set and the password unset. Expect `LoggedOnEvent{OK}`, then
   `ClientWelcomed` after `SetPlaying(true)` + `SayHello`.
3. After welcome: does `ListAdminLeagues` return within 5 s? Log `TargetJobId` for any 7634 seen on the tap.
   Repeat for a 7055 after `LeaveCreateLobby`.
4. Run the renewal sketch (`renew.go`) once and log the EResult and whether `refresh_token` comes back non-empty.
5. Restart the process and log in again with the same token. Do not call LogOff (04 §2.2).

## 8. Verified vs uncertain summary

**VERIFIED:** both masters resolve together (same go-steam commit). Both need `go 1.26.0`, with no `toolchain`
directive. All plan snippets compile and vet after porting. Upstream unit tests pass. The full B1-B12 break list.
`Authenticate`, TOTP and `AccessToken` logon code paths. Field 108 is marshalled. `PlayingSessionStateEvent` exists.
There is no `LoggedInElsewhere` handling. `InvalidSignature` (121) and EMsg 151 are missing. There is no renewal
helper, but the renewal protos exist and a CM renewal sketch compiles. The handler-registration and terminal-`Close`
constraints. `Emit` blocks.

**REPORTED:** PR #11's password login works live (pre-master). Users confirm the June 2026 legacy-logon shutdown.
The GC echoes job IDs for 7634 and 7055 (inferred from dota-ihl-bot/node-dota2 callback routing).

**UNCERTAIN:** a live token or TOTP login on the 2026-09-22 master. Whether 7634/7055 replies carry
`job_id_target` (i.e. whether `MakeRequest` on master completes for them). Whether CM `GenerateAccessTokenForApp`
renewal works for go-steam SteamClient tokens.

## Sources

Local (master): `$GS/go.mod`, `logon-details.go`, `auth.go`, `auth-token.go`, `auth_session.go`, `auth_events.go`,
`playing-session-event.go`, `client.go`, `gamecoordinator.go`, `live_smoke_test.go`, `protocol/msg.go`,
`protocol/packet.go`, `protocol/gamecoordinator/{msg,packet}.go`, `protocol/steamlang/{enums,messages}.go`,
`protocol/protobuf/client_server_login.pb.go`, `protocol/protobuf/unified/auth.{proto,pb.go}`; `$GD/go.mod`,
`client.go`, `client-request.go`, `client-request_test.go`, `client-session.go`, `client_lobby.go`,
`client_generated.go`, `coordinator.go`, `steam-coordinator.go`, `events/{event,session,generated}.go`,
`cso/cso_types.go`, `socache/*.go`, `protocol/*.pb.go`; `$LITE/protobuf-go-lite.go`. Pinned:
`go-dota2@v0.0.0-20250623204622-532cb1851217/client_request.go:17-60`. Repo: `go.mod:3`, `pkg/bot/manager.go:81,95`.
Scratch: `/tmp/gap01/{main.go,renew.go,go.mod}`, `/tmp/gap01b/main.go`, `/tmp/gap01c/go.mod`.

Web/GitHub:
- https://github.com/paralin/go-steam/pull/11, https://github.com/paralin/go-steam/pull/12
- https://github.com/paralin/go-steam/commit/6b06e91640b1, `c82323035020`, `908c060af61e`, `8c23cbb74e27`
- https://github.com/paralin/go-dota2/issues/78, /issues/77, /issues/21; commits `f93daab5`, `e63ba66e`, `6d32dc38d075`
- https://github.com/devilesk/dota-ihl-bot/blob/master/lib/dotaBot.js
- https://github.com/devilesk/node-dota2/tree/a71891d (`handlers/leagues.js`, `handlers/helper.js`)
- https://github.com/Arcana/node-dota2/blob/master/index.js, /handlers/lobbies.js
- https://github.com/seishun/node-steam/blob/master/lib/handlers/game_coordinator/index.js
- https://github.com/SteamRE/SteamKit/blob/master/SteamKit2/SteamKit2/Steam/Authentication/SteamAuthentication.cs
- https://github.com/SteamRE/SteamKit/blob/master/Resources/SteamLanguage/emsg.steamd, /eresult.steamd
