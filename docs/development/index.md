# Overview

This page is for people changing the code or the docs. To run the service, start with the
[quick start](../getting-started/quick-start.md) instead.

## Repository layout

| Path | What it holds |
|---|---|
| `cmd/dota_lobby/` | Entry point (`main.go`): loads the config, starts the bot manager and the HTTP API |
| `pkg/api/` | HTTP server, handlers and the API key middleware (`server.go`) |
| `pkg/bot/` | Bot manager and the Steam client for each bot (`manager.go`) |
| `pkg/config/` | Config loading with Viper and the `Secret` type that redacts itself (`config.go`) |
| `docs/` | This documentation site (MkDocs Material) |
| `changelog.d/` | Changelog fragments, added by each pull request; see [Changelog](changelog.md) |
| `CHANGELOG.md` | Release notes, assembled from `changelog.d/` at release time; never edited in a pull request |
| `scripts/` | CI helper scripts (`ci-merge-base.sh`) |
| `just/` | just modules: `dev.just`, `go.just`, `docs.just`, `ci.just`, `release.just`; the changelog checker `changelog.py` and its tests |
| `Dockerfile` | Two-stage image build: Go cross-compile, then distroless `nonroot` |
| `compose.yaml` | Single-machine deployment, see [Docker Compose](../deployment/docker-compose.md) |
| `Dockerfile.docs` | Pinned `squidfunk/mkdocs-material` image used by the Docker docs recipes |
| `mkdocs.yml` | Site config and navigation |
| `pyproject.toml`, `uv.lock` | Pinned docs toolchain for `uv` |

## Bootstrap

```bash
./dev
```

`./dev` installs [just](https://just.systems/) into `~/.local/bin` if it is missing, then runs
`just dev`. That recipe checks that `go` and `docker` are on your `PATH`, then runs
`go mod tidy` and `go mod download` and lists the available recipes. You need Go 1.24 or
newer.

## Build, run, test and lint

Both `make` and `just` work. The just recipes run from the repository root no matter which
directory you call them from.

| Task | make | just |
|---|---|---|
| Build `bin/dota_lobby` | `make build` | `just go::build` |
| Build and run | `make run` | `just go::run` |
| Tests | `make test` | `just go::test` |
| Lint | `make lint` | `just go::lint` |
| Format | `make format` | `just go::format` |
| Clean | `make clean` | `just go::clean` |

`build`, `lint` and `format` run `go mod tidy` and `go mod download` first, so they can
change `go.mod` and `go.sum`. Do not commit those changes unless you meant to change
dependencies.

`run` starts the binary from the repository root, so it reads `config.yaml` and
`secrets.yaml` from there or from `config/` (see the
[configuration reference](../configuration/index.md)). It will try to log your bots in to
Steam, so read [Bot accounts](../configuration/bot-accounts.md) first.

Lint is `golangci-lint run` with the repository's `.golangci.yml`. CI runs the same linter in
`.github/workflows/lint.yml` on every pull request and every push to `main`.

The `changelog` check (`.github/workflows/changelog.yml`, `just ci::changelog-check`) runs on
the same events and validates the changelog fragments; see [Changelog](changelog.md).

## Docs

Preview the site with live reload on <http://localhost:8000>:

=== "Docker"

    ```bash
    just docs::serve
    ```

    Builds the image from `Dockerfile.docs` and serves the repository's docs from it.

=== "uv"

    ```bash
    just docs::serve-local
    ```

    Runs `uv run mkdocs serve` with the versions pinned in `uv.lock`.

Before you push, run the same check CI runs:

```bash
uv run --locked mkdocs build --strict
```

`--strict` turns warnings into errors, and `mkdocs.yml` makes broken links and missing
anchors warnings, so a bad link fails the build. `--locked` fails if `uv.lock` no longer
matches `pyproject.toml`. `just docs::build` (Docker) and `just docs::build-local` (uv) build
the site into `site/` the same way.

New pages must be added to `nav:` in `mkdocs.yml`.

!!! note "Internal notes"
    `docs/plans/` and `docs/superpowers/` hold internal design notes and plans. They stay in
    the repository but are excluded from the published site (`exclude_docs` in `mkdocs.yml`).
    Do not link to them from site pages.

## Writing docs

Every command, config key, endpoint and log line in these pages must match what the code on
`main` does. When you change a log message or a config key, search `docs/` for the old text
and update it in the same pull request.

See [Releasing](releasing.md) for how tags turn into binaries, images and the published site.
