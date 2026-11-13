- The bot no longer takes the caster slot before a launch. The game waits for every caster to
  load, and the headless bot never connects, so the first load failed. `lobby.host_seat_strategy`
  now defaults to `[player_pool, self_kick, no_team]`, which keeps the bot in the unassigned
  pool or outside the teams.
