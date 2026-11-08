# 10 - Headless bot accounts: creation, mobile authenticator without a phone, zero-touch login

Date: 2026-11-07. Scope: how to set up Steam bot accounts for `dota_lobby` that **never run the Steam client or
Dota 2** and log in with a Steam Guard mobile authenticator **with no human input at login time**. It builds on
[04](04-steam-auth-headless.md) (§4 TOTP, §7.1 limited accounts, §7.2 app 570) and
[gap-01](gap-01-upstream-master-build-auth.md) (go-steam master auth surface) and does not repeat them.
Live results from 2026-11-08 are in [09 - Live probe](09-live-probe.md).

Labels: **VERIFIED** = read in source, reproduced locally, or read on a primary page. **REPORTED** = third party
(issue, forum, library docs). **UNCERTAIN** = conflicting or no evidence; needs a live account.

`go-steam@336e824f8540/...` paths are relative to the root of `github.com/paralin/go-steam v0.0.0-20260922075330-336e824f8540` (upstream master).
`SGC` = [dyc3/steamguard-cli](https://github.com/dyc3/steamguard-cli) `master` (last push 2026-09-22, release v0.18.4 of 2026-07-16).

---

## TL;DR

1. **The human part is unavoidable and happens once per bot, about 15-20 minutes:** create the account in a browser
   (email, hCaptcha, "I am 13+" + SSA checkbox, click the email link), optionally add $5, then run
   `steamguard setup` on the operator's machine. That run needs the password, the **email** Steam Guard code of
   the fresh account, and the email (or SMS) activation code Steam sends to link the authenticator. After that,
   nothing needs a human until the authenticator is lost.
2. **No phone and no Steam Mobile app are needed to add an authenticator, as far as current sources show.**
   steamguard-cli speaks the same `ITwoFactorService/AddAuthenticator` + `FinalizeAddAuthenticator` API as the app
   and handles both SMS (`confirm_type` 1) and email (`confirm_type` 3) activation. Its own code says *"As of Dec 12,
   2023, Steam no longer appears to require a phone number to add an authenticator"*. VERIFIED (source); one
   2025 report of `InvalidState` failures on phone-less fresh accounts was closed as unreproducible (UNCERTAIN). A
   phone number is the fallback; one number may sit on many accounts, but bans then spread across them.
3. **Login with zero interaction is implemented in go-steam master.** `Auth.Authenticate` with `TwoFactorCode`
   submits the TOTP code as `DeviceCode` inside the same auth session, whatever order Steam lists the allowed
   confirmations in, and then polls for the refresh token. No callback fires and no approval is needed. VERIFIED in
   source (`go-steam@336e824f8540/auth_session.go:113-149`) and VERIFIED live (Q1): a refresh token in 0.7 s with no
   prompt ([09](09-live-probe.md) §2). Steam tolerates **60 s** of clock
   skew (`skew_tolerance_seconds` from a live `ITwoFactorService/QueryTime` call, VERIFIED 2026-11-07).
   identity_secret / auto-approval is **not** needed.
4. **Never running Dota is probably fine.** Dota 2 ships a free-on-demand package, sub **49307 "Dota 2 - Free
   On-Demand"** (app 570), which is VERIFIED through the store API. go-steam has the
   `ClientRequestFreeLicense` (5572/5573) EMsgs and protos but no helper. A ~100-line helper (plus a
   `QueryTime` skew helper) **compiles against go-steam master** (§4.3). VERIFIED live (Q21): an account that
   never launched Dota 2 got a GC welcome and hosted league lobbies ([09](09-live-probe.md) §3). Its licence list
   was not read, so whether the GC checks it is still open; one 2026 report shows Dota running on accounts whose
   licence list lacks it.
5. **ToS risk is real but not new.** The Steam Subscriber Agreement (revision **2026-09-10**) §4.C bans
   "scripts, bots, macros, or other non-human-controlled systems" interacting with Steam *"in any manner"*, naming
   *"Automating the Steam account creation process"*, and §2.G(ii) bans emulating Valve's protocols without written
   consent. Every go-steam / node-dota2 lobby bot is in that grey zone. Keep the footprint small: few accounts, few
   logins, never automate account creation, no trading or market, and no gameplay.

---

## 1. Account creation

### 1.1 What a human must do (VERIFIED on `store.steampowered.com/join/`, fetched 2026-11-07)

| Step | Evidence |
|---|---|
| Enter an email address and country, then solve an **hCaptcha** | The join page loads `hcaptcha` (`BCaptchaCodeReady() { return typeof hcaptcha !== 'undefined' ...}`) and sends a `captchagid` |
| Tick **"I am 13 years of age or older and agree to the terms of the Steam Subscriber Agreement and the Valve Privacy Policy"** | Join page text |
| Click the verification link in the email (*"Check for an email from Steam to complete your account setup."*) | `email_verification_*` dialog on the join page |
| Choose the account name and password | Join flow |

**Automatable? No, and it must not be.** SSA §4.C (revision 2026-09-10) names *"Automating the Steam account
creation process"* as prohibited Automation ([SSA](https://store.steampowered.com/subscriber_agreement/), VERIFIED).
A third-party summary dates the explicit list to April 2026
([conductatlas](https://conductatlas.com/platform/steam/steam-subscriber-agreement/), REPORTED). The hCaptcha is
there to stop exactly this. Treat account creation as a manual step.

There is **no age gate beyond the 13+ checkbox** for Dota. Dota 2 has `required_age: 0` in
`store.steampowered.com/api/appdetails?appids=570` (VERIFIED).

### 1.2 Limits per email / per IP

- **Email:** several accounts may share one email. Steam warns that the address is already in use and offers
  "Continue". The join page has a `use_existing_btn`. Forum posts quote Valve's *"there is no limit to the number of
  Steam accounts that can be used with a single email address"*
  ([PC Gamer forum](https://forums.pcgamer.com/threads/hey-guys-is-this-true-you-can-make-multiple-steam-accounts-but-using-only-one-gmail-account-by-using-aliases.149969/),
  [Steam forum 2025](https://steamcommunity.com/discussions/forum/1/727997490786235987/)). REPORTED. Several
  third-party guides claim the opposite, so this is UNCERTAIN until we create the second bot.
  **Recommendation:** give each bot its own address (`ops+bot-a@domain` plus-address, or a catch-all domain) anyway.
  Steam Guard email codes, the authenticator activation code and recovery mail are then never ambiguous between bots.
- **IP:** Valve publishes no per-IP creation cap. A browser-fingerprinting vendor says Steam may ask for extra
  email/SMS confirmation when many accounts are created from one IP
  ([Octo Browser](https://blog.octobrowser.net/how-to-create-multiple-steam-accounts)). REPORTED, low trust.
  Creating a handful of accounts by hand, spread over days, is not something to worry about.

### 1.3 Limited vs non-limited

New accounts are **limited** until US$5 has been spent on the store. Restrictions (04 §7.1): no outgoing friend
invites, no Market, no Web API key, and rate-limited chat. go-steam already surfaces the state in two places.
`CMsgClientIsLimitedAccount{bis_limited_account, bis_community_banned, bis_locked_account,
bis_limited_account_allowed_to_invite_friends}` (EMsg 5430, `go-steam@336e824f8540/protocol/protobuf/client_server.proto:194-198`,
`go-steam@336e824f8540/protocol/steamlang/enums.go:1405`) is pushed after logon, and `EAccountFlags_LimitedUser = 4096`
(`enums.go:4235`) appears in `LoggedOnEvent.AccountFlags` (`go-steam@336e824f8540/auth.go:118`). VERIFIED. §4.3 logs both.

Implications for lobby hosting:

| Need | Limited OK? | Confidence |
|---|---|---|
| Log in, GC session, create a lobby | likely yes | REPORTED (04 §7.1) |
| GC lobby invite (`InviteLobbyMember`) | unknown | UNCERTAIN (Q22) |
| Lobby chat (rules, "gl hf", results) | Dota limits chat for limited accounts ([teamsmurf](https://teamsmurf.com/limited-steam-accounts-dota-2/), [05](05-prior-art.md) §3) | REPORTED |
| League admin on dota2.com/league | unknown | UNCERTAIN (Q13) |
| Account recovery via Steam Support | a card purchase made on the account is the strongest proof of ownership | REPORTED |

**Cheapest legitimate way to lift the limit:** add US$5 (or the local equivalent) to the bot's own **Steam
Wallet** with the operator's card. Alternatively, redeem a $5 Steam Wallet **card/code** on the bot account. The
amount counts when the code is redeemed, so the funds can stay unspent. Receiving a Steam *gift* or a *digital gift card* from a friend
does **not** count until it is spent, and retail/promo keys never count
([Steam forum quoting Valve's FAQ](https://steamcommunity.com/discussions/forum/1/2941371547495172568/),
[GPDS](https://gpdsgameshop.com/blog/steam-account-still-limited-after-a-gift-check-what-counts-toward-the-5-requirement)).
REPORTED. Valve's own FAQ (`help.steampowered.com/en/faqs/view/71D3-35C2-AD96-AA3A`) renders client-side and could
not be read here. Refunds that bring the total back below $5 re-limit the account (04 §7.1). The wallet balance
is not wasted: it stays on the bot and can later pay for a league ticket or a Battle Pass for the league account
if ever needed. **Recommendation:** fund every production bot. A limited test bot is only useful to answer Q22.

---

## 2. Adding a mobile authenticator without a phone app

### 2.1 Tools (status November 2026)

| Tool | Status | Use for us |
|---|---|---|
| **steamguard-cli** (Rust; `cargo install steamguard-cli`, AUR `steamguard-cli`) | Maintained. Releases 2026-06 to 2026-07, last push 2026-09-22. Commands: `setup, code, confirm, approve, status, remove, import, encrypt, decrypt, qr, qr-login, list` (SGC `src/commands/`) | **Recommended.** Its `setup` command does the whole link from a terminal |
| Steam Desktop Authenticator (SDA, Jessecar96, C#/SteamKit-era) | Unmaintained. The steamguard-cli maintainer, 2026-07-10: *"SDA does not work anymore. It's unmaintained. It uses APIs that no longer exist."* ([SGC#508](https://github.com/dyc3/steamguard-cli/issues/508)) | Only for reading old maFiles; the format is compatible |
| node `steam-totp` / `steamcommunity.enableTwoFactor` / `steam-session` (DoctorMcKay) | Maintained libraries, not a turnkey CLI | Possible, but we would have to write the linker script |
| Python `steam` (ValvePython) `SteamAuthenticator` | Docs still say `add()` "will receive a SMS code" ([docs](https://steam.readthedocs.io/en/stable/api/steam.guard.html)) | Outdated; avoid |

### 2.2 What `steamguard setup` does (VERIFIED in SGC source, `master`)

1. Prompts for the username and logs in with password plus the account's **current** Steam Guard (for a fresh account,
   an **email code** the operator reads from the inbox) (`src/commands/setup.rs:30-41`).
2. `AccountLinker::link()` calls `ITwoFactorService/AddAuthenticator` with `authenticator_type=1`,
   `sms_phone_id="1"`, a random `device_identifier` and **`version=2`**
   (`steamguard/src/accountlinker.rs:45-60`). The comment says version 2 allows `DeviceConfirmation` as a login
   guard type. The response carries `shared_secret`, `identity_secret`, `revocation_code`, `serial_number`, `uri`,
   `token_gid`, `secret_1`, `server_time`, `phone_number_hint` and `confirm_type` (`:72-95`).
3. **It saves the maFile before finalizing** and prints the revocation code (`setup.rs:173-190`).
4. It asks for the activation code: `confirm_type` **1 = SMS**, **3 = Email** (`accountlinker.rs:279-283`,
   `setup.rs:192-205`). With no phone on the account, Steam sends it by **email**.
5. `FinalizeAddAuthenticator` with a TOTP code generated from `shared_secret` at Steam's `server_time` plus the
   activation code. It retries up to 30 times while Steam answers `want_more` (`setup.rs:212-228`,
   `accountlinker.rs:100-133`). Then `QueryStatus` checks `state != 0` (`setup.rs:232-245`).

Error mapping when linking (`accountlinker.rs:313-327`):

- `NoVerifiedPhone` means "add a phone".
- `DuplicateRequest` means "an authenticator is already present".
- `RateLimitExceeded` means *"Wait at least a couple hours"*.
- `Fail` means generic. The tool suggests adding a phone at `https://store.steampowered.com/phone/add`.

### 2.3 Is a phone number required in 2026?

- **Probably not.** SGC `setup.rs:52`: *"As of Dec 12, 2023, Steam no longer appears to require a phone number to
  add an authenticator."* The email `confirm_type` 3 path exists for exactly this case. VERIFIED (code and comment). A
  2026 article says Valve offers an "I don't have access to a phone number" option in the app flow
  ([windowsforum](https://windowsforum.com/news/how-to-set-up-steam-guard-mobile-authenticator-recovery-codes-trade-holds-and-windows-security.449139/)).
  REPORTED.
- **Counter-evidence:** [SGC#443](https://github.com/dyc3/steamguard-cli/issues/443) (2025-02) reports
  `InvalidState` on about 99% of setups for fresh, never-phoned accounts. The reporter said SDA and the Python module
  failed too. The maintainer could not reproduce it and closed the issue. **UNCERTAIN.** If it happens, add a phone and
  retry.
- **Sharing one phone number:** Valve's FAQ (quoted in forums) says *"You may use the same phone number on multiple
  accounts"*, but accounts sharing a number *"may be treated as the same identity"*. A VAC or game ban on one then
  bans all of them and suspends the number for 3 months
  ([Steam forum](https://steamcommunity.com/discussions/forum/1/1489992080501353141/)). REPORTED, sources from
  2016-2021. Adding the same number to many accounts quickly hits *"too many recent attempts to add this phone number
  ... wait 1 week"* (one user, after about 30 accounts). REPORTED. For a few bots that is fine. Do **not** use the
  number of a person's own main account (ban coupling, and Dota ranked wants a *unique* number per account,
  [Dota wiki](https://dota2.fandom.com/wiki/Matchmaking)).

### 2.4 maFile fields and where each goes

steamguard-cli writes `<maFiles>/<account_name>.maFile`, by default under `~/.config/steamguard-cli/maFiles/`,
plus a `manifest.json` (SGC `src/accountmanager.rs:164`, README). Keys, from the SGC fixture
`src/fixtures/maFiles/compat/1-account/1234.maFile` and `steamguard/src/lib.rs:36-52`:
`account_name, steam_id (SDA: Session.SteamID), serial_number, revocation_code, shared_secret, token_gid,
identity_secret, uri, device_id, secret_1, server_time, status, fully_enrolled, Session/tokens`.

| Field | Needed by | Store |
|---|---|---|
| `shared_secret` (base64) | **Login** (TOTP) | `secrets/bots/<bot>.sops.yaml` → `STEAM_SHARED_SECRET` (existing key) |
| `identity_secret` | Only mobile confirmations (trades, market, some account changes). **Not** login | Separate file `secrets/guard/<bot>.sops.yaml` → `STEAM_IDENTITY_SECRET`. Never loaded by the service |
| `revocation_code` (`R12345`) | Removing the authenticator when `shared_secret` is lost | `secrets/guard/<bot>.sops.yaml` → `STEAM_REVOCATION_CODE` **and** an offline copy (password manager or paper). If the age key is lost, the sops copy is lost with it |
| `serial_number`, `device_id`, `steam_id` | Diagnostics, transfer, support tickets | `secrets/guard/<bot>.sops.yaml` |
| `uri` (`otpauth://...`) | Same secret as `shared_secret`, base32 | Not stored separately |
| `Session` / `tokens` | steamguard-cli's own web session | Not stored; it is steamguard-cli state |

`secrets/guard/...` matches the existing `.sops.yaml` rule `^secrets/.*\.sops\.ya?ml$` and the git-ignored
`secrets/` (`.gitignore:44`, checked with `git check-ignore`). It stays out of `just secrets list` and
`just probe ...`, which glob only `secrets/bots/*.sops.yaml` (`just/secrets.just:55`, `just/probe.just:25`).
`sops exec-env` therefore never puts `identity_secret` or `revocation_code` into the probe's environment.

After import, keep the steamguard-cli maFiles **encrypted** (`steamguard encrypt`) as a second backup, or delete
them. Never put them in the repo; `maFiles/` is not covered by `.gitignore`.

### 2.5 Restrictions after adding the authenticator

| Restriction | Source | Affects lobbies? |
|---|---|---|
| Trades and Market listings are **held up to 15 days** unless the authenticator has been active for 7 days. If the account had no Steam Guard at all before, trading and Market are **blocked for 15 days** | Valve FAQ wording quoted by [Steam Market group](https://steamcommunity.com/groups/community_market/discussions/0/2592234299548710456/), [SteamVaults](https://steamvaults.org/blog/why-steam-trade-hold-is-15-days) (REPORTED) | **No.** Bots never trade |
| Removing the authenticator with the revocation code: 15-day trade/Market restriction. Transfer to a new device via SMS: 2 days | SGC `setup.rs:61-62` (VERIFIED text) | No |
| New-device trade cooldown (email Guard only) | REPORTED | No |

None of these touch CM login, the GC, lobby creation, invites or league admin. VERIFIED by absence: every listed
restriction concerns trading or Market only. A freshly linked bot can host lobbies the same day. **UNCERTAIN:**
whether Valve adds a non-trade cooldown on brand-new accounts. Nothing was found.

---

## 3. Logging in with no human input

### 3.1 go-steam master path (VERIFIED in source)

`Auth.Authenticate(ctx, &LogOnDetails{Username, Password, TwoFactorCode, DeviceFriendlyName, ShouldRememberPassword:
true})` (`go-steam@336e824f8540/auth-token.go:12-17`) runs these steps:

1. `GetPasswordRSAPublicKey`, then `BeginAuthSessionViaCredentials` with platform `SteamClient`, persistence
   `Persistent`, `WebsiteId "Client"` (`go-steam@336e824f8540/auth_session.go:38-76`).
2. `submitAvailableSteamGuardCode` (`:113-149`) walks **all** `allowed_confirmations`. `DeviceConfirmation` and
   `EmailConfirmation` only set a flag (`:128-130`). When it reaches `DeviceCode` and `TwoFactorCode != ""`, it
   calls `UpdateAuthSessionWithSteamGuardCode{client_id, steamid, code, code_type=DeviceCode}` and returns
   (`:124-127`, `:175-189`). **So the version-2 authenticator from steamguard-cli does not matter.** Even if Steam
   lists `DeviceConfirmation` first, the code path is chosen. `ConfirmSteamGuard` is never called when a TOTP code
   was supplied and `DeviceCode` is allowed.
3. Poll `PollAuthSessionStatus` up to 10 times at Steam's `interval` (`:82-110`) and return the `refresh_token`.

The probe already wires this up: `tools/probe/auth.go:136-180` generates the code from `STEAM_SHARED_SECRET`
(`steamTOTP`, `:22-41`, the same algorithm as 04 §4.2). It sets `ShouldRememberPassword: true` and a
`ConfirmSteamGuard` that errors out (so the run fails loudly instead of waiting for a human). `just probe auth`
pipes the token into sops (`just/probe.just:22-29`). **Zero interaction is confirmed by the code; it has not yet been
run live** (probe Q1).

Gaps in go-steam to handle in our code:

| Gap | Evidence | Handling |
|---|---|---|
| No time-skew correction | No `QueryTime` anywhere in go-steam master (grep) | Compute the code at `time.Now().Add(offset)`, with `offset` from `ITwoFactorService/QueryTime` (§4.3 helper, VERIFIED live: offset −0.42 s on the test host). Steam returned `skew_tolerance_seconds: "60"`, so NTP alone is normally enough |
| A wrong or reused code fails the whole session | A non-1 `x-eresult` → `AuthSessionStateDenied` (`go-steam@336e824f8540/auth_session.go:229-235`); no retry | On `eresult 88` (TwoFactorCodeMismatch), wait for the next 30 s window and call `Authenticate` again (a new session). Never submit the same code twice; stagger bots that log in together |
| Same device name for every bot | Default `"go-steam"` (`:53-56`) | Set `DeviceFriendlyName: "dota_lobby <bot>"` so the account's "authorized devices" page tells sessions apart |

### 3.2 Will "new login" email confirmations still appear?

- **Blocking confirmations: no.** With a mobile authenticator, the allowed guard types are `DeviceCode` and/or
  `DeviceConfirmation`. Email types appear only on email-Guard accounts (04 §4.1). Submitting `DeviceCode` completes
  the session (§3.1). VERIFIED for the code path. That Steam never adds an `EmailConfirmation` requirement for a new
  IP on top of a valid device code is LIKELY; no report of that was found.
- **Notification emails** ("new sign-in" or similar): UNCERTAIN whether Steam sends them. They would be informational
  only and need no action. Probe step: check the bot's inbox after `just probe auth`.
- **A first login from a datacenter IP may re-trigger Steam Guard** (REPORTED by existing league bots). With TOTP this is handled
  automatically by re-running `Authenticate`. To keep the token-only path stable, mint the refresh token from the host
  (egress IP) that will use it, or let the service re-authenticate on its first start (UNCERTAIN whether refresh
  tokens are IP-bound; gap-01 §7).

### 3.3 Is auto-approval with `identity_secret` needed?

**No.** `DeviceConfirmation` ("approve in the app") is only an *alternative* Steam offers. We never pick it. For
completeness, SGC 0.17.0+ has `steamguard approve`, which finds pending sessions through
`IAuthenticationService/GetAuthSessionsForAccount` and approves them
([SGC#447](https://github.com/dyc3/steamguard-cli/issues/447), maintainer 2025-03-19/21, VERIFIED quote). That is
how a CI box with `steamcmd` copes when it insists on app approval. Our login does not need it. Do not load
`identity_secret` into the service.

Side note from the same issue: authenticators created by SGC before 0.17 are `version 1`, and app-created ones are
`version 2` (`steamguard status` shows it). Current SGC creates version 2 (`accountlinker.rs:57-60`). Irrelevant for
`DeviceCode` logins.

---

## 4. Never running Dota 2

### 4.1 Licence for app 570

- **Packages (VERIFIED, `store.steampowered.com/api/appdetails?appids=570` and `api/packagedetails`, 2026-11-07):**
  Dota 2 lists packages `49307` = **"Dota 2 - Free On-Demand"** (apps: 570), `197846` = "Dota 2 - Commercial
  License" (`is_free_license: true`, the store's default purchase option), and `330209` (no public details).
- node-steam-user documents that older F2P games live in the "Steam" package (sub 0) granted to every account. Newer
  ones use free-on-demand packages requested **by appid** through `ClientRequestFreeLicense`, rate-limited to about
  50 apps per hour ([node-steam-user README](https://github.com/DoctorMcKay/node-steam-user),
  [McKay forum](https://dev.doctormckay.com/topic/1244-requestfreelicense-still-works/)). REPORTED. Whether 570
  is still also in sub 0 is UNCERTAIN.
- **2026 reports** ([Dota2-Gameplay#32961](https://github.com/ValveSoftware/Dota2-Gameplay/issues/32961),
  [#32983](https://github.com/ValveSoftware/Dota2-Gameplay/issues/32983), June-Sept 2026). Accounts, new ones
  included, show no Dota licence although *"the game ... launches fine"*. The Steam console `app_license_request 570`
  *"hangs indefinitely"*. Adding sub 49307 through the store fixed the profile display (2026-09-02, 2026-09-05).
  REPORTED. Takeaways: (a) the GC very likely does not check the licence list, because those clients played; (b) the
  CM free-licence request for 570 **may hang or be ignored**, so our helper needs a timeout; (c) the store endpoint
  for sub 49307 is the fallback.

### 4.2 GC welcome, Dota profile, EULA, new-account restrictions

- **GC welcome without ever launching:** go-dota2 sends `ClientHello` after `SetPlaying(570)`. The Dota game account
  (`CSODOTAGameAccountClient` in the SOCache) is created server-side on first contact. **VERIFIED live (Q21):** the never-launched test bot got a
  welcome with `CSODOTAGameAccountClient` in the SOCache ([09](09-live-probe.md) §3).
- **EULA:** Dota 2 has no separate third-party EULA beyond the SSA, which was accepted at signup. The SSA can change
  (current revision 2026-09-10). If Steam ever needs a new acceptance, the auth flow says so:
  `PollAuthSessionStatus.agreement_session_url` / `UpdateAuthSessionWithSteamGuardCode.agreement_session_url` →
  `AuthSessionStateAgreement` (`go-steam@336e824f8540/auth_session.go:100-102`, `:185-187`). VERIFIED code path. **Handling:** alert
  the operator, who logs in once in a browser and accepts.
- **Dota restrictions for new accounts** (all REPORTED, third-party; none mention practice lobbies):
  - Ranked needs a unique phone number and playtime ([Dota wiki](https://dota2.fandom.com/wiki/Matchmaking)). Bots
    never queue ranked.
  - Behaviour and communication score gate voice/text chat below 8000/6000 for new accounts, whose starting score
    is reported as 8000-10000 ([teamsmurf](https://teamsmurf.com/behavior-score-guide/),
    [hawk.live](https://hawk.live/posts/player-behavior-system-reports-dota-2)). If the bot's lobby chat messages
    vanish, this is a suspect. The fresh test bot's lobby chat replies were delivered ([09](09-live-probe.md) §9).
  - Limited accounts have restricted chat (§1.3).
  - **Lobby creation with zero playtime:** works. VERIFIED live: the test bot, with no playtime, created league lobbies.

### 4.3 Go sketch: free licence, account-state capture, Steam time offset (VERIFIED compiles)

The helper below builds and passes `go vet` against go-steam master `v0.0.0-20260922075330-336e824f8540`.
Running it called `QueryTime` live and printed `steam time offset: -420ms`. Building blocks used
(all VERIFIED):

- `RegisterPacketHandler` (`go-steam@336e824f8540/client.go:117-121`, `PacketHandler` `:70-72`)
- `GetNextJobId` (`:124-126`), `Write` (`:273`)
- `protocol.NewClientMsgProtobuf` (`go-steam@336e824f8540/protocol/msg.go:38`)
- `Packet.ReadProtoMsg` (`go-steam@336e824f8540/protocol/packet.go:80`)
- `EMsg_ClientRequestFreeLicense = 5572` / `...Response = 5573`, `EMsg_ClientLicenseList = 780`,
  `EMsg_ClientIsLimitedAccount = 5430` (`go-steam@336e824f8540/protocol/steamlang/enums.go:1541-1542, 341, 1405`)
- `CMsgClientRequestFreeLicense{appids=2}` / `Response{eresult=1 (default 2), granted_packageids=2,
  granted_appids=3}` (`go-steam@336e824f8540/protocol/protobuf/client_server_2.proto:215-223`, Go `client_server_2.pb.go:1081-1121`)

```go
// Register BEFORE Connect so the post-logon pushes (780 licence list, 5430 limited flags) are seen.
type accountProbe struct {
	sc       *steam.Client
	mu       sync.Mutex
	licenses []uint32                        // package IDs from ClientLicenseList
	limited  *pb.CMsgClientIsLimitedAccount  // pushed after logon
	pending  map[protocol.JobId]chan *pb.CMsgClientRequestFreeLicenseResponse
	freeResp chan *pb.CMsgClientRequestFreeLicenseResponse // reply without echoed job id
}

func (p *accountProbe) HandlePacket(pkt *protocol.Packet) {
	switch pkt.EMsg {
	case steamlang.EMsg_ClientLicenseList:
		body := &pb.CMsgClientLicenseList{}
		pkt.ReadProtoMsg(body)
		// collect l.GetPackageId() for each body.GetLicenses() into p.licenses
	case steamlang.EMsg_ClientIsLimitedAccount:
		body := &pb.CMsgClientIsLimitedAccount{}
		pkt.ReadProtoMsg(body)
		// p.limited = body
	case steamlang.EMsg_ClientRequestFreeLicenseResponse:
		body := &pb.CMsgClientRequestFreeLicenseResponse{}
		pkt.ReadProtoMsg(body)
		// deliver to p.pending[pkt.TargetJobId], else p.freeResp
	}
}

// After LoggedOnEvent. Use a short ctx (e.g. 20 s): reports say the 570 request can hang.
func (p *accountProbe) RequestFreeLicense(ctx context.Context, appIDs ...uint32) (*pb.CMsgClientRequestFreeLicenseResponse, error) {
	msg := protocol.NewClientMsgProtobuf(steamlang.EMsg_ClientRequestFreeLicense,
		&pb.CMsgClientRequestFreeLicense{Appids: appIDs})
	job := p.sc.GetNextJobId()
	msg.SetSourceJobId(job)
	// register p.pending[job], p.sc.Write(msg), select on ctx / pending / freeResp,
	// and treat steamlang.EResult(resp.GetEresult()) != EResult_OK as an error
	...
}

// Steam server time minus local time; add it to time.Now() before generating a TOTP code.
func steamTimeOffset(ctx context.Context) (time.Duration, error) {
	// POST https://api.steampowered.com/ITwoFactorService/QueryTime/v0001 (empty body)
	// -> {"response":{"server_time":"1794053462","skew_tolerance_seconds":"60",...}}
	...
}
```

Expected results (UNCERTAIN until run):

- First grant: `eresult 1` with `granted_appids` containing 570 (or `granted_packageids` containing 49307).
- Already owned: `eresult 1` with empty lists (McKay: *"it won't do anything if you already own those apps"*).
- Not free-on-demand or ignored: a timeout.

Afterwards, `ClientLicenseList` should contain 49307. A proposed `probe license` subcommand would print
`licenses ∋ {0, 49307, 197846}`, `bis_limited_account`, `AccountFlags & LimitedUser`, then request 570, then
re-read the licence list. Q21 showed the GC welcomes a never-launched account, so skip this step unless a bot's welcome fails.

**Store fallback (one-time, human, in the browser session from account creation):** the store endpoint
`POST https://store.steampowered.com/checkout/addfreelicense` with form fields `action=add_to_cart`, `subid=49307`
and `sessionid=<the store sessionid cookie>` adds the package. The `sessionid` is a CSRF token that must match the
store cookie ([gist](https://gist.github.com/Pelirrojo/ee9c2c5dfda8ae41aa3950be856e44f2),
[node-steamstore `addFreeLicense`](https://github.com/DoctorMcKay/node-steamstore)). REPORTED. Doing this headlessly
would need a store web session from our client token. go-steam's `Web.LogOn` still uses the legacy
`ISteamUserAuth/AuthenticateUser` nonce flow (`go-steam@336e824f8540/web.go:69-108`), which is unlikely to work with new tokens, so
there is no headless store path in go-steam today.

---

## 5. Operational

### 5.1 ToS and ban risk

- SSA (revision 2026-09-10), VERIFIED on the page:
  - §4.C "Automation" bans *"scripts, bots, macros, or other non-human-controlled systems ... to interact with
    Content and Services on Steam in any manner"*. Examples include account-creation automation, *"Earning rewards
    or progress without genuine user input"* and faking gameplay statistics. Enforcement uses *"automated detection
    methods and human review"*, and accounts can be terminated without notice (§4.D).
  - §2.G(ii) bans emulating Valve's network protocols *"without the prior written consent of Valve"*.
  - §1.C makes accounts personal and non-transferable.
- **Reading:** a headless GC lobby bot is technically covered by these clauses. Valve has tolerated such bots for
  years: inhouse/league bots (05), the steamguard-cli maintainer's audience, and paralin's *"if you're just doing
  normal non abusive things at a reasonable rate it shouldn't be a problem"* (04 §5.3). INFERRED risk profile:
  - low for a handful of dedicated accounts that only host lobbies
  - higher for anything that looks like farming: drops, playtime, Battle Pass, trading, market, many fresh accounts
- Mitigations:
  - Create accounts by hand.
  - Fund them ($5 makes them look like customers and gives proof of ownership).
  - Never idle for rewards.
  - Never trade.
  - Keep `SetPlaying(570)` only while a lobby is needed, if cheap to do so.
  - Do not share a phone number with a personal account (§2.3).
  - Name the accounts honestly (e.g. "<league> lobby bot").
- A ban on a bot cannot spread to the operator's personal account unless they share a phone number (§2.3). REPORTED.

### 5.2 Rate limits for N bots from one IP

| Operation | Known limit | Source |
|---|---|---|
| Credential auth (`Authenticate`) | Unpublished. Failures show up as `RateLimitExceeded` (84), `AccountLoginDeniedThrottle` (87) or `InvalidPassword` | 04 §5.3 |
| Authenticator linking | `RateLimitExceeded`: wait hours | SGC `accountlinker.rs:302-303` |
| Adding a phone number | "wait 1 week" after many adds | §2.3 |
| Mobile confirmations (`/mobileconf/*`) | Since about 2026-07-09, 429/"Rate limit exceeded" unless browser-like headers (`Accept-Language`, a Chrome UA) are sent | [SGC#509](https://github.com/dyc3/steamguard-cli/issues/509), [McKay forum](https://dev.doctormckay.com/topic/5946-new-request-limits-429-error/) (REPORTED). Not used by us |
| `ClientRequestFreeLicense` | About 50 apps per hour per account | node-steam-user README (REPORTED) |

Practice:

- Bots log in once and stay connected.
- Restarts use the refresh token, with no credential exchange.
- Credential auths are staggered by at least 30 s per bot. That also gives each bot its own TOTP window if they ever
  share an authenticator, which they should not.
- Back off exponentially on 84/87.

### 5.3 Keeping accounts alive

- **Tokens:** the refresh token lasts about 200 days (04 §2.2). Renew over the CM (gap-01 §3.1 sketch, `just probe
  renew`). If that fails, re-run `Authenticate` with password + TOTP when `exp` is less than 30 days away, or on a
  dead-token EResult. All headless.
- **Activity:** no Steam inactivity deletion policy was found. UNCERTAIN. A bot that logs in at least monthly
  (`just probe run <bot> login`, or the service's normal operation) shows activity anyway.
- **Password changes** revoke tokens (REPORTED, 04). Change passwords only deliberately, then re-run `just probe auth`.
- **SSA updates:** watch for `AuthSessionStateAgreement` (§4.2).

### 5.4 Recovery

| Lost | Recovery | Human? |
|---|---|---|
| Refresh token | `just probe auth <bot>` (password + TOTP) | No |
| Password (known to be compromised) | Change it in a browser (requires a TOTP code: `steamguard -u <acct> code`, or compute it from `shared_secret`), update `STEAM_PASS`, re-auth | Yes, once |
| `shared_secret` (sops/age key lost) **with** `revocation_code` | `steamguard remove` / Steam's "remove authenticator" with the R-code (falls back to email Guard), then `steamguard setup` again. 15-day trade restriction, irrelevant | Yes |
| Authenticator, with a phone on the account | "Move authenticator" via SMS (`steamguard setup` → transfer, 2-day restriction) (SGC `setup.rs:61-62, 264-305`) | Yes |
| Both secret and R-code, no phone | Steam Support with proof of ownership (the card used for the $5 wallet funding) | Yes, slow |

**Back up the age key** (`~/.config/sops/age/keys.txt`, per `just/secrets.just:1-3`) **and** keep the revocation codes
offline. They are independent failure domains.

---

## 6. RUNBOOK: one bot from scratch

Notation: `<bot>` = our short name (e.g. `bot-a`, the sops file name). `<acct>` = the Steam account name.
Never paste secrets into chat, tickets or shell history. Use the `$EDITOR` that `sops edit` opens.

### Part A: one-time human steps (per bot, about 15-20 min plus any $5 payment)

1. **Prepare.** Pick `<bot>` and `<acct>`. Generate a 24+ character random password in your password manager. Pick a
   unique mailbox (`ops+<bot>@yourdomain`). Install steamguard-cli once per machine:
   `cargo install steamguard-cli` or the AUR `steamguard-cli`. Make sure the host clock is NTP-synced (`timedatectl`).
2. **Create the account** in a normal browser at `https://store.steampowered.com/join/`: email, country, hCaptcha,
   tick the 13+/SSA box, click the link in the verification email, then set `<acct>` and the password. Do not script
   this (SSA §4.C).
3. **(Production bots) Lift the limit:** log in to the store as `<acct>` and add US$5 to the Steam Wallet with your
   card, or redeem a $5 wallet code. Skip this only for a deliberately limited test bot (Q22).
4. **Link the authenticator, no phone:** `steamguard setup`.
   - Enter `<acct>`, then the password.
   - Enter the **email** Steam Guard code sent to the mailbox (fresh accounts have email Guard).
   - When it prints the **revocation code**, store it in the password manager **now** (it pauses for this).
   - Enter the activation code Steam sends by **email** (or SMS if the account has a phone). steamguard-cli then
     finalizes on its own.
   - If it fails with "add a phone number" / `GenericFailure` / `InvalidState`: add a phone at
     `https://store.steampowered.com/phone/add` (not your personal account's number) and rerun. On
     `RateLimitExceeded`, wait a few hours.
5. **Check the link:** `steamguard -u <acct> status` (state ≠ 0, version 2). `steamguard -u <acct> code` prints a
   5-character code. Optional: log in once at `store.steampowered.com` with that code to confirm.
6. **Secure the maFile:** run `steamguard encrypt` (sets a passkey on `~/.config/steamguard-cli/maFiles/`). Keep it as
   a backup, or delete it after step 8. Never copy maFiles into the repo.

### Part B: secrets, then the fully automated part

7. **Create the sops file:** `just secrets new-bot <bot>`. In the editor, set `STEAM_USER: "<acct>"` and
   `STEAM_PASS: "<password>"`. Leave `STEAM_SHARED_SECRET` empty or paste it (see step 8). Save and quit.
8. **Import the shared secret** without it touching disk in plaintext. Decrypt the maFile to stdout first if it is
   encrypted (`steamguard decrypt` decrypts in place; re-encrypt afterwards):

   ```bash
   jq -c .shared_secret ~/.config/steamguard-cli/maFiles/<acct>.maFile | just secrets set-stdin <bot> STEAM_SHARED_SECRET
   ```

   This uses the existing `set-stdin` recipe (`just/secrets.just:64-67`; `jq -c` emits the JSON-encoded string it
   expects). Or use the proposed `just secrets import-mafile` (§7), which also files `identity_secret`,
   `revocation_code`, `serial_number`, `device_id` and `steam_id` into `secrets/guard/<bot>.sops.yaml`.
   `just secrets list` should now show `STEAM_USER/PASS/SHARED_SECRET set`.
9. **Authenticate headlessly:** `just probe auth <bot>`. This is password + TOTP → `Authenticate` → refresh token
   written into the sops file. **From here on, no human input is needed.** `just secrets list` should show
   `STEAM_REFRESH_TOKEN set`, and `probe-state/<bot>/report.jsonl` should have `authenticate_ok` with
   `used_totp: true` and the `aud`/`exp` claims. If it fails with `eresult 88`, check the clock and rerun after 30 s.
   If it fails with an agreement URL, log in once in a browser and accept.

After the runbook, the automated checks are the existing probe plan:

- `just probe run <bot> login` (GC welcome; works for a never-launched account, Q21)
- `just probe renew <bot>`
- the proposed `probe license` from §4.3, only if the welcome fails.

---

## 7. `just secrets import-mafile`

Implemented in `just/secrets.just`: it copies `shared_secret` from a steamguard-cli/SDA maFile into the bot's sops
file and the other fields (identity secret, revocation code, serial, device ID, Steam ID) into
`secrets/guard/<bot>.sops.yaml`, checks the account name, and never prints a value. It needs jq ≥ 1.7, which keeps
the 64-bit `steam_id` exact.

Still proposals: a `guard: set/-` column in `just secrets list`, a `just probe code <bot>` recipe that prints the
current TOTP, and having `tools/probe` apply the `QueryTime` offset and set `DeviceFriendlyName: "dota_lobby <bot>"`.

---

## 8. Open questions only a live account can answer

| ID | Question | How |
|---|---|---|
| H1 | Does `steamguard setup` succeed on a phone-less fresh account in Nov 2026 (email `confirm_type` 3), or does it hit `InvalidState`/`NoVerifiedPhone`? | Runbook step 4 on Bot A/B |
| H2 | Does `Authenticate` with `DeviceCode` complete with no email/app approval from a datacenter IP? | Answered for the probe host (no approval, [09](09-live-probe.md) §2); still to run from the deployment host |
| H3 | Does Steam send a non-blocking "new sign-in" email on each `Authenticate`? | Watch the bot mailbox |
| H4 | Does the GC welcome an account that never launched Dota, and does its licence list contain 0/49307/197846? | Welcome: yes (Q21, [09](09-live-probe.md) §3). Licence list: the proposed `probe license` |
| H5 | Does `ClientRequestFreeLicense{570}` answer, or hang as `app_license_request 570` reportedly does? | `probe license` with a 20 s timeout |
| H6 | Can a limited account create/invite/be league admin, and does its lobby chat show up? | A limited test bot (Q13, Q22) |
| H7 | Can several bots share one email address (a second signup with the same email)? | Account creation for bot 2 |
| H8 | Are refresh tokens bound to the minting IP? | Mint on a laptop, use on the server (gap-01 §7) |

---

## Sources

### Module and repo source (VERIFIED)

- `go-steam@336e824f8540/auth_session.go:20-149, 175-243`, `go-steam@336e824f8540/auth-token.go:12-17`, `go-steam@336e824f8540/logon-details.go:10-39`, `go-steam@336e824f8540/auth.go:118`
- `go-steam@336e824f8540/client.go:70-72, 117-126, 273`, `go-steam@336e824f8540/web.go:69-108`, `go-steam@336e824f8540/protocol/msg.go:38`, `go-steam@336e824f8540/protocol/packet.go:80`
- `go-steam@336e824f8540/protocol/steamlang/enums.go:341, 1405, 1541-1542, 4235`
- `go-steam@336e824f8540/protocol/protobuf/client_server.proto:168-198`, `client_server_2.proto:215-223`, `client_server_2.pb.go:1081-1121`
- `tools/probe/auth.go:22-41, 136-180`, `just/secrets.just:1-67`, `just/probe.just:20-29`, `.gitignore:40-44`

### Upstream code (read via `gh api`, `master`)

- steamguard-cli: `src/commands/setup.rs`, `steamguard/src/accountlinker.rs`, `steamguard/src/lib.rs:36-52`,
  `src/accountmanager.rs:164`, `src/fixtures/maFiles/compat/1-account/1234.maFile`, README;
  releases v0.18.1-v0.18.4 (2026-06-04 to 2026-07-16)
- Issues: [SGC#443](https://github.com/dyc3/steamguard-cli/issues/443) (2025-02),
  [SGC#447](https://github.com/dyc3/steamguard-cli/issues/447) (2025-03),
  [SGC#508](https://github.com/dyc3/steamguard-cli/issues/508) / [#509](https://github.com/dyc3/steamguard-cli/issues/509) (2026-07)
- [ValveSoftware/Dota2-Gameplay#32961](https://github.com/ValveSoftware/Dota2-Gameplay/issues/32961),
  [#32983](https://github.com/ValveSoftware/Dota2-Gameplay/issues/32983) (2026-06 to 2026-09)

### Primary web pages (VERIFIED, fetched 2026-11-07)

- [Steam Subscriber Agreement](https://store.steampowered.com/subscriber_agreement/) (revision 2026-09-10; §1.C, §2.G, §4.C, §4.D)
- `https://store.steampowered.com/join/` (hCaptcha, 13+ checkbox, email verification)
- `https://store.steampowered.com/api/appdetails?appids=570`, `.../api/packagedetails?packageids=49307|197846|330209`
- `https://api.steampowered.com/ITwoFactorService/QueryTime/v1/` (live response incl. `skew_tolerance_seconds: 60`)

### Secondary (REPORTED; may be outdated)

- [node-steam-user](https://github.com/DoctorMcKay/node-steam-user), [node-steamstore](https://github.com/DoctorMcKay/node-steamstore),
  [McKay forum: requestFreeLicense](https://dev.doctormckay.com/topic/1244-requestfreelicense-still-works/),
  [McKay forum: 429](https://dev.doctormckay.com/topic/5946-new-request-limits-429-error/)
- [Steam forum: one phone, many accounts](https://steamcommunity.com/discussions/forum/1/1489992080501353141/) (2016-2021 quotes)
- [Steam forum: $5 rule](https://steamcommunity.com/discussions/forum/1/2941371547495172568/),
  [GPDS](https://gpdsgameshop.com/blog/steam-account-still-limited-after-a-gift-check-what-counts-toward-the-5-requirement)
- [Steam forum: email reuse](https://steamcommunity.com/discussions/forum/1/727997490786235987/),
  [PC Gamer forum](https://forums.pcgamer.com/threads/hey-guys-is-this-true-you-can-make-multiple-steam-accounts-but-using-only-one-gmail-account-by-using-aliases.149969/)
- [Steam Market group: trade holds](https://steamcommunity.com/groups/community_market/discussions/0/2592234299548710456/),
  [SteamVaults](https://steamvaults.org/blog/why-steam-trade-hold-is-15-days)
- [Dota 2 wiki: Matchmaking](https://dota2.fandom.com/wiki/Matchmaking), [teamsmurf behaviour score](https://teamsmurf.com/behavior-score-guide/),
  [teamsmurf limited accounts](https://teamsmurf.com/limited-steam-accounts-dota-2/), [hawk.live](https://hawk.live/posts/player-behavior-system-reports-dota-2)
- [conductatlas SSA summary](https://conductatlas.com/platform/steam/steam-subscriber-agreement/),
  [windowsforum authenticator guide](https://windowsforum.com/news/how-to-set-up-steam-guard-mobile-authenticator-recovery-codes-trade-holds-and-windows-security.449139/),
  [Octo Browser](https://blog.octobrowser.net/how-to-create-multiple-steam-accounts), [ValvePython steam.guard docs](https://steam.readthedocs.io/en/stable/api/steam.guard.html)
