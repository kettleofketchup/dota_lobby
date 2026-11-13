# Known limitations

This version keeps a set of Steam bot accounts logged in to Steam and the Dota 2 game
coordinator without anyone at a keyboard, reports their state, and hosts lobbies through
`/v1/lobbies`. Read this list before you use real accounts.

1. **The service itself has not run against live Steam yet.** The headless login and game
   coordinator session that `pkg/gc` uses were verified live with the probe on 2026-11-08
   (token login, login after a restart, and an account that never launched Dota 2; see
   [live probe results](https://github.com/kettleofketchup/dota_lobby/blob/main/docs/research/09-live-probe.md)), but the bot manager as a whole is only tested against
   fakes. The remaining open questions are listed under
   [What is still unverified](configuration/bot-accounts.md#what-is-still-unverified).
2. **Lobby hosting is only tested against fakes.** Creating a league lobby, setting the game
   mode, inviting players and joining and answering in the lobby's chat were checked live with
   the probe; the lobby manager and its chat commands as a whole were not. A live launch with
   the bot in the caster slot failed the first load, so the caster strategy is gone and the
   bot waits in the unassigned pool (`lobby.host_seat_strategy`). How a finished match shows in the
   lobby (`POSTGAME`, `match_outcome`) is also unconfirmed.
3. **The match result is provisional.** It comes from the lobby alone (`match.outcome`). For
   league games, the Steam Web API's match data is the final word; the service does not fetch it
   yet. Unscored outcomes (64-69) need a human decision.
4. **One game per lobby.** Series (Bo3, Bo5), bots that fill empty slots, team names shown in
   the client, and a host that leaves after the launch are not supported. The bot stays in the
   lobby until the match ends, so you need one bot per game played at the same time.
5. **Webhooks are delivered at least once.** A receiver can see an event twice and must
   de-duplicate on its `id`. A delivery that keeps failing is given up after
   `webhook_delivery.max_age` (24 h); the event stays readable in
   `GET /v1/lobbies/{id}/events`. See [Webhooks](operations/webhooks.md).
6. **Account setup is manual.** Creating a Steam account and linking its authenticator with
   steamguard-cli is done by a person, once per bot (about 15 to 20 minutes); see
   [Bot accounts](configuration/bot-accounts.md). Creating accounts automatically is against the
   Steam Subscriber Agreement.
7. **A bot without a `shared_secret` needs a person** whenever its token dies (about every
   200 days) or Steam asks for a code: it goes to `needs_attention` until someone runs
   `dota_lobby auth` and calls the retry endpoint. There is no API endpoint for entering a Steam
   Guard code.
8. **Limited accounts are untested.** A limited account (no $5 on record) may be enough to log
   in and reach the game coordinator, but whether its lobby invites arrive (Q22) and whether its
   chat shows up is unknown.
9. **Credentials are read at startup.** Changing a password or adding a bot needs a restart
   (`just docker restart`), which restarts every bot. There is no hot reload.
10. **Decrypted credentials live on a host tmpfs** while the service runs, readable by root on the
   host. After a reboot, `just docker up` must run again before the container can start.
11. **One instance per set of accounts.** The state lock stops a second copy on the same state
   directory, but nothing stops a copy on another machine; the two would keep taking the
   accounts from each other (each side then cools down for 10 minutes).
12. **The clock matters.** Steam Guard codes come from the host clock. Without NTP, logons fail
   with EResult 88. `steam.totp_skew` is a manual workaround; the service does not ask Steam for
   its time.
13. **No metrics yet.** `metrics.addr` is accepted but no metrics listener runs. Token expiry is
    shown in `/v1/bots` and renewals are logged.
14. **Several config keys are accepted but unused**: `lobby.series_types`, `match`, `metrics` and
    `league.webapi_enrichment`. The `match` keys belong to the match result lookup (Steam Web
    API, OpenDota), which does not run yet. They are validated so configs keep working once the
    features arrive.
15. **No TLS.** Put a reverse proxy in front of the API before exposing it beyond localhost.

## Roadmap

Planned work, in order. No dates are promised.

- Run the Phase 0 probe against real bot accounts and fix whatever it finds.
- Match results from the game coordinator, the Steam Web API and OpenDota.
- Prometheus metrics.
- Move the docs site from Material for MkDocs to Zensical before 2027-05.

The research behind the design is in the repository under
[`docs/research/`](https://github.com/kettleofketchup/dota_lobby/tree/main/docs/research).
