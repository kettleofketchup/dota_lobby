# Design: Docker packaging, GHCR image and MkDocs documentation site

Date: 2026-11-06
Branch: `worktree-docker-and-docs` (based on `main`)
Status: draft for review (revised after review round 1)

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
  - Outbound only: TCP 27017 to a Steam CM picked from go-steam's built-in hard-coded list
    (`InitializeSteamDirectory` is never called, so the list can go stale), plus DNS. No 443
    traffic on this code path.
- **Failure behaviour that the docs must describe** (checked against the pinned go-steam
  v0.0.0-20250502043548-f167ff28a93a and Viper v1.21.0):
  - A failed logon emits `LogOnFailedEvent` and then `Disconnect()`, which emits
    `DisconnectedEvent`. `pkg/bot` schedules a reconnect after 5s *and* another after 30s, so a
    wrong password or Steam Guard rejection retries about every 5s per bot, forever, and the
    attempts can pile up.
  - A failed TCP dial (startup or any reconnect) emits `FatalErrorEvent`, which `pkg/bot` does
    not handle: nothing is logged, no `DisconnectedEvent` follows, and the bot stays dead until
    the process restarts.
  - An empty `username`, or an empty `password`, makes go-steam's `Auth.LogOn` panic
    (`Username must be set!` / `Password or LoginKey must be set!`) inside the bot goroutine,
    which kills the whole process.
  - Viper's file search treats any `stat` error (EACCES on a non-traversable dir, an SELinux
    denial) as "not found". An unreadable config **directory** therefore silently becomes
    "files missing": defaults are used, API key auth is off and no bots load. Only a traversable
    dir with an unreadable **file** gives the fatal
    `Failed to load configuration: error reading secrets file: ... permission denied` (or
    `error reading config file: ...`).
- **Build:**
  - Pure Go, so `CGO_ENABLED=0` works.
  - ldflags `-s -w -X main.version -X main.commit -X main.buildDate`.
  - `main.go` prints `v%s`, so the image must pass the version **without** a leading `v`.
  - `make build` runs `go mod tidy`, so the Dockerfile must not call make.
- **Toolchain:** `go.mod` says `go 1.24.11`. Go 1.24 is out of support (last patch 1.24.13).
- **Docs tooling:** mkdocs-material 9.8.0 is current (a security release that needs
  Python >= 3.11). Material is end of life on 2027-05-05, and Zensical is its successor (only
  at v0.1.0).
- **Local environment:** `docker` on the dev machine is rootless Podman (6.1.4, subuid range
  starting at 100000, so container UID 65532 is not host UID 65532), and the daemon state is
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
| `README.md` | changed | Short and accurate: what it is, a link to the docs site, a Compose quick start, a pointer to limitations. Remove the `sentry_hash` claim, the "creates a lobby" claim, and the "Consider enabling Steam Guard" security note (Steam Guard breaks login) |
| `IMPLEMENTATION_SUMMARY.md` | changed (header only) | Add a top note marking it as a historical record of the first implementation, not current documentation, linking to Known limitations (its sentry-hash and lobby-creation claims are wrong). Body left as is |
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
- **File permissions** (our call, split by runtime). The container runs as UID/GID 65532.
  Whether a host file is readable depends on how the runtime maps that UID, so the docs give
  one recipe per runtime in tabs and say plainly that the recipes are **alternatives that must
  not be combined** (for example, a host `chown 65532` breaks the rootless recipes).
  - **The `config/` directory must be traversable by the container user** (mode 755, the
    default with umask 022). A 0700 directory, or an SELinux denial, does **not** produce an
    error: Viper reports the files as missing and the service starts with no API key and no
    bots (see section 3). Each recipe therefore starts with `chmod 755 config`.
  - **Rootful Docker** (default Docker Engine install, no userns-remap): either
    `sudo chown 65532:65532 config/secrets.yaml config/config.yaml && sudo chmod 600 config/*.yaml`,
    or keep the files owned by your user at 0600 and run the container as you:
    `user: "${UID}:${GID}"` in Compose (export `UID`/`GID`, or put them in an `.env` next to
    `compose.yaml`) / `--user "$(id -u):$(id -g)"` with `docker run`.
  - **Rootless Podman** (pick one): `podman unshare chown 65532:65532 config/*.yaml` with the
    files at 0600; **or** keep the files user-owned at 0600 and map your user onto 65532 with
    `--userns=keep-id:uid=65532,gid=65532` (Compose: `userns_mode: "keep-id:uid=65532,gid=65532"`).
    Plain `--userns=keep-id` is not enough and is not documented.
  - **Rootless Docker**: run the chown inside the rootless namespace (`rootlesskit` /
    `nsenter`, per Docker's rootless docs), or use the `user:` override above.
  - **SELinux hosts** (Fedora, RHEL, and Podman on them): mount with `:ro,Z`.
  - `chmod 644` is **not** a recommended option. If mentioned at all, it is labelled as making
    the Steam passwords readable by every local user.
  - `compose.yaml` itself stays as shown above; the `user:` / `userns_mode:` lines are shown in
    the docs as optional additions, not committed defaults.
- **Startup check before exposing anything.** After `docker compose up -d` the docs tell the
  user to confirm in `docker compose logs` that the log shows `API key authentication enabled`
  (when `api_key` is set) and does **not** show `WARNING: secrets.yaml not found`. Either
  message while the files exist means a directory-permission or SELinux mount problem, not a
  missing file.
- **Port.** Leave `server.port` at 8080 and change only the host side of the mapping
  (`127.0.0.1:9000:8080`) to use another port. If `server.port` is changed anyway, the
  container side of the mapping (and `-p`) must match it, or the container starts cleanly but
  is unreachable. The config reference, `config.yaml.example` and the compose page say this.
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
| `index.md` | What it is, a status admonition that says both: Steam login with the current go-steam library is **reported to fail** (unverified here), and lobby creation is not implemented yet; use throwaway accounts, start with one bot, see Known limitations. Then a mermaid diagram (Client → API → Bot manager → Steam CM → Dota 2 GC, with the GC edge marked "not yet"), quick links |
| `getting-started/prerequisites.md` | Opens with a warning admonition, **before** any account step: login is reported to fail with the current library (unverified), lobbies are not created, so use throwaway accounts and start with one bot; link to Known limitations. Host needs (Docker + Compose v2, outbound TCP 27017 to Steam CMs from go-steam's built-in list, which can go stale; DNS; NTP). **Bot accounts:** dedicated, one per bot, Steam Guard **off**, owns Dota 2 / launched once, non-limited recommended, never logged in elsewhere at the same time |
| `getting-started/quick-start.md` | The stranger's path. Step 0 gets the files: `git clone --branch vX.Y.Z --depth 1 https://github.com/kettleofketchup/dota_lobby.git` with the tag matching `DOTA_LOBBY_TAG` (before the first release: clone `main` and build locally). Then: `mkdir config && chmod 755 config`, copy the two examples into it, edit (configure **one** bot first), apply the permission recipe for your runtime (5.4), `docker compose up -d`, `docker compose logs -f` (with the real startup lines), the startup check from 5.4, `curl /health`, `curl /bots` (with the API key header if set), what success and failure look like. A warning: failed logins retry about every 5s per bot, forever; run `docker compose stop` at the first `Failed to log on:` line. An admonition: if the pull fails with `unauthorized` or `manifest unknown`, no public image exists yet, so build locally (link) |
| `configuration/index.md` | A reference for every key in both files: type, default, which file, required or not. `username` and `password` are **required for every bot entry** (an empty or misspelled key crashes the whole process). Search paths, and that `/app/config/` is the container path, which must be traversable (755). No env vars and no flags. Restart to apply. Missing-file behaviour, including that an unreadable directory looks like missing files. `host` must be `0.0.0.0` in containers. Leave `port` at 8080 in containers and remap the host side instead. `sentry_hash` is not supported |
| `configuration/bot-accounts.md` | Opens with the same login-risk warning as prerequisites. Account setup in depth, Steam Guard (email codes and the mobile authenticator both break login), rate limits and lockout risk with the real cadence (a failed login retries about every 5s per bot, forever, and can pile up), start with one bot, the one-instance rule, weekly Steam maintenance (a bot whose reconnect dial fails stays down until restart) |
| `deployment/docker-compose.md` | The compose file explained line by line, pinning `DOTA_LOBBY_TAG` to a version, upgrade flow (`pull` then `up -d`), logs, exposing beyond localhost with a reverse proxy (a short nginx or Caddy example), security hardening already in place |
| `deployment/docker.md` | The equivalent `docker run` command (`--read-only --cap-drop ALL --security-opt no-new-privileges -v "$PWD/config:/app/config:ro" -p 127.0.0.1:8080:8080`), image tags, platforms, building the image locally |
| `deployment/from-source.md` | Go 1.24+, `make build` or `just go::build`, running `./bin/dota_lobby` from a directory containing `config.yaml`/`secrets.yaml` or `config/` |
| `api/index.md` | Written from `server.go`: auth headers, each endpoint with method, request, response and plain-text errors, placeholder warnings on the lobby endpoints, auth-before-method quirk, curl examples. Tabs for other languages are optional |
| `operations/troubleshooting.md` | A table of real log lines, quoted exactly as printed (`[username]` prefix, full `EResult_` names), and their meaning or fix. Covers: `WARNING: secrets.yaml not found - no bots will be configured` and `WARNING: API key authentication disabled - all endpoints are public` **while the files exist** (config dir not traversable by 65532, or SELinux needs `:Z`); `WARNING: No bots configured in secrets.yaml`; `[user] Failed to log on: EResult_InvalidPassword` (wrong password, or the legacy-logon rejection in section 9); `[user] Failed to log on: EResult_AccountLogonDenied` (email Steam Guard on); `[user] Failed to log on: EResult_AccountLoginDeniedNeedTwoFactor` (mobile authenticator on); `[user] Failed to log on: EResult_RateLimitExceeded` (too many attempts; stop and wait); `[user] Logged off: EResult_LoggedInElsewhere` / `EResult_LogonSessionReplaced` (a second instance or a desktop login; these appear on the `Logged off:` line, not `Failed to log on:`); `Failed to load configuration: error reading secrets file: ... permission denied` (dir traversable, file not readable by 65532; apply the runtime recipe); a panic `Username must be set!` or `Password or LoginKey must be set!` with the container restarting in a loop (a bot entry has an empty or misspelled `username`/`password`); port unreachable (host is not `0.0.0.0`, the bind is localhost-only, or `server.port` no longer matches the container side of the mapping); `unauthorized` / `manifest unknown` on pull (package not public yet); `Added bot: X` or `[X] Disconnected from Steam` followed by no `[X] Connected to Steam` and `/bots` stuck at `false` (a TCP dial failed, which stops the bot silently; `docker compose restart`) |
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
- Concurrency: workflow level `group: docs-${{ github.ref }}`, `cancel-in-progress: true`, so
  PR builds never share a queue with deploys. The deploy job alone has
  `concurrency: {group: pages, cancel-in-progress: false}`.
- Build job: `actions/checkout@v6`, `astral-sh/setup-uv@v7`, `uv run --locked mkdocs build
  --strict` (fails if `uv.lock` is stale), then `actions/upload-pages-artifact@v5`
  (`path: site`), which runs only off pull requests. **No `actions/configure-pages`**: it needs
  a Pages permission the build job does not have, and its output is unused because `site_url`
  is hard-coded.
- Deploy job: `needs: build`, `if: github.event_name != 'pull_request'`, environment
  `github-pages`, `actions/deploy-pages@v5`.
- The deploy path can only be verified by a real run on `main` after merge; the plan says so.

**`container.yml`**
- Triggers: `push` on tags `v*`, and `pull_request` with paths `Dockerfile`, `.dockerignore`,
  `go.*`, `cmd/**`, `pkg/**` and the workflow file.
- Permissions: `contents: read`, `packages: write`.
- Steps:
  - `docker/setup-qemu-action@v4` (harmless, kept for safety) and
    `docker/setup-buildx-action@v4`.
  - `docker/login-action@v4` against `ghcr.io` with `github.actor` / `secrets.GITHUB_TOKEN`,
    skipped on PRs.
  - `docker/metadata-action@v6` with image `ghcr.io/${{ github.repository }}` (resolves to
    `ghcr.io/kettleofketchup/dota_lobby` upstream and to the fork's own namespace in a fork;
    metadata-action lowercases it). The literal name stays in `compose.yaml` and the user docs.
    Tags:
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
6. A failed login (wrong password, Steam Guard, legacy-logon rejection) is retried about
   **every 5 seconds per bot, forever** (a 5s and a 30s timer both fire, and attempts can pile
   up), with no backoff and no classification. This risks rate limiting
   (`EResult_RateLimitExceeded`) or account lock. Start with one bot and stop the container at
   the first `Failed to log on:` line.
7. Any TCP-level connection failure, at startup **or on any reconnect** (network blip, weekly
   Steam maintenance, a stale built-in CM list), stops that bot permanently with **no log
   line**. It stays `false` in `/bots` until the container is restarted.
8. An empty or missing `username`/`password` in a bot entry crashes the whole process
   (go-steam panics), and Compose then restarts it in a loop.
9. An unreadable config directory (permissions or SELinux) is treated as "no config files":
   the service starts with defaults, API key auth **off**, and no bots, without an error.
   Check the startup log before exposing the port.
10. Config comes from files only. There are no env vars, no flags, and no hot reload.
11. One instance per set of accounts. There is no protection against running two.
12. There is no lobby or league logic, no persistence, no TLS, and no metrics. Errors are plain
    text.
13. Go 1.24 is past upstream support. A toolchain bump is a follow-up.

The roadmap section lists these future items briefly, without promising dates:

- a go-steam/go-dota2 upgrade with modern auth and a token store
- GC session handling
- real lobby create
- backoff, and handling of dial failures so bots always reconnect
- config validation (required bot fields, permission errors reported as errors)
- `/livez` and `/readyz`
- env config
- a Material → Zensical move before 2027-05

### 5.9 Example-file edits (our call, packaging only)

- `secrets.yaml.example`: remove the `sentry_hash` lines and add a comment that Steam Guard
  must be disabled (email codes and the mobile authenticator), that `username` and `password`
  are required for every entry, and that the file is read from `/app/config/` in the
  container. The struct field stays in Go; this is not a code change.
- `config.yaml.example`: add comments saying to keep `host: "0.0.0.0"` in containers, to leave
  `port: 8080` in containers and change the host side of the port mapping instead, and that
  setting `api_key` is strongly recommended if the port is reachable beyond localhost.

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
3. **Compose smoke test** (only if the daemon works), using a `config/` (mode 755) with
   `config.yaml` setting `api_key: test` and a `secrets.yaml` containing
   `secrets:` / `  bots: []` (the real schema, read via `UnmarshalKey("secrets")`). Never use
   real or fake credentials, because a fake bot would hammer Steam.
   - Apply the **documented rootless Podman recipe** from 5.4 on this host (files at 0600),
     exactly as the docs state it, and record which variant was run.
   - Run `DOTA_LOBBY_TAG=local docker compose up -d`.
   - Logs show `Dota Lobby Manager v0.0.0-test` (one `v`), `API key authentication enabled`
     and `WARNING: No bots configured in secrets.yaml`, and do **not** show
     `secrets.yaml not found`.
   - `curl /health` returns 200 JSON.
   - `curl /bots` returns 401 without the key and `{"bots":{}}` with it.
   - `read_only` causes no errors.
   - `docker compose down` completes within the grace period.
   - Negative check: with `config/` at mode 700, confirm the logs show the silent fallback
     (`secrets.yaml not found` and `API key authentication disabled`), matching the
     troubleshooting row. Restore 755 afterwards.
   - Remove the test `config/` afterwards. It is gitignored and must never be committed.
4. `uv run --locked mkdocs build --strict` passes. `site/` contains no `plans/` or
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
- per-runtime permission recipes (rootful `chown 65532` or `user:`; rootless Podman
  `podman unshare chown` or `keep-id:uid=65532,gid=65532`), never `chmod 644`
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
