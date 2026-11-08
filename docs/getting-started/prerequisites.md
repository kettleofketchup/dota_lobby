# Prerequisites

!!! warning "Read this before you create any Steam accounts"

    - **Not yet run against live Steam.** Headless login is built and tested against fakes;
      the first live run is pending. See [Known limitations](../known-limitations.md).
    - **Lobbies are not created yet.** The lobby endpoints answer `501`.

    Use dedicated Steam accounts that you can afford to lose, and start with a single bot.

## The host

You need one machine that stays on while your league plays:

- **Linux with a container runtime.** Docker Engine or Podman, plus Docker Compose v2 or newer
  (`docker compose version`). Other platforms are covered on the
  [Docker Compose](../deployment/docker-compose.md#file-permissions) page, but only Linux with
  rootless Podman is tested.
- **A per-user tmpfs at `$XDG_RUNTIME_DIR`** (normally `/run/user/<uid>`, created by systemd
  when you log in). `just docker up` decrypts the bot credentials there. Run the service as a
  normal user with a login session, or set `DOTA_LOBBY_SECRETS_HOST_DIR` to another directory
  whose parent is mode `0700`.
- **An accurate clock.** The service computes Steam Guard codes from the host clock; a clock
  more than about a minute off makes every fresh login fail with EResult 88. Keep NTP running
  (`timedatectl` should say `System clock synchronized: yes`).
- **Outbound access to Steam**: HTTPS to the Steam Web API (logins use it) and TCP 27017 to
  Steam's connection servers, plus working DNS.
- **No inbound ports** apart from the HTTP API. The default `compose.yaml` publishes it on
  `127.0.0.1:8080` only.

## The workstation

Where you set up the bot accounts and keep the encrypted credentials (it can be the same
machine as the host):

- a clone of this repository;
- [just](https://just.systems/), [sops](https://github.com/getsops/sops),
  [age](https://github.com/FiloSottile/age) and [jq](https://jqlang.org/);
- [steamguard-cli](https://github.com/dyc3/steamguard-cli), to link each bot's authenticator
  once (`cargo install steamguard-cli`, or the AUR package). It is never installed in the
  container;
- Go 1.26 or newer for the probe tool (`mise install` gives you the pinned version).

The encrypted files in `secrets/` and the age key must be on the **host** at start, because
`just docker up` decrypts them there.

## Bot accounts

Each bot is a Steam account that:

- is **dedicated** to this service and never used to play;
- is used by **one bot in one running instance** only, and is not logged in anywhere else while
  the bot runs;
- has a **Steam Guard mobile authenticator linked with steamguard-cli**, so that you hold its
  `shared_secret`. The service computes Steam Guard codes from it; Steam Guard stays **on**;
- optionally has US$5 on record, which lifts the limited-account restrictions.

The step-by-step setup is on [Bot accounts](../configuration/bot-accounts.md).

Next: [Quick start](quick-start.md).
