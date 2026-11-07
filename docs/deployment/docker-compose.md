# Docker Compose

Docker Compose on a single Linux machine is the recommended way to run Dota Lobby. The
[Quick start](../getting-started/quick-start.md) walks through the first run; this page explains
the setup in more detail: the compose file, file permissions per runtime, upgrades, logs and
exposing the API.

!!! danger "Run one instance per set of Steam accounts"

    Never run two copies of the service with the same bot accounts, and never log in to a bot
    account in the Steam client while the service is running. Each login kicks the other off,
    and they keep doing so forever (`[<username>] Logged off: EResult_LoggedInElsewhere` or
    `EResult_LogonSessionReplaced` in the logs). `compose.yaml` sets a fixed `container_name`
    so Compose cannot start a second copy by accident, but it cannot stop a second copy on
    another machine.

## The compose file

This is `compose.yaml` from the repository, unchanged:

```yaml
# Single-machine deployment. See https://kettleofketchup.github.io/dota_lobby/deployment/docker-compose/
# DOTA_LOBBY_TAG is a Compose variable (image tag, no leading "v"), not an app setting.
services:
  dota_lobby:
    image: ghcr.io/kettleofketchup/dota_lobby:${DOTA_LOBBY_TAG:-latest}
    container_name: dota_lobby # also blocks accidental --scale
    restart: unless-stopped
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - ./config:/app/config:ro
    read_only: true
    cap_drop:
      - ALL
    security_opt:
      - "no-new-privileges:true"
    stop_grace_period: 30s
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
```

Line by line:

| Line | What it does |
|---|---|
| `image: ghcr.io/kettleofketchup/dota_lobby:${DOTA_LOBBY_TAG:-latest}` | The published image. The tag comes from the `DOTA_LOBBY_TAG` Compose variable and falls back to `latest` when it is unset. There is no `build:` key; see [Using a local build](#using-a-local-build) |
| `container_name: dota_lobby` | A fixed name, so `docker compose up --scale` cannot start a second copy (see the one-instance rule above) |
| `restart: unless-stopped` | Restarts the container if the process exits, until you stop it yourself |
| `ports: "127.0.0.1:8080:8080"` | Publishes the API on localhost only. See [Changing the port](#changing-the-port) and [Exposing it beyond localhost](#exposing-it-beyond-localhost) |
| `volumes: ./config:/app/config:ro` | Mounts your `config/` directory read-only at `/app/config`, the only path the image reads its config from. The directory must be traversable by the container user; see [File permissions](#file-permissions) |
| `read_only: true` | The container's root filesystem is read-only. The service writes nothing to disk, so it needs no writable volume |
| `cap_drop: ALL` | Drops all Linux capabilities |
| `no-new-privileges:true` | The process can never gain privileges (for example through setuid binaries) |
| `stop_grace_period: 30s` | On `docker compose stop` the service gets SIGTERM and 30 seconds to shut down the HTTP server (it allows itself 10s) and disconnect the bots before it is killed |
| `logging:` | The `json-file` log driver, rotated at 10 MB, keeping 3 files |

The image runs as the non-root user UID/GID `65532`. There is no `.env` file and the
service reads no environment variables: everything it needs comes from the two files in
`config/` (see the [Configuration reference](../configuration/index.md)).

## Choosing the image tag

`DOTA_LOBBY_TAG` is a **Compose variable, not an app setting**. Compose uses it to pick the
image; the service never sees it.

Pin it to a full version, such as `1.2.3`. A full version tag never moves, so the container
only changes when you change the variable. The image tags have **no leading `v`**: the git tag
`v1.2.3` is the image tag `1.2.3`, and `DOTA_LOBBY_TAG=v1.2.3` fails with `manifest unknown`.

There are also moving tags: `latest` (the newest release) and `1.2` (the newest `1.2.x`
patch). They change under you on the next `docker compose pull`, which is why the docs pin a
full version. The full list is under [Image tags](docker.md#image-tags).

!!! warning "Set the Compose variables in every shell that runs `docker compose`"

    `DOTA_LOBBY_TAG`, and for the rootful `user:` variant below also `DOTA_LOBBY_UID` and
    `DOTA_LOBBY_GID`, must be set in **every shell that runs `docker compose`**: `pull`, `up`,
    `restart`, `logs`, `stop`, all of them. Either put them in front of each command:

    ```bash
    DOTA_LOBBY_TAG=1.2.3 docker compose up -d
    ```

    or persist them in your shell profile:

    ```bash
    echo 'export DOTA_LOBBY_TAG=1.2.3' >> ~/.bashrc
    ```

    This project does not use a `.env` file. What goes wrong when a variable is unset:

    - unset `DOTA_LOBBY_TAG` silently means `latest`, and `docker compose up -d` recreates the
      container on that image;
    - unset `DOTA_LOBBY_UID` or `DOTA_LOBBY_GID` (only if you added the `user:` line) makes
      every compose command, `restart` included, fail with a
      `required variable DOTA_LOBBY_UID is missing a value` error.

### Using a local build

Before the first release, or to run your own changes, build the image yourself and point
`DOTA_LOBBY_TAG` at it:

```bash
docker build -t ghcr.io/kettleofketchup/dota_lobby:local .
DOTA_LOBBY_TAG=local docker compose up -d
```

See [Build the image locally](docker.md#build-the-image-locally) for build options.

## File permissions

The container runs as UID/GID `65532` and must be able to read both files in `config/`.
Whether it can depends on how your container runtime maps that UID onto your host, so there is
one recipe per runtime below.

Every recipe starts the same way: the `config/` directory itself must be traversable, mode
`755`:

```bash
chmod 755 config
```

A `700` directory, or an SELinux denial, does **not** produce an error. The service treats
the files as missing and starts with defaults: API key authentication off and no bots. If
you see `WARNING: secrets.yaml not found - no bots will be configured` (followed by
`WARNING: No bots configured in secrets.yaml`) or
`WARNING: API key authentication disabled - all endpoints are public` while both files exist,
this is the cause. Do not edit `secrets.yaml` in response to the `No bots configured` line
here: the file is fine, the container just cannot see it.

A readable directory with an unreadable file is fatal instead. `config.yaml` is read first, so
the line you usually see is:

```text
Failed to load configuration: error reading config file: open /app/config/config.yaml: permission denied
```

and the container restarts in a loop until the files are fixed.

!!! warning "Pick exactly one tab. The recipes must not be combined"

    The recipes are alternatives. Mixing them breaks the result. The two easy mistakes:

    - a host `chown 65532` (the rootful recipe) with a **rootless** runtime: under rootless
      Podman or rootless Docker, host UID 65532 is not the container's UID 65532, so the
      container cannot read the files, and you cannot either;
    - the rootful `DOTA_LOBBY_UID` override on **rootless Docker**: it runs the container as a
      UID that maps into your subuid range, which cannot read your files.

### Which runtime do I have?

The `docker` command is not always Docker: on many distributions it is Podman behind a
compatibility wrapper. Run these checks before picking a tab:

| Check | Result | Tab |
|---|---|---|
| `docker version` | the `Client:` line says `Podman Engine`, or `docker` is not installed and you use `podman` | **Rootless Podman** if `podman info --format '{{.Host.Security.Rootless}}'` prints `true`. If it prints `false` (Podman run as root), use **Rootful Docker** |
| `docker info --format '{{.SecurityOptions}}'` | contains `name=rootless` | **Rootless Docker** |
| `docker context ls` | the active context (marked `*`) is `desktop-linux`, or you are on macOS or Windows | **Docker Desktop** |
| none of the above | Docker Engine, run as root (you usually need `sudo` or the `docker` group) | **Rootful Docker** |

Work down the table and stop at the first match. The second check only works on real Docker;
under Podman it fails with a template error, which is why the `docker version` check comes
first. If `docker version` says Podman, do not use
the Rootful Docker tab just because the command is called `docker`: its `sudo chown 65532`
recipe locks you out of the files and the container still cannot read them.

The optional lines below (`user:`, `userns_mode:`, the SELinux `:Z` mount) go in a file named
`compose.override.yaml` next to `compose.yaml`. Compose merges it in automatically, and
keeping it separate means `compose.yaml` stays exactly as released, so
[upgrading](#upgrading) with `git checkout` never conflicts with your changes.

=== "Rootful Docker"

    The default Docker Engine install (no userns-remap). Pick one of the two variants.

    **Variant 1: give the files to UID 65532.**

    ```bash
    chmod 755 config
    sudo chown 65532:65532 config/secrets.yaml config/config.yaml && sudo chmod 600 config/*.yaml
    ```

    **Variant 2: keep the files owned by you and run the container as you.**

    ```bash
    chmod 755 config
    chmod 600 config/*.yaml
    export DOTA_LOBBY_UID=$(id -u) DOTA_LOBBY_GID=$(id -g)
    ```

    and in `compose.override.yaml`:

    ```yaml
    services:
      dota_lobby:
        user: "${DOTA_LOBBY_UID:?}:${DOTA_LOBBY_GID:?}"
    ```

    `DOTA_LOBBY_UID` and `DOTA_LOBBY_GID` are Compose variables like `DOTA_LOBBY_TAG`, not app
    settings, and they must be set in every shell that runs `docker compose` (no `.env` file).
    The `:?` makes Compose fail loudly when they are unset instead of starting the container
    as the wrong user. Do not use `${UID}` and `${GID}` here: in bash `UID` is a read-only,
    unexported shell variable and `GID` does not exist, and in zsh assigning `UID` tries to
    change your process's UID, so Compose would get an empty or partial `user:` value.

    **Editing later:** after variant 1, edit with `sudo -e config/secrets.yaml` (or
    `sudo -e config/config.yaml`); ownership and mode are kept. Variant 2 keeps the files
    owned by you, so edit them normally. Then run `docker compose restart` (for variant 2, in
    a shell where `DOTA_LOBBY_UID`, `DOTA_LOBBY_GID` and `DOTA_LOBBY_TAG` are set; otherwise
    compose fails on the missing variable).

=== "Rootless Podman"

    Podman running as your user, with `docker compose` or `podman compose`. This is the setup
    the project is tested on. Pick one of the two variants.

    **Variant 1 (tested by this project): chown inside your user namespace.**

    ```bash
    chmod 755 config
    chmod 600 config/*.yaml
    podman unshare chown 65532:65532 config/*.yaml
    ```

    `podman unshare` runs `chown` inside your rootless user namespace, so the files end up
    owned by whatever host UID the container's 65532 maps to.

    **Variant 2: keep the files owned by you and map your user onto 65532.**

    ```bash
    chmod 755 config
    chmod 600 config/*.yaml
    ```

    and in `compose.override.yaml`:

    ```yaml
    services:
      dota_lobby:
        userns_mode: "keep-id:uid=65532,gid=65532"
    ```

    Plain `keep-id` is not enough: it maps your user onto your own UID inside the container,
    not onto 65532.

    **Editing later:** after variant 1, edit inside the user namespace, for example
    `podman unshare vi config/secrets.yaml` (any editor works; ownership is kept). Variant 2
    keeps the files owned by you, so edit them normally. Then run `docker compose restart`
    (or `podman compose restart`).

=== "Rootless Docker (untested)"

    Docker Engine in rootless mode. This tab is **untested**: the project's test host only has
    rootless Podman. Under rootless Docker only container UID 0 maps to your host user. Pick
    one of the two variants.

    **Variant 1: chown through a throwaway container.** Its root is your user, so it can chown
    the files into the subuid range:

    ```bash
    chmod 755 config
    chmod 600 config/*.yaml
    docker run --rm -v "$PWD/config:/c" busybox chown 65532:65532 /c/config.yaml /c/secrets.yaml
    ```

    **Variant 2: run as container root**, which is your unprivileged host user. Keep the files
    owned by you at `0600` and add to `compose.override.yaml`:

    ```yaml
    services:
      dota_lobby:
        user: "0:0"
    ```

    `cap_drop: ALL`, `read_only` and `no-new-privileges` still apply.

    Never use the rootful `DOTA_LOBBY_UID` override here. It runs the container as a UID in
    your subuid range, which cannot read your `0600` files, and the service exits with
    `Failed to load configuration: error reading config file: open /app/config/config.yaml: permission denied`
    (config.yaml is read first).

    **Editing later:** after variant 1, hand the files back to yourself with
    `docker run --rm -v "$PWD/config:/c" busybox chown 0:0 /c/config.yaml /c/secrets.yaml`
    (container root is your host user), edit them, then run the
    `chown 65532:65532` command from variant 1 again. Variant 2 keeps the files owned by you,
    so edit them normally. Then run `docker compose restart`.

=== "Docker Desktop (unverified)"

    Docker Desktop on macOS or Windows. This tab is **unverified**. Bind mounts go through
    Docker Desktop's file sharing, so no `chown` is needed. Do not run `sudo chown 65532`: it
    only locks you out of your own files.

    ```bash
    chmod 755 config
    chmod 600 config/*.yaml
    ```

    Keep the files owned by you; on macOS keep them at `chmod 600`. On Windows, use WSL2 and
    follow the Linux tab for the runtime you run inside it.

    **Editing later:** the files stay owned by you; edit them, then run
    `docker compose restart`.

Never fall back to `chmod 644` to make editing easier. It is not recommended: it makes the
Steam passwords readable by every local user on the machine. Use the "Editing later" line of
your tab instead.

**SELinux hosts** (Fedora, RHEL and similar, including Podman on them) also need the `Z`
option on the mount, or SELinux blocks the container from reading `config/` (which looks like
missing files, see above). Add to `compose.override.yaml`:

```yaml
services:
  dota_lobby:
    volumes:
      - ./config:/app/config:ro,Z
```

Compose merges volumes by their container path, so this replaces the mount from
`compose.yaml` instead of adding a second one. If the override file already has a
`dota_lobby:` section (for a `user:` or `userns_mode:` line), add `volumes:` to it rather than
writing a second section.

## Upgrading

The quick start clones one release with `--depth 1`. To move to a new release, check out the
new tag so `compose.yaml` and the example files stay in step with the image, then pull and
recreate:

```bash
git fetch --depth 1 origin tag v1.2.4
git checkout v1.2.4
export DOTA_LOBBY_TAG=1.2.4   # and update your shell profile if you persisted it there
docker compose pull
docker compose up -d
```

- `DOTA_LOBBY_TAG=1.2.4` has no leading `v`, as always.
- `docker compose pull` and `docker compose up -d` with an **unchanged** `DOTA_LOBBY_TAG`
  change nothing: a pinned full-version tag never moves. Changing the variable is what
  upgrades.
- The checkout leaves your `config/` files alone: git ignores them. It also leaves
  `compose.override.yaml` alone if you created one: the file is not part of the repository
  (git lists it as untracked), so no release overwrites it.
- Check the release notes for config changes, and compare `config.yaml.example` and
  `secrets.yaml.example` with your files.

## Logs

```bash
docker compose logs -f
docker compose logs --tail 100
```

The service logs to stdout only. Compose keeps the logs with the `json-file` driver, rotated
at 10 MB and keeping 3 files, so they cannot fill the disk. Podman accepts `json-file` as an
alias for its own `k8s-file` driver.

## Health checks

There is no healthcheck, neither in the image nor in `compose.yaml`, on purpose:

- `/health` always answers `{"status":"healthy",...}` while the process runs, whatever the
  bots are doing, so a healthcheck on it proves only that the HTTP listener is up;
- the image is distroless: it has no shell and no HTTP client to run a check with;
- `restart: unless-stopped` already restarts the container if the process dies.

For a liveness check from outside the container (for example from a monitoring system on the
same host):

```bash
curl -fsS http://127.0.0.1:8080/health
```

Real readiness means checking `GET /bots` (every bot `true`) together with the logs. Even
`true` only means "logged on to Steam", not "connected to the Dota 2 game coordinator" or
"ready to host a lobby"; lobby creation is not implemented yet (see
[Known limitations](../known-limitations.md)).

## Changing the port

Leave `server.port` at `8080` in `config.yaml` and change only the **host** side of the
mapping. For example, to serve on port 9000 in `compose.override.yaml`:

```yaml
services:
  dota_lobby:
    ports: !override
      - "127.0.0.1:9000:8080"
```

`!override` replaces the port list from `compose.yaml` instead of adding to it (Docker
Compose 2.24.4 or newer). You can also edit the line in `compose.yaml` directly, at the cost
of a conflict on the next `git checkout` during an upgrade.

If you change `server.port` anyway, the container side of the mapping must match it
(`127.0.0.1:9000:9000` for `port: 9000`). Otherwise the container starts cleanly but nothing
answers.

## Exposing it beyond localhost

By default the API is reachable only from the machine itself. The service speaks plain HTTP
with no TLS. Before anything else can reach it:

1. set `server.api_key` in `config.yaml` (and check for `API key authentication enabled` in
   the logs after a restart);
2. put a reverse proxy with TLS in front of it.

If the reverse proxy runs on the same machine, keep the localhost binding: the proxy reaches
the service on `127.0.0.1:8080`. A minimal [Caddy](https://caddyserver.com/) `Caddyfile`,
which gets a TLS certificate for the name automatically:

```text
league.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Only change the mapping to `8080:8080` (all interfaces) if the proxy runs on another machine,
and only with `server.api_key` set. Without it, every endpoint is public.

## Hardening already in place

The image and `compose.yaml` already apply:

- a non-root user, UID/GID `65532`, in a distroless image with no shell;
- a read-only root filesystem (`read_only: true`), with config mounted read-only;
- all Linux capabilities dropped (`cap_drop: ALL`);
- `no-new-privileges`;
- the API published on localhost only.

The [`docker run`](docker.md) page has the same settings as `docker run` flags.
