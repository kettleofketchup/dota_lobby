- **Headless bot login.** Each bot logs in with password plus a Steam Guard code generated
  from its `shared_secret`, keeps the refresh token in `state_dir`, and logs in again from
  that token after a restart. Bots never run Steam or Dota 2.
- **Per-bot supervisor** with staggered logins, reconnect with jittered backoff, cooldowns
  for rate limits and sessions elsewhere, and a `needs_attention` state for problems a
  person must fix.
- Endpoints `/livez`, `/readyz`, `/health`, `/v1/bots`, `/v1/bots/{name}` and
  `POST /v1/bots/{name}/retry`.
- Subcommands `serve`, `auth --bot <name>` and `healthcheck`, and a container
  `HEALTHCHECK`.
- Config from per-bot credentials files (`credentials_file`, `credentials_dir`) in the
  format `sops decrypt` produces, `DOTA_LOBBY_*` environment variables and `*_FILE`
  variants.
- `just secrets` and `just docker` recipes that keep bot credentials in local sops files and
  decrypt them to a tmpfs directory only while the stack runs.
- `renew_blocked` in `/v1/bots`: why background token renewal stopped. A refused password is
  never sent to Steam again, a rate limit pauses logons for every bot, and a rejected Steam
  Guard code is retried in the next code window at most twice.
