- `lobby.host_seat_strategy` defaults to `[caster, self_kick]` (was
  `[self_kick, player_pool, spectator, caster]`), and `no_team` is a valid strategy.
- `/v1/lobbies` no longer answers `501`, and `lobby` in `/v1/bots` names the lobby a bot hosts.
