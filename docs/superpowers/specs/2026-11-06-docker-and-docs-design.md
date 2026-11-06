# Design: Docker packaging, GHCR image and MkDocs documentation site

Date: 2026-11-06
Branch: `worktree-docker-and-docs` (based on `main`)
Status: draft for review

## 1. Intent and why

dota_lobby already runs (`cmd/dota_lobby`, `pkg/api`, `pkg/bot`, `pkg/config`), but you can
only use it by cloning the repo and building it yourself. Draft PR #2 (Copilot) tried to add
Docker and docs. It forked before main's code existed, so it builds a different program
(`internal/`, credentials from env vars, a `main.go` that exits at once), commits a 7.6 MB
binary, and documents an API that does not exist. We will keep the useful shape of that work
and rebuild it on main's real layout and config model. PR #2 is closed afterwards (not part of
this work).

The audience is the maintainer and **other people running this for their own league**. `docs/`
becomes the project's real documentation.

**Success:** a stranger can go from the docs site to a running container with their bots
connected without asking the maintainer anything. Section 9 explains why the "bots connected"
half may not be reachable with today's code, and how the docs handle that honestly.

## 2. Goals, non-goals, anti-goals

**Goals**
- A small, non-root, multi-arch (amd64 + arm64) container image built from `./cmd/dota_lobby`,
  published to `ghcr.io/kettleofketchup/dota_lobby` when a `v*` tag is pushed.
- A Docker Compose file for one machine as the main way to run it, `docker run` as the
  alternative, and building from source also documented.
- A MkDocs Material site built from `docs/` and deployed to GitHub Pages on every push to
  `main`. `docs/plans/` and `docs/superpowers/` stay in the repo but are not published.
- Docs that match what main **actually** does today, including an honest known-limitations page.
- Fixes for the existing broken references: `just/docs.just` points at a `Dockerfile.docs` and a
  `uv` project that do not exist.

**Non-goals**
- No changes to bot, API or config behaviour. In particular, this branch does not fix the
  `sentry_hash` decoding bug, add Steam Guard support, start the GC session, make the lobby
  endpoints work, add env-var config, or add a healthcheck subcommand. These go on the
  limitations page and the roadmap.
- No change to `go.mod`'s Go version, to `lint.yml`, or to how `release.yml` publishes the
  binary.
- No Kubernetes manifests, Helm chart, metrics, or Docker Hub publishing.

**Anti-goals** (any of these makes the work a failure even if every build passes)
- Docs that describe PR #2's model: `DOTA_LOBBY_*` env vars, `internal/` paths, `.env` files,
  a `--config` flag, `/etc/dota_lobby`, a Steam Web API key, YAML `${VAR}` interpolation,
  `/api/...` routes, data or log volumes.
- Docs that claim lobbies are created, or that `/health` or `/bots: true` means "ready to host".
- `secrets.yaml` ending up in an image layer, the build context, git or CI logs.
- A committed binary, `config.yaml` or `secrets.yaml`.
- A healthcheck that pretends to check something it does not (PR #2's `test -f`).
- Any AI attribution in commits.

## 3. Constraints (facts from main that the design must respect)

- **Config loading** (`pkg/config/config.go`). Viper reads `config.yaml` and `secrets.yaml`,
  searching `.`, then `./config`, then `$HOME/.config/dota_lobby`. The first two are relative
  to the process's working directory.
  - There is no env binding and no CLI flags.
  - If `config.yaml` is missing, defaults are used (`0.0.0.0:8080`, no API key).
  - If `secrets.yaml` is missing, a warning goes to stdout and the service runs with no bots.
  - Malformed YAML is fatal.
  - Config is read once at startup, so a restart is needed to apply changes.
- **Keys:**
  - `server.host`, `server.port`, `server.api_key` (`Secret`) live in `config.yaml`.
  - `secrets.bots[].username` and `.password` (`Secret`) live in `secrets.yaml`.
  - `sentry_hash` is **never loaded** (no mapstructure tag; checked in recon) and never sent to
    Steam.
- **HTTP API** (`pkg/api/server.go`). Endpoints are `GET /health` (no auth, always
  `{"status":"healthy",...}`), `GET /bots` (`{"bots":{"<user>":<bool>}}`), `POST /lobby/create`
  (201 placeholder, creates no lobby) and `GET|POST /lobby/info` (placeholder).
  - Errors are plain text.
  - Auth is optional, via `X-API-Key` or `Authorization: Bearer`, and runs before the method
    check.
  - There is no TLS.
- **Process:**
  - Handles SIGINT and SIGTERM with a 10s HTTP shutdown, then disconnects the bots.
  - Writes nothing to disk.
  - Outbound only: TCP 27017 to Steam CMs, TCP 443 to Steam web APIs, and DNS.
- **Build:**
  - Pure Go, so `CGO_ENABLED=0` works.
  - ldflags `-s -w -X main.version -X main.commit -X main.buildDate`.
  - `main.go` prints `v%s`, so the image must pass the version **without** a leading `v`.
  - `make build` runs `go mod tidy`, so the Dockerfile must not call make.
- **Toolchain:** `go.mod` says `go 1.24.11`. Go 1.24 is out of support (last patch 1.24.13).
- **Docs tooling:** mkdocs-material 9.8.0 is current (a security release that needs
  Python >= 3.11). Material is end of life on 2027-05-05, and Zensical is its successor (only
  at v0.1.0).
- **Local environment:** `docker` on the dev machine is Podman, and the daemon state is
  unknown. Never claim a build or smoke result that was not actually run.
- **Repo rules:** never push. Never touch the main checkout. The research notes in the main
  checkout's `docs/research/` are reference only and are never copied into this branch.

## 4. Decisions the user made (binding)

1. Salvage PR #2's useful parts onto a clean branch from main, adapted to `pkg/` and
   `config.yaml` + `secrets.yaml`. PR #2 is closed afterwards.
2. The audience is the user and future third-party league operators. `docs/` becomes the real
   documentation.
3. Docs use MkDocs Material, with source in `docs/`, published to GitHub Pages on every push to
   `main`. `docs/plans/` and `docs/superpowers/` stay in the repo but are excluded from the site.
4. Docs describe what main actually does. Placeholders and limitations are stated honestly.
   PR #2's env-var model and `internal/` paths are not documented.
5. Docs lead with Docker Compose on a single machine. Plain `docker run` is the alternative,
   and building from source is also covered.
6. The image goes to GHCR (`ghcr.io/kettleofketchup/dota_lobby`) on `v*` tags using
   `GITHUB_TOKEN`, multi-arch amd64 + arm64 if practical. Recon says it is practical.
7. Success means a stranger gets from the docs to a running container with bots connected
   without help.

## 5. Design

### 5.1 Deliverable files

| File | New/changed | Purpose |
|---|---|---|
| `Dockerfile` | new | Multi-stage image for the service |
| `.dockerignore` | new | Keeps secrets, git, docs and build output out of the context |
| `compose.yaml` | new | Single-machine deployment that the docs lead with |
| `secrets.yaml.example` | changed | Drop `sentry_hash`, add a comment that Steam Guard must be off (see 5.9) |
| `config.yaml.example` | changed (comment only) | Note that `host` must stay `0.0.0.0` inside a container |
| `Dockerfile.docs` | new | `FROM squidfunk/mkdocs-material:9.8.0`, so the existing `just docs::serve` and `just docs::build` work unchanged |
| `pyproject.toml` + `uv.lock` | new | `uv run mkdocs` for `just docs::*-local` and CI |
| `mkdocs.yml` | new | Site config |
| `docs/**` | new pages | See 5.6 |
| `.github/workflows/docs.yml` | new | Build on PRs, build and deploy to Pages on push to main |
| `.github/workflows/container.yml` | new | Build on PRs, build and push to GHCR on `v*` tags |
| `.gitignore` | changed | Add `site/`, `.venv/`, `/dota_lobby`, `/config/*.yaml` |
| `README.md` | changed | Short and accurate: what it is, a link to the docs site, a Compose quick start, a pointer to limitations. Remove the `sentry_hash` and "creates a lobby" claims |
| `.claude/CLAUDE.md` | changed | Add the docs/Docker commands and the `docs/` layout; note that the lobby endpoints are placeholders |

### 5.2 Dockerfile

- **Build stage** (our call): `FROM --platform=$BUILDPLATFORM golang:1.24.13-bookworm`, so the
  build matches `go.mod` and the lint CI.
  - `ARG TARGETOS TARGETARCH VERSION=dev COMMIT=unknown BUILD_DATE=unknown`.
  - Copy `go.mod` and `go.sum`, then `go mod download`, using a cache mount on `/go/pkg/mod`.
  - Copy the source, then run `CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X
    main.buildDate=${BUILD_DATE}" -o /out/dota_lobby ./cmd/dota_lobby`, with cache mounts for
    the module and build caches.
  - Cross-compiling this way means building arm64 needs no QEMU.
- **Runtime stage** (our call): `gcr.io/distroless/static-debian13:nonroot`.
  - It ships CA certificates and tzdata, runs as UID/GID 65532, and has no shell. The builder
    may pin it by digest.
  - `COPY --from=build /out/dota_lobby /usr/local/bin/dota_lobby`, `WORKDIR /app`,
    `USER 65532:65532`, `EXPOSE 8080`, `ENTRYPOINT ["/usr/local/bin/dota_lobby"]`.
  - OCI labels `org.opencontainers.image.source`, `.description` and `.licenses`. The CI
    metadata action also adds them, but having them in the file covers local builds.
- **No `VOLUME` and no `HEALTHCHECK`** (our call; see 5.5).
- **Config location:** `WORKDIR /app` means Viper's `./config` resolves to `/app/config`.
  **`/app/config/` is the only documented container path.** Do not document `$HOME` or `/app`
  root mounts.

### 5.3 .dockerignore

Exclude these:

- `.git`, `.github`, `.claude`, `.worktrees`
- `bin/`, `/dota_lobby`, `site/`, `.venv/`
- `docs/`, `*.md`
- `config.yaml`, `secrets.yaml`, `config/`, `**/secrets*.yaml`, `*.yaml.example` (the image
  needs none of them; the docs copy the root example files into `./config/`, and the examples
  stay at the repo root)
- `.env*`, `**/*_test.go`, `Dockerfile.docs`, `compose.yaml`

The builder must check that a `secrets.yaml` placed at the repo root and in `config/` is
absent from the context. One way is a throwaway build stage running `ls`, or
`docker build --no-cache --progress=plain` with a test `RUN` that is not committed.

### 5.4 compose.yaml (single machine)

```yaml
services:
  dota_lobby:
    image: ghcr.io/kettleofketchup/dota_lobby:${DOTA_LOBBY_TAG:-latest}
    container_name: dota_lobby          # also blocks accidental --scale
    restart: unless-stopped
    ports: ["127.0.0.1:8080:8080"]
    volumes: ["./config:/app/config:ro"]
    read_only: true
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    stop_grace_period: 30s
    logging: {driver: json-file, options: {max-size: 10m, max-file: "3"}}
```

- **Image only, no `build:` key** (our call). Strangers use the published image. People
  building from source run `docker build -t ghcr.io/kettleofketchup/dota_lobby:local .` and
  then `DOTA_LOBBY_TAG=local docker compose up -d`. `DOTA_LOBBY_TAG` is a **Compose**
  variable, not an app setting, and the docs must say so to avoid confusion with "no env vars".
- **Port bound to localhost** by default (our call). The docs explain how to change it to
  `8080:8080`, and say to set `server.api_key` and put a TLS reverse proxy in front before
  exposing it.
- **No healthcheck block** (see 5.5).
- **File permissions.** The container runs as 65532, so a host `secrets.yaml` with mode 0600
  owned by the user is unreadable inside it.
  - The docs recommend `sudo chown 65532:65532 config/secrets.yaml && chmod 600
    config/secrets.yaml` (our call), with `chmod 644` as the simpler alternative and its
    trade-off stated.
  - The docs also add a Podman/SELinux note: `:Z` on the mount, and `--userns=keep-id` for
    rootless setups.
- **One instance per set of Steam accounts.** The docs state this rule prominently. Two
  instances, or a desktop client logged in to a bot account, kick each other off forever.

### 5.5 Healthcheck stance (our call)

`/health` always reports healthy whatever the bot state, and distroless has no shell or HTTP
client. An in-image `HEALTHCHECK` would need either Alpine or new Go code. Even then it would
only prove the HTTP listener is up, and `restart: unless-stopped` already covers process
death. So we ship **no in-image or Compose healthcheck**. The docs:

- explain why;
- give `curl -fsS http://127.0.0.1:8080/health` for liveness checks from outside;
- say plainly that real readiness means checking `GET /bots` (all `true`) and the logs, and
  that even `true` means "logged on to Steam", not "connected to the Dota GC".

A real `/livez`, `/readyz` and a `healthcheck` subcommand go on the roadmap.

### 5.6 Docs site

**mkdocs.yml**
- `site_name: Dota Lobby`, `site_url: https://kettleofketchup.github.io/dota_lobby/`,
  `repo_url`, `edit_uri: edit/main/docs/`.
- Theme `material`, with a light/dark palette toggle.
- Features `navigation.tabs`, `navigation.sections`, `navigation.top`, `content.code.copy`,
  `content.tabs.link`, `search.highlight`.
- Markdown extensions `admonition`, `pymdownx.details`, `pymdownx.superfences` (with the
  mermaid custom fence), `pymdownx.tabbed` (`alternate_style: true`), `pymdownx.highlight`,
  `pymdownx.inlinehilite`, `attr_list`, `tables`, and `toc` (`permalink: true`).
- **No third-party plugins** (our call), which keeps a later move to Zensical cheap.
- `exclude_docs` set to `plans/`, `superpowers/` and `research/`. Research is never committed;
  excluding it is defensive.
- An explicit `nav:` listing exactly the pages below, so `--strict` passes cleanly.

**Page tree** (the builder may merge or split pages, but must cover every purpose):

| Page | Purpose |
|---|---|
| `index.md` | What it is, a status admonition ("lobby creation is not implemented yet; see Known limitations"), a mermaid diagram (Client → API → Bot manager → Steam CM → Dota 2 GC, with the GC edge marked "not yet"), quick links |
| `getting-started/prerequisites.md` | Host needs (Docker + Compose v2, outbound 27017/443, NTP). **Bot accounts:** dedicated, one per bot, Steam Guard **off**, owns Dota 2 / launched once, non-limited recommended, never logged in elsewhere at the same time |
| `getting-started/quick-start.md` | The stranger's path: make `config/`, copy the two examples, edit, fix permissions, `docker compose up -d`, `docker compose logs -f` (with the real startup lines), `curl /health`, `curl /bots` (with the API key header if set), what success and failure look like, how to stop (and stop **promptly** if login loops) |
| `configuration/index.md` | A reference for every key in both files: type, default, which file. Search paths, and that `/app/config/` is the container path. No env vars and no flags. Restart to apply. Missing-file behaviour. `host` must be `0.0.0.0` in containers. `sentry_hash` is not supported |
| `configuration/bot-accounts.md` | Account setup in depth, Steam Guard, rate limits and lockout risk, the one-instance rule, weekly Steam maintenance |
| `deployment/docker-compose.md` | The compose file explained line by line, pinning `DOTA_LOBBY_TAG` to a version, upgrade flow (`pull` then `up -d`), logs, exposing beyond localhost with a reverse proxy (a short nginx or Caddy example), security hardening already in place |
| `deployment/docker.md` | The equivalent `docker run` command (`--read-only --cap-drop ALL --security-opt no-new-privileges -v "$PWD/config:/app/config:ro" -p 127.0.0.1:8080:8080`), image tags, platforms, building the image locally |
| `deployment/from-source.md` | Go 1.24+, `make build` or `just go::build`, running `./bin/dota_lobby` from a directory containing `config.yaml`/`secrets.yaml` or `config/` |
| `api/index.md` | Written from `server.go`: auth headers, each endpoint with method, request, response and plain-text errors, placeholder warnings on the lobby endpoints, auth-before-method quirk, curl examples. Tabs for other languages are optional |
| `operations/troubleshooting.md` | A table of real log lines and their meaning or fix. Covers: `secrets.yaml not found`; `No bots configured`; `Failed to log on: InvalidPassword` (see section 9); `AccountLogonDenied` (Steam Guard is on); `LogonSessionReplaced` / `LoggedInElsewhere` (a second instance); permission denied reading config (UID 65532); port unreachable (host is not `0.0.0.0`, or the bind is localhost-only); `unauthorized` on pull (package not public yet); a bot that never reconnects after a startup network failure (restart the container) |
| `known-limitations.md` | The list in 5.8 plus a short roadmap |
| `development/index.md` | Repo layout, `./dev`, `make`/`just` targets, lint, previewing docs locally (`just docs::serve` or `just docs::serve-local`), and that `docs/plans/` and `docs/superpowers/` are internal |
| `development/releasing.md` | For maintainers and forks: tag `vX.Y.Z` → binary release + GHCR image. The one-time steps: make the GHCR package public, and set Settings → Pages → Source to "GitHub Actions" |

Writing rules: every command and log line in the docs must match main's code. Use admonitions
for warnings. Write plain prose and don't overclaim (IMPLEMENTATION_SUMMARY.md is not a source
of truth).

### 5.7 CI workflows

Use major-version tags on actions, as recon checked (our call).

**`docs.yml`**
- Triggers: `push` to `main` (no path filter, so every push to main deploys, as the user
  asked), `pull_request` (build only), and `workflow_dispatch`.
- Permissions at the top: `contents: read`. The deploy job adds `pages: write` and
  `id-token: write`.
- Concurrency: `group: pages`, `cancel-in-progress: false`.
- Build job: `actions/checkout@v6`, `astral-sh/setup-uv@v7`, `uv run --frozen mkdocs build
  --strict`, `actions/configure-pages@v6`, then `actions/upload-pages-artifact@v5`
  (`path: site`). The last two steps run only off pull requests.
- Deploy job: `needs: build`, `if: github.event_name != 'pull_request'`, environment
  `github-pages`, `actions/deploy-pages@v5`.

**`container.yml`**
- Triggers: `push` on tags `v*`, and `pull_request` with paths `Dockerfile`, `.dockerignore`,
  `go.*`, `cmd/**`, `pkg/**` and the workflow file.
- Permissions: `contents: read`, `packages: write`.
- Steps:
  - `docker/setup-qemu-action@v4` (harmless, kept for safety) and
    `docker/setup-buildx-action@v4`.
  - `docker/login-action@v4` against `ghcr.io` with `github.actor` / `secrets.GITHUB_TOKEN`,
    skipped on PRs.
  - `docker/metadata-action@v6` with image `ghcr.io/kettleofketchup/dota_lobby` and tags:
    - `type=semver,pattern={{version}}`
    - `type=semver,pattern={{major}}.{{minor}}`
    - `type=semver,pattern={{major}},enable=${{ !startsWith(github.ref, 'refs/tags/v0.') }}`
    - `type=sha`
    - `latest` comes from the default `flavor: latest=auto`, which skips pre-releases.
  - `docker/build-push-action@v7` with:
    - `platforms: linux/amd64,linux/arm64`
    - `push: ${{ github.event_name != 'pull_request' }}`
    - meta tags and labels
    - `cache-from: type=gha` and `cache-to: type=gha,mode=max`
    - build-args `VERSION=${{ steps.meta.outputs.version }}` (no `v`), `COMMIT=${{ github.sha }}`
      (or its short form), and `BUILD_DATE` taken from the metadata `created` label.
- **Leave `release.yml` unchanged** (our call). It keeps publishing the amd64 binary on the
  same tag with its own `contents: write`.
- Both workflows must pass `actionlint`.

**Image tags in the docs:** recommend pinning `DOTA_LOBBY_TAG` to a full version such as
`1.2.3`. Mention `latest` and `1.2` as moving tags. No image exists until the first `v*` tag
is pushed and the package is made public; the docs say this and point to "build locally" in
the meantime.

### 5.8 Known-limitations page (content that must be present)

1. `POST /lobby/create` returns `201 success` but creates nothing; `server_region` and
   `game_mode` are ignored. `/lobby/info` echoes the ID back.
2. The Dota 2 GC session is never started. `/bots: true` means logged on to Steam only.
3. `/health` is always healthy.
4. Steam Guard is unsupported, and `sentry_hash` is ignored. Bot accounts need Steam Guard off.
5. The pinned go-steam uses legacy password logon. Upstream reports say current Steam rejects
   it (`InvalidPassword` for correct credentials). It has not been verified here; see section 9.
6. Login failures retry forever on fixed timers, with no backoff and no classification, which
   risks rate limiting or account lock. Stop the container if it loops on auth errors.
7. A bot whose first connection fails at startup never reconnects. Restart the container.
8. Config comes from files only. There are no env vars, no flags, and no hot reload.
9. One instance per set of accounts. There is no protection against running two.
10. Bots drop during weekly Steam maintenance.
11. There is no lobby or league logic, no persistence, no TLS, and no metrics. Errors are plain
    text.
12. Go 1.24 is past upstream support. A toolchain bump is a follow-up.

The roadmap section lists these future items briefly, without promising dates:

- a go-steam/go-dota2 upgrade with modern auth and a token store
- GC session handling
- real lobby create
- backoff
- `/livez` and `/readyz`
- env config
- a Material → Zensical move before 2027-05

### 5.9 Example-file edits (our call, packaging only)

- `secrets.yaml.example`: remove the `sentry_hash` lines and add a comment that Steam Guard
  must be disabled and that the file is read from `/app/config/` in the container. The struct
  field stays in Go; this is not a code change.
- `config.yaml.example`: add a comment saying to keep `host: "0.0.0.0"` in containers, and
  that setting `api_key` is strongly recommended if the port is reachable beyond localhost.

### 5.10 just and uv

- `pyproject.toml`: `[project] name = "dota-lobby-docs"`, `version = "0"`,
  `requires-python = ">=3.11"`, `dependencies = ["mkdocs-material==9.8.0"]`;
  `[tool.uv] package = false`. Commit `uv.lock`.
- `Dockerfile.docs`: `FROM squidfunk/mkdocs-material:9.8.0`, nothing else. The existing
  recipes mount `$PWD` at `/docs` and the image's entrypoint is `mkdocs`.
  - Note: `docker build -f Dockerfile.docs .` uses the repo as its context, so `.dockerignore`
    applies. That is harmless because nothing is copied.
- `just/docs.just`: leave the recipes alone, apart from optionally adding `--strict` to the
  build recipes (builder's call). No new just module for the service image (our call); the docs
  use plain `docker`/`docker compose` commands, which strangers already know.

## 6. Acceptance verification

Every item below must actually be run. Record results, including "not run: reason", in the
final report. Never fake a result.

1. `go build ./...`, `go vet ./...` and `golangci-lint run` are still green. Use the mise
   GOROOT workaround. Confirm `git diff main --stat -- '*.go'` is empty.
2. **Image build**, if the Docker/Podman daemon works:
   - Run `docker build --build-arg VERSION=0.0.0-test -t ghcr.io/kettleofketchup/dota_lobby:local .`.
   - Confirm the image runs as 65532 and has no secrets: `docker image inspect`, plus a
     context check with a dummy `secrets.yaml` present.
   - If buildx or Podman can do it, also run `--platform linux/arm64`. Otherwise CI covers arm64.
   - Run hadolint via `docker run --rm -i hadolint/hadolint < Dockerfile`, if available.
3. **Compose smoke test** (only if the daemon works), using a `config/` with `config.yaml`
   setting `api_key: test` and a `secrets.yaml` with `bots: []`. Never use real or fake
   credentials, because a fake bot would hammer Steam.
   - Run `DOTA_LOBBY_TAG=local docker compose up -d`.
   - Logs show `Dota Lobby Manager v0.0.0-test` (one `v`) and `No bots configured`.
   - `curl /health` returns 200 JSON.
   - `curl /bots` returns 401 without the key and `{"bots":{}}` with it.
   - `read_only` causes no errors.
   - `docker compose down` completes within the grace period.
   - Remove the test `config/` afterwards. It is gitignored and must never be committed.
4. `uv run --frozen mkdocs build --strict` passes. `site/` contains no `plans/` or
   `superpowers/` pages, and every nav page exists.
   `uvx --with mkdocs-material==9.8.0 mkdocs build --strict` is an acceptable fallback.
5. `actionlint` passes on all workflows.
6. **Docs accuracy pass:** a reviewer checks each command, path, key, endpoint and log line in
   `docs/` against main's source. It must find no PR #2 artefacts (grep for `DOTA_LOBBY_`
   apart from `DOTA_LOBBY_TAG`, `internal/`, `--config`, `/etc/dota_lobby`, `/api/`,
   `STEAM_API_KEY`).
7. `git status` is clean, and nothing tracked matches `secrets.yaml`, `config.yaml`, `bin/` or
   `dota_lobby`. Commit messages contain no AI attribution.

## 7. Calls we made (review these)

- distroless `static-debian13:nonroot` runtime rather than Alpine, so no in-image healthcheck
- builder `golang:1.24.13-bookworm`, matching `go.mod`, with Go EOL listed as a limitation
- `/app/config/` as the single documented config mount, with `WORKDIR /app`
- `compose.yaml`: image only, port bound to localhost, `read_only`, `cap_drop`, log rotation,
  a `DOTA_LOBBY_TAG` variable
- recommending `chown 65532` for `secrets.yaml`
- no healthcheck anywhere, documented external checks instead
- a separate `container.yml`, with `release.yml` left untouched; PR builds without pushing in
  both workflows
- mkdocs-material 9.8.0 via `pyproject.toml` + `uv.lock`, no third-party plugins, `exclude_docs`
- removing `sentry_hash` from `secrets.yaml.example`, and comment-only edits to
  `config.yaml.example`
- no new just module; `Dockerfile.docs` is just the pinned upstream image

## 8. Left to the builder

- Exact page wording, merging or splitting pages, and whether to add tabbed examples on the
  API page.
- Pinning the distroless image by digest, and short vs full SHA for `COMMIT`.
- Whether to add `--strict` to `just docs::build*`.
- The reverse-proxy example (nginx or Caddy).
- Commit granularity. One logical change per commit is preferred.

## 9. Risk the user should know about (raise before calling this done)

The research notes cite upstream go-steam reports that **password logon with the pinned
library is rejected by current Steam** (`InvalidPassword` for every account). We have not
verified this here, because doing so needs a real account. If it is true, the "bots connected"
half of the success criterion cannot be reached without a library upgrade, and that upgrade is
a behaviour change outside this branch's scope.

This design still delivers a correct path to a running container. The docs present the login
risk as a top-level known limitation and as a troubleshooting entry. The orchestrator should
tell the user that a follow-up (go-steam/go-dota2 upgrade, plus Steam Guard and token support)
is probably needed for full success.
