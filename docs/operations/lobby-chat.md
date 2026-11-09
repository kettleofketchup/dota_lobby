# Lobby chat

With `lobby.chat.enabled: true`, the bot that hosts a lobby joins the lobby's chat and answers
commands typed there. Players can see who is missing, say they are ready and start the match
themselves; admins can also start a lobby that is not ready and stop a start.

Chat is off by default. Lobbies work the same without it, except that the ready check is skipped
(see [The ready check](#the-ready-check)).

## Turning it on

```yaml
lobby:
  admins: [11111111]        # account IDs allowed admin commands in every lobby
  chat:
    enabled: true
    prefix: "!"             # what every command starts with
    send_interval: 1s       # between two bot messages in one lobby
    start_countdown: 10s    # from !start to the launch
```

Each lobby can add its own admins with `admins` in [`POST /v1/lobbies`](../api/index.md#post-v1lobbies).
Admin and player IDs are 32-bit Steam account IDs, the number in a Dotabuff or OpenDota profile
link.

The prefix cannot start with `/`: the Dota client treats text that starts with `/` as a
console command and never sends it to the chat.

## What the bot does in the chat

- It joins once the lobby exists and says hello with a pointer to `!help`. After the bot loses
  its game coordinator session it joins again on its own, as long as the lobby is still
  `waiting` or `ready`. Joining never holds up the lobby: API calls, invites and the launch go
  on while it runs. A join gets at most 5 s (or `gc.request_timeout`, if shorter).
- It says when everyone is set (`Type !start to launch`, or the launch delay with
  `launch: auto`) and when a launch did not reach a game.
- When it moves a rostered player who sat on the wrong side back to the pool, it tells them
  which side is theirs.
- It leaves the chat when the lobby ends.

Messages are sent at most one per `lobby.chat.send_interval`. If more than ten are waiting,
the oldest are dropped.

## Commands

Commands are case-insensitive, and anything after the command is ignored. Only members of the
lobby can use them. Who sent a command is decided by the account ID the game coordinator puts on
the message, never by the name shown in the chat, which anyone can change.

| Command | Who | What it does |
|---|---|---|
| `!help` | Anyone in the lobby | Lists the commands this player can use |
| `!teams` | Anyone in the lobby | Per side: who is seated, who is missing, who sits on the wrong side and, with the ready check, how many are ready |
| `!ready` | A rostered player seated on their own side | Marks them ready. Only with the ready check |
| `!unready` | A rostered player | Takes their ready mark back. Only with the ready check |
| `!start` | A rostered player while the lobby is `ready`; an admin while it is `waiting` or `ready` | Announces the start, waits `lobby.chat.start_countdown` (10 s), then launches |
| `!cancel` | An admin, during the countdown | Stops the start |

A start by an admin is a forced launch: it does not wait for the roster or the ready check. No
chat start, not even an admin's, launches once `lobby.max_launch_attempts` launches have been
sent; only [`POST /v1/lobbies/{id}/launch`](../api/index.md#post-v1lobbiesidlaunch) with `{"force": true}` can try
again. No start launches while the bot itself still sits on Radiant or Dire (see
[Host seat](../api/index.md#host-seat)). A start by a player stops on its own if the lobby
stops being ready before the countdown ends.

To keep the chat readable, the bot answers `!help` once per player every 30 s, `!teams` every
10 s, and a refused command once every 10 s. `!ready` and `!unready` share a 3 s cooldown: a
player can change their mark once every 3 s, and commands inside that window get no answer.

## The ready check

A lobby created with `"ready_check": true` and a roster is only `ready` once every rostered
player who is seated has typed `!ready`. A player who leaves the lobby or their side loses the
mark (`lobby.player_unready` with `reason` `left` or `moved`). Without chat, or in an open lobby
without a roster, there is nothing to type `!ready` with, so `ready_check` is ignored and the
lobby is `ready` as soon as everyone is seated.

The ready marks are in each player's `ready` field in
[`GET /v1/lobbies/{id}`](../api/index.md#the-lobby-object), and every change is a
`lobby.player_ready` or `lobby.player_unready` event.

## Logs

Every command is logged as `chat command` with the lobby ID, the sender's account ID and the
command. The text of messages and the names players chose are not logged.

## When the bot does not answer

See [The bot does not answer in the lobby chat](troubleshooting.md#the-bot-does-not-answer-in-the-lobby-chat).
