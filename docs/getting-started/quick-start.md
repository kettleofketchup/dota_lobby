# Quick start

This page takes you from nothing to a running container with one bot, using Docker Compose
on a single Linux machine. Check the [Prerequisites](prerequisites.md) first, and have one
bot account ready (Steam Guard off).

The worked example uses release `v1.2.3`. Replace it with the release you picked in step 0.

## 0. Pick a release

Find the latest release on the
[GitHub Releases page](https://github.com/kettleofketchup/dota_lobby/releases), or list the
tags from the command line:

```bash
git ls-remote --tags https://github.com/kettleofketchup/dota_lobby.git
```

Git tags start with a `v` (`v1.2.3`). Image tags do not (`1.2.3`).

!!! note "No releases yet?"

    Before the first release there is no published image. Clone `main` instead
    (`git clone https://github.com/kettleofketchup/dota_lobby.git`), then
    [build the image locally](../deployment/docker.md#build-the-image-locally) and use
    `DOTA_LOBBY_TAG=local` wherever this page says `DOTA_LOBBY_TAG=1.2.3`.

## 1. Get the release and choose the image tag

```bash
git clone --branch v1.2.3 --depth 1 https://github.com/kettleofketchup/dota_lobby.git
cd dota_lobby
export DOTA_LOBBY_TAG=1.2.3
```

`DOTA_LOBBY_TAG` is a Compose variable, not an app setting: `compose.yaml` uses it to pick
the image `ghcr.io/kettleofketchup/dota_lobby:<tag>`. The service itself never reads it.

!!! note "The image tag has no leading `v`"

    The git tag is `v1.2.3`, the image tag is `1.2.3`. Setting `DOTA_LOBBY_TAG=v1.2.3` makes
    the pull fail with `manifest unknown`.

!!! warning "Set `DOTA_LOBBY_TAG` in every shell that runs `docker compose`"

    `export` only lasts for the current shell. The variable must be set in **every shell that
    runs `docker compose`** (`pull`, `up`, `restart`, `logs` and the rest). Either put it in
    front of each command:

    ```bash
    DOTA_LOBBY_TAG=1.2.3 docker compose up -d
    ```

    or persist it in your shell profile:

    ```bash
    echo 'export DOTA_LOBBY_TAG=1.2.3' >> ~/.bashrc
    ```

    If it is unset, Compose silently uses the `latest` tag instead and recreates the container
    on a different image. This project does not use a `.env` file for it; see
    [Upgrading](../deployment/docker-compose.md#upgrading) for how to move to a new version.

## 2. Create the config directory

```bash
mkdir config && chmod 755 config
cp config.yaml.example config/config.yaml
cp secrets.yaml.example config/secrets.yaml
```

The directory must stay at mode `755`: the container runs as UID 65532 and has to be able to
enter it. `config/` is ignored by git, so your credentials never end up in a commit.

## 3. Edit the two files

In `config/secrets.yaml`, replace the one active bot entry with your bot's username and
password. Leave the commented-out second entry alone for now; add more bots only after the
first one logs on.

```yaml
secrets:
  bots:
    - username: "my_league_bot"
      password: "the bot account's password"
```

In `config/config.yaml`, uncomment `api_key` and set it to a long random value (for example
the output of `openssl rand -hex 32`). Leave `host` and `port` as they are. Every key is
explained in the [Configuration reference](../configuration/index.md), and account setup in
[Bot accounts](../configuration/bot-accounts.md).

Then check that no placeholder is left:

```bash
grep -n your_steam config/secrets.yaml
```

This must print **nothing**. A leftover placeholder bot is a real login attempt with a fake
account: it fails, is retried about every 5 seconds forever, and invites
`EResult_RateLimitExceeded` from Steam for your IP address.

## 4. Apply the permission recipe for your runtime

Now that both files are edited, apply the recipe for your container runtime from
[File permissions](../deployment/docker-compose.md#file-permissions). Pick exactly one tab;
the recipes are alternatives and must not be combined.

Some recipes make the files unwritable by you. To change them later (fix a password, add a
bot, change `api_key`), follow the "Editing later" line in the same tab for your runtime, then
apply the change with:

```bash
docker compose restart
```

## 5. Start it and watch the logs

```bash
docker compose up -d
docker compose logs -f
```

A healthy start with one bot looks like this (timestamps and the order of the last lines will
differ):

```text
2026/11/06 12:00:00 Dota Lobby Manager v1.2.3 (commit: <commit>, built: <date>)
2026/11/06 12:00:00 Server configuration: 0.0.0.0:8080
2026/11/06 12:00:00 Added bot: my_league_bot
2026/11/06 12:00:00 Starting API server on 0.0.0.0:8080
2026/11/06 12:00:00 API key authentication enabled
2026/11/06 12:00:01 [my_league_bot] Connected to Steam
2026/11/06 12:00:02 [my_league_bot] Logged on to Steam
```

Check the startup lines before anything else:

- `API key authentication enabled` must be **present** (you set `api_key` in step 3).
- `WARNING: secrets.yaml not found - no bots will be configured` must be **absent**.

If you see `WARNING: API key authentication disabled - all endpoints are public` or the
`secrets.yaml not found` warning while both files exist, the container cannot read the
`config/` directory: that is a directory permission or SELinux mount problem. Stop the
container and fix it using [File permissions](../deployment/docker-compose.md#file-permissions).

!!! danger "Stop at the first failed login"

    A failed login is retried about every 5 seconds per bot, forever. Repeated failures can get
    the account or your IP rate-limited or locked. As soon as you see a
    `[<username>] Failed to log on:` line, run:

    ```bash
    docker compose stop
    ```

    and look the error up in [Troubleshooting](../operations/troubleshooting.md) before you
    start it again.

Press Ctrl+C to leave `docker compose logs -f`; the container keeps running.

## 6. Check the API

```bash
curl -fsS http://127.0.0.1:8080/health
curl -fsS -H 'X-API-Key: <your api_key>' http://127.0.0.1:8080/bots
```

`/health` always answers `{"status":"healthy","time":"..."}` while the process runs, whatever
the bots are doing. `/bots` is the useful one.

**Success** looks like this:

```json
{"bots":{"my_league_bot":true}}
```

`true` means the bot is logged on to Steam. It does **not** mean the bot is connected to the
Dota 2 game coordinator or ready to host a lobby; lobby creation is not implemented yet (see
[Known limitations](../known-limitations.md)).

**Failure** shows `false` for the bot, and the logs contain a line like
`[my_league_bot] Failed to log on: EResult_...`. Stop the container (see above) and look the
`EResult_...` value up in [Troubleshooting](../operations/troubleshooting.md).

## 7. Stop it

```bash
docker compose stop
```

The container and its settings are kept; `docker compose up -d` (with `DOTA_LOBBY_TAG` set)
starts it again.

!!! note "If the image pull fails"

    - `manifest unknown`: first check that `DOTA_LOBBY_TAG` has no leading `v` (`1.2.3`, not
      `v1.2.3`).
    - Still failing, or `unauthorized`: there is no public image for that tag yet.
      [Build the image locally](../deployment/docker.md#build-the-image-locally) and use
      `DOTA_LOBBY_TAG=local`.

## Next steps

- [Configuration reference](../configuration/index.md): every key and how it is loaded
- [Docker Compose](../deployment/docker-compose.md): upgrading, logs, exposing the API safely
- [Troubleshooting](../operations/troubleshooting.md): log lines and what they mean
