- Multi-bot manager: logs every Steam account listed in `secrets.yaml` on to Steam and
  reconnects them when they drop.
- REST API: `GET /health`, `GET /bots`, `POST /lobby/create` and `GET`/`POST /lobby/info`.
  The lobby endpoints validate requests and pick a free bot but do not create lobbies yet.
- Configuration with Viper, split into `config.yaml` (server settings) and `secrets.yaml`
  (bot credentials).
- Optional API key authentication: set `server.api_key` and send the key as `X-API-Key` or
  `Authorization: Bearer <key>`. `/health` stays public.
- Request validation on `POST /lobby/create`: `lobby_name` is required and at most 100
  characters, `password` at most 50.
- Release workflow that attaches the Linux amd64 binary to a GitHub Release for every `v*`
  tag, and golangci-lint on every pull request and push to `main`.
