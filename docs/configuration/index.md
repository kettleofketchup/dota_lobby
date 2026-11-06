# Configuration reference

The service reads two YAML files at startup:

- `config.yaml` holds the server settings. It contains nothing secret apart from the
  optional API key.
- `secrets.yaml` holds the Steam bot credentials. Never commit it.

Both files are optional as far as startup goes, but without `secrets.yaml` no bots are
started.

## Keys

| Key | File | Type | Default | Required |
|---|---|---|---|---|
| `server.host` | `config.yaml` | string | `0.0.0.0` | No |
| `server.port` | `config.yaml` | int | `8080` | No |
| `server.api_key` | `config.yaml` | string | unset (authentication off) | No, but strongly recommended |
| `secrets.bots[].username` | `secrets.yaml` | string | none | **Yes, for every bot entry** |
| `secrets.bots[].password` | `secrets.yaml` | string | none | **Yes, for every bot entry** |

There are no other keys. In particular:

- **`sentry_hash` is not supported.** Older examples listed a `sentry_hash` field per bot.
  The service never loads it and never sends it to Steam, so setting it has no effect. Steam
  Guard must be off instead; see [Bot accounts](bot-accounts.md).
- **No environment variables and no command-line flags.** Everything comes from the two
  files. (`DOTA_LOBBY_TAG` in the Compose docs is a Compose variable that picks the image
  tag, not an app setting.)

### `server.host`

The address the HTTP API listens on. Inside a container it **must** be `0.0.0.0`; any other
value (such as `127.0.0.1`) makes the API unreachable from outside the container, even
through a published port. To keep the API private, restrict the host side of the port
mapping instead (the default `compose.yaml` publishes `127.0.0.1:8080:8080`).

### `server.port`

The port the HTTP API listens on. In a container, leave it at `8080` and change the **host**
side of the port mapping to use another port, for example `127.0.0.1:9000:8080`. If you do
change `server.port`, the container side of the mapping must be changed to match, or the
port is unreachable.

### `server.api_key`

When set, `GET /bots`, `POST /lobby/create` and `/lobby/info` require the key, sent as either
header:

```text
X-API-Key: <your api_key>
Authorization: Bearer <your api_key>
```

`GET /health` never needs the key. When `api_key` is unset or empty, every endpoint is public
and the service logs `WARNING: API key authentication disabled - all endpoints are public`
at startup. Set a long random value, for example the output of `openssl rand -hex 32`. See
the [API reference](../api/index.md) for the endpoints.

### `secrets.bots[]`

A list with one entry per Steam account. Each entry needs **both** `username` and
`password`.

!!! danger "An empty or misspelled key crashes the whole service"

    If any entry has an empty `username` or `password`, or the key is misspelled (for example
    `user:` instead of `username:`), the Steam library panics with `Username must be set!` or
    `Password or LoginKey must be set!` and the whole process exits. In a container this
    shows up as a restart loop.

Listing the same `username` twice starts it once and logs
`Failed to add bot <n> (<username>): bot with username <username> already exists` for the
duplicate.

`api_key` and `password` values are stored as redacted secrets: if they are ever printed or
encoded to JSON they appear as `***REDACTED***`.

## Example files

The repository ships both files as examples. Copy them into `config/` and edit the copies.

```yaml title="config.yaml.example"
server:
  # Keep "0.0.0.0" when running in a container, or the API is unreachable
  # from outside the container.
  host: "0.0.0.0"
  # Leave 8080 in a container. To use another port, change the HOST side of the
  # port mapping instead (for example 127.0.0.1:9000:8080). If you change this
  # value anyway, the container side of the mapping must match it.
  port: 8080
  # API key for the protected endpoints. Strongly recommended whenever the port
  # is reachable beyond localhost. Send it as "X-API-Key: <key>" or
  # "Authorization: Bearer <key>".
  # api_key: "your-secret-api-key-here"
```

```yaml title="secrets.yaml.example"
# Steam bot credentials for dota_lobby. Copy this file to config/secrets.yaml
# and never commit it. In the container it is read from /app/config/secrets.yaml.
#
# Every bot entry needs BOTH username and password. An empty or misspelled key
# crashes the whole service at startup.
#
# Steam Guard must be OFF on every bot account (email codes and the mobile
# authenticator both block login). dota_lobby cannot answer Steam Guard prompts.
#
# Start with one bot: a failed login is retried about every 5 seconds, forever.
secrets:
  bots:
    - username: "your_steam_username_1"
      password: "your_steam_password_1"
    # To add another bot, uncomment and fill in (one entry per Steam account):
    # - username: "another_bot_username"
    #   password: "another_bot_password"
```

## Where the files are found

Each file is searched for in these directories, in order, and the first match wins:

1. `.` (the current working directory)
2. `./config` (relative to the current working directory)
3. `$HOME/.config/dota_lobby`

The two files are looked up independently, so they can come from different directories.

**In the container**, the working directory is `/app`, and the only supported location is
`/app/config/` (mount your `config/` directory with `./config:/app/config:ro`). The
directory must be traversable by the container user (UID 65532), so keep it at mode `755`.
The files themselves must be readable by that user; how to arrange that depends on your
runtime, see [File permissions](../deployment/docker-compose.md#file-permissions).

**From source**, run `./bin/dota_lobby` from a directory that contains the two files or a
`config/` subdirectory with them; see [Build from source](../deployment/from-source.md).

## Applying changes

The files are read **once, at startup**. After editing them, restart the service:

```bash
docker compose restart
```

If the permission recipe for your runtime made the files unwritable by you, follow the
"Editing later" line for your runtime under
[File permissions](../deployment/docker-compose.md#file-permissions) to edit them, then
restart.

## What happens when something is wrong

| Situation | Result |
|---|---|
| `config.yaml` missing | Defaults are used: `0.0.0.0:8080`, API key authentication **off** |
| `secrets.yaml` missing | `WARNING: secrets.yaml not found - no bots will be configured` (printed without a timestamp), and the service runs with no bots |
| `secrets.yaml` present but with no bot entries | `WARNING: No bots configured in secrets.yaml` |
| Malformed YAML in either file | Fatal: the process exits with `Failed to load configuration: ...` |
| Config **directory** not traversable by the service (mode `700`, or an SELinux denial) | Looks exactly like both files are missing: defaults, authentication off, no bots. Nothing says permission denied |
| Directory traversable, but a **file** not readable by the service | Fatal, see below; the container restarts in a loop |
| Empty or misspelled `username` / `password` | The process panics and exits; the container restarts in a loop |

!!! warning "Check the startup log every time"

    If the files exist but the log shows `WARNING: secrets.yaml not found` or
    `WARNING: API key authentication disabled`, the service could not see into the config
    directory. That is a directory permission or SELinux mount problem, not a missing file.
    A healthy start logs `API key authentication enabled` (when `api_key` is set) and one
    `Added bot: <username>` line per bot.

An unreadable file in a traversable directory gives a fatal error. `config.yaml` is read
first, so this is the line you usually see:

```text
Failed to load configuration: error reading config file: open /app/config/config.yaml: permission denied
```

Only when `config.yaml` is readable but `secrets.yaml` is not does it say:

```text
Failed to load configuration: error reading secrets file: ... permission denied
```

Both lines carry the usual `YYYY/MM/DD HH:MM:SS` log timestamp in front. The fix is the
permission recipe for your runtime under
[File permissions](../deployment/docker-compose.md#file-permissions). More log lines and
their fixes are on [Troubleshooting](../operations/troubleshooting.md).
