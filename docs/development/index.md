# Overview

This page is for people changing the code or the docs. To run the service, start with the
[quick start](../getting-started/quick-start.md) instead.

## Repository layout

| Path | What it holds |
|---|---|
| `cmd/dota_lobby/` | Entry point and subcommands: `serve`, `auth`, `healthcheck`, `version` |
| `pkg/config/` | Config loading with its own Viper instance: files, credentials files, `DOTA_LOBBY_*` env, `*_FILE`, validation, the `Secret` type |
| `pkg/gc/` | The only package that imports go-steam and go-dota2. `gc.Client` is the seam; `pkg/gc/fake` is a scripted fake for tests |
| `pkg/auth/` | Token store, Steam Guard TOTP, the EResult classifier, the state-dir `flock` |
| `pkg/bot/` | The per-bot state machine (`next()`, a pure function), its supervisor, the GC request worker and the manager |
| `pkg/api/` | HTTP API: health endpoints, `/v1/bots`, the error envelope |
| `tools/probe/` | Phase 0 live probe against real Steam. A **separate Go module** |
| `just/` | just modules: `dev`, `go`, `docs`, `docker`, `secrets`, `probe` |
| `docs/` | This site (MkDocs Material). `docs/plans/` and `docs/research/` are not published |
| `Dockerfile`, `compose.yaml` | Image and single-machine deployment, see [Docker Compose](../deployment/docker-compose.md) |
| `Dockerfile.docs`, `mkdocs.yml`, `pyproject.toml`, `uv.lock` | The docs toolchain |
| `mise.toml` | Pinned Go and golangci-lint versions |

The design is in
[`docs/plans/2026-11-06-lobby-service-architecture.md`](https://github.com/kettleofketchup/dota_lobby/blob/main/docs/plans/2026-11-06-lobby-service-architecture.md),
and what Phase 1 delivered, with the open live-Steam questions, in
[`docs/plans/2026-11-07-phase1-progress.md`](https://github.com/kettleofketchup/dota_lobby/blob/main/docs/plans/2026-11-07-phase1-progress.md).

## Toolchain

`go.mod` needs Go 1.26.0 or newer, because the upstream go-steam and go-dota2 masters do.
`mise.toml` pins the versions everyone uses:

```toml
[tools]
go = "1.27.1"
golangci-lint = "2.15.0"
```

Run `mise install` once. `make` and the `just go` recipes run `go` and `golangci-lint` through
`mise exec` when mise is installed. CI, the Dockerfile's builder stage and `mise.toml` use the
same Go version; change them together.

golangci-lint must be built with a Go at least as new as the one you compile with: 2.11.x is
built with Go 1.26 and panics on the 1.27 standard library, 2.15.0 works.

## Bootstrap

```bash
./dev
```

`./dev` installs [just](https://just.systems/) into `~/.local/bin` if it is missing, then runs
`just dev`, which checks for `go` and `docker`, downloads the modules and lists the recipes.

## Build, test and lint

| Task | make | just |
|---|---|---|
| Build `bin/dota_lobby` | `make build` | `just go build` |
| Build and run `serve` | `make run` | `just go run` |
| Tests (`go test -race ./...`) | `make test` | `just go test` |
| Tests with coverage | | `just go test-full` |
| Lint | `make lint` | `just go lint` |
| Format | `make format` | `just go format` |
| Tidy modules | | `just go deps` |
| Clean | `make clean` | `just go clean` |

The just recipes run from the repository root whichever directory you call them from.

The tests run against `pkg/gc/fake` and a fake Steam transport, never against Steam. `./...`
in the repository root does **not** include `tools/probe`, because it is its own module. Test it
separately:

```bash
go -C tools/probe test ./...      # or: just probe test
```

CI runs `go test -race -count=1 ./...`, the probe's tests, `go vet` and golangci-lint on every
pull request and every push to `main`.

## The live probe

`tools/probe` answers the questions only a real account can (token login, game coordinator
welcome, league listing). Its recipes take a bot's sops file and never print secrets:

| Recipe | What it does |
|---|---|
| `just probe build` | Build `bin/probe` |
| `just probe auth <bot>` | Password + Steam Guard code, store the refresh token in the bot's sops file (Q1) |
| `just probe renew <bot>` | Token login, then token renewal (Q12) |
| `just probe code <bot>` | Print the current Steam Guard code |
| `just probe run <bot> <cmd> ...` | Any probe command, for example `login` or `leagues` |
| `just probe report <bot> <Q>` | Report lines for one question from `probe-state/<bot>/report.jsonl` |

See `tools/probe/README.md` and
[the Phase 0 plan](https://github.com/kettleofketchup/dota_lobby/blob/main/docs/plans/2026-11-06-phase0-live-probe.md).
Never run the probe for a bot that a running service is using.

## Secrets recipes

| Recipe | What it does |
|---|---|
| `just secrets init` | Add a labelled age key to your sops key file and write `.sops.yaml` |
| `just secrets new-bot <bot>` | Create `secrets/bots/<bot>.sops.yaml` and open it |
| `just secrets edit <bot>` | Edit it |
| `just secrets list` | Which keys each bot has set (values are never printed) |
| `just secrets import-mafile <bot> <maFile>` | Import a steamguard-cli maFile, see [Bot accounts](../configuration/bot-accounts.md#7-import-the-shared-secret-from-the-mafile) |
| `just secrets set-stdin <bot> <KEY>` | Store a JSON-encoded string from stdin under a key |

`secrets/`, `.sops.yaml` and `probe-state/` are ignored by git.

## Docs

Preview the site with live reload on <http://localhost:8000>:

=== "Docker"

    ```bash
    just docs serve
    ```

    Builds the image from `Dockerfile.docs` and serves the repository's docs from it.

=== "uv"

    ```bash
    just docs serve-local
    ```

    Runs `uv run mkdocs serve` with the versions pinned in `uv.lock`.

Before you push, run the same check CI runs:

```bash
uv run --locked mkdocs build --strict
```

`--strict` turns warnings into errors, and `mkdocs.yml` makes broken links and missing
anchors warnings, so a bad link fails the build. `just docs build` (Docker) and
`just docs build-local` (uv) build the site into `site/` the same way.

New pages must be added to `nav:` in `mkdocs.yml`.

!!! note "Internal notes"
    `docs/plans/`, `docs/research/` and `docs/superpowers/` hold design notes, research and
    plans. They stay in the repository but are excluded from the published site
    (`exclude_docs` in `mkdocs.yml`). Link to them with a full GitHub URL, never a relative
    link: a relative link to an excluded page fails the strict build.

## Writing docs

Every command, config key, endpoint and log line in these pages must match what the code on
`main` does. When you change a log message or a config key, search `docs/` for the old text
and update it in the same pull request.

See [Releasing](releasing.md) for how tags turn into binaries, images and the published site.
