- Built on the upstream go-steam and go-dota2 libraries with Go 1.26 or newer, replacing the
  pinned versions whose password login Steam no longer accepts.
- The container image builds with Go 1.27.1 and keeps a writable state volume at
  `/app/state`; the root filesystem stays read-only.
- `dota_lobby healthcheck` reads `server.port` from the same config as `serve`, over loopback,
  and no longer needs `DOTA_LOBBY_SERVER_PORT` when the port is set in `config.yaml`.
- A credentials file that sets one field under two names (for example `STEAM_PASS` and
  `password`) with different values is rejected instead of picking one.
- A `STEAM_REFRESH_TOKEN` that is not a JWT is accepted as the seed, with an unknown expiry.
- A graceful stop sends `SetPlaying(false)` before closing the connection, and stopping a bot
  no longer waits past its budget for a stuck game coordinator request.
- Background token renewal runs only while a bot is `gc_ready` with an unexpired token. If a
  reconnect needs a password login while a renewal is running, it waits for that renewal
  instead of submitting the same Steam Guard code twice. A renewed token is used at the next
  logon rather than being replaced by a new password login.
- A refresh token Steam has refused is remembered (as a hash, in the bot's state directory)
  and is not seeded again from `STEAM_REFRESH_TOKEN` on a retry or restart; a new seed is
  accepted.
- Logon stagger slots are held one per bot, so retries and backoff loops no longer push
  other bots' first logons later.
- `POST /v1/bots/{name}/retry` answers `503` instead of hanging when the bot's run loop is
  backed up, and stopping a bot no longer waits out the whole shutdown budget when Steam
  never confirms the disconnect.
