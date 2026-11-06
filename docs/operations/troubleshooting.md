# Troubleshooting

## First checks

1. Is the container running, or restarting in a loop?

    ```bash
    docker compose ps
    ```

2. What did it log? Most problems show up in the first few lines after startup.

    ```bash
    docker compose logs
    ```

3. Run the startup check. With `api_key` set and both files in `config/`, a healthy start
   logs `API key authentication enabled` and one `Added bot: <username>` line per bot, and
   does **not** log `WARNING: secrets.yaml not found`. If you see either warning below while
   the files exist, the container cannot see into `config/`.

Lines printed through Go's `log` package start with a `YYYY/MM/DD HH:MM:SS` timestamp, which
the table leaves out. Lines from a bot start with the bot's Steam username in brackets, shown
here as `[user]`.

!!! danger "Stop at the first failed login"

    A failed login is retried about every 5 seconds per bot, forever. If you see a
    `Failed to log on:` line, run `docker compose stop` before you investigate, so the
    retries do not get your IP rate limited or the account restricted.

## Log lines and symptoms

| Log line / symptom | Meaning | Fix |
|---|---|---|
| `WARNING: secrets.yaml not found - no bots will be configured` (no timestamp) **while `config/secrets.yaml` exists** | The container user (UID 65532) cannot traverse `config/` (mode `700`), or SELinux blocks the mount. Both look exactly like missing files | `chmod 755 config`. On SELinux hosts (Fedora, RHEL) add the `:Z` mount option, see [File permissions](../deployment/docker-compose.md#file-permissions). Then `docker compose restart` |
| `WARNING: API key authentication disabled - all endpoints are public` **while `api_key` is set in `config/config.yaml`** | Same cause: `config.yaml` is not visible, so defaults are used and every endpoint is public | Same fix as the row above. Do not expose the port until the startup log says `API key authentication enabled` |
| `WARNING: secrets.yaml not found - no bots will be configured` when there really is no file | No `secrets.yaml` in any search path | `cp secrets.yaml.example config/secrets.yaml`, edit it, apply the permission recipe |
| `WARNING: No bots configured in secrets.yaml` | `secrets.yaml` was read but has no entries under `secrets.bots` (all commented out, or a misspelled `secrets:` / `bots:` key) | Add one bot entry, see [Configuration](../configuration/index.md#secretsbots) |
| `Failed to load configuration: error reading config file: open /app/config/config.yaml: permission denied`, container restarting | `config/` is traversable but UID 65532 cannot read `config.yaml`. It is read first, so this is the line you usually see | Apply the permission recipe for your runtime under [File permissions](../deployment/docker-compose.md#file-permissions) (exactly one recipe) |
| `Failed to load configuration: error reading secrets file: ... permission denied`, container restarting | `config.yaml` is readable but `secrets.yaml` is not | Same fix as the row above |
| `Failed to load configuration: ...` with any other reason | Usually malformed YAML in one of the files | Fix the YAML (indentation, quotes) and restart |
| A panic with `Username must be set!` or `Password or LoginKey must be set!`, container restarting | A bot entry has an empty or misspelled `username` / `password` key (for example `user:`). The Steam library panics and the whole process exits | Fix the entry in `config/secrets.yaml`. Every entry needs both keys |
| `[user] Failed to log on: EResult_InvalidPassword` | Wrong password, **or** Steam rejecting this library's legacy password logon even with correct credentials (reported upstream, not verified by this project; see [Known limitations](../known-limitations.md)) | Stop the container. Check the password by logging in once with a Steam client, then log that client out. If the password is right, this is the library limitation and cannot be fixed by configuration |
| `[user] Failed to log on: EResult_AccountLogonDenied` | Steam Guard email codes are on for the account | Stop the container, turn Steam Guard off for the bot account, see [Bot accounts](../configuration/bot-accounts.md#steam-guard-must-be-off) |
| `[user] Failed to log on: EResult_AccountLogonDeniedNeedTwoFactorCode` | The Steam mobile authenticator is on for the account | Stop the container, remove the mobile authenticator from the bot account |
| `[user] Failed to log on: EResult_RateLimitExceeded` | Too many login attempts from your IP | Stop the container now and wait (an hour or more) before trying again. Fix the underlying failure first |
| `[user] Logged off: EResult_LoggedInElsewhere` or `[user] Logged off: EResult_LogonSessionReplaced` | The account was logged in somewhere else: a second instance of this service, or a desktop, web or phone client. These appear on the `Logged off:` line, not on `Failed to log on:` | Run only one instance per set of accounts and never log in to a bot account yourself while the service runs, see [One instance per account](../configuration/bot-accounts.md#one-instance-per-account) |
| `Added bot: user` or `[user] Disconnected from Steam`, then no `[user] Connected to Steam`, and `/bots` stays `false` | The TCP connection to Steam failed (network problem, Steam maintenance, or a stale built-in server list). This stops the bot silently: no further log line and no retry | `docker compose restart`. If it keeps happening at startup, check outbound TCP 27017 and DNS, see [Prerequisites](../getting-started/prerequisites.md) |
| Port unreachable (`curl` cannot connect) | `server.host` is not `0.0.0.0`, the port is published on `127.0.0.1` only and you are connecting from another machine, or `server.port` no longer matches the container side of the port mapping | Keep `host: "0.0.0.0"` and `port: 8080` in the container, remap the host side instead, see [Changing the port](../deployment/docker-compose.md#changing-the-port) |
| `Failed to start API server: ...` | The listener could not start, for example the port is already in use when running from source | Free the port or change `server.port` |
| `manifest unknown` on `docker compose pull` / `up` | The image tag does not exist. Most often `DOTA_LOBBY_TAG` (a Compose variable, not an app setting) has a leading `v` (`v1.2.3` instead of `1.2.3`) | Use the tag without `v`. If it still fails, no public image exists for that version yet: [build it locally](../deployment/docker.md#build-the-image-locally) |
| `unauthorized` (or `denied`) on pull | The image package is not public yet | [Build the image locally](../deployment/docker.md#build-the-image-locally) |
| `401` with `Unauthorized: Invalid or missing API key` | The API key header is missing or wrong | Send `X-API-Key: <key>` or `Authorization: Bearer <key>`, see [API](../api/index.md#authentication) |

!!! note "`/health` is not a readiness signal"

    `GET /health` answers `healthy` whenever the process is running, even with no bot logged
    on. To see whether bots are logged on, use `GET /bots` together with the logs. Even
    `true` there only means "logged on to Steam", not "ready to host a lobby"; see
    [Known limitations](../known-limitations.md).
