- **Lobby hosting.** `POST /v1/lobbies` accepts a lobby and answers `202` in `pending`; a free
  bot that holds the league creates it in the background, and a lobby that comes back with a
  different league is destroyed and fails as `league_rejected`. `external_ref` makes the
  request idempotent: the same request returns the existing lobby, a different one is
  `409 conflict`, and the ref is free again once the lobby ends. A league no bot holds is
  `422 league_not_held`.
- `GET /v1/lobbies` (filters, cursor paging), `GET`, `PATCH` and `DELETE /v1/lobbies/{id}`,
  and `invite`, `kick`, `roster`, `launch`, `teams/flip` and `teams/shuffle` under it. The
  lobby password is only in the create and `GET /v1/lobbies/{id}` responses.
- Rostered players get one invite each, recorded before it is sent so a restart never repeats
  it; resends are limited by `lobby.invite.min_resend` and `max_per_player`. Players on the
  wrong side and strangers on a side are moved back to the pool, at most once per
  `lobby.enforce_cooldown` per member.
- Before every launch the bot leaves its player slot with the `lobby.host_seat_strategy` list,
  each strategy confirmed in the lobby. A launch that does not reach a game returns the lobby to
  `waiting` and the next launch uses the next strategy, up to `lobby.max_launch_attempts`.
  `launch: auto` launches `lobby.auto_launch_delay` after the lobby is ready.
- The match ID and the lobby's outcome (winner side and `team_ref`, marked provisional) are
  recorded, and every change is stored as a numbered lobby event.
- Lobbies survive a restart: each one gets its bot back and carries on, or fails as
  `lobby_lost` / `lost_in_game` when the lobby is gone. With `lobby.destroy_on_shutdown`,
  lobbies that have not launched are destroyed at shutdown instead.
- New keys: `lobby.host_seat_confirm_timeout`, `max_launch_attempts`, `pending_timeout`,
  `auto_launch_delay`, `postgame_linger`, `enforce_cooldown`, `readopt_timeout`,
  `drain_timeout`, `destroy_on_shutdown`, `admins`, and `store.retention`. `gc.create_timeout`
  and the existing `lobby.*` timeouts are now used.
