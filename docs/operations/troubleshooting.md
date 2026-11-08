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
| `410 gone` on `/bots` or `/lobby/...` | The old endpoints were removed | Use `/v1/bots` |
