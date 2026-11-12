- Simulation mode for integration tests of consumers: `serve --simulate`,
  `simulation.enabled: true` or `DOTA_LOBBY_SIMULATION_ENABLED=true` runs the bots against an
  in-memory Steam and game coordinator. The bots need no credentials and reach `gc_ready` at
  once; lobbies are created with the requested details (league read-back included) and
  reported exactly as live, so the lobby manager, seating enforcement, chat commands, the
  database and signed webhooks run unchanged. The service refuses to start in simulation mode
  when any bot has a password, shared secret or refresh token, logs a `SIMULATION MODE`
  warning, and uses its own database (`<state_dir>/dota_lobby-simulation.db`) and backup
  directory unless `store.path` and `store.backup.dir` are set. The image is unchanged.
- `/v1/sim` control operations (simulation mode only, API key required, tag Simulation in
  the OpenAPI document): list and get simulated lobbies; make a player join, take a seat, say
  the ready command, chat, or leave; start a launched lobby's match; end it with a radiant,
  dire or not_scored result; make a lobby fail (`lost` or `load_failed`); read and change the
  auto-mode settings.
- Auto mode: `simulation.auto_join` and `auto_join_delay` make invited players join and sit on
  their rostered side, `auto_ready` makes them say the ready command, `auto_start` and
  `auto_start_delay` start a launched lobby's match; `simulation.bots[]` sets a bot's
  simulated account ID and leagues.
- `GET /health` and `GET /v1/bots` report `simulation`.
