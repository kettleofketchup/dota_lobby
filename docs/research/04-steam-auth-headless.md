# 04 - Headless Steam Authentication for Many Bot Accounts

> Research track: running several headless Steam bot accounts with `paralin/go-steam` + `paralin/go-dota2`,
> logging in reliably, persisting credentials across Docker restarts, and keeping within Steam's limits.
>
> Date of research: 2026-11-06. Module versions examined (the pre-PR `go.mod` pin); paths written
> `go-steam@f167ff28a93a/...` or `go-dota2@532cb1851217/...` are relative to these module roots:
>
> - `github.com/paralin/go-steam v0.0.0-20250502043548-f167ff28a93a`
> - `github.com/paralin/go-dota2 v0.0.0-20250623204622-532cb1851217`
>
> The recommended token flow was run live on go-steam master on 2026-11-08 ([09 - Live probe](09-live-probe.md) §2).
>
> Upstream `master` of both repositories was also checked through the GitHub API (go-steam `336e824f8540`,
> 2026-09-22; go-dota2 `6d32dc38d075`, 2026-09-22).

Legend: **[VERIFIED]** means I read it in source code or a primary document. **[INFERRED]** means it is a
reasonable conclusion I have not tested. **[UNCERTAIN]** means the sources conflict or I could not confirm it.

---

## TL;DR

1. **The pre-PR pinned go-steam can no longer log in.** It sends only the legacy
   `CMsgClientLogon{account_name, password, auth_code, two_factor_code, sha_sentryfile, login_key}`
   message (`auth.go:52-82`). It has no `access_token` (field 108) in its generated `CMsgClientLogon`, and no
   `IAuthenticationService` client. The upstream PR that fixed this says: *"Steam has deprecated the legacy
   username+password `CMsgClientLogon` flow (it now returns `EResult_InvalidPassword`)"*. A contributor added on
   2026-06-24: *"As of last night steam fully deprecated the old logon method."* The PR author (markus-wa, a
   contributor, not the maintainer) then confirmed *"master does not work in any case right now"* in answer to a
   question from the maintainer, paralin, who shipped a rewrite the next day
   ([go-steam#11](https://github.com/paralin/go-steam/pull/11)). The `InvalidPassword` rejection was already reported
   when the PR was opened on 2026-06-10, before the reported "full" shutdown around 2026-06-23. **[VERIFIED: the code
   and what was said in the thread. The cutoff date comes from the PR thread only; neither Valve nor any other
   library published a date.]**
2. **Upstream go-steam fixed this on 2026-06-27** (commit `6b06e91640b1`, "new protobuf generator, new token
   authentication method"), and both libraries got more updates on 2026-09-22. The new API is
   `Auth.LogOn(ctx, *LogOnDetails) error` and `Auth.Authenticate(ctx, *LogOnDetails) (token string, err error)`.
   `LogOnDetails` has new fields: `AccessToken`, `DeviceFriendlyName` and `ConfirmSteamGuard`. The `SentryFileHash`
   and `LoginKey` fields are gone. **This is a breaking API change, and go-dota2 has to be upgraded together with
   go-steam** (go-dota2 master pins go-steam `v0.0.0-20260922075330-336e824f8540`). **[VERIFIED]**
3. **What to build:** for each bot, call `Auth.Authenticate()` once. Supply a Steam Guard TOTP code generated
   from that bot's `shared_secret`, or an email code. Then **persist the returned refresh token (a JWT) to a Docker
   volume**, and on every later start or reconnect log in with `LogOnDetails{Username, AccessToken: token}`. The
   password is only needed again when the token expires (Valve's tokens last about 200 days) or is revoked.
   **[VERIFIED live 2026-11-08]:** `Authenticate` with password + TOTP returned a refresh token in 0.7 s with
   no prompt (JWT `aud` `client, web, renew, derive`, lifetime about 210 days), and token-only `LogOn` reached
   the GC welcome on every one of seven fresh-process runs ([09](09-live-probe.md) §2).
4. **Use a mobile authenticator (TOTP) on every bot account.** With TOTP the service can log in again with no
   human involved. Email-code Steam Guard needs a person each time a new token is needed, and upstream go-steam does
   **not** persist `new_guard_data`, so email codes will be requested again.
5. **Account requirements:** use one Steam account per concurrent bot, because one CM session can run one game
   (GC) session. Accounts should be non-limited (at least $5 spent) so they can send friend invites (verified
   Valve restriction). Whether limited accounts can send GC **lobby** invites is UNCERTAIN (see section 7.1). Do not log in to a bot account from a desktop client at the same time (`LoggedInElsewhere` /
   `PlayingBlocked`). Stagger logins and back off exponentially. Steam rate-limits login attempts, and failures
   can show up as `InvalidPassword`, `RateLimitExceeded` or `AccountLoginDeniedThrottle`.

---

## 1. What the pinned go-steam version does

### 1.1 `LogOnDetails` (pinned version)

`go-steam@f167ff28a93a/auth.go:22-39`

| Field | Type | Maps to `CMsgClientLogon` field | Status in 2026 |
|---|---|---|---|
| `Username` | string | `account_name` (50) | still required |
| `Password` | string | `password` (51) | **rejected** by CM since about June 2026 ([#11](https://github.com/paralin/go-steam/pull/11)) |
| `AuthCode` | string | `auth_code` (84), the email Steam Guard code | legacy only |
| `TwoFactorCode` | string | `two_factor_code` (101), the mobile TOTP code | legacy only |
| `SentryFileHash` | `[]byte` | `sha_sentryfile` (83) | dead: the sentry/machine-auth flow was deprecated in 2023 ([SteamKit discussion #1258](https://github.com/SteamRE/SteamKit/discussions/1258)) |
| `LoginKey` | string | `login_key` (60) | dead: Steam stopped issuing login keys in March 2023 ([node-steam-user v5 guide](https://github.com/DoctorMcKay/node-steam-user/wiki/v4-%E2%86%92-v5-migration-guide)) |
| `ShouldRememberPassword` | bool | `should_remember_password` (8) | still meaningful, but for token login |
| *(none)* | | `access_token` (108) | **missing**: the generated struct (`protocol/protobuf/client_server_login.pb.go:30-79`) ends at field 103 `web_logon_nonce` |

Current SteamDatabase protos define `CMsgClientLogon` fields 104 `priority_reason`, 105 `embedded_client_secret`,
106 `disable_partner_autogrants`, **108 `access_token`**, 109 `is_chrome_os` and 111 `gaming_device_type`
([SteamDatabase/Protobufs steammessages_clientserver_login.proto](https://github.com/SteamDatabase/Protobufs/blob/master/steam/steammessages_clientserver_login.proto)).
**[VERIFIED]**

### 1.2 Legacy login sequence (pinned version)

`auth.go:41-51` documents the legacy flow:

1. Log on with username and password. A Steam Guard account gets `EResult_AccountLogonDenied` (63) and Steam
   emails a code.
2. Reconnect and log on with `AuthCode`. Steam sends `EMsg_ClientUpdateMachineAuth`. go-steam SHA-1s the payload,
   ACKs it, and emits `MachineAuthUpdateEvent{Hash}` (`auth.go:168-182`).
3. Later logins use `SentryFileHash`. Optionally, `LoginKeyEvent` (`auth.go:142-152`) gave a password-less key.

Every step of this flow is obsolete:

- SteamKit marks its own legacy samples as *"the old login flow, which may still work, but you will not receive
  machine auth ... This sample will be removed in the future"* (`Samples/005_LegacySteamGuard/Program.cs`) and
  *"still currently works for simple cases where you do not need to remember password"*
  (`Samples/004_LegacyLogin/Program.cs`, last touched 2025-10-08). **[VERIFIED in the SteamKit repo via `gh api`]**
- The go-steam PR #11 thread (June 2026) reports that password `CMsgClientLogon` now returns
  `EResult_InvalidPassword` for every account. paralin (maintainer) asked *"the old approach on master right now - is it not
  working anymore?"* and markus-wa replied *"Correct, master does not work in any case right now, Valve removed the
  API / logon method this was using for security reasons"*.
  **[VERIFIED that this was said. I have not tested it against live Steam.]**
- A web search found no Valve announcement or SteamKit/node-steam-user changelog dated June 2026. Those projects
  moved to the token flow in 2023, so a final shutdown of the legacy path would not have broken them.
  **[UNCERTAIN about the exact cutoff date. Treat legacy login as non-functional.]**

### 1.3 Other pinned-version facts relevant to auth

| Topic | Code | Notes |
|---|---|---|
| Logon result handling | `auth.go:103-140` | `OK` emits `LoggedOnEvent` and starts the heartbeat (`out_of_game_heartbeat_seconds`). `Fail`, `ServiceUnavailable` and `TryAnotherCM` emit `SteamFailureEvent` (no disconnect, an EOF follows). **Any other result emits `LogOnFailedEvent` and calls `Disconnect()`, which also emits `DisconnectedEvent`.** |
| Web session | `web.go:52-116` | Uses `ISteamUserAuth/AuthenticateUser` with the `webapi_authenticate_user_nonce` from the logon response. This is the old cookie mechanism. Valve removed the old community login in Oct 2023 ([node-steamcommunity wiki](https://github.com/DoctorMcKay/node-steamcommunity/wiki/SteamCommunity)). **[INFERRED: probably broken. We do not need it for lobbies.]** |
| CM list (static) | `servers.go:13-34` | 20 hard-coded `IP:27017` TCP endpoints. `GetRandomCM()` picks one at random. |
| CM list (directory) | `steam_directory.go:17-58` | `InitializeSteamDirectory()` calls `ISteamDirectory/GetCMList/v1/?cellId=0`. It must be called explicitly, and `Connect()` falls back to the static list when it has not been (`client.go:137-148`). |
| Transport | `connection.go`, `client.go:156-169` | **TCP only** (`net.DialTCP`, magic `VT01`). No WebSocket CM support. `ConnectToBind(addr, localTCPAddr)` exists for multi-homed hosts. |
| Playing-session protos | `protocol/protobuf/client_server_2.pb.go:7042,7067` | `CMsgClientPlayingSessionState{playing_blocked, playing_app}` and `CMsgClientKickPlayingSession` exist, but **no handler** emits them in the pinned version. |
| Free license | `client_server_2.pb.go:4524` / `EMsg_ClientRequestFreeLicense = 5572` | Proto and EMsg exist. There is no helper method. |
| New-auth EMsgs | `protocol/steamlang/enums.go` | No `EMsg_ClientHello` (9805) or `ServiceMethodCallFromClientNonAuthed` (9804). Only `ServiceMethod` (146) and `ClientServiceMethod` (5594). This is why every 2026 fix does the auth handshake over HTTPS instead of the CM. |

---

## 2. Steam's current ("new") authentication flow

### 2.1 Overview

Since 2023, the Steam client and every maintained library authenticate in two phases:

```
             HTTPS (api.steampowered.com)                              CM (TCP/WebSocket)
┌───────────────────────────────────────────────────────────┐   ┌──────────────────────────────┐
│ IAuthenticationService/GetPasswordRSAPublicKey (GET)      │   │                              │
│ RSA-PKCS1v15 encrypt password                             │   │                              │
│ IAuthenticationService/BeginAuthSessionViaCredentials     │   │                              │
│   -> client_id, request_id, interval, allowed_confirmations│  │                              │
│ IAuthenticationService/UpdateAuthSessionWithSteamGuardCode│   │                              │
│   (EmailCode=2 or DeviceCode=3)                           │   │                              │
│ IAuthenticationService/PollAuthSessionStatus (loop)       │   │                              │
│   -> refresh_token (JWT), access_token, new_guard_data    │──▶│ CMsgClientLogon{             │
└───────────────────────────────────────────────────────────┘   │   account_name,              │
                                                                │   access_token = refresh_token│
                                                                │   should_remember_password } │
                                                                └──────────────────────────────┘
```

**[VERIFIED]** The RPC names and fields below come from
[SteamDatabase/Protobufs `steammessages_auth.steamclient.proto`](https://github.com/SteamDatabase/Protobufs/blob/master/steam/steammessages_auth.steamclient.proto):

- `service Authentication`: `GetPasswordRSAPublicKey`, `BeginAuthSessionViaQR`, `BeginAuthSessionViaCredentials`,
  `PollAuthSessionStatus`, `GetAuthSessionInfo`, `GetAuthSessionRiskInfo`, `NotifyRiskQuizResults`,
  `UpdateAuthSessionWithMobileConfirmation`, `UpdateAuthSessionWithSteamGuardCode`, `GenerateAccessTokenForApp`,
  `EnumerateTokens`, `GetAuthSessionsForAccount`, `RevokeToken`, `RevokeRefreshToken`.
- `CAuthentication_BeginAuthSessionViaCredentials_Request`: 1 `device_friendly_name`, 2 `account_name`,
  3 `encrypted_password`, 4 `encryption_timestamp`, 5 `remember_login`, 6 `platform_type`,
  7 `persistence` (default Persistent), 8 `website_id`, 9 `device_details`, **10 `guard_data`**, 11 `language`,
  12 `qos_level`.
- `..._Response`: 1 `client_id`, 2 `request_id`, 3 `interval`, 4 `allowed_confirmations`, 5 `steamid`,
  6 `weak_token`, 7 `agreement_session_url`, 8 `extended_error_message`.
- `CAuthentication_PollAuthSessionStatus_Response`: 1 `new_client_id`, 2 `new_challenge_url`,
  **3 `refresh_token`**, 4 `access_token`, 5 `had_remote_interaction`, 6 `account_name`, **7 `new_guard_data`**,
  8 `agreement_session_url`.
- `EAuthTokenPlatformType`: Unknown=0, **SteamClient=1**, WebBrowser=2, MobileApp=3.
- `EAuthSessionGuardType`: Unknown=0, None=1, EmailCode=2, DeviceCode=3, DeviceConfirmation=4,
  EmailConfirmation=5, MachineToken=6, LegacyMachineAuth=7.
- `CAuthentication_AccessToken_GenerateForApp_Request`: 1 `refresh_token`, 2 `steamid`, 3 `renewal_type`
  (`k_ETokenRenewalType_None=0`, `k_ETokenRenewalType_Allow=1`). The response returns `access_token` and
  `refresh_token`.

### 2.2 Token semantics (cross-library consensus)

| Fact | Source | Confidence |
|---|---|---|
| The **refresh token** goes in `CMsgClientLogon.access_token` (field 108). SteamKit's sample sets `AccessToken = pollResponse.RefreshToken`. | SteamKit `Samples/000_Authentication/Program.cs` lines 83-89; [node-steam-user guide](https://github.com/DoctorMcKay/node-steam-user/wiki/v4-%E2%86%92-v5-migration-guide) | VERIFIED |
| `IsPersistentSession=true` also needs `ShouldRememberPassword=true` on `LogOn` *"for it to work correctly"*. | SteamKit sample line 89 comment; [steam-vent PR #2](https://github.com/deadlock-api/steam-vent/pull/2) | VERIFIED |
| Only **SteamClient** platform tokens can log in to a CM. A WebBrowser token gives *"This refreshToken is not valid for logging in to the Steam client"*. | [node-steam-session README](https://github.com/DoctorMcKay/node-steam-session) | VERIFIED (node) |
| Tokens are JWTs, and you can base64-decode the payload to read `exp`. Persistent sessions last about 200 days ("207 days" on the web, "~200 day lifetime" in the gg-cr fork). | [SteamKit #1125](https://github.com/SteamRE/SteamKit/issues/1125); [gg-cr/go-steam](https://pkg.go.dev/github.com/gg-cr/go-steam); live token: 210 days ([09](09-live-probe.md) §2) | VERIFIED live for one token (the exact lifetime is Valve's choice) |
| A SteamClient refresh token copied to another host logs in with no new sign-in. | REPORTED by an open-source league bot (field notes, October 2026); not tested by us | REPORTED |
| Calling LogOff **can** invalidate the refresh token (not necessarily always). A plain Disconnect keeps it. | [DepotDownloader PR #5](https://github.com/Reino-Doce/DepotDownloader/pull/5) (*"Steam can invalidate refresh tokens when an application logs off. The next ... run then failed with `AccessDenied`"*; fixes its issue #4) | LIKELY (one project's report, manually validated; not tested here) |
| A failed **token** logon with `InvalidPassword` means the token is expired or revoked, not that the password is wrong. A dead token can also come back as `AccessDenied`, `Expired`, `Revoked` or `InvalidSignature`. | [wabbajack PR #3005](https://github.com/wabbajack-tools/wabbajack/pull/3005) (classes all five as dead credentials); DepotDownloader PR #5 (`AccessDenied` after LogOff) | LIKELY |
| `new_guard_data` (a JWT with its own expiry) replaces the sentry file. Passing it as `guard_data` in a later `BeginAuthSessionViaCredentials` skips the email code. | SteamKit sample lines 75-80 | VERIFIED (SteamKit) |
| Renewal: `GenerateAccessTokenForApp` with `renewal_type=Allow`. Steam renews only near expiry. node-steam-session says its WebAPI `renewRefreshToken` *"works only for EAuthTokenPlatformType MobileApp"* as of 2025-04-30, and that SteamClient tokens should be renewed over the CM (steam-user `renewRefreshTokens`). | [node-steam-session README](https://github.com/DoctorMcKay/node-steam-session); SteamKit 3.4 (search summary) | UNCERTAIN for Go |
| `PollAuthSessionStatus` sometimes returns only `{"had_remote_interaction": false}`, with no token. | [steampy #380](https://github.com/bukson/steampy/issues/380) | LIKELY, so keep polling up to the limit |

---

## 3. Upstream go-steam (2026-06+) and how it fits

### 3.1 New API surface (upstream `master`, read via `gh api`)

`logon-details.go` (upstream):

```go
type LogOnDetails struct {
    Username           string
    Password           string // starts a modern auth session, exchanged for a token before CM logon
    AccessToken        string // log on to CM directly with a stored token
    DeviceFriendlyName string // label shown in the account's "authorized devices"
    AuthCode           string // Steam Guard email code
    TwoFactorCode      string // Steam Guard mobile (TOTP) code
    ConfirmSteamGuard  func(context.Context, []SteamGuardConfirmation) (string, unified.EAuthSessionGuardType, error)
    ShouldRememberPassword bool // request a persistent token
}
```

`auth.go` (upstream):

- `func (a *Auth) LogOn(ctx context.Context, details *LogOnDetails) error`. If `AccessToken` is empty, it runs the
  HTTPS credential exchange **synchronously** (`getAccessTokenViaCredentials`) and then writes
  `CMsgClientLogon{AccountName, AccessToken, ClientLanguage, ProtocolVersion, [ShouldRememberPassword]}`.
  On failure it emits `LogOnFailedEvent{Err, AuthSessionState, Confirmations}` and returns the error.
- `handleLogOnResponse` now reads `heartbeat_seconds`, and after `OK` it explicitly requests the WebAPI nonce.
- **New:** `EMsg_ClientPlayingSessionState` is handled and emits
  `PlayingSessionStateEvent{PlayingBlocked bool, PlayingApp uint32}` (`playing-session-event.go`). Its comment
  says: *"Announcing playback while blocked can end the login with LoggedInElsewhere."*
- The `MachineAuthUpdateEvent` and `LoginKeyEvent` handlers are **removed**.

`auth-token.go` (upstream):

```go
// Authenticate exchanges credentials and Steam Guard confirmation for a token.
// It does not open a CM connection. The returned token is a secret; store it in
// protected storage and pass it as LogOnDetails.AccessToken for later logins.
func (a *Auth) Authenticate(ctx context.Context, details *LogOnDetails) (string, error)
```

`auth_session.go` (upstream) implementation details:

- Calls `https://api.steampowered.com/IAuthenticationService/<Method>/v1/` with `input_protobuf_encoded=<base64>`.
  It checks the `x-eresult` and `x-error_message` response headers.
- Uses `platform=SteamClient`, `persistence=Persistent` and `website_id="Client"`. The default device name is
  `"go-steam"`.
- Steam Guard: if `AuthCode` is set and EmailCode is allowed, or `TwoFactorCode` is set and DeviceCode is allowed,
  it calls `UpdateAuthSessionWithSteamGuardCode`. Otherwise it calls the `ConfirmSteamGuard` callback once, within
  the same session, so an emailed code stays valid. DeviceConfirmation and EmailConfirmation (an approval tap in
  the app) are allowed through to polling.
- Polls `PollAuthSessionStatus` up to **10 attempts** (`maxPollAttempts`) at the server-provided interval. It
  returns `refresh_token`, falling back to `access_token`.
- **It never sends `guard_data` and never surfaces `new_guard_data`.** It also exposes no token-renewal helper.
  (Not found in `auth_session.go`, `auth.go` or `auth-token.go`. **[VERIFIED for those files]**)
- `AuthSessionState` values include `email_code`, `device_code`, `manual_confirmation`, `unsupported_guard`,
  `denied`, `canceled`, `transport`, `protobuf`, `agreement` and `timeout`.

Other upstream changes that matter to us:

| Commit | Change |
|---|---|
| `4263b220cdcf` (2026-04-13) | auto-initialize the Steam Directory cache on connect |
| `9a903700fc88` (2026-06-27) | handle Steam directory HTTP errors |
| `8c23cbb74e27` (2026-09-22) | bind transport work to connection lifetime (`ConnectContext`, `Wait(ctx)`) |
| `e882f1724962` (2026-09-22) | titled "reconnect after unanswered heartbeats", but the code (upstream `client.go` heartbeat setup, about lines 373-376) only sets a read deadline of 3x the heartbeat interval (*"Three unanswered heartbeat intervals terminate a silently broken transport"*). On expiry `failSession` emits `FatalErrorEvent` and disconnects. **go-steam does not reconnect by itself**; our supervisor must call `Connect()` again. |
| `908c060af61e` (2026-09-22) | expose playing session availability |
| `c82323035020` (2026-09-22) | switch to `protobuf-go-lite` (breaking for callers that use `golang/protobuf` types) |
| go.mod | `go 1.26.0`. The local toolchain is 1.27.1, which is fine. |

go-dota2 `master` (`6d32dc38d075`) depends on go-steam `336e824f8540` and also moved to lite messages, with
`fix(gc): correlate requests and cancel ended sessions`. **We must bump both modules together**, and every
`LogOn` call site changes (`LogOn` now takes a ctx and returns an error).

### 3.2 Alternatives if upstream is not adopted

| Option | Notes |
|---|---|
| [gg-cr/go-steam](https://pkg.go.dev/github.com/gg-cr/go-steam) fork | Adds `AccessToken`, `RefreshToken` and `GuardData` to `LogOnDetails`, plus a raw writer for field 108, because *"`proto.Marshal` silently drops field 108"* when the descriptor lacks it. Its README warns that the CM-based `BeginAuthSessionViaCredentials` path *"is unreliable with paralin's wire protocol ... causes EOF disconnects on some CM servers"* and recommends the HTTP API. |
| Hand-rolled HTTP auth + raw field 108 | Feasible: a few hundred lines. But the upstream fix already exists, so this is not worth doing. |
| [lbt05/go-steam `iauthenticationservice`](https://pkg.go.dev/github.com/lbt05/go-steam/api/services/iauthenticationservice) | A Go WebAPI wrapper for the same RPCs, including `GenerateAccessTokenForApp` and `RevokeToken`. |

**Recommendation:** upgrade to upstream go-steam and go-dota2 `master`. Call `Auth.Authenticate` ourselves so we
receive the token and can persist it. Then use `LogOn(ctx, {Username, AccessToken, ShouldRememberPassword:true})`.

---

## 4. Steam Guard for unattended bots

### 4.1 Modes

| Mode | What Steam asks for | Unattended? | Notes |
|---|---|---|---|
| None (Guard disabled) | nothing (`EAuthSessionGuardType_None`) | yes | Valve strongly discourages this. New logins can still trigger email confirmation. Limited accounts with no Guard are high-risk. |
| Email | `EmailCode` (5-char code emailed) or `EmailConfirmation` (click a link) | **no**: a human or a mail-parsing hook is needed every time a new token is needed | Without `guard_data` persistence (upstream does not do it), **every** credential exchange triggers an email. A token lasts about 200 days, so this happens roughly twice a year per bot, plus after any revocation. |
| Mobile authenticator | `DeviceCode` (5-char TOTP) or `DeviceConfirmation` (tap approve) | **yes**, if we hold the `shared_secret` | Recommended. The `shared_secret` comes from the authenticator setup (`maFile` from SteamDesktopAuthenticator or steamguard-cli, or `steam-totp` style tooling). It also gives a 15-day trade hold exemption, which is irrelevant here. |

### 4.2 Generating Steam Guard TOTP codes in Go

The algorithm is **[VERIFIED]** against several independent implementations:
[doctype/steam totp.go](https://github.com/doctype/steam/blob/master/totp.go),
[DoctorMcKay/php-steam-totp](https://github.com/DoctorMcKay/php-steam-totp/blob/master/src/SteamTotp.php),
[lazybw#115](https://github.com/juthrbog/lazybw/issues/115) and
[creachadair/otp](https://pkg.go.dev/github.com/creachadair/otp).

- key = base64-decode(`shared_secret`)
- counter = floor(unix_time / 30), as an 8-byte big-endian integer
- mac = HMAC-SHA1(key, counter); offset = mac[19] & 0x0F; code = BE uint32(mac[offset:offset+4]) & 0x7FFFFFFF
- emit 5 characters: `alphabet[code % 26]`; `code /= 26`, with alphabet `23456789BCDFGHJKMNPQRTVWXY`

```go
package steamguard

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"time"
)

const alphabet = "23456789BCDFGHJKMNPQRTVWXY"

// Code returns the Steam Guard mobile code for sharedSecret at time t.
// Pass t = time.Now().Add(offset), where offset is the server-time skew
// (ITwoFactorService/QueryTime), if the host clock may drift.
func Code(sharedSecret string, t time.Time) (string, error) {
	key, err := base64.StdEncoding.DecodeString(sharedSecret)
	if err != nil {
		return "", fmt.Errorf("decode shared_secret: %w", err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(t.Unix()/30))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[19] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	out := make([]byte, 5)
	for i := range out {
		out[i] = alphabet[v%uint32(len(alphabet))]
		v /= uint32(len(alphabet))
	}
	return string(out), nil
}
```

Operational notes:

- **Clock skew matters.** The code window is 30 seconds. Run NTP in the container host. Optionally query
  `ITwoFactorService/QueryTime` and apply an offset. doctype/steam has a `ServerTimeTip` for this.
  **[INFERRED: QueryTime is the endpoint every steam-totp library uses. I have not tested it here.]**
- **Do not reuse a code.** Steam rejects a code that was already used (`TwoFactorCodeMismatch` / `EResult 88`).
  When logging in several bots that share one authenticator (not recommended), or retrying quickly, wait for the
  next 30-second window. **[LIKELY]**
- Store `shared_secret` as a `config.Secret` in `secrets.yaml` (it is as sensitive as the password). Never store
  `identity_secret` or `revocation_code` in the service. They are not needed for login.

### 4.3 Proposed `secrets.yaml` shape (design only, not code)

```yaml
secrets:
  bots:
    - username: lobbybot01
      password: "..."          # used only when no valid token is on disk
      shared_secret: "..."     # base64, from maFile; enables unattended TOTP
      device_name: "dota_lobby-01"
    - username: lobbybot02
      password: "..."
      # no shared_secret -> email Guard; service exposes POST /bots/{name}/guard-code
```

---

## 5. Error handling and retry policy

### 5.1 `EResult` values to handle (pinned `protocol/steamlang/enums.go:3473-3572`)

| EResult (value) | Where it appears | Meaning in the token world | Action |
|---|---|---|---|
| `OK` (1) | logon response | success | reset backoff |
| `InvalidPassword` (5) | credential exchange (`x-eresult`) **or** CM logon with token | credential exchange: wrong password (or login rate limiting, see [steamerrors.com/5](https://steamerrors.com/5)). Token logon: **token expired or revoked**. | Token: delete the stored token, then redo `Authenticate` once. Password: mark the bot `needs_attention` and **stop retrying** (retrying a bad password triggers throttling). |
| `AccessDenied` (15), `Revoked` (26), `Expired` (27), `InvalidSignature` (121) | CM logon with token | **dead token** (same branch as `InvalidPassword` on a token logon; per wabbajack PR #3005 and DepotDownloader PR #5). Note: `InvalidSignature` (121 in SteamKit `eresult.steamd`) is **not** in the pinned go-steam enum; upstream should be checked. | Delete the stored token, redo `Authenticate` once; if that also fails, `needs_attention`. |
| `LoggedInElsewhere` (6) | `LoggedOffEvent` | another session started the game for this account | Do not fight it. Back off for minutes and alert. See section 7. |
| `AlreadyLoggedInElsewhere` (50) | logon | another session holds the account | same as above |
| `AccountLogonDenied` (63) | legacy only | email Guard needed | n/a with the new flow: you get `AuthSessionState=email_code` instead |
| `InvalidLoginAuthCode` (65) / `ExpiredLoginAuthCode` (71) | Guard code submit | wrong or stale email code | request a new code from the operator |
| `RateLimitExceeded` (84) | auth or logon | too many attempts | long backoff (start at 30 minutes or more), with no immediate retry |
| `AccountLoginDeniedNeedTwoFactor` (85) | legacy | needs TOTP | new flow: `AuthSessionState=device_code` |
| `AccountLoginDeniedThrottle` (87) | logon | throttled | long backoff, same as 84 |
| `TwoFactorCodeMismatch` (88) | Guard code submit | wrong or reused TOTP, or clock skew | wait for the next 30s window, resync time, retry at most twice, then alert |
| `AccountLimitExceeded` (95) | various | too many accounts on a resource | alert |
| `Fail` (2), `ServiceUnavailable` (20), `TryAnotherCM` (48) | logon | Steam-side trouble | go-steam emits `SteamFailureEvent`. Reconnect to a **different** CM with short backoff. |

Upstream `LogOnFailedEvent` also carries `Err` and `AuthSessionState`. Branch on `AuthSessionState`
(`email_code`, `device_code`, `manual_confirmation`, `agreement`, `denied`, `transport`, `timeout`) for failures
that happen before the CM logon.

### 5.2 Reconnect and backoff strategy

Recommended state machine for each bot:

```
Disconnected --connect--> Connecting --ConnectedEvent--> Authenticating
Authenticating --(token ok)--> LoggedOn --SetPlaying+SayHello--> GCReady
Authenticating --InvalidPassword/AccessDenied/Expired/Revoked/InvalidSignature on token--> drop token --> Authenticating(credentials) [once]
Authenticating --InvalidPassword on creds / Guard needed w/o secret--> NeedsAttention (no auto retry)
Authenticating --RateLimit/Throttle--> CoolingDown(30-60 min)
any --DisconnectedEvent/SteamFailure/heartbeat timeout--> Backoff(min(2^n * 5s, 5m) ± 20% jitter) --> Connecting
LoggedOn --LoggedOff(LoggedInElsewhere)--> CoolingDown(10 min) + alert
```

Implementation guidance:

- Classify each failure **once per attempt**. Deduplicate the `LogOnFailedEvent` + `DisconnectedEvent` pair with
  an attempt ID or a single reconnect goroutine for each bot.
- Use exponential backoff with full jitter (for example `cenkalti/backoff/v4` or a small hand-rolled helper) and
  reset it after about 5 minutes of stable `GCReady`.
- **Stagger the initial logins** (for example a 5-15 second gap between bots at start-up). On CM-wide failures,
  spread the reconnects too. This is about not hammering Steam. It is not a technique for getting around limits.
- Pick a new CM on every reconnect. `Connect()` already randomizes. Upstream refreshes the directory list and
  emits `ClientCMListEvent`, which can be cached.
- Never call any "log off" on shutdown if one is added. **Only `Disconnect()`**, so the refresh token is not
  invalidated (see section 2.2).

### 5.3 Rate limits (what is known)

Valve does not publish numeric login limits. What the sources say:

- paralin (go-dota2 maintainer), 2025-07-16: *"you may be IP banned, steam account banned, or rate limited if
  abusing the privilege ... if you're just doing normal non abusive things at a reasonable rate it shouldn't be a
  problem"*. And on 2026-02-24: *"there are definitely rate limits"*
  ([go-dota2#21](https://github.com/paralin/go-dota2/issues/21)). **[VERIFIED quote]**
- `InvalidPassword` can be returned *"if your network is being temporarily login rate-limited"*
  ([steamerrors.com/5](https://steamerrors.com/5)). **[LIKELY]**
- Token logins are much cheaper than credential exchanges, because they need no RSA key fetch, no Guard step and
  no auth session. **Persisting tokens is the single most effective way to stay under login limits.** **[INFERRED]**
- Running a handful of accounts (the scale of a league lobby service) from one server IP is common for inhouse and
  league bots. I found **no authoritative per-IP account cap**. **[UNCERTAIN]** Plan for few logins with long
  sessions, not frequent re-logins, and keep the bot count to what the league actually needs.

---

## 6. Persisting auth state across Docker restarts

### 6.1 What to persist (for each bot)

| Item | Sensitivity | Lifetime | Needed for |
|---|---|---|---|
| `refresh_token` (JWT, SteamClient audience) | **secret**: equivalent to a logged-in session | about 200 days, revocable | every CM logon |
| `steamid` (decoded from JWT `sub`) | low | permanent | logging, renewal call |
| token `exp` (decoded) | low | | proactive re-auth alerts |
| `new_guard_data` | secret | JWT with expiry | skipping email codes. **Upstream go-steam cannot use it today.** |
| last-good CM list | low | hours to days | faster reconnect |

### 6.2 Layout and handling

```
/data/                        # Docker named volume, e.g. dota_lobby_state:/data
  bots/
    lobbybot01.json           # {"refresh_token": "...", "steamid": "7656...", "exp": 1790000000, "updated_at": "..."}
    lobbybot02.json
```

- Write files atomically (temp file plus `rename`) with mode `0600`, and run the container as a non-root UID that
  owns `/data`.
- Load the token into a `config.Secret`-like type so it is redacted in logs and JSON. **Never** include tokens in
  `/bots` API responses. Expose only `{token_present, token_expires_at, state}`.
- On startup: valid token, then token logon. Missing or expired token, then credential exchange plus Guard (TOTP
  or operator code), then save.
- Alert when `exp - now < 14d`. Re-authenticate proactively at a quiet time rather than mid-match. Upstream
  go-steam has no renewal helper, so the simplest renewal is a fresh `Authenticate` (cheap with TOTP).
  **[INFERRED]**
- Use a single writer for each bot. Two containers sharing the volume and the same accounts will fight
  (`LoggedInElsewhere`). See section 7.
- Back up the volume, or accept that losing it only costs one Guard round per bot.

### 6.3 Operator flow for email-Guard bots (if any)

Expose an authenticated admin endpoint such as `POST /bots/{name}/steam-guard {"code":"ABCDE"}`. Use it in the
upstream `ConfirmSteamGuard` callback, which **blocks the same auth session** until the code arrives (with a
timeout). Starting a new session would invalidate the email code that was already sent. **[VERIFIED: the
callback's purpose is documented in upstream `auth_session.go:135` "Prompt without beginning another session, so
emailed codes remain valid".]**

---

## 7. Account requirements and multi-bot concerns

### 7.1 Limited accounts ($5 rule)

The official Valve article ([Limited User Accounts](https://support.steampowered.com/kb/3330-IAGK-7663/limited-user-accounts))
says accounts that have not spent $5 cannot **send** friend invites (they can still accept them), have rate-limited
discussion posting, and lose the unlock again if refunds drop the total below $5. Third-party guides add
restrictions on group chat, chat with non-friends and the market (**UNCERTAIN**: not on the official page).

Impact on lobby bots:

| Bot action | Limited account? | Confidence |
|---|---|---|
| Log in, start a Dota GC session, create a lobby | works | LIKELY (common inhouse-bot practice. go-dota2#21's 2025 walkthrough created lobbies with a plain account.) |
| Invite players to a lobby via GC (`InviteLobbyMember`) | **unclear**. GC lobby invites are not Steam friend invites, but Valve may gate them for limited accounts. | UNCERTAIN |
| Send Steam friend requests / chat to non-friends | blocked | VERIFIED (friend invites) / UNCERTAIN (chat) |
| Be added as a league (ticket) admin/caster on dota2.com league tools | not investigated here, see the league track | UNCERTAIN |

**Recommendation:** make every bot account non-limited (add $5 to the wallet once). The `EAccountFlags_LimitedUser`
(4096) bit appears in `LoggedOnEvent.AccountFlags` and `AccountInfoEvent.AccountFlags` (`enums.go:3943`). Log it at
startup and flag the bot in `/bots`. **[VERIFIED that the flag exists. INFERRED that Steam sets it for limited
accounts.]**

### 7.2 Owning Dota 2 (app 570)

Dota 2 is free-to-play. A fresh account normally gets the free license the first time Dota 2 is "installed" or
"played" from a client. go-steam has `EMsg_ClientRequestFreeLicense` (5572) and `CMsgClientRequestFreeLicense{appids}`
but no helper, and `ClientLicenseList` (780) can confirm ownership. **[VERIFIED that the protos exist.]** **[VERIFIED live (Q21)]:** an account that had never launched Dota 2 and
has no client install got `PlayingSessionStateEvent` for app 570, a GC welcome and league lobbies
([09](09-live-probe.md) §3), so a manual first launch is not required. Still untested: an account whose licence
list lacks the free 570 entry. A one-off `RequestFreeLicense(570)` helper remains an option for that case.

### 7.3 One game session per account

- One Steam account can have **one active "playing" session**. go-dota2 announces Dota with
  `SetGamesPlayed(570)` (`go-dota2 client_session.go:14-24` to `go-steam gamecoordinator.go:68-80`).
- If a person (or a second container) logs in to the same account and launches a game, Steam sends
  `ClientPlayingSessionState{playing_blocked=true}` to the other session. If the bot then announces Dota it can be
  kicked with `LoggedInElsewhere`. Upstream now surfaces this as `PlayingSessionStateEvent` (commit `908c060af61e`).
  The pinned version ignores it. **[VERIFIED: the event and its comment upstream.]**
- Policy: **dedicated accounts only**. When `PlayingBlocked` is true, do not call `SetPlaying(true)`. Mark the bot
  `blocked_elsewhere` and wait. Never send `ClientKickPlayingSession` automatically, because that would kick a
  human. To run N lobbies at the same time, use **N accounts, one account per bot goroutine.**

### 7.4 Several accounts in one process

- Each bot needs its own `steam.Client` (its own TCP connection, event channel, heartbeat and GC session).
- The **event channel must be drained continuously**. The pinned `Emit` blocks on a 30-buffer channel
  (`client.go:89-91`). Upstream `LogOn` performs HTTPS calls synchronously, so run it in its own goroutine (with a
  ctx and timeout) and not inside the event switch, or the reader can stall. **[INFERRED from the code]**
- `steam.InitializeSteamDirectory()` uses a **package-global** cache (`steam_directory.go:21`). Call it once per
  process, not once per bot.
- Memory and CPU are small (one TCP socket and a few goroutines per bot). Practical limits come from Steam policy,
  not from resources.

---

## 8. Web session (only if needed)

Lobby management happens entirely over the CM/GC protocol, so no web session is needed. If one is needed later
(for example to scrape dota2.com league pages that require login), do not use `steam.Web.LogOn()`, which relies
on the legacy `ISteamUserAuth/AuthenticateUser` path. With the new system, a SteamClient refresh token can be
exchanged for web cookies, but node-steam-session notes `getWebCookies` with SteamClient fails *"with response
`AccessDenied` unless sent over an authenticated CM session"*. Implementing that in Go is non-trivial.
**[UNCERTAIN: not needed for current goals.]** For league listings, prefer the Steam Web API with a publisher or
user API key, as covered in the other research tracks.

---

## 9. Concrete recommendations for `dota_lobby`

1. **Upgrade dependencies:** `go get github.com/paralin/go-steam@master github.com/paralin/go-dota2@master`. Then
   fix the call sites: `LogOn(ctx, ...) error`, the protobuf-go-lite types, and the removed
   `SentryFileHash`/`LoginKey`/`MachineAuthUpdateEvent`.
2. **Config:** replace `BotConfig.SentryHash` with `SharedSecret Secret` (optional) and `DeviceName string`. Add
   `state_dir` (default `/data`) to `config.yaml`.
3. **TokenStore:** a per-bot JSON file on a volume, written atomically with `0600` permissions, with JWT `exp`
   decoding and a redacted type.
4. **Login flow:** token, then `LogOn{AccessToken, ShouldRememberPassword:true}`. If there is no token or it is
   invalid: `Authenticate{Password, TwoFactorCode: totp(shared_secret)}` (or `ConfirmSteamGuard` with an operator
   endpoint), save the token, then `LogOn`.
5. **Supervisor per bot:** a single reconnect loop, exponential backoff with jitter, failure classification as in
   section 5, staggered start, a `NeedsAttention` state that stops retries, and `PlayingSessionStateEvent`
   handling.
6. **Never log off on shutdown.** Use `Disconnect()` only.
7. **Expose state** in `/bots`: `state`, `limited`, `token_expires_at`, `last_error` (EResult name), `steam_id`.
   Never expose tokens or secrets.
8. **Docker:** declare a named volume for `/data`, mount `secrets.yaml` read-only (or use Docker secrets), run
   NTP on the host, and run the container as non-root.

---

## 10. Verified vs Uncertain

### Verified (source or primary doc read)

- Pinned go-steam `LogOnDetails` has only Username/Password/AuthCode/TwoFactorCode/SentryFileHash/LoginKey/
  ShouldRememberPassword (`auth.go:22-39`), and its `CMsgClientLogon` has no `access_token` field
  (`client_server_login.pb.go:30-79`).
- Pinned go-steam has no IAuthenticationService protos (`protocol/protobuf/unified/` has only base, cloud,
  credentials, deviceauth, gamenotifications, offline, parental, partnerapps, player and publishedfile).
- Pinned go-steam CM transport is TCP-only, with static CM list `servers.go:13-34` and optional directory
  `steam_directory.go:17-58`.
- Pinned logon failure emits `LogOnFailedEvent` **and** `Disconnect()` (`auth.go:131-137`), so a naive handler
  schedules two reconnects per failure.
- Upstream go-steam (2026-06-27 and later) implements HTTPS IAuthenticationService login with Steam Guard code
  submission, `AccessToken` logon, `Authenticate()` returning the token, and `PlayingSessionStateEvent`. It does
  not implement guard-data persistence or token renewal.
- go-dota2 master requires go-steam `336e824f8540`.
- Field numbers and enums of `CMsgClientLogon` and `CAuthentication_*` per SteamDatabase/Protobufs.
- SteamKit's own samples label password/sentry login as legacy, and its new sample uses
  `AccessToken = RefreshToken`.
- Steam TOTP algorithm (HMAC-SHA1, 30s, 5 chars from a 26-char alphabet).
- Official Valve Limited User Account rules on the $5 threshold and friend invites.

### Verified live (2026-11-08, [09](09-live-probe.md))

- Password + TOTP `Authenticate` on go-steam master returns a SteamClient refresh token (about 210-day lifetime)
  with no prompt; token-only `LogOn` reaches the GC welcome in 1.7-7.4 s, across restarts.
- An account that never launched Dota 2 gets a GC session.

### Likely

- Password-only CM logon has been fully rejected since about 2026-06-23 (reported by contributors markus-wa and
  Peadar-Kenny in PR #11; the maintainer asked rather than confirmed, then shipped the rewrite. I did not test it
  myself.)
- Refresh tokens last about 200 days. LogOff can invalidate them and Disconnect does not (DepotDownloader PR #5).
  `InvalidPassword`, `AccessDenied`, `Expired`, `Revoked` or `InvalidSignature` on a token logon means the token is
  dead (wabbajack PR #3005).
- Upstream go-steam detects a dead socket after three missed heartbeat intervals and tears it down
  (`FatalErrorEvent` + disconnect). It does not reconnect; the caller must.
- `PlayingBlocked` followed by announcing a game leads to `LoggedInElsewhere`.

### Uncertain / not verified

- The exact date and Valve announcement for the final legacy-logon shutdown. A web search found none.
- Whether limited accounts can send **GC lobby invites** or be added to leagues.
- Whether the Dota GC requires the app-570 free licence entry before `ClientHello` (a never-launched account worked).
- Numeric per-IP or per-account login rate limits. Valve does not publish them.
- How to renew SteamClient refresh tokens from Go (`GenerateAccessTokenForApp` with `renewal_type=Allow` over
  WebAPI versus CM). node-steam-session says WebAPI renewal only works for MobileApp tokens as of 2025-04.
- Whether `guard_data` / `new_guard_data` works with go-steam's HTTP flow. Upstream does not send or return it.

---

## Sources

### Module source

- `go-steam@f167ff28a93a/auth.go:22-39,52-82,103-140,142-195`
- `go-steam@f167ff28a93a/auth_events.go:9-47`
- `go-steam@f167ff28a93a/client.go:63-83,89-91,137-169,257-270`
- `go-steam@f167ff28a93a/servers.go:13-45`, `steam_directory.go:17-76`, `connection.go:16-40`, `web.go:52-116`, `gamecoordinator.go:68-80`
- `go-steam@f167ff28a93a/protocol/protobuf/client_server_login.pb.go:30-79,422-444`
- `go-steam@f167ff28a93a/protocol/protobuf/client_server_2.pb.go:4524,7042,7067`
- `go-steam@f167ff28a93a/protocol/steamlang/enums.go:3473-3572 (EResult), 3930-3958 (EAccountFlags), 727 (messages.go: MsgClientLogon_CurrentProtocol)`
- `go-dota2@532cb1851217/client_session.go:14-38`

### Upstream repositories (read via GitHub API)

- paralin/go-steam master: `logon-details.go`, `auth.go`, `auth-token.go`, `auth_session.go`, `auth_events.go`, `playing-session-event.go`, `steam_directory.go`, `client.go`, `go.mod`, `README.md`: https://github.com/paralin/go-steam
- go-steam PR #11 (modern auth, discussion of the logon shutdown): https://github.com/paralin/go-steam/pull/11
- go-steam PR #12 (Steam Guard support fork): https://github.com/paralin/go-steam/pull/12
- go-steam PR #6 (CM list auto-update): https://github.com/paralin/go-steam/pull/6
- go-dota2 master go.mod and commits: https://github.com/paralin/go-dota2
- go-dota2 issue #21 (usage, rate-limit and ban comments): https://github.com/paralin/go-dota2/issues/21
- SteamKit samples `000_Authentication`, `004_LegacyLogin`, `005_LegacySteamGuard`: https://github.com/SteamRE/SteamKit/tree/master/Samples

### Web

- SteamKit issue #1125 "New Steam login flow": https://github.com/SteamRE/SteamKit/issues/1125
- SteamKit discussion #1258 (UpdateMachineAuth / sentry flow deprecated; says nothing about LogOff or tokens): https://github.com/SteamRE/SteamKit/discussions/1258
- SteamDatabase Protobufs, auth: https://github.com/SteamDatabase/Protobufs/blob/master/steam/steammessages_auth.steamclient.proto
- SteamDatabase Protobufs, clientserver_login: https://github.com/SteamDatabase/Protobufs/blob/master/steam/steammessages_clientserver_login.proto
- node-steam-user v4 to v5 migration guide: https://github.com/DoctorMcKay/node-steam-user/wiki/v4-%E2%86%92-v5-migration-guide
- node-steam-session README: https://github.com/DoctorMcKay/node-steam-session
- node-steamcommunity wiki (2023-10-17 login change): https://github.com/DoctorMcKay/node-steamcommunity/wiki/SteamCommunity
- gg-cr/go-steam fork docs: https://pkg.go.dev/github.com/gg-cr/go-steam
- lbt05/go-steam IAuthenticationService wrapper: https://pkg.go.dev/github.com/lbt05/go-steam/api/services/iauthenticationservice
- wabbajack PR #3005 (token InvalidPassword semantics): https://github.com/wabbajack-tools/wabbajack/pull/3005
- DepotDownloader PR #5 (avoid LogOff to keep tokens; LogOff led to `AccessDenied` on next token logon; fixes issue #4): https://github.com/Reino-Doce/DepotDownloader/pull/5
- steam-vent PR #2 (should_remember_password needed for token reuse): https://github.com/deadlock-api/steam-vent/pull/2
- steampy issue #380 (poll without refresh_token): https://github.com/bukson/steampy/issues/380
- steamguard-cli issues #193 / #319: https://github.com/dyc3/steamguard-cli/issues/193, https://github.com/dyc3/steamguard-cli/issues/319
- DepotDownloader login error guide: https://depotdownloader.com/troubleshooting/login-errors/
- SteamErrors InvalidPassword: https://steamerrors.com/5
- Steam TOTP implementations: https://github.com/doctype/steam/blob/master/totp.go, https://github.com/DoctorMcKay/php-steam-totp/blob/master/src/SteamTotp.php, https://pkg.go.dev/github.com/creachadair/otp, https://github.com/juthrbog/lazybw/issues/115, https://github.com/geekdada/steam-otp/blob/master/otp/utils.go
- Valve: Limited User Accounts: https://support.steampowered.com/kb/3330-IAGK-7663/limited-user-accounts

