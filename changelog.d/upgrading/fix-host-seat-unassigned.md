`caster` is no longer a valid `lobby.host_seat_strategy`, and the service refuses to start with
it. Remove it from your config, or drop the key to use the new default:

```yaml
lobby:
  host_seat_strategy: [player_pool, self_kick, no_team]
```

Lobby chat is now on by default: the hosting bot joins each lobby's chat and answers commands
such as `!help`, `!ready` and `!start`. Set `lobby.chat.enabled: false` to keep it off.
