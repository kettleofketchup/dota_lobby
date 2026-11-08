# Quick start

This page takes you from nothing to a running container with one bot, using Docker Compose on
a single Linux machine. Check the [Prerequisites](prerequisites.md) first.

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
    (`git clone https://github.com/kettleofketchup/dota_lobby.git`), build the image with
    `just docker build`, and use `DOTA_LOBBY_TAG=local` wherever this page says
    `DOTA_LOBBY_TAG=1.2.3`.

## 1. Get the release and choose the image tag

```bash
git clone --branch v1.2.3 --depth 1 https://github.com/kettleofketchup/dota_lobby.git
cd dota_lobby
export DOTA_LOBBY_TAG=1.2.3
```

`DOTA_LOBBY_TAG` is a Compose variable, not an app setting: `compose.yaml` uses it to pick
the image `ghcr.io/kettleofketchup/dota_lobby:<tag>`. It has **no leading `v`**:
`DOTA_LOBBY_TAG=v1.2.3` makes the pull fail with `manifest unknown`.

!!! warning "Set `DOTA_LOBBY_TAG` in every shell that runs `docker compose` or `just docker`"

    If it is unset, Compose silently uses the `latest` tag. Put it in front of each command
    (`DOTA_LOBBY_TAG=1.2.3 just docker up`) or persist it in your shell profile
    (`echo 'export DOTA_LOBBY_TAG=1.2.3' >> ~/.bashrc`).

## 2. Set up the bot account

Follow [Bot accounts](../configuration/bot-accounts.md) for one bot. At the end you have:

- `secrets/bots/<bot>.sops.yaml` with `STEAM_USER`, `STEAM_PASS`, `STEAM_SHARED_SECRET` and
  `STEAM_REFRESH_TOKEN` set (check with `just secrets list`);
- the authenticator's revocation code in your password manager.

The rest of this page calls the bot `league_bot_1`.

## 3. Write `config/config.yaml`

```bash
mkdir config && chmod 755 config
cat > config/config.yaml <<'EOF'
bots:
  - name: league_bot_1
EOF
chmod 644 config/config.yaml
```

`name` must match the sops file name (`secrets/bots/league_bot_1.sops.yaml`). Everything else
keeps its default; `config.yaml.example` lists every key and the
[Configuration reference](../configuration/index.md) explains them. `config.yaml` holds no
secrets, so it can stay readable.

## 4. Set an API key

Keep it in sops next to the bot credentials:

```bash
sops edit secrets/service.sops.yaml
```

```yaml
secrets:
  api_key: "<paste the output of: openssl rand -hex 32>"
```

Keep the value at hand as `$KEY` for the `curl` commands below.

## 5. Start it

```bash
just docker up
just docker logs
```

`just docker up` decrypts the sops files into `$XDG_RUNTIME_DIR/dota_lobby` (a tmpfs) and runs
`docker compose up -d`. A healthy start with one bot logs lines like these (shown here with
`log.format: text`; the default is JSON with the same fields):

```text
level=INFO msg="starting dota_lobby" version=1.2.3 ... bots=1 state_dir=/app/state
level=INFO msg="bots started" count=1 login_stagger=10s
level=INFO msg="API listening" addr=[::]:8080
level=INFO msg="token store seeded from configured refresh token" bot=league_bot_1
level=INFO msg="bot state changed" bot=league_bot_1 from=disconnected to=connecting
...
level=INFO msg="bot state changed" bot=league_bot_1 from=gc_hello to=gc_ready
```

Check for two things:

- `API key authentication disabled` must be **absent**; if you see it, step 4 did not take
  effect;
- `no bots configured` must be **absent**; if you see it, check that `just docker up` said
  `decrypted 1 bot file(s)`.

A bot that cannot log in does not retry in a tight loop. It backs off, cools down or stops in
`needs_attention`, depending on what Steam answered; look its `reason` up in
[Troubleshooting](../operations/troubleshooting.md).

Press Ctrl+C to leave the logs; the container keeps running.

## 6. Check the API

```bash
curl -s http://127.0.0.1:8080/readyz
curl -s -H "X-API-Key: $KEY" http://127.0.0.1:8080/v1/bots
```

**Success** is `"status":"ready"` from `/readyz` and `"state":"gc_ready"` for the bot: it is
logged in to Steam and connected to the Dota 2 game coordinator. Until then `/readyz` answers
`503` with `"status":"not_ready"`.

**Anything else** shows in the bot's `state`, `reason` and `last_error`. See
[Troubleshooting](../operations/troubleshooting.md).

`/livez` answers `200` as long as the process is healthy, whatever Steam is doing; the
container healthcheck uses it (`just docker ps` shows `healthy`).

## 7. Stop it

```bash
just docker down
```

This stops and removes the container and deletes the decrypted files. The token store in the
`dota_lobby_state` volume is kept, so the next `just docker up` logs in with the stored token.

!!! note "After a reboot"

    The decrypted files live on a tmpfs, so they are gone after a reboot and the container
    cannot start on its own. Run `just docker up` again.

## Next steps

- [Docker Compose](../deployment/docker-compose.md): credentials, the state volume, upgrades and
  exposing the API safely
- [Configuration reference](../configuration/index.md): every key and how it is loaded
- [Troubleshooting](../operations/troubleshooting.md): EResults, `needs_attention` and the retry
  endpoint
