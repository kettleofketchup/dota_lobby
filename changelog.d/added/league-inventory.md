- **League inventory.** `GET /v1/leagues` now shows, for every league, each holder's
  `first_seen_at`, `bot_state`, `ready` and the `lobby_id` it hosts, plus `holders_total` and
  `holders_free` (holders that could take a new lobby now). The existing fields stay.
- `GET /v1/leagues/{id}` returns one league in that shape; `404` when no bot holds it.
- `GET /v1/bots/{name}/leagues` lists a bot's leagues with their source, `verified` and
  `first_seen_at`, the outcome of its last league refresh, and its recent ticket changes
  (`added` / `removed`, newest first).
- `POST /v1/bots/{name}/leagues/refresh` fetches a bot's league list from the game coordinator
  now, within `gc.request_timeout`, so a newly granted ticket counts at once; `503` when the bot
  is not `gc_ready`. `POST /v1/leagues/refresh` does it for every ready bot and reports each
  bot's outcome.
- The database records when each league first appeared for a bot and every league that
  appeared or disappeared; each change is also logged at info as `bot league tickets changed`.
