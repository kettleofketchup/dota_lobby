Bots now log in headless with a Steam Guard mobile authenticator instead of a bare
password. Before upgrading, set up each bot as described in
[Bot accounts](https://kettleofketchup.github.io/dota_lobby/configuration/bot-accounts/):
add an authenticator with steamguard-cli, then give the service the account's
`shared_secret` alongside the username and password. An account with Steam Guard turned
off, as the old docs advised, should have the authenticator added first.

The service now writes a token store and a lock file to `state_dir`. With Docker Compose,
`compose.yaml` mounts the named volume `state` at `/app/state`; with `docker run`, mount a
writable volume there.

The old `secrets.bots` list and `server.api_key` still load, with a deprecation warning.
Move them to the keyed `secrets.bots.<name>` map and `secrets.api_key`.

`/bots`, `/lobby/create` and `/lobby/info` now return `410 Gone`. Use `/v1/bots`; lobby
endpoints return `501` until lobby support lands.
