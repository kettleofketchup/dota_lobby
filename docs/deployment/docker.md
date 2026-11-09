# docker run

If you do not want Compose, these commands start the same container as
[`compose.yaml`](docker-compose.md), setting for setting. Run them from your clone, with
`config/config.yaml` in place and the bot credentials set up as in
[Bot accounts](../configuration/bot-accounts.md).

First decrypt the credentials onto the host tmpfs (the same step `just docker up` does), then
start the container:

```bash
just docker decrypt   # writes $XDG_RUNTIME_DIR/dota_lobby/{creds/<bot>.yaml,secrets.yaml}

docker volume create dota_lobby_state
docker run -d --name dota_lobby --restart unless-stopped --stop-timeout 30 \
  --log-opt max-size=10m --log-opt max-file=3 \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  -e DOTA_LOBBY_CREDENTIALS_DIR=/run/secrets/dota_lobby/creds \
  -v "$PWD/config:/app/config:ro" \
  -v "$XDG_RUNTIME_DIR/dota_lobby:/run/secrets/dota_lobby:ro" \
  -v dota_lobby_state:/app/state \
  -p 127.0.0.1:8080:8080 \
  ghcr.io/kettleofketchup/dota_lobby:1.2.3
```

Replace `1.2.3` with the release you want, without a leading `v`. With Podman, the same
commands work as `podman volume create` and `podman run`. The default command is `serve`;
add `serve` (or another subcommand) after the image name to be explicit.

The same rules as with Compose apply: keep the `dota_lobby_state` volume across upgrades, run
`just docker decrypt` again after a reboot, and run only
[one instance per set of Steam accounts](docker-compose.md).

Before an upgrade, snapshot the database with the backup subcommand; it takes no lock, so it
runs next to the service (see [Backups](docker-compose.md#backups)):

```bash
docker exec dota_lobby /usr/local/bin/dota_lobby backup --out /app/state/backups/
docker cp dota_lobby:/app/state/backups/dota_lobby-manual-<time>.db ./
```

When you remove the container for good, delete the decrypted files with `just docker wipe`.

## What the image contains

| Item | Value |
|---|---|
| Base | `gcr.io/distroless/static-debian13:nonroot`: no shell, no package manager |
| Binary | `/usr/local/bin/dota_lobby`, static, built with `CGO_ENABLED=0` |
| User | `65532:65532` |
| Working directory | `/app`, so `config.yaml` is read from `/app/config/` |
| `ENV` | `DOTA_LOBBY_STATE_DIR=/app/state` |
| `/app/state` | Empty, owned by `65532`. A new named volume mounted there inherits that ownership |
| `ENTRYPOINT` / `CMD` | `["/usr/local/bin/dota_lobby"]` / `["serve"]` |
| `HEALTHCHECK` | `dota_lobby healthcheck` every 30 s (timeout 5 s, start period 10 s, 3 retries); see [Health checks](docker-compose.md#health-checks) |
| `EXPOSE` | `8080` |

There are no sops or age tools or keys in the image, and `.dockerignore` keeps `config/`,
`secrets.*`, the docs and the tests out of the build context.

## Running the other subcommands

The binary has more subcommands than `serve`. In the running container:

```bash
docker exec -it dota_lobby /usr/local/bin/dota_lobby auth --bot <bot> --force
docker exec dota_lobby /usr/local/bin/dota_lobby version
```

`auth` needs `--force` while `serve` holds the state lock. Then call
`POST /v1/bots/<bot>/retry`, see
[Re-authenticating a bot](docker-compose.md#re-authenticating-a-bot).

## Secret files you keep yourself

If you mount secret files that are not written by `just docker decrypt` (an old
`config/secrets.yaml`, or `*_FILE` targets), the container user must be able to read them.
Apply the recipe for your runtime from [File permissions](docker-compose.md#file-permissions).
On SELinux hosts, add `,Z` to the mount options (`-v "$PWD/config:/app/config:ro,Z"`).

## Image tags

Images are published to `ghcr.io/kettleofketchup/dota_lobby` when a release tag is pushed.
Image tags have **no leading `v`**: the git tag `v1.2.3` becomes the image tag `1.2.3`.

| Tag | Meaning |
|---|---|
| `1.2.3` | Exactly that release. Never moves; use this one |
| `1.2` | The newest `1.2.x` release. Moves on every patch release |
| `1` | The newest `1.x.y` release. Only published from `v1` onwards, not for `0.x` releases |
| `latest` | The newest release (pre-releases excluded). Moves on every release |
| `sha-<short>` | The image built from that commit |

`latest`, `1.2` and `1` only exist once a release without a pre-release suffix has been
tagged. If the tag you want is not published yet, pull a specific version or
[build the image locally](#build-the-image-locally).

## Platforms

Images are published for `linux/amd64` and `linux/arm64`. Docker and Podman pick the right
one for your machine automatically.

## Build the image locally

From a clone of the repository:

```bash
just docker build
```

This runs `docker build -t ghcr.io/kettleofketchup/dota_lobby:local .` with the version,
commit and build date from git, and adds `--format docker` under Podman so the
`HEALTHCHECK` is kept. By hand:

```bash
docker build -t ghcr.io/kettleofketchup/dota_lobby:local .
```

Then use the tag `local`: `DOTA_LOBBY_TAG=local just docker up` with Compose, or
`ghcr.io/kettleofketchup/dota_lobby:local` as the last argument of `docker run`.

The version in the `starting dota_lobby` log line and in `/health` defaults to `dev`. To set it, pass the version
without a leading `v`:

```bash
docker build --build-arg VERSION=1.2.3 -t ghcr.io/kettleofketchup/dota_lobby:local .
```

`COMMIT` and `BUILD_DATE` build arguments fill in the commit and build date in the same line;
they default to `unknown`. The builder stage uses the Go version pinned in `mise.toml`. The
build only copies `go.mod`, `go.sum`, `cmd/` and `pkg/` into the image: your `config/`
directory, `secrets/`, `secrets.*` and the docs are excluded by `.dockerignore`, so
credentials never end up in an image layer.

## Logs, stop and remove

```bash
docker logs -f dota_lobby
docker stop dota_lobby
docker start dota_lobby
docker rm dota_lobby
```

`docker stop` sends SIGTERM and waits up to 30 seconds (`--stop-timeout 30`). The service
fails `/readyz`, finishes in-flight requests (up to 5 seconds), then disconnects every bot in
parallel (up to 15 seconds) and releases the state lock, and exits `0`. Config and
credentials are read once at startup, so after editing a file run `just docker decrypt` (for
sops files) and `docker restart dota_lobby`.

To move to another version, stop and remove the container, then run the `docker run` command
again with the new tag. `config/` and the `dota_lobby_state` volume are not part of the
container and are kept.
