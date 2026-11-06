# dota_lobby

A Go service for running Dota 2 lobbies for league play. It logs a set of Steam bot accounts
on to Steam and exposes a small REST API, so a league site or tool can check the bots and,
eventually, ask one of them to host a lobby. Settings live in `config.yaml` and bot
credentials in `secrets.yaml`.

> **Status: early version.** Bots log on to Steam, but no lobby is created yet:
> `/lobby/create` and `/lobby/info` are placeholders. Steam login is reported to fail with the
> pinned go-steam library (unverified by this project), and Steam Guard is not supported.
> Read [Known limitations](https://kettleofketchup.github.io/dota_lobby/known-limitations/)
> before you use real accounts.

**Documentation:** <https://kettleofketchup.github.io/dota_lobby/>

## Quick start (Docker Compose)

You need Docker with the Compose plugin and one Steam bot account with Steam Guard turned off.
The full walkthrough, with expected log output, is the
[Quick start](https://kettleofketchup.github.io/dota_lobby/getting-started/quick-start/) page.

The example uses release `v1.2.3`; replace it with the release you want. Git tags start with
a `v` (`v1.2.3`), image tags do not (`1.2.3`): `DOTA_LOBBY_TAG=v1.2.3` fails with
`manifest unknown`.

```bash
git clone --branch v1.2.3 --depth 1 https://github.com/kettleofketchup/dota_lobby.git
cd dota_lobby
export DOTA_LOBBY_TAG=1.2.3

mkdir config && chmod 755 config
cp config.yaml.example config/config.yaml
cp secrets.yaml.example config/secrets.yaml
```

Edit `config/secrets.yaml` (your bot's username and password) and `config/config.yaml` (set
`api_key` to a long random value, for example `openssl rand -hex 32`). Then apply the
[file permission recipe](https://kettleofketchup.github.io/dota_lobby/deployment/docker-compose/#file-permissions)
for your container runtime; the container runs as UID 65532 and must be able to read
`config/`.

```bash
docker compose up -d
docker compose logs -f
curl -fsS -H 'X-API-Key: <your api_key>' http://127.0.0.1:8080/bots
```

`{"bots":{"my_league_bot":true}}` means the bot is logged on to Steam. At the first
`Failed to log on:` line, run `docker compose stop`: failed logins are retried about every
5 seconds, forever. See
[Troubleshooting](https://kettleofketchup.github.io/dota_lobby/operations/troubleshooting/).

Before the first release there is no published image; build one locally as described in
[Docker](https://kettleofketchup.github.io/dota_lobby/deployment/docker/#build-the-image-locally)
and use `DOTA_LOBBY_TAG=local`.

## Build from source

Requires Go (see `go.mod` for the version).

```bash
make build
./bin/dota_lobby
```

The binary looks for `config.yaml` and `secrets.yaml` in `.`, `./config` and
`$HOME/.config/dota_lobby`. See
[Building from source](https://kettleofketchup.github.io/dota_lobby/deployment/from-source/).

## Development

Build, test, lint, docs preview and the release process are described on the
[Development](https://kettleofketchup.github.io/dota_lobby/development/) page.

## License

See LICENSE file for details.

## References

- [go-dota2](https://github.com/paralin/go-dota2) - Dota 2 client implementation
- [go-steam](https://github.com/paralin/go-steam) - Steam client implementation
- [Viper](https://github.com/spf13/viper) - Configuration management
