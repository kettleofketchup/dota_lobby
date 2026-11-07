# Prerequisites

!!! warning "Read this before you create any Steam accounts"

    - **Steam login is reported to fail.** Login with the pinned go-steam library is reported
      to fail against current Steam with `EResult_InvalidPassword`, even with the right
      password ([go-dota2#78](https://github.com/paralin/go-dota2/issues/78)). This project
      has not verified that report with a real account. Expect about 10 minutes of account
      setup before your first login attempt, and know that `InvalidPassword` at the end of it
      may be this problem rather than a typo.
    - **Lobbies are not created.** `POST /lobby/create` is a placeholder; no Dota 2 lobby is
      made.

    Use throwaway Steam accounts that you can afford to lose, and start with a single bot.
    See [Known limitations](../known-limitations.md) for the full list.

## The host

You need one machine that stays on while your league plays:

- **Linux with a container runtime.** Docker Engine or Podman, plus Docker Compose v2
  (`docker compose version` should print `v2.x` or later). Other platforms are covered on
  the [Docker Compose](../deployment/docker-compose.md#file-permissions) page, but only Linux
  is tested.
- **Outbound TCP 27017 to Steam.** The bots connect to Steam connection managers (CMs) on
  port 27017. The CM addresses come from a list built into the go-steam library, and that
  list can go stale; if none of its servers answer, the bots cannot connect.
- **Working DNS.**
- **An accurate clock.** Keep the host's time synchronised (NTP, for example
  `systemd-timesyncd` or `chrony`).
- **No inbound ports** apart from the HTTP API. The default `compose.yaml` publishes the API
  on `127.0.0.1:8080` only, so it is reachable from the host itself and nowhere else.

## Bot accounts

Each bot is a Steam account that the service logs in with a username and password. Prepare
every account before you put it in `secrets.yaml`:

- **Dedicated.** The account is used for nothing but this service. Do not use your
  personal account.
- **One account per bot.** Never list the same account twice, and never share one account
  between two running instances.
- **Steam Guard is off.** Email codes and the mobile authenticator both block login; the
  service cannot answer a Steam Guard prompt.
- **Owns Dota 2 and has launched it once.** Dota 2 is free; add it to the account and start
  it once from a normal Steam client.
- **Not a limited account (recommended).** Limited accounts (no purchase on record) have
  restrictions that may get in the way.
- **Not logged in anywhere else while the bot runs.** A desktop or phone login on the same
  account kicks the bot off, and the two keep kicking each other off.

More detail on each point, including the error each mistake produces, is on
[Bot accounts](../configuration/bot-accounts.md).

Next: [Quick start](quick-start.md).
