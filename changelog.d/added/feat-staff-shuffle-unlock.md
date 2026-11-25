- Lobby admins can run `!shuffle [N]` in lobby chat for 1-5 balanced shuffles, and `!unlock`
  and `!lock` to stop or restart enforcing the roster's sides.
- `teams_unlocked` on create, PATCH and the lobby object keeps the roster but stops moving
  players back to their rostered side. New events `lobby.teams_unlocked`, `lobby.teams_locked`
  and `lobby.teams_shuffled`; `match.started` gains `teams_unlocked`.
