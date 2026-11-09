# Bot accounts

!!! warning "Read this before you create any Steam accounts"

    - **Not yet run against live Steam.** The headless login (password plus a Steam Guard code
      the service computes itself, then a stored refresh token) is built and tested against
      fakes only. The first run against a real account is still pending; see
      [What is still unverified](#what-is-still-unverified).
    - **Lobbies are not created yet.** The lobby endpoints answer `501`. See
      [Known limitations](../known-limitations.md).

    Use dedicated accounts that you can afford to lose, and start with a single bot.

Each bot is a Steam account with a **Steam Guard mobile authenticator**. You link the
authenticator once, on your workstation, with
[steamguard-cli](https://github.com/dyc3/steamguard-cli). The service only needs the
authenticator's `shared_secret`: from it, it computes the same 5-character Steam Guard codes
the Steam Mobile app shows, so it can log in and renew its own tokens with nobody at a phone.

This page is the setup runbook for one bot. It takes about 15 to 20 minutes per account. The
background research, with sources, is
[10 - Headless bot accounts](https://github.com/kettleofketchup/dota_lobby/blob/main/docs/research/10-headless-bot-accounts.md)
§6.

!!! info "What goes where"

    - **steamguard-cli is a one-time workstation tool.** It is not in the container image and
      the service never runs it.
    - **The service gets only** the account name, the password, the `shared_secret` and
      (optionally) a refresh token. It generates Steam Guard codes from `shared_secret` itself.
    - **The service never gets** the `identity_secret` or the revocation code. They are kept in a
      separate sops file (`secrets/guard/<bot>.sops.yaml`) for recovery only.

## What you need on the workstation

- [just](https://just.systems/), [sops](https://github.com/getsops/sops),
  [age](https://github.com/FiloSottile/age) and [jq](https://jqlang.org/).
- steamguard-cli: `cargo install steamguard-cli`, or the `steamguard-cli` package from the AUR
  on Arch.
- Go, for the probe tool used in [step 9](#9-mint-the-first-refresh-token). With
  [mise](https://mise.jdx.dev) installed, `mise install` in the repository gives you the pinned
  version.
- A clock synchronised with NTP (`timedatectl` should say `System clock synchronized: yes`).
  Steam Guard codes are time-based.
- A clone of the repository. All `just` commands below run from it.

Notation: `<bot>` is the short name you give the bot (for example `bot-a`). It names the
sops file and is the bot's `name` in `config.yaml`. `<acct>` is the Steam account name.
Never paste a secret into chat, a ticket or your shell history; use the editor that
`sops edit` opens.

## Part A: one-time steps by a person

### 1. Prepare

Pick `<bot>` and `<acct>`. Generate a random password of 24 characters or more in your
password manager, and pick a mailbox only this bot uses (for example `ops+<bot>@example.com`).

### 2. Create the account in a browser

Go to <https://store.steampowered.com/join/>: email, country, the captcha, the age and
Subscriber Agreement checkbox, then the link in the verification email, then `<acct>` and the
password.

Do this by hand. The Steam Subscriber Agreement forbids automating account creation, so the
project has no tool for it.

### 3. Optional: lift the limited-account restriction

A new account is **limited** until US$5 has been added to it: no friend invites, no Market,
no Web API key and rate-limited chat. To lift it, log in to the store as `<acct>` and add $5 to
the bot's own Steam Wallet, or redeem a $5 wallet code on it. A gift from another account does
not count.

A limited account may be enough to log in, reach the game coordinator and host a lobby, but
that is reported, not tested. Whether a limited bot's lobby invites arrive (**Q22**) and
whether its lobby chat shows up are still open. Fund production bots; keep one limited
account for testing if you want to find out.

### 4. Link the authenticator with steamguard-cli

```bash
steamguard setup
```

- Enter `<acct>`, then the password.
- Enter the **email** Steam Guard code Steam sends to the bot's mailbox. New accounts have
  email Steam Guard on.
- When it prints the **revocation code** (`R12345`), store it in your password manager
  **now**. steamguard-cli waits for you. Without it, losing the `shared_secret` means a
  support ticket.
- Enter the activation code Steam sends by email (or SMS, if the account has a phone
  number). steamguard-cli finishes the link itself.

No phone and no Steam Mobile app are needed, as far as current sources show. If setup fails
with an "add a phone number" message, `GenericFailure` or `InvalidState`, add a phone number to
the account (not your personal account's number) and run it again. On `RateLimitExceeded`,
wait a few hours.

Check the link: `steamguard -u <acct> code` prints a 5-character code.

steamguard-cli writes `~/.config/steamguard-cli/maFiles/<acct>.maFile`. Do not encrypt it yet;
the import in step 7 reads it.

## Part B: credentials and the first token

### 5. Create the sops key (once per workstation)

```bash
just secrets init
```

This adds a labelled age key to your sops key file (`~/.config/sops/age/keys.txt`) and writes
`.sops.yaml`. Back that key file up: without it, the encrypted files cannot be read. `.sops.yaml`
and everything under `secrets/` are ignored by git; they stay on your machine.

### 6. Create the bot's credentials file

```bash
just secrets new-bot <bot>
```

It creates `secrets/bots/<bot>.sops.yaml` and opens it in `$EDITOR`. Set:

```yaml
STEAM_USER: "<acct>"
STEAM_PASS: "<the password>"
STEAM_SHARED_SECRET: ""
```

**Quote every value.** An unquoted value that YAML reads as a number (`STEAM_PASS: 0123`) is
rejected at startup instead of being silently rewritten.

### 7. Import the shared secret from the maFile

```bash
just secrets import-mafile <bot> ~/.config/steamguard-cli/maFiles/<acct>.maFile
```

This checks that the maFile belongs to the account in `STEAM_USER`, then:

| maFile field | Goes to | Used by |
|---|---|---|
| `shared_secret` | `secrets/bots/<bot>.sops.yaml` as `STEAM_SHARED_SECRET` | The service, to compute Steam Guard codes |
| `identity_secret`, `revocation_code`, `serial_number`, `device_id`, `steam_id` | `secrets/guard/<bot>.sops.yaml` | You, for recovery and support tickets. Never loaded by the service |

Nothing is printed. Afterwards `just secrets list` shows `STEAM_USER`, `STEAM_PASS` and
`STEAM_SHARED_SECRET` as `set`.

Then protect the maFile, which still holds every secret in plain text:

```bash
steamguard encrypt
```

Keep the encrypted maFiles as a second backup, or delete them. Never copy maFiles into the
repository.

### 8. Keep the revocation code offline too

The sops copy of the revocation code is only as safe as your age key. Keep the copy you made
in step 4 in your password manager or on paper.

### 9. Mint the first refresh token

```bash
just probe auth <bot>
```

This builds `tools/probe`, logs in once with the password and a computed Steam Guard code, and
writes the refresh token straight into the bot's sops file as `STEAM_REFRESH_TOKEN`. Nobody has
to type a code. `just secrets list` then shows `STEAM_REFRESH_TOKEN set`.

- `eresult 88` (TwoFactorCodeMismatch): the clock is off. Check NTP and run it again after
  30 seconds.
- An agreement URL: Steam wants a new Subscriber Agreement acceptance. Log in once in a
  browser and accept it.

This step is optional: a bot with a password and a `shared_secret` can mint its own token on
first start. Doing it here proves the credentials work before the service starts trying them.

### 10. A Steam Guard code for a browser login

To log in to the bot's account on the Steam website (to fund the wallet, accept an agreement,
check the account), get the current code with:

```bash
just probe code <bot>
```

Log out of the website again before the service runs; see
[One instance per account](#one-instance-per-account).

## How the service uses the credentials

The service never reads sops files. `just docker up` decrypts each
`secrets/bots/<bot>.sops.yaml` into a tmpfs directory that the container sees as
`credentials_dir` (see [Docker Compose](../deployment/docker-compose.md#credentials)). Other
ways to hand them over are in the [Configuration reference](index.md#bot-credentials).

On each logon a bot uses, in order:

1. the token in its token store, `<state_dir>/bots/<username>/token.json`;
2. `STEAM_REFRESH_TOKEN`, which **seeds** the store when the store has nothing usable (no file,
   an unreadable file or an expired token), or when it expires later than the stored token;
   A seed that Steam has refused (the bot logged on with it and was told the token is dead)
   is remembered by its SHA-256 fingerprint in `rejected.sha256` next to `token.json`, and is
   not seeded again on a retry or restart. Put a newly minted token in `STEAM_REFRESH_TOKEN`
   to replace it.
3. otherwise password plus a Steam Guard code computed from `STEAM_SHARED_SECRET`, after which
   the new token is stored.

Tokens last about 200 days and are renewed in the background when fewer than 14 days are left.
Renewed tokens are written only to the token store, never back to the sops file, so keep
`state_dir` on a persistent volume.

| Credentials | What the bot can do |
|---|---|
| password + `shared_secret` (+ token) | Fully headless: mints and renews its own tokens |
| password only | Logs in once a token exists. When the token dies it goes to `needs_attention`; see [Recovering a bot](../operations/troubleshooting.md#recovering-a-bot-in-needs_attention) |
| refresh token only | Runs until the token dies (about 200 days), then needs an operator |

## Adding a league ticket

A bot can host lobbies for a league only when its Steam account administers that league, the
"ticket". Tickets are granted by hand, per account, by an admin of the organisation that owns
the league, so different bots usually hold different leagues:

1. Read the bot's `steam_id` from `GET /v1/bots/<name>`.
2. On [dota2.com/league](https://www.dota2.com/league), open the league and add that Steam ID
   under **Admins**.
3. Make the service fetch the bot's list now instead of at the next
   `league.refresh_interval` (30 min):

    ```bash
    curl -fsS -X POST -H "X-API-Key: $KEY" http://127.0.0.1:8080/v1/bots/<name>/leagues/refresh
    ```

    The answer lists the bot's leagues; the new one has `"source": "gc"`, `"verified": true`
    and a `first_seen_at` of this refresh, and `changes` starts with an `added` entry for it.
    `POST /v1/leagues/refresh` does the same for every ready bot at once. A bot that is not
    `gc_ready` answers `503`; its list is fetched when it gets there.

4. Check the league's holders with `GET /v1/leagues/<league_id>`: the bot is listed, and
   `holders_free` counts it while it hosts no lobby.

The next lobby for that league can go to the bot as soon as the refresh has answered. Taking a
ticket away works the same way: after the refresh the league is gone from the bot's list, a
`removed` change is recorded, and no new lobby for that league goes to the bot; a lobby it
already hosts carries on. Every change is also logged at info as `bot league tickets changed`.

`bots[].leagues` in `config.yaml` is not a ticket. It is an unverified fallback, used only until
the bot's list has been fetched once (or always with `league.allow_unverified`); see
[`GET /v1/leagues`](../api/index.md#get-v1leagues).

## One instance per account

Steam allows one session per account. If the same account is logged in by two instances of
this service, by the service and the probe, or by the service and a desktop, web or phone
client, they kick each other off. The bot sees `LoggedInElsewhere` (6) or
`LogonSessionReplaced` (34) and **cools down for 10 minutes** (`steam.cooldown_elsewhere`)
before it tries again, so it does not fight the other session in a loop.

- Run one instance of the service per set of accounts. The service also takes a `flock` on
  `<state_dir>/LOCK`, so a second copy on the same state directory refuses to start, but
  nothing stops a copy on another machine.
- Do not run `just probe` commands for a bot while the service runs it.
- Do not log in to a bot account yourself while the service runs it.

## Rate limits

Steam throttles logins per IP. The service is built not to provoke that:

- bots log on one at a time, `steam.login_stagger` (10 s) apart;
- a refused password is never retried: the bot goes to `needs_attention`;
- `RateLimitExceeded` (84) or `AccountLoginDeniedThrottle` (87) cools that bot down for 30
  minutes (`steam.cooldown_rate_limited`) and pauses logons for every bot.

Still start with one bot, and add the next one once the first is `gc_ready`.

## What is still unverified

A live run on 2026-11-08 ([live probe results](https://github.com/kettleofketchup/dota_lobby/blob/main/docs/research/09-live-probe.md)) confirmed that password plus a
computed Steam Guard code returns a refresh token with no prompt, that the token alone logs on
again after a restart, and that an account which never launched Dota 2 gets a game coordinator
session, so no manual Dota launch is needed. These are still open
([open questions](https://github.com/kettleofketchup/dota_lobby/blob/main/docs/research/README.md)):

| Question | What is assumed | If it is wrong |
|---|---|---|
| **Q22** limited accounts | Unknown whether a limited bot's lobby invites arrive | Fund every bot |
| Phone-less authenticator | `steamguard setup` works with email codes only | Add a phone number in step 4 |
| Tokens across hosts | A token minted on your workstation works from the server | The service mints its own (it needs password and `shared_secret`) |
| Dead-token EResults | A dead token fails with 5, 15, 26, 27 or 121, which triggers one re-authentication | A change to the EResult table |
