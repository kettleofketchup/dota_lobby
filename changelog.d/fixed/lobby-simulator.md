- Chat commands said in the moment after the bot joined the lobby chat are no longer lost. A
  line could reach the lobby before the join was applied and was dropped, so a `!ready` sent
  right away never counted.
