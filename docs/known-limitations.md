# Known limitations

This version keeps a set of Steam bot accounts logged in to Steam and the Dota 2 game
coordinator without anyone at a keyboard, and reports their state. It does not host lobbies
yet. Read this list before you use real accounts.

1. **Not yet run against live Steam.** The headless login (password plus a computed Steam
   Guard code, then a stored refresh token) and the game coordinator session are built and
   tested against fakes of Steam and the game coordinator, not against a real account. The
   open questions are listed under
   [What is still unverified](configuration/bot-accounts.md#what-is-still-unverified); the most
   important are whether token login works on the current go-steam master (Q1) and whether an
   account that never launched Dota 2 gets a game coordinator welcome (Q21).
2. **Lobbies are not created.** Everything under `/v1/lobbies` answers `501`. League discovery,
   lobby hosting and match results come in later phases.
3. **Account setup is manual.** Creating a Steam account and linking its authenticator with
   steamguard-cli is done by a person, once per bot (about 15 to 20 minutes); see
   [Bot accounts](configuration/bot-accounts.md). Creating accounts automatically is against the
   Steam Subscriber Agreement.
4. **A bot without a `shared_secret` needs a person** whenever its token dies (about every
   200 days) or Steam asks for a code: it goes to `needs_attention` until someone runs
   `dota_lobby auth` and calls the retry endpoint. There is no API endpoint for entering a Steam
   Guard code.
5. **Limited accounts are untested.** A limited account (no $5 on record) may be enough to log
   in and reach the game coordinator, but whether its lobby invites arrive (Q22) and whether its
   chat shows up is unknown.
6. **Credentials are read at startup.** Changing a password or adding a bot needs a restart
   (`just docker restart`), which restarts every bot. There is no hot reload.
7. **Decrypted credentials live on a host tmpfs** while the service runs, readable by root on the
   host. After a reboot, `just docker up` must run again before the container can start.
8. **One instance per set of accounts.** The state lock stops a second copy on the same state
   directory, but nothing stops a copy on another machine; the two would keep taking the
   accounts from each other (each side then cools down for 10 minutes).
9. **The clock matters.** Steam Guard codes come from the host clock. Without NTP, logons fail
   with EResult 88. `steam.totp_skew` is a manual workaround; the service does not ask Steam for
   its time.
10. **No metrics yet.** `metrics.addr` is accepted but no metrics listener runs. Token expiry is
    shown in `/v1/bots` and renewals are logged.
11. **Several config sections are accepted but unused**: `league`, `lobby`, `match`, `store`,
    `webhooks` and `metrics`. They are validated so configs keep working once the features
    arrive.
12. **No TLS.** Put a reverse proxy in front of the API before exposing it beyond localhost.

## Roadmap

Planned work, in order. No dates are promised.

- Run the Phase 0 probe against real bot accounts and fix whatever it finds.
- League discovery per bot and bot leasing, with state in SQLite.
- Lobby creation, invites and lobby lifecycle through `/v1/lobbies`.
- Match results from the game coordinator, the Steam Web API and OpenDota, with webhooks.
- Prometheus metrics.
- Move the docs site from Material for MkDocs to Zensical before 2027-05.

The design and the research behind it are in the repository under
[`docs/plans/`](https://github.com/kettleofketchup/dota_lobby/tree/main/docs/plans) and
[`docs/research/`](https://github.com/kettleofketchup/dota_lobby/tree/main/docs/research).
