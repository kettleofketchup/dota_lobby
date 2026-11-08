- **Lobby chat.** With `lobby.chat.enabled`, the bot joins each lobby's chat (again after a
  reconnect) and answers `!help`, `!teams`, `!ready`, `!unready` and `!start`, and `!cancel`
  for admins (`lobby.admins` and the lobby's `admins`). Senders are identified by the account
  ID the game coordinator puts on the message, never by their chat name, and only lobby members
  are answered.
- `!start` launches after `lobby.chat.start_countdown` (10 s). A player can start a `ready`
  lobby; an admin's start is a forced launch from `waiting` too. A player's start stops if the
  lobby stops being ready.
- With `"ready_check": true`, a rostered lobby is only `ready` once every seated player typed
  `!ready`. Players lose the mark when they leave their side, shown in their `ready` field and
  in `lobby.player_ready` / `lobby.player_unready` events.
- The bot tells a player it moved off the wrong side which side is theirs, and says when
  everyone is set. Its messages are spaced by `lobby.chat.send_interval` (1 s).
- New keys: `lobby.chat.prefix`, `lobby.chat.send_interval`, `lobby.chat.start_countdown`.
  `lobby.chat.enabled` and `lobby.admins` are now used.
