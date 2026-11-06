# Bot accounts

!!! warning "Read this before you create any Steam accounts"

    - **Steam login is reported to fail.** Login with the pinned go-steam library is reported
      to fail against current Steam. This project has not verified that report either way.
    - **Lobbies are not created.** `POST /lobby/create` is a placeholder; no Dota 2 lobby is
      made.

    Use throwaway Steam accounts that you can afford to lose, and start with a single bot.
    See [Known limitations](../known-limitations.md) for the full list.

Each bot is an ordinary Steam account that the service logs in to with a username and a
password from `secrets.yaml`. This page explains how to set those accounts up and what goes
wrong when one of them is not set up right.

## Setting up an account

Do this for every bot, before you add it to `secrets.yaml`:

1. **Create a new Steam account just for the bot.** Never use your personal account or an
   account anybody plays on. If Steam restricts or locks it, you lose only the bot.
2. **Use a separate account for each bot.** Each `secrets.yaml` entry must be a different
   account.
3. **Add Dota 2 and launch it once** from a normal Steam client while logged in as the bot,
   then log out of that client completely. Dota 2 is free.
4. **Turn Steam Guard off** (see below).
5. **Consider making the account non-limited.** Steam limits accounts that have never made
   a purchase. A limited account is not known to block this service, but it has
   restrictions that may get in the way; a small purchase or wallet top-up lifts them.
6. **Log out everywhere else.** While the bot runs, nobody may be logged in to the same
   account on a desktop client, the web client, a phone or another instance of this
   service (see [One instance per account](#one-instance-per-account)).

Then add the entry to `config/secrets.yaml` with both keys:

```yaml
secrets:
  bots:
    - username: "my_league_bot_1"
      password: "the account password"
```

The full key reference is on [Configuration reference](index.md).

## Steam Guard must be off

The service only sends a username and password. It cannot answer a Steam Guard prompt, so
any form of Steam Guard breaks login:

| Steam Guard mode | Log line |
|---|---|
| Email codes | `[<username>] Failed to log on: EResult_AccountLogonDenied` |
| Mobile authenticator | `[<username>] Failed to log on: EResult_AccountLogonDeniedNeedTwoFactorCode` |

Turn Steam Guard off on the account's **Manage Steam Guard** page (in the Steam client's
security settings, or under account details on the Steam website). Steam warns that this makes the account less secure, which is one more
reason to use a throwaway account. Older docs mentioned a `sentry_hash` setting for Steam
Guard; it is not supported and has no effect.

## Rate limits and lockout risk

A failed login is retried automatically, about **every 5 seconds per bot, forever**. Each
failure also schedules a second retry after 30 seconds, so retry attempts can pile up the
longer a bot keeps failing. The service never gives up on its own.

Steam answers repeated failed logins from one IP address with:

```text
[<username>] Failed to log on: EResult_RateLimitExceeded
```

Once you see that, further attempts only keep the limit in place, and repeated failures put
the account itself at risk of being restricted. So:

- **Start with one bot.** Add a second only after the first one logs
  `[<username>] Logged on to Steam`.
- **Stop at the first failure.** Run `docker compose stop` as soon as a
  `Failed to log on:` line appears, fix the cause, then start again. If you saw
  `EResult_RateLimitExceeded`, wait before you retry (Steam does not publish how long; an
  hour or more is a reasonable start).
- **Never leave a placeholder entry in `secrets.yaml`.** `grep -n your_steam
  config/secrets.yaml` must print nothing; a leftover example entry retries a fake logon
  every 5 seconds and counts against your IP.

What each `EResult_...` value means is on [Troubleshooting](../operations/troubleshooting.md).

## One instance per account

Steam allows one session per account. If the same account is logged in by two instances of
this service, or by the service and a desktop, web or phone client, each login kicks the
other off. The service then reconnects, which kicks the other side off again, forever. The
bot that was kicked logs one of:

```text
[<username>] Logged off: EResult_LoggedInElsewhere
[<username>] Logged off: EResult_LogonSessionReplaced
```

These appear on the `Logged off:` line, not on `Failed to log on:`. Run only one instance of
the service per set of accounts (the default `compose.yaml` sets a fixed `container_name`,
which stops Compose from starting a second copy by accident), and do not log in to a bot
account yourself while the service is running.

## Weekly Steam maintenance

Steam goes down for maintenance about once a week (usually Tuesday, US Pacific time), and
connection servers also drop connections at other times. When a bot is disconnected it logs
`[<username>] Disconnected from Steam` and tries to reconnect after 5 seconds.

If that reconnect cannot reach a Steam server at all (the TCP connection fails), the bot
stops **silently**: nothing more is logged, no further retry happens, and it shows as
`false` in `GET /bots` until the service restarts. The sign is a
`Disconnected from Steam` line with no `[<username>] Connected to Steam` after it. Fix it
with:

```bash
docker compose restart
```

Check `GET /bots` after Steam maintenance, and restart if a bot is stuck at `false`. This is
a known gap in the service; see [Known limitations](../known-limitations.md).
