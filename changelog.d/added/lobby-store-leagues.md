- **League discovery.** Each bot asks the game coordinator which leagues its account
  administers when it reaches `gc_ready` and every `league.refresh_interval` (30 min).
  `bots[].leagues` from the config is an unverified fallback until that list is known, or
  always with `league.allow_unverified: true`.
- `GET /v1/leagues` lists every league a bot may host and which bots hold it, with the source
  (`gc` or `config`) and when the list was fetched. `GET /v1/leagues/{id}/nodes` returns a
  league's lobby nodes through a ready bot that holds it, falling back to the next ready holder
  when one fails, within `gc.request_timeout` (capped at 12 s) including queue wait.
- `/v1/bots` and `/v1/bots/{name}` gain `ready_since`, `leagues` and `lobby` (always `null`
  until lobbies are created).
- **SQLite database** at `store.path` (default `<state_dir>/dota_lobby.db`, mode `0600`),
  migrated automatically at startup. It keeps the league lists across restarts. A database
  written by a newer release is refused.
