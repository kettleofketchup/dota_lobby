The database gains the `lobbies`, `lobby_players` and `lobby_events` tables. The migration runs
by itself at startup; a database written by this release is refused by older ones.

The default `lobby.host_seat_strategy` changed to `[caster, self_kick]`. A config that sets the
key keeps its own list. Which strategy lets a launch reach a game has not been confirmed live
yet; if launches keep failing with the caster slot, put another strategy first:

```yaml
lobby:
  host_seat_strategy: [player_pool, self_kick, caster]
```

The lobby passwords are stored in the database (`store.path`, mode `0600`); include it in
whatever protects the state volume.
