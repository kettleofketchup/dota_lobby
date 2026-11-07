# docker run

If you do not want Compose, this `docker run` command starts the same container as
[`compose.yaml`](docker-compose.md), setting for setting. Run it from the directory that
contains your `config/` directory (prepared as in the [Quick start](../getting-started/quick-start.md)
steps 2 to 4):

```bash
docker run -d --name dota_lobby --restart unless-stopped --stop-timeout 30 \
  --log-opt max-size=10m --log-opt max-file=3 \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  -v "$PWD/config:/app/config:ro" -p 127.0.0.1:8080:8080 \
  ghcr.io/kettleofketchup/dota_lobby:1.2.3
```

Replace `1.2.3` with the release you want, without a leading `v`. With Podman, the same
command works as `podman run`.

The same rules as with Compose apply: the `config/` directory must be mode `755`, the files
must be readable by the container user, and you must run only
[one instance per set of Steam accounts](docker-compose.md).

## File permissions with `docker run`

Apply the recipe for your runtime from [File permissions](docker-compose.md#file-permissions).
The recipes that need an extra Compose line map to these `docker run` flags (pick one, never
combine them):

| Recipe | `docker run` flag |
|---|---|
| Rootful Docker, files owned by you | `--user "$(id -u):$(id -g)"` |
| Rootless Podman, files owned by you | `--userns=keep-id:uid=65532,gid=65532` |
| Rootless Docker (untested), files owned by you | `--user 0:0` |

On SELinux hosts, mount with `-v "$PWD/config:/app/config:ro,Z"`.

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
docker build -t ghcr.io/kettleofketchup/dota_lobby:local .
```

Then use the tag `local`: `DOTA_LOBBY_TAG=local docker compose up -d` with Compose, or
`ghcr.io/kettleofketchup/dota_lobby:local` as the last argument of `docker run`.

The version printed in the first log line defaults to `dev`. To set it, pass the version
without a leading `v`:

```bash
docker build --build-arg VERSION=1.2.3 -t ghcr.io/kettleofketchup/dota_lobby:local .
```

`COMMIT` and `BUILD_DATE` build arguments fill in the commit and build date in the same line;
they default to `unknown`. The build only copies the Go sources into the image: your
`config/` directory, `config.yaml`, `secrets.yaml` and the docs are excluded by
`.dockerignore`, so credentials never end up in an image layer.

## Logs, stop and remove

```bash
docker logs -f dota_lobby
docker stop dota_lobby
docker start dota_lobby
docker rm dota_lobby
```

`docker stop` sends SIGTERM and waits up to 30 seconds (`--stop-timeout 30`) while the service
shuts down its HTTP server and disconnects the bots. Config is read once at startup, so after
editing a file run `docker restart dota_lobby`.

To move to another version, stop and remove the container, then run the `docker run` command
again with the new tag. Your `config/` directory is not part of the container and is kept.
