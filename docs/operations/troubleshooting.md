# Troubleshooting

## First checks

1. Is the container running and healthy, or restarting in a loop?

    ```bash
    just docker ps
    ```

2. What does each bot say?

    ```bash
    curl -s -H "X-API-Key: $KEY" http://127.0.0.1:8080/v1/bots
    ```

    `state`, `reason`, `last_error` (an EResult name) and `last_error_message` usually tell you
    what is wrong. The states are listed in the [API reference](../api/index.md#bot-states).

3. What did it log?

    ```bash
    docker compose logs --tail 200
    ```

    Logs are JSON lines (`log.format: text` for plain text). Lines about one bot carry
    `"bot":"<name>"`. Every state change is a `bot state changed` line with `from`, `to`,
    `reason` and `eresult`; entering `needs_attention` is logged at error level as
    `bot needs attention`.

## Startup failures

These stop the process before any bot logs on. The container exits and restarts in a loop;
the reason is the last log line, prefixed with `dota_lobby serve:`.

| Message | Cause | Fix |
|---|---|---|
| Docker cannot mount `.../dota_lobby` (no such file or directory), or `read credentials_dir: ...` | The decrypted credentials are not on the host tmpfs, typically after a reboot | `just docker up` |
| `load config: decode config: ... has invalid keys: ...` | A misspelled or removed key in `config.yaml` or the secrets file | Fix the key; see the [Configuration reference](../configuration/index.md#keys) |
| `secret must be a quoted string` or `key STEAM_PASS: quote the value` | An unquoted secret that YAML read as a number or boolean | Quote it: `STEAM_PASS: "0123"` |
| `still sops-encrypted; decrypt it first` | The service was given an encrypted file | Use `just docker up` (or `sops decrypt`) |
| `bot <name>: shared_secret is not valid base64` | A wrong value in `STEAM_SHARED_SECRET` | Re-run `just secrets import-mafile` |
| `username ... differs from STEAM_USER` | `bots[].username` and the credentials file disagree | Remove `username` from `config.yaml` or fix it |
| `bots "a" and "b" use the same Steam account` | Two bots point at one account | One bot per account |
| `start bots: auth: lock ...: state dir is locked by another process` | Another `serve` is running on the same state directory | Stop the other copy. Only one instance per set of accounts |
| `start bots: auth: open lock: ... permission denied` | The state directory is not writable | With Compose, use the named volume from `compose.yaml`; do not bind-mount a host directory there unless UID 65532 owns it |
| `webhooks[0]: secrets.webhook_secrets has no signing secret for "..."` | A webhook without a signing secret, or a secrets key that is not the exact URL. The message shows the URL without its query or user info | Add `secrets.webhook_secrets["<url>"]`; see [Webhooks](webhooks.md#configure-a-receiver) |
| `open store ...: refusing to migrate: pre-migration backup failed: ...` | A new release has migrations to apply, and the copy it takes first could not be written or did not verify. Nothing was migrated; the database is as the old release left it | Read the cause at the end of the message. Usually the disk is full (the copy is as large as the database), `store.backup.dir` is not writable, or a file sits where the directory should be. Fix it and start again. See [Backups](../deployment/docker-compose.md#backups) |
| `listen: ... address already in use` | Port taken | Free the port or change the host side of the mapping |
| `read config /app/config/config.yaml: ... permission denied` | `config.yaml` is not readable by UID 65532 | `chmod 644 config/config.yaml` (it holds no secrets) |

## Logon results

How the service reacts to each Steam result. The bot's `reason` in `/v1/bots` is shown in
brackets.

| EResult | Meaning | What the bot does | What you do |
|---|---|---|---|
| 2 `Fail`, 20 `ServiceUnavailable`, 48 `TryAnotherCM`, network errors | Steam-side trouble | `backoff`: reconnects after a jittered delay, 5 s doubling up to 5 min (`transient`) | Nothing. During Steam's weekly maintenance (usually Tuesday, US Pacific time) this is normal |
| 5 `InvalidPassword`, 15, 26, 27, 121 on a **token** logon | The stored token is dead | Deletes it and authenticates once with password and code (`dead_token`). A second failure goes to `needs_attention` | Nothing, unless it ends in `needs_attention` |
| 5 `InvalidPassword` on a **password** logon | Wrong password | `needs_attention` (`bad_credentials`). Never retried, because retrying a bad password is what gets an IP throttled | Fix `STEAM_PASS`, `just docker restart`, then [retry](#recovering-a-bot-in-needs_attention) |
| 84 `RateLimitExceeded`, 87 `AccountLoginDeniedThrottle` | Too many logins from this IP or account | `cooling_down` for 30 min (`steam.cooldown_rate_limited`), and **logons pause for every bot** (`pausing logons for every bot`) (`rate_limited`) | Wait. Do not restart or retry to get around it |
| 6 `LoggedInElsewhere`, 34 `LogonSessionReplaced` | Another session took the account | `cooling_down` for 10 min (`logged_in_elsewhere`), without kicking the other session | Find the other session: a second instance, `just probe`, or a person in the Steam client. See [One instance per account](../configuration/bot-accounts.md#one-instance-per-account) |
| 88 `TwoFactorCodeMismatch` | The computed Steam Guard code was refused, almost always a clock problem | Retries twice, each in a new 30-second code window, then `needs_attention` (`totp_mismatch`) | See [Clock skew](#clock-skew-eresult-88) |
| Steam Guard prompt, no `shared_secret` | The bot cannot answer the prompt | `needs_attention` (`guard_required`) | Add the `shared_secret` ([Bot accounts](../configuration/bot-accounts.md)), or run `auth` with `--code`, then retry |
| Agreement URL | Steam wants a new Subscriber Agreement acceptance | `needs_attention` (`agreement_required`) | Log in once in a browser (`just probe code <bot>` for the code), accept, then retry |
| No password and no usable token | Nothing to log in with | `needs_attention` (`no_credentials`) | Add `STEAM_PASS` or a token |

The game coordinator can also put the bot in `gc_queued` (its logon queue; the position is in
`gc_queue_position`) or `suspended` (the account is suspended from Dota 2). Neither needs a
fix on this side; a suspension needs Steam Support.

## Recovering a bot in `needs_attention`

A bot in `needs_attention` stays there until you act; it never retries on its own. Fix the
cause first (table above), then:

1. If you changed a sops file: `just docker restart`, which re-decrypts and restarts. This
   also restarts every other bot.
2. If the bot needs a new token and has no `shared_secret`:

    ```bash
    just docker auth <bot>                  # asks for a Steam Guard code
    just docker auth <bot> --code 12345     # or pass it
    ```

    This runs `dota_lobby auth --bot <bot> --force` in the running container and stores the
    token in the state volume.
3. Tell the bot to try again:

    ```bash
    curl -X POST -H "X-API-Key: $KEY" http://127.0.0.1:8080/v1/bots/<bot>/retry
    ```

    The bot re-reads its token store and logs on. `retry` also ends a `backoff` or
    `cooling_down` early.

## Token renewal stopped

When the refresh token has under 14 days left, the service renews it in the background with
the bot's password and Steam Guard code. Renewal follows the same rules as a logon: a refused
password is never sent twice, a rate limit (84 or 87) pauses logons for every bot, and a
Steam Guard code that Steam rejects (88) is retried in the next code window, at most twice.
Renewal only starts while the bot is `gc_ready` with a token that has not expired. A bot that
is reconnecting is renewed once it is back; if its token runs out first, the reconnect logs in
with the password and mints a new token, so there is never more than one password login in
flight and no repeated Steam Guard code.

If renewal stops for something only you can fix, the log shows `token renewal stopped` and
`renew_blocked` in `/v1/bots` names the reason (`bad_credentials`, `guard_required` or
`totp_mismatch`). The bot is **not** disconnected: its current token still works, and cutting
it off would turn a credential problem into an outage. Fix the password or `shared_secret`
(see [Recovering a bot](#recovering-a-bot-in-needs_attention)), then call
`POST /v1/bots/<bot>/retry`, which clears the block and renews again. If the token expires
first, the bot goes to `needs_attention` with `reason: bad_credentials` at its next logon, and
the same password is not sent to Steam again until you retry.

## Clock skew (EResult 88)

Steam Guard codes change every 30 seconds and are computed from the host clock. Steam
tolerates about a minute of difference. When the code is refused (`TwoFactorCodeMismatch`,
88), the bot retries twice in fresh code windows and then goes to `needs_attention`.

1. Check the host clock: `timedatectl` should say `System clock synchronized: yes`. Install or
   fix NTP (`systemd-timesyncd`, `chrony`). The container uses the host clock.
2. Compare against a code you know is right: `just probe code <bot>` on a workstation with a
   good clock prints the same code the service should compute at that moment.
3. Only if the host clock cannot be fixed, set `steam.totp_skew` to Steam's time minus the
   host's (for example `-45s`). Values beyond ±10 minutes are refused.

Then [retry](#recovering-a-bot-in-needs_attention) the bot.

## A bot does not list my league

`GET /v1/leagues` and the `leagues` field of `/v1/bots` show which leagues each bot may host.
When a league is missing for a bot:

1. Is the bot `gc_ready`? The game coordinator list is fetched when the bot reaches `gc_ready`
   and every `league.refresh_interval` (30 min) after. Before the first fetch, the list saved by
   the last run is used (`"stale": true`), or `bots[].leagues` if there is none.
2. Look for the bot's last refresh in the logs:

    ```bash
    docker compose logs | grep -E 'league list refresh|configured leagues are not'
    ```

    - `league list refreshed` with `leagues` names what the game coordinator returned.
    - `league list refresh failed` means the request failed (`err` says why); the service tries
      again after a minute and keeps the last good list meanwhile.
    - `configured leagues are not in the bot's GC admin list` names leagues from
      `bots[].leagues` that the game coordinator does not list. They are ignored.

3. If the game coordinator does not list the league, the bot's Steam account is not one of the
   league's admins. Add it on [dota2.com/league](https://www.dota2.com/league), under the
   league's **Admins**, using the account's Steam ID from `/v1/bots`. Then fetch the list
   again with `POST /v1/bots/<name>/leagues/refresh`, or wait for the next refresh; see
   [Adding a league ticket](../configuration/bot-accounts.md#adding-a-league-ticket).
   `GET /v1/bots/<name>/leagues` shows the last refresh's outcome and when each league
   appeared or disappeared.

Configured leagues are only a fallback, because a lobby created for a league the account does
not administer is refused by the game coordinator. `league.allow_unverified: true` keeps them in
use anyway; only set it when you know the game coordinator list is wrong.

## Lobby problems

Every lobby keeps its history. `GET /v1/lobbies/{id}` shows the current `state`, `state_reason`,
`host_seat` and each player's `status`; the logs carry the same lobby ID:

```bash
docker compose logs | grep lob_01JC9Z7Q8W5X2M3N4P5R6S7T8V
```

### The lobby failed with `league_rejected`

The game coordinator would not create a lobby in that league for that bot: it sent a
`NOT_LEAGUE_ADMIN` popup, or the created lobby came back with a different league, in which case
the bot destroyed it. The bot's account is not an admin of the league, although the service
thought it was. That happens when only `bots[].leagues` lists the league (`league_verified:
false`) or the account was removed from the league's admins since the last refresh. See
[A bot does not list my league](#a-bot-does-not-list-my-league), then send the request again
with the same `external_ref`.

### `409 host_not_seated` on launch

The bot still sits on Radiant or Dire, or not where the confirmed strategy put it. Look at
`host_seat` in the lobby and for `host seat not confirmed; trying the next strategy` in the
logs:

- `"confirmed": false` with every strategy tried means the game coordinator ignored all of
  them. The bot tries again every 30 s while the lobby waits. Add more strategies to
  `lobby.host_seat_strategy`, for example `[caster, self_kick, player_pool, spectator]`.
- The strategy is retried automatically before each launch; retry the launch after a few
  seconds.
- A bot that is moved back onto a team after its seat was confirmed seats itself again
  (`host left its seat; seating it again` in the logs).

### The launch goes back to the lobby screen

A `lobby.launch_failed` event with `reason`:

| `reason` | Meaning | What happens |
|---|---|---|
| `ignored` | Still in the lobby screen `lobby.launch_settle_timeout` (2 min) after the launch | Back to `waiting`; the next launch moves the bot with the next strategy |
| `load_failed` | A server was assigned, then the lobby went back to the lobby screen: a member, possibly the bot, never loaded | The same |
| `host_not_seated` | The bot could not be moved off its team right before the launch | Back to `waiting` |
| `server_stall` | No game server within `lobby.server_stall_timeout` (10 min) | The lobby ends as `timed_out` |

Reports say a bot in the caster slot can block the load. If launches with `caster` keep
failing, put another strategy first: `lobby.host_seat_strategy: [player_pool, self_kick, caster]`.
After `lobby.max_launch_attempts` (3) failed launches, `state_reason` is
`launch_attempts_exhausted` and only `{"force": true}` launches again. A real player on each
side is reported to be needed for a launch.

### Invites do not arrive

- `teams.*.players[].invites_sent` counts the invites sent, and `invite_confirmed` turns `true`
  when the game coordinator confirms one. Unconfirmed invites are in `pending_invites`.
- A player who blocks invites from people who are not friends never sees one. They can join from
  the lobby browser with the lobby name and password.
- Limited accounts (no purchase on record) may not be able to invite; `limited` in `/v1/bots`
  shows it.
- Invites are sent once automatically. Resend with `POST /v1/lobbies/{id}/invite`, at most every
  `lobby.invite.min_resend` (30 s) and `lobby.invite.max_per_player` (5) times per player.

### The bot does not answer in the lobby chat

- Chat is off by default: set `lobby.chat.enabled: true` and restart.
- Commands start with `lobby.chat.prefix` (`!`). Text that starts with `/` is handled by the
  Dota client and never sent, so `/ready` does nothing.
- Only members of the lobby are answered. A spectator watching through Dota TV is not a member.
- `could not join the lobby chat; retrying` in the logs: the game coordinator refused the join
  (the result, such as `USER_IN_TOO_MANY_CHANNELS`, is in `err`) or did not answer it within
  5 s (`context deadline exceeded`). The bot tries again after 30 s, and waits twice as long
  after each further failure, up to 5 min; `failures` counts them. It only joins while the
  lobby is `waiting` or `ready`, so a lobby that reconnected in game stays out of the chat.
- Each answer has a cooldown: `!help` once per player every 30 s, `!teams` every 10 s, a refused
  command every 10 s, and `!ready`/`!unready` together every 3 s. More than ten waiting messages
  drop the oldest.
- `No launch attempts left` in the chat: `lobby.max_launch_attempts` launches were sent and
  none reached a game. Chat cannot start the lobby any more, not even for admins; launch it
  through the API with `{"force": true}`.
- Limited accounts (no purchase on record) may not be able to chat; `limited` in `/v1/bots`
  shows it.
- With `ready_check: true` and no working chat, players cannot type `!ready`, so a rostered
  lobby never becomes `ready`. Launch it with `{"force": true}`, or create it without
  `ready_check`.

### A lobby stays `pending`

No ready bot that holds the league is free: every holder hosts another lobby or is not
`gc_ready`. Check `lobby` and `state` in `/v1/bots`. After `lobby.pending_timeout` (10 min) the
lobby ends as `timed_out` (`pending_timeout`).

### A lobby failed after a restart

On startup a lobby whose bot no longer hosts it fails: `lobby_lost` (not launched),
`lost_in_game` (launched; `match.match_id` is kept) or `create_interrupted` (the create had been
sent but had not finished). The bot gets `lobby.readopt_timeout` (5 min) to be ready again
before that happens. A create that was adopted but came back with another league fails as
`league_rejected`, and its lobby is destroyed. A lobby whose create had not been sent yet at
shutdown is back in `pending` (`requeued`) and gets a bot again.

The same re-adoption runs when the database cannot store the result of a create (a busy or
full disk: `could not record the created lobby` in the logs): the lobby moves on once a write
works. A lobby whose bot is gone and whose failure cannot be written at startup stays as it is
in the database (`could not fail lobby; leaving it for the next start`).

### A bot leaves a lobby on its own after a reconnect

`bot is in a lobby no active lobby holds; leaving it` in the logs. A lobby that ended while its
bot was away (for example `idle_timeout` during a Steam outage, or a lobby that failed because
its bot was not back within `lobby.readopt_timeout`) could not be destroyed then. When the bot is
`gc_ready` again and hosts no lobby, it destroys that lobby if it is still in the lobby screen,
or only leaves it if the match launched, so the game goes on. It tries again every 30 s while
the lobby is still there.

### Webhooks do not arrive

Look for `webhook delivery failed; will retry` (warn) and `webhook delivery given up` (error)
in the logs; both name the event, the lobby, the receiver URL without its query, and the last
status or error.

- `401`, `403` or `404`, given up at once: the receiver refuses the request. Check the URL,
  the `token_header` token and, for DraftForge, that the service's address is in
  `INTERNAL_SERVICE_ALLOWED_IPS`.
- `the receiver redirected`: redirects are not followed. Put the final URL (often with a
  trailing `/`) in `webhooks[].url`.
- `timed out (webhook_delivery.timeout)` or `connection refused`: retried with backoff up to
  `webhook_delivery.max_age`. The receiver should answer quickly and do slow work afterwards.
- The receiver rejects the signature: it must sign the raw body, before parsing it, with the
  same secret, and both clocks must be within 5 minutes of each other.
- `webhook delivered but not recorded`, `cannot reschedule a webhook delivery` or `cannot
  give up a webhook delivery`: the database refused the write (a full disk or a read-only
  volume). The delivery stays queued and is tried again every 30 s until the write works, so
  a receiver may see an event twice. Free the disk.
- Nothing logged: the event type does not match the webhook's `events`, or the event happened
  before the webhook was configured. Events are queued when they are stored.
- Events of one lobby arrive in `seq` order, so one failing event holds the lobby's later ones
  back until it is delivered or given up. Other lobbies are not held back.

`GET /v1/lobbies/{id}/events?after=<seq>` lists every event of a lobby, whatever happened to
its deliveries.

## Other symptoms

| Symptom | Cause | Fix |
|---|---|---|
| `/readyz` stays `503` with `bots_ready: 0` | No bot has reached `gc_ready` yet, or none is configured | Check `/v1/bots`. Bots log on `steam.login_stagger` (10 s) apart, so several bots take a while. With no bots it stays `503` unless `readiness.min_ready_bots` is `0` |
| `/livez` answers `503` with `stalled_bots` | A bot's run loop has not moved for 30 s; the names are in the `bot run loop stalled` log line | Collect the logs and restart the container. This is a bug; please report it |
| Warning `no bots configured` | No `bots:` list, no files in `credentials_dir`, no old-style secrets | Check `just docker up` found `secrets/bots/*.sops.yaml`, and that `config/` is mode `755` |
| Warning `API key authentication disabled; /v1 endpoints are public` | No `secrets.api_key` | Create `secrets/service.sops.yaml` (see [Credentials](../deployment/docker-compose.md#credentials)) and `just docker restart` |
| Warning `secrets.bots entry matches no configured bot` | Credentials for a bot name that is not in `bots:` | Fix the name, or remove the entry |
| Warning `stored token unusable; will authenticate` or `stored token has expired` | The token in the state volume cannot be used | Nothing, if the bot has a password and `shared_secret` |
| Warning `token renewal failed; will retry` | Background renewal (under 14 days left) failed for a transient reason such as a network error | Nothing at first. If `token_expires_at` keeps getting closer, check the logs |
| Warning `token renewal refused; backing off` | Steam rate-limited the renewal (84 or 87). Logons are paused for every bot, like a rate-limited logon | Nothing; renewal runs again after `steam.cooldown_rate_limited` |
| Error `token renewal stopped` and `renew_blocked` set in `/v1/bots` | Steam refused the password, wants a Steam Guard code the bot cannot answer, or rejected the code three times | See [Token renewal stopped](#token-renewal-stopped) |
| Container `unhealthy` but running | `/livez` failing: see above, or the healthcheck cannot read the same `config.yaml` or `DOTA_LOBBY_SERVER_*` as the service | See [Changing the port](../deployment/docker-compose.md#changing-the-port) |
| No health status under Podman | The image was built in OCI format, which drops `HEALTHCHECK` | Build with `just docker build` |
| Port unreachable | `server.host` is not `0.0.0.0` in the container, or you connect from another machine to a `127.0.0.1` binding | Keep `host: 0.0.0.0`, change the host side of the mapping |
| `manifest unknown` on `pull` / `up` | `DOTA_LOBBY_TAG` has a leading `v`, or no image exists for that version | Use `1.2.3`, not `v1.2.3`. If it still fails, [build locally](../deployment/docker.md#build-the-image-locally) |
| `unauthorized` (or `denied`) on pull | The image package is private. This happens with a fork whose repository is private, or before a maintainer has made the package public | [Build the image locally](../deployment/docker.md#build-the-image-locally) |
| `401` with `missing or invalid API key` | Key header missing or wrong | Send `X-API-Key: <key>` or `Authorization: Bearer <key>` |
| `422 invalid_request` from a `/v1` call | A field or parameter failed validation (older releases answered `400`) | Read `errors[].location` and `errors[].message` in the response; see [Errors](../api/index.md#errors) |
| A client cannot read error responses | It expects the old `{"error": {...}}` envelope | Read `code`, `detail` and `retryable` at the top level of the `application/problem+json` body; see [Errors](../api/index.md#errors) |
| `410 gone` on `/bots` or `/lobby/...` | The old endpoints were removed | Use `/v1/bots` |
| `422 league_not_held` from `POST /v1/lobbies` | No enabled bot holds the league | See [A bot does not list my league](#a-bot-does-not-list-my-league) |
| `409 conflict` from `POST /v1/lobbies` | An active lobby already uses that `external_ref` with a different request | Close it, or send exactly the same request to get it back |
| `503 unavailable` from `/v1/leagues/{id}/nodes` | Bots hold the league, but none is `gc_ready` | Check `/v1/bots`, then retry |
| `404 not_found` from `/v1/leagues/{id}/nodes` | No enabled bot holds that league | See [A bot does not list my league](#a-bot-does-not-list-my-league) |
| Startup fails with `open store ...` | `store.path` (default `<state_dir>/dota_lobby.db`) cannot be created or opened, for example a read-only state volume | Make the state volume writable by the service user |
| Startup fails with `database schema is newer than this binary` | The database was used by a newer release | Run that release again, or [restore a backup](../deployment/docker-compose.md#restoring-a-backup) taken before the upgrade |
