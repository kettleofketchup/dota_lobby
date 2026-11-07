# Known limitations

This is an early version. The service runs, loads its configuration and logs bots on to
Steam, but it does not create lobbies yet. Read this list before you use real accounts.

1. **Lobbies are not created.** `POST /lobby/create` returns `201` with `"status":"success"`
   but creates nothing, and `server_region` and `game_mode` are ignored. `/lobby/info` only
   echoes the lobby ID back. See the [API reference](api/index.md).
2. **The Dota 2 game coordinator session is never started.** `true` in `GET /bots` means the
   bot is logged on to Steam only, not that it is connected to Dota 2 or can host a lobby.
3. **`/health` is always healthy.** It answers `healthy` while the process runs, whatever the
   bots are doing.
4. **Steam Guard is not supported**, and the `sentry_hash` field is ignored. Every bot
   account needs Steam Guard turned off (no email codes, no mobile authenticator). See
   [Bot accounts](configuration/bot-accounts.md).
5. **Login may fail for every account.** The pinned go-steam library uses the legacy password
   logon. Upstream reports say current Steam rejects it, answering `EResult_InvalidPassword`
   even for correct credentials
   ([go-dota2#78](https://github.com/paralin/go-dota2/issues/78),
   [go-steam#11](https://github.com/paralin/go-steam/pull/11)). This is reported, not verified
   by this project with a real account. What has been checked: the container reaches a Steam
   CM and completes the logon exchange (a test with a non-existent account got
   `EResult_InvalidPassword` back), so the network path works. That answer looks the same as
   the reported rejection, so a correct password that still gets `InvalidPassword` is most
   likely this limitation. No configuration change helps; it needs a library upgrade (see the
   roadmap).
6. **Failed logins are retried forever.** A failed login (wrong password, Steam Guard, or the
   legacy-logon rejection) is retried about **every 5 seconds per bot, forever**. A 5 second
   and a 30 second timer both fire, so attempts can pile up. There is no backoff and no
   distinction between failures that can and cannot succeed on retry. This risks rate
   limiting (`EResult_RateLimitExceeded`) or an account lock. Start with one bot and run
   `docker compose stop` at the first `Failed to log on:` line.
7. **A connection failure stops a bot silently.** Any TCP-level connection failure, at
   startup **or on any reconnect** (a network blip, weekly Steam maintenance, a stale built-in
   server list), stops that bot permanently with **no log line**. It stays `false` in
   `GET /bots` until the container is restarted.
8. **A bad bot entry crashes the whole service.** An empty or missing `username` or
   `password` in any bot entry makes the Steam library panic, and the process exits. Compose
   then restarts it in a loop.
9. **An unreadable config directory is not reported.** If the container cannot traverse
   `config/` (permissions or SELinux), the files are treated as missing: the service starts
   with defaults, API key authentication **off**, and no bots, without an error. The only
   signs are the `secrets.yaml not found`, `No bots configured in secrets.yaml` and
   `API key authentication disabled` warnings. Check the
   startup log before exposing the port; see
   [Troubleshooting](operations/troubleshooting.md).
10. **Configuration comes from files only.** There are no environment variables, no
    command-line flags and no hot reload; restart after every change.
11. **One instance per set of accounts.** Nothing stops you from running two instances with
    the same accounts, and if you do they keep kicking each other off Steam.
12. **No lobby or league logic, no persistence, no TLS and no metrics.** Errors from the API
    are plain text, not JSON.
13. **Go 1.24 is past upstream support.** Moving to a supported Go toolchain is a follow-up.

## Roadmap

Possible future work, in no particular order. No dates are promised.

- Upgrade go-steam and go-dota2, with modern Steam authentication and a token store.
- Start and maintain the Dota 2 game coordinator session.
- Real lobby creation.
- Login retries with backoff, and handling of connection failures so bots always reconnect.
- Config validation: required bot fields checked up front, permission errors reported as
  errors.
- Separate `/livez` and `/readyz` endpoints.
- Configuration through environment variables.
- Move the docs site from Material for MkDocs to Zensical before 2027-05.
