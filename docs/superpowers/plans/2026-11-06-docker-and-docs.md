# Docker Packaging and Docs Site Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a non-root multi-arch container image (GHCR on `v*` tags), a single-machine Compose deployment, and a MkDocs Material site (GitHub Pages on every push to `main`) that documents what `main` actually does.

**Architecture:** Packaging, docs and CI only. The image is a two-stage build (Go cross-compile on `$BUILDPLATFORM` into distroless `static-debian13:nonroot`) that reads config from `/app/config/` via Viper's existing `./config` search path. Docs live in `docs/`, built with a pinned mkdocs-material through `uv`, with `docs/plans/` and `docs/superpowers/` excluded. Two new workflows: `docs.yml` (build on PR, deploy on main) and `container.yml` (build on PR, push on tag).

**Tech Stack:** Docker/Podman (BuildKit/buildah), Docker Compose v2, Go 1.24 (`golang:1.24.13-bookworm`), distroless, MkDocs Material 9.8.0, uv, GitHub Actions, just.

**Spec:** `docs/superpowers/specs/2026-11-06-docker-and-docs-design.md` (read it before your task; section numbers below refer to it).

## Global Constraints

- Work only in the worktree `/home/kettle/git_repos/dota_lobby/.claude/worktrees/docker-and-docs` (branch `worktree-docker-and-docs`). It is your session's working directory: run every command from there **without** a `cd <dir> &&` prefix (a hook blocks chained `cd`). **One exception:** the subdirectory just checks (Task 4 Steps 1 and 4, Task 5 Step 7, Task 14 Step 6) must really change directory, so they are written as `( cd docs && ... ) # allow-cd`; the trailing `# allow-cd` is the hook's opt-out and is allowed only for those checks. `env -C docs ...` and `just -d docs ...` are **not** valid substitutes: they keep the caller's `PWD` environment variable, and `PWD` is exactly what the `just/go.just` bug reads, so the check would pass even without the fix. Never touch `/home/kettle/git_repos/dota_lobby` (main checkout) and never copy its `docs/research/` notes.
- **Go env (`<GOENV>`).** The mise `go` shim is broken, the harness refuses `export` with `$HOME`/`$PATH` values, and shell state does not persist between Bash calls. So every command that runs `go`, `make`, `golangci-lint` or a `just go::*` recipe is prefixed inline, with literal paths, by:
  `GOROOT=/home/kettle/.local/share/mise/installs/go/1.27.1 PATH=/home/kettle/.local/share/mise/installs/go/1.27.1/bin:/home/kettle/.local/share/mise/installs/golangci-lint/2.15.0/golangci-lint-2.15.0-linux-amd64:/home/kettle/.local/share/zinit/polaris/bin:/home/kettle/.local/bin:/usr/local/bin:/usr/bin:/bin`
  Below this is written `<GOENV> <cmd>`; substitute the literal text. No `export`, no `$HOME`, no `$PATH`. If any command (e.g. `go mod tidy` inside `make build` / `just go::build`) modifies `go.mod` or `go.sum`, run `git restore go.mod go.sum` and report it; never commit those changes.
- `golangci-lint` binary: `/home/kettle/.local/share/mise/installs/golangci-lint/2.15.0/golangci-lint-2.15.0-linux-amd64/golangci-lint` (v2.15.0; `<GOENV>` puts its directory first). The zinit `golangci-lint` (v2.11.4, built with go1.26) that is otherwise first on PATH panics under Go 1.27 with `file requires newer Go version go1.27`; that is a tooling failure, not a lint finding, and never a reason to touch `.go` files.
- `actionlint` binary: `/home/kettle/.local/share/mise/installs/actionlint/1.7.12/actionlint` (the shim has no version set).
- **Docs check (`<DOCSCHECK>`).** Every docs build check in this plan is this command, run without a pipe so its exit status is visible:
  `NO_MKDOCS_2_WARNING=1 uv run --locked mkdocs build --strict > /tmp/dota_lobby_mkdocs.log 2>&1; echo "exit=$?"; grep -E '^WARNING|does not contain an anchor|is not found among documentation files' /tmp/dota_lobby_mkdocs.log; tail -2 /tmp/dota_lobby_mkdocs.log`
  Expected: `exit=0`, the grep prints nothing, the tail ends with `Documentation built in`. `NO_MKDOCS_2_WARNING=1` silences Material 9.8.0's end-of-life banner, which otherwise always contains the word "warning". Broken links and anchors fail the build because `mkdocs.yml` sets `validation:` (Task 5 Step 3); MkDocs 1.6 otherwise reports a missing anchor only at INFO and `--strict` would pass.
- **Exact strings from dependencies.** EResult names are printed by `EResult.String()`, which uses the `EResult_name` map, not the Go constant name. Where two constants share a value, the map has one name. Cross-check any `EResult_...` string you quote with `grep -n '"EResult_<Name>"' /home/kettle/.local/share/go/pkg/mod/github.com/paralin/go-steam@v0.0.0-20250502043548-f167ff28a93a/protocol/steamlang/enums.go`. Value 85 (mobile authenticator) prints as `EResult_AccountLogonDeniedNeedTwoFactorCode`.
- `docker` on this host is **rootless Podman 6.1.4** (`docker compose` runs the docker-compose plugin against Podman). Never claim a build/smoke result you did not run; if something cannot run, write "not run: <reason>" in your report.
- No behavioural Go changes. `git diff main --stat -- '*.go'` must stay empty.
- Never commit `config.yaml`, `secrets.yaml`, anything under `config/`, `bin/`, `site/`, `.venv/` or a `dota_lobby` binary. Never push.
- Commit messages: plain imperative English (e.g. "Add Dockerfile ..."), **no AI attribution of any kind** (no `Co-Authored-By`, no tool links, no "generated by"). Use `git commit -m "<msg>"` only.
- Docs anti-goals (spec §2): never document `DOTA_LOBBY_*` app env vars (only the Compose variables `DOTA_LOBBY_TAG`, `DOTA_LOBBY_UID`, `DOTA_LOBBY_GID`, each labelled "Compose variable, not an app setting"), `internal/` paths, `.env` files, a `--config` flag, `/etc/dota_lobby`, a Steam Web API key, `${VAR}` interpolation inside the app's YAML, `/api/...` routes, data/log volumes. Never claim lobbies are created or that `/health` / `/bots: true` means "ready to host".
- Image name in user docs and `compose.yaml` is the literal `ghcr.io/kettleofketchup/dota_lobby`. Image tags have **no leading `v`** (`1.2.3`, not `v1.2.3`). Worked example version everywhere: `v1.2.3` (git tag) / `1.2.3` (image tag).
- The only documented container config path is `/app/config/` (mount `./config:/app/config:ro`). Container user is UID/GID `65532`.
- Pinned versions: `golang:1.24.13-bookworm`, `gcr.io/distroless/static-debian13:nonroot`, `squidfunk/mkdocs-material:9.8.0`, `mkdocs-material==9.8.0`, `requires-python = ">=3.11"`. Actions: `actions/checkout@v6`, `astral-sh/setup-uv@v7`, `actions/upload-pages-artifact@v5`, `actions/deploy-pages@v5`, `docker/setup-qemu-action@v4`, `docker/setup-buildx-action@v4`, `docker/login-action@v4`, `docker/metadata-action@v6`, `docker/build-push-action@v7`. (Checked on 2026-11-06: `setup-uv` publishes no floating major tag above `v7`.)
- Images are written fully qualified (`docker.io/library/golang:...`, `docker.io/squidfunk/...`) so Podman's short-name policy cannot prompt or fail; this is the same image the spec names.
- Every log line, command, key and endpoint quoted in docs must match `cmd/dota_lobby/main.go`, `pkg/api/server.go`, `pkg/bot/manager.go`, `pkg/config/config.go` exactly. Go's `log` prefixes lines with `YYYY/MM/DD HH:MM:SS `; the `WARNING: secrets.yaml not found - no bots will be configured` line is printed with `fmt.Println` and has **no** timestamp.
- `IMPLEMENTATION_SUMMARY.md` is not a source of truth.

## Review Focus

1. A `secrets.yaml` sitting at the repo root or in `config/` while someone runs `docker build .` must never reach the build context or an image layer. Pinned by Task 2 Step 4 (context check build that fails if it sees one).
2. A `config/` directory that the container user cannot traverse (mode 700, or SELinux without `:Z`) silently yields defaults, API key auth off and no bots; files in a traversable directory that 65532 cannot read give a fatal `error reading config file: ... permission denied` (config.yaml is read first) and a restart loop. Pinned by Task 3 Steps 6 and 7 (negative checks) and the troubleshooting greps in Task 9.
3. `DOTA_LOBBY_TAG=v1.2.3` (leading `v`) gives `manifest unknown`. Pinned by Task 3 Step 3 (`compose config` resolves the tag literally) and the quick-start/docker-run greps in Tasks 6 and 8; Task 12 pins that CI's image version has no `v`.
4. Running just recipes from a subdirectory (`cd docs && just go::build`). Pinned by Task 4 Step 1 (fails before the fix) and Step 4, and Task 5 Step 7, all with a real `cd` in a subshell (Global Constraints).
5. Leaving the placeholder bot (or a second placeholder) in `secrets.yaml` makes a fake logon retry every 5s forever. Pinned by Task 1 Step 3 (exactly one active entry, the commented one does not match `your_steam`) and the quick-start `grep -n your_steam` check in Task 6.
6. Operators must be able to edit `config/` again after the permission recipe (wrong password, adding a bot, changing `api_key`). Pinned by the "Editing later" lines in Task 8 Step 1 and the grep in Task 8 Step 4.

---

### Task 1: Repo hygiene and example files

**Files:**
- Modify: `.gitignore` (append)
- Modify: `secrets.yaml.example` (full rewrite)
- Modify: `config.yaml.example` (full rewrite, comments only)

**Interfaces:**
- Produces: example files that later docs quote and that users copy into `./config/`. The single active placeholder is `your_steam_username_1` / `your_steam_password_1`; the commented second entry uses `another_bot_username` / `another_bot_password` so that `grep -n your_steam config/secrets.yaml` prints nothing once the first entry is edited (this resolves the spec's §5.6 grep vs §5.9 commented entry).

- [ ] **Step 1: Append to `.gitignore`**

```gitignore

# Docs build output and local Python env
site/
.venv/

# Local binary and container config directory
/dota_lobby
/config/*.yaml
```

- [ ] **Step 2: Rewrite the two example files**

`secrets.yaml.example`:

```yaml
# Steam bot credentials for dota_lobby. Copy this file to config/secrets.yaml
# and never commit it. In the container it is read from /app/config/secrets.yaml.
#
# Every bot entry needs BOTH username and password. An empty or misspelled key
# crashes the whole service at startup.
#
# Steam Guard must be OFF on every bot account (email codes and the mobile
# authenticator both block login). dota_lobby cannot answer Steam Guard prompts.
#
# Start with one bot: a failed login is retried about every 5 seconds, forever.
secrets:
  bots:
    - username: "your_steam_username_1"
      password: "your_steam_password_1"
    # To add another bot, uncomment and fill in (one entry per Steam account):
    # - username: "another_bot_username"
    #   password: "another_bot_password"
```

`config.yaml.example`:

```yaml
server:
  # Keep "0.0.0.0" when running in a container, or the API is unreachable
  # from outside the container.
  host: "0.0.0.0"
  # Leave 8080 in a container. To use another port, change the HOST side of the
  # port mapping instead (for example 127.0.0.1:9000:8080). If you change this
  # value anyway, the container side of the mapping must match it.
  port: 8080
  # API key for the protected endpoints. Strongly recommended whenever the port
  # is reachable beyond localhost. Send it as "X-API-Key: <key>" or
  # "Authorization: Bearer <key>".
  # api_key: "your-secret-api-key-here"
```

- [ ] **Step 3: Verify**

Run: `grep -c '^ *- username' secrets.yaml.example; grep -c sentry secrets.yaml.example; grep -c your_steam secrets.yaml.example; grep -c another_bot secrets.yaml.example`
Expected: `1`, `0`, `2`, `2` (one active entry; no sentry_hash; `your_steam` appears only in the active entry's two values, so once a user edits both, `grep -n your_steam` prints nothing).

Run: `uv run --no-project --with pyyaml python -I -c "import yaml; s=yaml.safe_load(open('secrets.yaml.example')); c=yaml.safe_load(open('config.yaml.example')); print(len(s['secrets']['bots']), c['server'])"`
Expected: `1 {'host': '0.0.0.0', 'port': 8080}`

Run: `git check-ignore -v config/secrets.yaml site/index.html .venv/x dota_lobby`
Expected: four lines, each naming a `.gitignore` rule.

- [ ] **Step 4: Commit**

```bash
git add .gitignore secrets.yaml.example config.yaml.example
git commit -m "Tidy example config files and ignore docs and container build output"
```

---

### Task 2: Dockerfile and .dockerignore

**Files:**
- Create: `Dockerfile`
- Create: `.dockerignore`

**Interfaces:**
- Produces: image entrypoint `/usr/local/bin/dota_lobby`, `WORKDIR /app`, `USER 65532:65532`, `EXPOSE 8080`; build args `VERSION` (no leading `v`), `COMMIT`, `BUILD_DATE`. Local tag used by Task 3 and docs: `ghcr.io/kettleofketchup/dota_lobby:local`.

- [ ] **Step 1: Write `Dockerfile`** (spec §5.2)

```dockerfile
# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.24.13-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ ./cmd/
COPY pkg/ ./pkg/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
      -o /out/dota_lobby ./cmd/dota_lobby

FROM gcr.io/distroless/static-debian13:nonroot
LABEL org.opencontainers.image.source="https://github.com/kettleofketchup/dota_lobby" \
      org.opencontainers.image.description="Dota 2 lobby service driven by Steam bot accounts" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/dota_lobby /usr/local/bin/dota_lobby
# Config is read from /app/config/config.yaml and /app/config/secrets.yaml
WORKDIR /app
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/dota_lobby"]
```

No `VOLUME`, no `HEALTHCHECK` (spec §5.5). Pinning distroless by digest is optional (spec §8); if you pin, use `@sha256:` of the current `nonroot` tag and keep the tag in a comment.

- [ ] **Step 2: Write `.dockerignore`** (spec §5.3)

```gitignore
.git
.github
.claude
.worktrees
bin/
/dota_lobby
site/
.venv/
docs/
*.md
config.yaml
secrets.yaml
**/config.yaml
config/
**/secrets*.yaml
*.yaml.example
.env*
**/*_test.go
Dockerfile.docs
compose.yaml
```

- [ ] **Step 3: Build and inspect** (skip with "not run: <reason>" only if `docker info` fails)

Run: `docker build --build-arg VERSION=0.0.0-test -t ghcr.io/kettleofketchup/dota_lobby:local .`
Expected: build succeeds.

Run: `docker image inspect ghcr.io/kettleofketchup/dota_lobby:local --format '{{.Config.User}} {{.Config.WorkingDir}} {{json .Config.Entrypoint}} {{.Architecture}}'`
Expected: `65532:65532 /app ["/usr/local/bin/dota_lobby"] amd64`

Run: `docker build --platform linux/arm64 --build-arg VERSION=0.0.0-test -t ghcr.io/kettleofketchup/dota_lobby:local-arm64 .` then `docker image inspect ghcr.io/kettleofketchup/dota_lobby:local-arm64 --format '{{.Architecture}}'`
Expected: `arm64` (no QEMU needed: the build stage runs natively and the runtime stage has no `RUN`). If it fails, record the error; CI covers arm64. Then `docker rmi ghcr.io/kettleofketchup/dota_lobby:local-arm64`.

Run: `docker run --rm -i docker.io/hadolint/hadolint:latest < Dockerfile`
Expected: no output, exit 0. If it reports only `DL3029` on the `--platform=$BUILDPLATFORM` line, add `# hadolint ignore=DL3029` directly above that `FROM` and re-run. If the image cannot be pulled, record "not run".

- [ ] **Step 4: Context secret check** (Review Focus 1)

Create dummies (both are gitignored): `printf 'secrets:\n  bots: []\n' > secrets.yaml; mkdir -p config; printf 'secrets:\n  bots: []\n' > config/secrets.yaml; printf 'server: {}\n' > config.yaml`

Write `/tmp/dota_lobby_ctxcheck.Dockerfile` (outside the repo, never committed):

```dockerfile
FROM docker.io/library/busybox:1.37
COPY . /ctx
RUN if find /ctx \( -name 'secrets*.yaml' -o -name 'config.yaml' -o -name '.git' -o -name 'docs' \) | grep .; then echo LEAK; exit 1; fi; echo CONTEXT_CLEAN
```

Run: `docker build --no-cache -f /tmp/dota_lobby_ctxcheck.Dockerfile . 2>&1 | tail -5`
Expected: output contains `CONTEXT_CLEAN`, build exits 0. Then clean up: `rm -f secrets.yaml config.yaml config/secrets.yaml /tmp/dota_lobby_ctxcheck.Dockerfile; rmdir config`, and `git status --short` shows only `Dockerfile` and `.dockerignore`.

- [ ] **Step 5: Commit**

```bash
git add Dockerfile .dockerignore
git commit -m "Add multi-stage distroless Dockerfile and .dockerignore"
```

---

### Task 3: compose.yaml and smoke test

**Files:**
- Create: `compose.yaml`

**Interfaces:**
- Consumes: image `ghcr.io/kettleofketchup/dota_lobby:local` from Task 2 (rebuild with Task 2 Step 3's first command if missing).
- Produces: the Compose file the docs explain line by line; the observed startup log lines (paste them into your report; Tasks 6 and 9 quote them).

- [ ] **Step 1: Write `compose.yaml`** (spec §5.4, exactly these settings)

```yaml
# Single-machine deployment. See https://kettleofketchup.github.io/dota_lobby/deployment/docker-compose/
# DOTA_LOBBY_TAG is a Compose variable (image tag, no leading "v"), not an app setting.
services:
  dota_lobby:
    image: ghcr.io/kettleofketchup/dota_lobby:${DOTA_LOBBY_TAG:-latest}
    container_name: dota_lobby # also blocks accidental --scale
    restart: unless-stopped
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - ./config:/app/config:ro
    read_only: true
    cap_drop:
      - ALL
    security_opt:
      - "no-new-privileges:true"
    stop_grace_period: 30s
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
```

- [ ] **Step 2: Preflight**

Run: `ss -ltn | grep -c ':8080 '`
Expected: `0`. If 8080 is busy, stop and report (do not edit `compose.yaml`).

- [ ] **Step 3: Static check** (Review Focus 3)

Run: `DOTA_LOBBY_TAG=1.2.3 docker compose config | grep 'image:'` and `docker compose config | grep 'image:'`
Expected: `image: ghcr.io/kettleofketchup/dota_lobby:1.2.3` and `image: ghcr.io/kettleofketchup/dota_lobby:latest`.

- [ ] **Step 4: Prepare the test config with the documented rootless Podman recipe** (spec §6.3; never real or fake credentials)

```bash
mkdir config && chmod 755 config
printf 'server:\n  host: "0.0.0.0"\n  port: 8080\n  api_key: "test"\n' > config/config.yaml
printf 'secrets:\n  bots: []\n' > config/secrets.yaml
chmod 600 config/*.yaml
podman unshare chown 65532:65532 config/config.yaml config/secrets.yaml
```

Record in the report: "rootless Podman recipe, variant `podman unshare chown`". Rootless Docker and Docker Desktop recipes: "not run: no such runtime".

- [ ] **Step 5: Smoke test**

Run: `DOTA_LOBBY_TAG=local docker compose up -d` then `curl -fsS --retry 15 --retry-all-errors --retry-delay 1 http://127.0.0.1:8080/health; echo "curl exit=$?"`
Expected: 200 with JSON `{"status":"healthy","time":"..."}`, `curl exit=0`. `--retry-all-errors` is required: rootless Podman's port forwarder (and docker-proxy) accepts the connection before the app listens and then resets it (curl exit 52/56), which `--retry-connrefused` does not retry. If curl still fails, run `docker compose logs` before debugging.

Run: `docker compose logs`
Expected: contains `Dota Lobby Manager v0.0.0-test (commit: unknown, built: unknown)` (exactly one `v`), `Server configuration: 0.0.0.0:8080`, `WARNING: No bots configured in secrets.yaml`, `Starting API server on 0.0.0.0:8080`, `API key authentication enabled`; does **not** contain `secrets.yaml not found`, and no read-only filesystem errors.

Run: `curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/bots; curl -fsS -H 'X-API-Key: test' http://127.0.0.1:8080/bots; curl -fsS -H 'Authorization: Bearer test' http://127.0.0.1:8080/bots`
Expected: `401`, then `{"bots":{}}` twice.

Run: `time docker compose stop && docker compose logs | tail -4 && docker compose down`
Expected: `stop` completes well under 30s; the tail shows `Shutting down gracefully...` and `Shutdown complete`; `down` removes the container.

- [ ] **Step 6: Negative check, untraversable directory** (Review Focus 2)

Run: `chmod 700 config; DOTA_LOBBY_TAG=local docker compose up -d; curl -fsS --retry 15 --retry-all-errors --retry-delay 1 http://127.0.0.1:8080/health; echo "curl exit=$?"; docker compose logs`
Expected: the logs contain `WARNING: secrets.yaml not found - no bots will be configured` and `WARNING: API key authentication disabled - all endpoints are public` (the silent fallback). The `;` separators make sure `docker compose logs` runs even if curl fails. Then `docker compose down; chmod 755 config`.

- [ ] **Step 7: Negative check, unreadable files** (Review Focus 2)

Hand the files back to your host user (container root under rootless Podman), still at 0600, so 65532 cannot read them but can traverse the directory:

Run: `podman unshare chown 0:0 config/config.yaml config/secrets.yaml; ls -ln config; DOTA_LOBBY_TAG=local docker compose up -d`, then in a **separate** Bash call (foreground `sleep` is blocked; the gap between calls is enough): `docker compose ps; docker compose logs`
Expected: files owned by your UID; the logs contain `Failed to load configuration: error reading config file:` and `permission denied` (config.yaml is read before secrets.yaml, so this is the line users see first; paste the full observed line into your report, Tasks 7 to 9 quote it); `docker compose ps` shows the container restarting or exited. Then `docker compose down`.

- [ ] **Step 8: Clean up and commit**

Run: `rm -rf config || podman unshare rm -rf config` and `git status --short`
Expected: only `?? compose.yaml`.

```bash
git add compose.yaml
git commit -m "Add single-machine compose.yaml"
```

---

### Task 4: Run go just recipes from the repo root

**Files:**
- Modify: `just/go.just`

**Interfaces:**
- Produces: `just go::build` writes `<repo>/bin/dota_lobby` from any directory (docs `deployment/from-source.md` and `development/index.md` rely on this).

- [ ] **Step 1: Show the bug**

Run: `<GOENV> just --justfile justfile go::test 2>&1 | tail -3`
Expected: `go: warning: "./..." matched no packages` or similar, proving recipes run in `just/`.

Run (subdirectory, real `cd`, see Global Constraints): `( cd docs && <GOENV> just go::build ); echo "exit=$?"; ls bin/dota_lobby docs/bin 2>&1 # allow-cd`
Expected: the unfixed recipe either fails (non-zero exit) or writes to `docs/bin`; in both cases `bin/dota_lobby` does not exist at the repo root. This proves the Step 4 check can fail. Then `rm -rf bin docs/bin`.

- [ ] **Step 2: Edit `just/go.just`** (spec §5.10)

- `BIN_DIR := justfile_directory() / "bin"` (replaces `env_var("PWD") / "bin"`).
- Prefix every command line that runs `go`, `golangci-lint`, `mkdir`, `rm` or the binary with `cd "{{justfile_directory()}}" && ` (keep `@` echo lines as they are; for lines starting with `@`, put the prefix after the `@`).
- Do not use `[working-directory]`, `set working-directory` or `[no-cd]` (`./dev` pins just 1.36.0).

- [ ] **Step 3: Verify from root**

Run: `<GOENV> just go::build && ls -l bin/dota_lobby && <GOENV> just go::test 2>&1 | grep -c 'no packages'`
Expected: binary exists; count `0` (output lists `?` lines for the packages with no test files). Then `git status --short go.mod go.sum` must be empty (else `git restore go.mod go.sum`).

- [ ] **Step 4: Verify from a subdirectory** (Review Focus 4)

Run: `rm -rf bin docs/bin; ( cd docs && <GOENV> just go::build ); echo "exit=$?"; ls -l bin/dota_lobby; ls docs/bin 2>&1 # allow-cd`
Expected: `exit=0`; `bin/dota_lobby` at the repo root; `docs/bin` does not exist. Do not substitute `env -C` or `just -d` (Global Constraints). Then `rm -rf bin`.

- [ ] **Step 5: Commit**

```bash
git add just/go.just
git commit -m "Run go just recipes from the repo root"
```

---

### Task 5: Docs toolchain, site skeleton and docs just recipes

**Files:**
- Create: `pyproject.toml`, `uv.lock` (generated), `Dockerfile.docs`, `mkdocs.yml`, `docs/index.md`
- Create stubs: `docs/getting-started/prerequisites.md`, `docs/getting-started/quick-start.md`, `docs/configuration/index.md`, `docs/configuration/bot-accounts.md`, `docs/deployment/docker-compose.md`, `docs/deployment/docker.md`, `docs/deployment/from-source.md`, `docs/api/index.md`, `docs/operations/troubleshooting.md`, `docs/known-limitations.md`, `docs/development/index.md`, `docs/development/releasing.md`
- Modify: `just/docs.just`

**Interfaces:**
- Produces: the full `nav:` (later tasks only replace stub bodies, never edit `nav:`); stub marker text `This page is being written.` that Task 14 greps for; `<DOCSCHECK>` (Global Constraints; wraps `uv run --locked mkdocs build --strict`) as the docs check every later docs task runs; the `validation:` block that makes broken links and anchors fail it.

- [ ] **Step 1: `pyproject.toml`** (spec §5.10), then run `uv lock`

```toml
[project]
name = "dota-lobby-docs"
version = "0"
description = "Documentation toolchain for dota_lobby"
requires-python = ">=3.11"
dependencies = ["mkdocs-material==9.8.0"]

[tool.uv]
package = false
```

Expected: `uv.lock` created and contains `name = "mkdocs-material"` with `version = "9.8.0"`.

- [ ] **Step 2: `Dockerfile.docs`**

```dockerfile
FROM docker.io/squidfunk/mkdocs-material:9.8.0
```

- [ ] **Step 3: `mkdocs.yml`** (spec §5.6)

```yaml
site_name: Dota Lobby
site_description: Self-hosted Dota 2 lobby service driven by Steam bot accounts
site_url: https://kettleofketchup.github.io/dota_lobby/
repo_url: https://github.com/kettleofketchup/dota_lobby
repo_name: kettleofketchup/dota_lobby
edit_uri: edit/main/docs/

# Internal design notes stay in the repo but are not published.
exclude_docs: |
  plans/
  superpowers/
  research/

# Make broken links and anchors warnings, so --strict fails on them.
validation:
  omitted_files: warn
  absolute_links: warn
  unrecognized_links: warn
  anchors: warn

theme:
  name: material
  palette:
    - media: "(prefers-color-scheme: light)"
      scheme: default
      toggle:
        icon: material/brightness-7
        name: Switch to dark mode
    - media: "(prefers-color-scheme: dark)"
      scheme: slate
      toggle:
        icon: material/brightness-4
        name: Switch to light mode
  features:
    - navigation.tabs
    - navigation.sections
    - navigation.top
    - content.code.copy
    - content.tabs.link
    - search.highlight

markdown_extensions:
  - admonition
  - attr_list
  - tables
  - toc:
      permalink: true
  - pymdownx.details
  - pymdownx.highlight
  - pymdownx.inlinehilite
  - pymdownx.superfences:
      custom_fences:
        - name: mermaid
          class: mermaid
          format: !!python/name:pymdownx.superfences.fence_code_format
  - pymdownx.tabbed:
      alternate_style: true

nav:
  - Home: index.md
  - Getting started:
      - Prerequisites: getting-started/prerequisites.md
      - Quick start: getting-started/quick-start.md
  - Configuration:
      - Reference: configuration/index.md
      - Bot accounts: configuration/bot-accounts.md
  - Deployment:
      - Docker Compose: deployment/docker-compose.md
      - docker run: deployment/docker.md
      - Build from source: deployment/from-source.md
  - API: api/index.md
  - Operations:
      - Troubleshooting: operations/troubleshooting.md
      - Known limitations: known-limitations.md
  - Development:
      - Overview: development/index.md
      - Releasing: development/releasing.md
```

- [ ] **Step 4: Stubs and `docs/index.md`**

Each stub file is exactly `# <nav title>`, a blank line, then `This page is being written.` Two stubs also carry the headings other pages link to before Task 8 fills them in (with `anchors: warn` a missing anchor fails the strict build): `docs/deployment/docker-compose.md` ends with a blank line and `## File permissions`, and `docs/deployment/docker.md` ends with a blank line and `## Build the image locally`.

`docs/index.md` (full page, spec §5.6 row `index.md`): H1 `Dota Lobby`; one paragraph on what it is (a Go service that logs Steam bot accounts in and exposes a small HTTP API, meant for league lobby hosting); a `!!! warning "Project status"` admonition stating both facts: Steam login with the pinned go-steam library is **reported to fail** with current Steam (not verified by this project), and lobby creation is **not implemented yet** (`POST /lobby/create` is a placeholder); use throwaway accounts, start with one bot, link `known-limitations.md`. Then a mermaid `flowchart LR` with nodes Client → HTTP API → Bot manager → Steam CM, and Bot manager -.->|not yet| Dota 2 GC. Then "Quick links" to quick start, configuration reference, API, troubleshooting, known limitations (relative `.md` links).

- [ ] **Step 5: Rewrite `just/docs.just`**

Same four recipes and comments; each command line prefixed with `cd "{{justfile_directory()}}" && `; the mount becomes `-v "{{justfile_directory()}}":/docs`; builds use `--strict` (`dota-lobby-docs build --strict`, `uv run mkdocs build --strict`). Serve recipes unchanged otherwise.

- [ ] **Step 6: Strict build**

Run: `<DOCSCHECK>` (Global Constraints), then `ls site; ls site | grep -cE '^(plans|superpowers|research)$'`
Expected: `<DOCSCHECK>` as defined (`exit=0`, no warning or anchor lines, `Documentation built in`); `site/` has `getting-started configuration deployment api operations development known-limitations index.html ...`; count `0`.

Prove the anchor validation works: append `[x](deployment/docker.md#no-such-anchor)` to `docs/index.md`, run `<DOCSCHECK>`, expect a non-zero exit and a `does not contain an anchor` line; then remove the line again (`git diff docs/index.md` shows nothing beyond your Step 4 content) and re-run `<DOCSCHECK>` to `exit=0`.

- [ ] **Step 7: just recipes, root and subdirectory** (Review Focus 4)

Run: `rm -rf site docs/site; ( cd docs && just docs::build-local ); echo "exit=$?"; ls site/index.html; ls docs/site 2>&1 # allow-cd`
Expected: `exit=0`; `site/index.html` at the repo root; `docs/site` absent. Real `cd` only; not `env -C` / `just -d` (Global Constraints).

Run (only if `docker info` works): `rm -rf site && just docs::build && ls site/index.html`
Expected: file exists. Otherwise "not run: <reason>". Then `rm -rf site` and `git status --short` shows no `site/` or `.venv/`.

- [ ] **Step 8: Commit**

```bash
git add pyproject.toml uv.lock Dockerfile.docs mkdocs.yml docs/index.md docs/getting-started docs/configuration docs/deployment docs/api docs/operations docs/known-limitations.md docs/development just/docs.just
git commit -m "Add MkDocs Material site skeleton and fix docs just recipes"
```

---

**Docs content tasks (6 to 10) share these rules.** Replace the stub body of each listed page; do not touch `nav:`. Use `!!! warning` / `!!! danger` / `!!! note` admonitions for warnings, `=== "Tab"` content tabs for alternatives, fenced code with language tags. Plain prose, no overclaiming. Cross-link with relative `.md` links. Each task ends with:

- `<DOCSCHECK>` → `exit=0`, no warning/anchor lines, ends with `Documentation built in` (broken links and anchors fail it);
- `grep -rn 'This page is being written' <your pages>` → no output;
- the anti-goal grep: `grep -rnE 'internal/|--config|/etc/dota_lobby|STEAM_API_KEY|\.env\b' <your pages>` → no output, except `.env` hits that are explicitly telling the reader **not** to use a `.env` file (check each hit by eye), and `grep -rnoE 'DOTA_LOBBY_[A-Z]+' <your pages> | grep -vE 'DOTA_LOBBY_(TAG|UID|GID)$'` → no output;
- `rm -rf site`, then commit only your pages.

### Task 6: Getting started pages

**Files:**
- Modify: `docs/getting-started/prerequisites.md`, `docs/getting-started/quick-start.md`

**Interfaces:**
- Consumes: example files (Task 1), `compose.yaml` (Task 3), smoke-test log lines (Task 3 report). Links to `../configuration/index.md`, `../configuration/bot-accounts.md`, `../deployment/docker-compose.md#file-permissions`, `../deployment/docker.md#build-the-image-locally`, `../operations/troubleshooting.md`, `../known-limitations.md`. Task 8 must create the headings `## File permissions` (in docker-compose.md) and `## Build the image locally` (in docker.md) so these anchors resolve.

- [ ] **Step 1: `prerequisites.md`** — content per spec §5.6 row: opening `!!! warning` before any account step (login reported to fail, unverified; no lobbies; throwaway accounts; one bot; link known limitations); host needs (Linux host with Docker Engine or Podman + Compose v2; outbound TCP 27017 to Steam CMs from go-steam's built-in list, which can go stale; DNS; accurate clock/NTP; no inbound ports needed except the API); bot account checklist (dedicated, one per bot, Steam Guard off, owns Dota 2 / launched once, non-limited recommended, never logged in elsewhere at the same time).

- [ ] **Step 2: `quick-start.md`** — the stranger's path, numbered steps per spec §5.6 row, using exactly these commands:

```bash
git ls-remote --tags https://github.com/kettleofketchup/dota_lobby.git
git clone --branch v1.2.3 --depth 1 https://github.com/kettleofketchup/dota_lobby.git
cd dota_lobby
export DOTA_LOBBY_TAG=1.2.3
mkdir config && chmod 755 config
cp config.yaml.example config/config.yaml
cp secrets.yaml.example config/secrets.yaml
grep -n your_steam config/secrets.yaml
docker compose up -d
docker compose logs -f
curl -fsS http://127.0.0.1:8080/health
curl -fsS -H 'X-API-Key: <your api_key>' http://127.0.0.1:8080/bots
docker compose stop
```

Required content: step 0 (pick a release on the GitHub Releases page or via `git ls-remote`); `!!! note` that the image tag has no leading `v` and `DOTA_LOBBY_TAG=v1.2.3` gives `manifest unknown`; `DOTA_LOBBY_TAG` is a Compose variable, not an app setting; before the first release, clone `main` and build locally (link); edit the one bot entry and set `api_key`; `grep -n your_steam` must print nothing, and why (a leftover placeholder retries a fake logon every 5s forever and invites `EResult_RateLimitExceeded`); "apply the permission recipe for your runtime" linking `../deployment/docker-compose.md#file-permissions` and stating recipes must not be combined (this step comes **after** editing both files and the `grep -n your_steam` check, as listed above); a sentence right after it that some recipes make the files unwritable by you, and that the same section's "Editing later" line for your runtime says how to change them later (fix a password, add a bot, change `api_key`), followed by `docker compose restart`; a sample startup log block built from the real lines (timestamped `log` lines, `Added bot: <username>`, `[<username>] Connected to Steam`, `[<username>] Logged on to Steam`); the startup check (`API key authentication enabled` present, `WARNING: secrets.yaml not found` absent; either message while the files exist means a directory permission or SELinux mount problem); what success looks like (`{"bots":{"<username>":true}}`, which means logged on to Steam only, not GC/ready to host) and failure (`[<username>] Failed to log on: EResult_...` → troubleshooting); `!!! danger` that failed logins retry about every 5s per bot forever, run `docker compose stop` at the first `Failed to log on:` line; `!!! note` on pull failure: `manifest unknown` → check for the `v`; still failing or `unauthorized` → no public image yet, build locally.

- [ ] **Step 3: Verify** (shared rules above, plus)

Run: `grep -c 'DOTA_LOBBY_TAG=v1.2.3' docs/getting-started/quick-start.md; grep -c 'manifest unknown' docs/getting-started/quick-start.md; grep -c 'grep -n your_steam config/secrets.yaml' docs/getting-started/quick-start.md; grep -c 'docker compose stop' docs/getting-started/quick-start.md`
Expected: each `>= 1`.

- [ ] **Step 4: Commit**

```bash
git add docs/getting-started
git commit -m "Write prerequisites and quick start docs"
```

### Task 7: Configuration pages

**Files:**
- Modify: `docs/configuration/index.md`, `docs/configuration/bot-accounts.md`

- [ ] **Step 1: `configuration/index.md`** — per spec §5.6 row and §3. A table with columns Key | File | Type | Default | Required for: `server.host` (config.yaml, string, `0.0.0.0`, no), `server.port` (config.yaml, int, `8080`, no), `server.api_key` (config.yaml, string, unset = auth off, no but strongly recommended), `secrets.bots[].username` (secrets.yaml, string, none, **yes for every entry**), `secrets.bots[].password` (secrets.yaml, string, none, **yes for every entry**). State: search order `.`, `./config`, `$HOME/.config/dota_lobby` (first two relative to the working directory); container path `/app/config/` only, must be traversable (755); files only (no env vars, no flags); read once at startup, restart to apply (`docker compose restart`); if the permission recipe made the files unwritable by you, edit them as described under "Editing later" for your runtime (link `../deployment/docker-compose.md#file-permissions`); missing `config.yaml` → defaults; missing `secrets.yaml` → warning, no bots; malformed YAML is fatal; an unreadable directory looks like missing files (silent fallback) while an unreadable file in a traversable directory is fatal: `config.yaml` is read first, so the usual line is `Failed to load configuration: error reading config file: open /app/config/config.yaml: permission denied` (quote the exact line observed in Task 3 Step 7), and only when config.yaml is readable but secrets.yaml is not does it say `Failed to load configuration: error reading secrets file: ... permission denied`; the container then restarts in a loop; empty/misspelled `username`/`password` crashes the process; `host` must be `0.0.0.0` in containers; leave `port` at 8080 in containers and remap the host side (`127.0.0.1:9000:8080`), else the container side must match; `sentry_hash` is not supported (ignored). `api_key` and `password` are redacted (`***REDACTED***`) if ever logged. Include the two example files' YAML as shown in Task 1.

- [ ] **Step 2: `configuration/bot-accounts.md`** — per spec §5.6 row: same opening login-risk warning as prerequisites; account setup in depth; Steam Guard (email codes → `EResult_AccountLogonDenied`, mobile authenticator → `EResult_AccountLogonDeniedNeedTwoFactorCode`, which is what go-steam's `EResult.String()` prints for value 85 (do not mention the Go constant alias with "Login" and no "Code"; it never appears in logs); both break login; turn it off); rate limits and lockout risk with the real cadence (retry about every 5s per bot, forever, timers can pile up; `EResult_RateLimitExceeded`); start with one bot, add more only after it shows `Logged on to Steam`; the one-instance rule (two instances or a desktop login kick each other off forever: `Logged off: EResult_LoggedInElsewhere` / `EResult_LogonSessionReplaced`); weekly Steam maintenance (a bot whose reconnect dial fails stays down silently until restart).

- [ ] **Step 3: Verify** (shared rules, plus)

Run: `grep -c 'sentry_hash' docs/configuration/index.md; grep -c '/app/config' docs/configuration/index.md; grep -c 'error reading config file' docs/configuration/index.md; grep -c 'EResult_AccountLogonDeniedNeedTwoFactorCode' docs/configuration/bot-accounts.md; grep -n '"EResult_AccountLogonDeniedNeedTwoFactorCode"' /home/kettle/.local/share/go/pkg/mod/github.com/paralin/go-steam@v0.0.0-20250502043548-f167ff28a93a/protocol/steamlang/enums.go`
Expected: the first four `>= 1`; the last prints the `85:` entry of `EResult_name` (cross-check against the pinned go-steam). Also `grep -c 'AccountLoginDeniedNeedTwoFactor' docs/configuration/bot-accounts.md` → `0`.

- [ ] **Step 4: Commit**

```bash
git add docs/configuration
git commit -m "Write configuration reference and bot account docs"
```

### Task 8: Deployment pages

**Files:**
- Modify: `docs/deployment/docker-compose.md`, `docs/deployment/docker.md`, `docs/deployment/from-source.md`

**Interfaces:**
- Produces headings (anchors used by Task 6): `## File permissions` in `docker-compose.md`, `## Build the image locally` in `docker.md`.

- [ ] **Step 1: `docker-compose.md`** — include `compose.yaml` verbatim (copy from the file) and explain each line; pinning `DOTA_LOBBY_TAG` to a full version (`1.2.3`), moving tags `latest` and `1.2`; using a local build (`docker build -t ghcr.io/kettleofketchup/dota_lobby:local .` then `DOTA_LOBBY_TAG=local docker compose up -d`); upgrade flow (`docker compose pull` then `docker compose up -d`); logs (`docker compose logs -f`, rotation 10m x 3); why there is no healthcheck (spec §5.5: `/health` is always healthy, distroless has no shell/HTTP client, `restart: unless-stopped` covers process death; external `curl -fsS http://127.0.0.1:8080/health`; readiness = `GET /bots` all `true` plus logs, and `true` means logged on to Steam, not GC); changing the port (host side only); exposing beyond localhost (`8080:8080` only with `server.api_key` set and a TLS reverse proxy; include one short Caddy example `league.example.com { reverse_proxy 127.0.0.1:8080 }`); hardening already in place (read-only rootfs, all capabilities dropped, no-new-privileges, non-root 65532); the one-instance rule as a `!!! danger`.

  `## File permissions` section, per spec §5.4 exactly: config dir 755 first in every recipe; a `!!! warning` that the tabs are alternatives that must not be combined, naming the two mistakes (host `chown 65532` with a rootless runtime; the `DOTA_LOBBY_UID` override on rootless Docker); tabs:
  - `=== "Rootful Docker"`: `sudo chown 65532:65532 config/secrets.yaml config/config.yaml && sudo chmod 600 config/*.yaml`, **or** files at 0600 owned by you plus `export DOTA_LOBBY_UID=$(id -u) DOTA_LOBBY_GID=$(id -g)` and the optional Compose line `user: "${DOTA_LOBBY_UID:?}:${DOTA_LOBBY_GID:?}"` (explain why not `${UID}`/`${GID}`; these are Compose variables; no `.env` file). **Editing later:** after the `sudo chown` variant use `sudo -e config/secrets.yaml` (ownership and mode are kept); the `user:` variant keeps the files owned by you, so edit them normally. Then `docker compose restart`.
  - `=== "Rootless Podman"`: `podman unshare chown 65532:65532 config/*.yaml` (files 0600), **or** files at 0600 owned by you plus `userns_mode: "keep-id:uid=65532,gid=65532"` (plain `keep-id` is not enough). Mark the `podman unshare chown` variant as the one tested by this project. **Editing later:** after the `podman unshare chown` variant edit inside the user namespace, e.g. `podman unshare vi config/secrets.yaml` (any editor; ownership is kept); the `keep-id` variant keeps the files owned by you. Then `docker compose restart` (or `podman compose restart`).
  - `=== "Rootless Docker (untested)"`: `docker run --rm -v "$PWD/config:/c" busybox chown 65532:65532 /c/config.yaml /c/secrets.yaml` (files 0600 first), **or** `user: "0:0"` (container root = your unprivileged user; hardening still applies); never the rootful `DOTA_LOBBY_UID` override (it fails with `Failed to load configuration: error reading config file: open /app/config/config.yaml: permission denied`, because config.yaml is read first). **Editing later:** after the busybox chown, hand the files back with `docker run --rm -v "$PWD/config:/c" busybox chown 0:0 /c/config.yaml /c/secrets.yaml` (container root is your host user), edit, then run the `chown 65532:65532` command again; the `user: "0:0"` variant keeps the files owned by you. Then `docker compose restart`.
  - `=== "Docker Desktop (unverified)"`: no `chown` needed (a `sudo chown 65532` locks you out), keep files owned by you, `chmod 600` on macOS; Windows users use WSL2 with the Linux tabs. **Editing later:** the files stay owned by you; edit them, then `docker compose restart`.
  - Never fall back to `chmod 644` to make editing easier (see below).
  - SELinux hosts: mount with `./config:/app/config:ro,Z`.
  - `chmod 644` only mentioned as "not recommended: makes Steam passwords readable by every local user".

- [ ] **Step 2: `docker.md`** — the exact `docker run` from spec §5.6 row:

```bash
docker run -d --name dota_lobby --restart unless-stopped --stop-timeout 30 \
  --log-opt max-size=10m --log-opt max-file=3 \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  -v "$PWD/config:/app/config:ro" -p 127.0.0.1:8080:8080 \
  ghcr.io/kettleofketchup/dota_lobby:1.2.3
```

  plus: `--user "$(id -u):$(id -g)"` as the rootful alternative (link to File permissions); image tags (`1.2.3`, `1.2`, `1` from v1 onwards, `latest`, `sha-<short>`; no leading `v`); platforms `linux/amd64`, `linux/arm64`; `## Build the image locally` with `docker build -t ghcr.io/kettleofketchup/dota_lobby:local .` and optional `--build-arg VERSION=1.2.3`; logs/stop/remove commands.

- [ ] **Step 3: `from-source.md`** — Go 1.24+; `git clone`, `make build` (produces `bin/dota_lobby`; note it runs `go mod tidy`), `just go::build` as an alternative (works from any directory in the repo); run `./bin/dota_lobby` from a directory containing `config.yaml`/`secrets.yaml` or a `config/` subdirectory, or put them in `$HOME/.config/dota_lobby/`; stop with Ctrl+C (SIGINT/SIGTERM handled).

- [ ] **Step 4: Verify** (shared rules, plus)

Run: `grep -c 'ghcr.io/kettleofketchup/dota_lobby:1.2.3' docs/deployment/docker.md; grep -c '^## File permissions' docs/deployment/docker-compose.md; grep -c '^## Build the image locally' docs/deployment/docker.md; grep -c 'keep-id:uid=65532,gid=65532' docs/deployment/docker-compose.md; grep -ciE 'untested|unverified' docs/deployment/docker-compose.md; grep -c ':v1' docs/deployment/*.md`
Expected: `>= 1` for each except the last, which reports `0` for every file.

Run: `<DOCSCHECK>`, then `grep -o 'id="file-permissions"' site/deployment/docker-compose/index.html; grep -o 'id="build-the-image-locally"' site/deployment/docker/index.html; grep -c 'Editing later' docs/deployment/docker-compose.md; grep -c 'sudo -e' docs/deployment/docker-compose.md; grep -c 'podman unshare' docs/deployment/docker-compose.md`
Expected: `<DOCSCHECK>` passes as defined; both ids found; `Editing later` count `>= 4` (one per tab); the other two `>= 1`.

- [ ] **Step 5: Commit**

```bash
git add docs/deployment
git commit -m "Write Docker Compose, docker run and from-source deployment docs"
```

### Task 9: API reference, troubleshooting and known limitations

**Files:**
- Modify: `docs/api/index.md`, `docs/operations/troubleshooting.md`, `docs/known-limitations.md`

- [ ] **Step 1: `api/index.md`** — written from `pkg/api/server.go`. Auth: only when `server.api_key` is set; `X-API-Key: <key>` or `Authorization: Bearer <key>`; failure `401` plain text `Unauthorized: Invalid or missing API key`; auth runs before the method check (a wrong method without a key gets `401`, not `405`). Errors are plain text, not JSON; wrong method → `405 Method not allowed`. Endpoints:
  - `GET /health` (no auth) → `200 {"status":"healthy","time":"<RFC3339 UTC>"}`; always healthy.
  - `GET /bots` → `200 {"bots":{"<username>":true|false}}`; `true` = logged on to Steam only.
  - `POST /lobby/create` with JSON `lobby_name` (required, ≤100 chars), `password` (optional, ≤50), `server_region`, `game_mode` (accepted and ignored) → `201 {"status":"success","message":"Lobby creation initiated","lobby_name":"...","bot_used":"..."}`; errors `400 Invalid request body: ...`, `400 lobby_name is required`, `400 lobby_name must be 100 characters or less`, `400 password must be 50 characters or less`, `503 No available bots: no available bots`, `503 Bot not ready: bot is not connected`. `!!! danger "Placeholder"`: no lobby is created.
  - `GET /lobby/info?lobby_id=...` or `POST /lobby/info` with `{"lobby_id":"..."}` → `200 {"status":"success","lobby_id":"...","message":"Lobby info retrieval not yet fully implemented"}`; `400 lobby_id is required`. `!!! warning "Placeholder"`: echoes the ID.
  - curl example per endpoint against `http://127.0.0.1:8080`. No TLS.

- [ ] **Step 2: `operations/troubleshooting.md`** — a table Log line / symptom | Meaning | Fix, with **every row listed in spec §5.6 row `operations/troubleshooting.md`**, quoting the lines exactly (`[username]` prefix, full `EResult_` names as printed by `EResult.String()`; the mobile-authenticator row is `[user] Failed to log on: EResult_AccountLogonDeniedNeedTwoFactorCode`). The permission row quotes both variants: `Failed to load configuration: error reading config file: open /app/config/config.yaml: permission denied` (exact line from Task 3 Step 7; this is the one users usually see, because config.yaml is read first) and `Failed to load configuration: error reading secrets file: ... permission denied` (config.yaml readable, secrets.yaml not); both mean the directory is traversable but 65532 cannot read the file, so apply the runtime recipe; the container restarts in a loop. Above the table, a short "first checks" list: `docker compose ps`, `docker compose logs`, the startup check. Below it, a `!!! note` that `/health` is always healthy and is not a readiness signal.

- [ ] **Step 3: `known-limitations.md`** — the 13 numbered items of spec §5.8 in that order, then `## Roadmap` with the 8 bullets of §5.8 and "no dates promised". Item 5 says "reported, not verified by this project".

- [ ] **Step 4: Verify** (shared rules, plus)

```bash
for s in 'WARNING: secrets.yaml not found - no bots will be configured' \
  'WARNING: API key authentication disabled - all endpoints are public' \
  'WARNING: No bots configured in secrets.yaml' \
  'Failed to log on: EResult_InvalidPassword' 'EResult_AccountLogonDenied' \
  'Failed to log on: EResult_AccountLogonDeniedNeedTwoFactorCode' 'EResult_RateLimitExceeded' \
  'EResult_LoggedInElsewhere' 'EResult_LogonSessionReplaced' \
  'error reading config file' 'error reading secrets file' 'Username must be set!' 'Password or LoginKey must be set!' \
  'manifest unknown' 'unauthorized' ':Z' 'Connected to Steam'; do
  printf '%s: ' "$s"; grep -cF -- "$s" docs/operations/troubleshooting.md; done
grep -cE '^1[0-3]\. |^[1-9]\. ' docs/known-limitations.md
grep -c 'Unauthorized: Invalid or missing API key' docs/api/index.md
grep -rn '/api/' docs/api/index.md docs/operations docs/known-limitations.md
```

Expected: every count `>= 1`; the numbered-items count is `13` (plus any numbered lines you added elsewhere on the page; keep the page to the 13); the last grep prints nothing.

Cross-check each string against the Go source: `grep -rnF 'Failed to log on' pkg/bot/manager.go`, `grep -rnF 'No bots configured' cmd/dota_lobby/main.go`, `grep -rnF 'not found - no bots' pkg/config/config.go`, `grep -nF 'error reading config file' pkg/config/config.go`, `grep -nF 'error reading secrets file' pkg/config/config.go` all match. Cross-check every `EResult_` name on the page against the pinned go-steam's `EResult_name` map (Global Constraints, "Exact strings from dependencies"): for each, `grep -c '"EResult_<Name>"' /home/kettle/.local/share/go/pkg/mod/github.com/paralin/go-steam@v0.0.0-20250502043548-f167ff28a93a/protocol/steamlang/enums.go` → `1`. `grep -c 'AccountLoginDeniedNeedTwoFactor' docs/operations/troubleshooting.md` → `0`.

- [ ] **Step 5: Commit**

```bash
git add docs/api docs/operations docs/known-limitations.md
git commit -m "Write API reference, troubleshooting and known limitations docs"
```

### Task 10: Development and releasing pages

**Files:**
- Modify: `docs/development/index.md`, `docs/development/releasing.md`

- [ ] **Step 1: `development/index.md`** — repo layout (`cmd/dota_lobby`, `pkg/api`, `pkg/bot`, `pkg/config`, `docs/`, `just/`, `Dockerfile`, `compose.yaml`, `Dockerfile.docs`, `mkdocs.yml`, `pyproject.toml`); `./dev` bootstrap (installs just, runs `just dev`); `make build|run|test|lint|clean` and `just go::build|run|test|lint|format|clean`; lint is `golangci-lint run` (CI: `lint.yml`); docs preview `just docs::serve` (Docker, http://localhost:8000) or `just docs::serve-local` (uv); docs check `uv run --locked mkdocs build --strict`; `docs/plans/` and `docs/superpowers/` are internal notes excluded from the site.

- [ ] **Step 2: `development/releasing.md`** — tag and push `vX.Y.Z` → `release.yml` publishes the amd64 binary to GitHub Releases, `container.yml` pushes `ghcr.io/<owner>/dota_lobby:X.Y.Z`, `X.Y`, `X` (from v1), `latest` (not for pre-releases), `sha-<short>` for amd64 + arm64; forks publish under their own namespace via `ghcr.io/${{ github.repository }}`; docs deploy on every push to `main` via `docs.yml`. One-time steps (spec §6a, same order): (1) before the first push to `main`: Settings → Pages → Build and deployment → Source = "GitHub Actions", else the first deploy fails; (2) after the first `v*` tag: set the GHCR package visibility to Public and link it to the repo, else pulls fail with `unauthorized`.

- [ ] **Step 3: Verify** (shared rules, plus)

Run: `grep -c 'GitHub Actions' docs/development/releasing.md; grep -c 'Public' docs/development/releasing.md`
Expected: each `>= 1`.

- [ ] **Step 4: Commit**

```bash
git add docs/development
git commit -m "Write development and releasing docs"
```

---

### Task 11: docs.yml workflow

**Files:**
- Create: `.github/workflows/docs.yml`

- [ ] **Step 1: Write the workflow** (spec §5.7)

```yaml
name: Docs

on:
  push:
    branches: [main]
  pull_request:
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: docs-${{ github.ref }}
  cancel-in-progress: true

jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@v6
      - uses: astral-sh/setup-uv@v7
      - name: Build site
        run: uv run --locked mkdocs build --strict
      - name: Upload Pages artifact
        if: github.event_name != 'pull_request'
        uses: actions/upload-pages-artifact@v5
        with:
          path: site

  deploy:
    needs: build
    if: github.event_name != 'pull_request'
    runs-on: ubuntu-latest
    timeout-minutes: 10
    permissions:
      pages: write
      id-token: write
    concurrency:
      group: pages
      cancel-in-progress: false
    environment:
      name: github-pages
      url: ${{ steps.deployment.outputs.page_url }}
    steps:
      - name: Deploy to GitHub Pages
        id: deployment
        uses: actions/deploy-pages@v5
```

No `actions/configure-pages` (spec §5.7).

- [ ] **Step 2: Verify**

Run: `/home/kettle/.local/share/mise/installs/actionlint/1.7.12/actionlint .github/workflows/docs.yml && echo OK`
Expected: `OK`. Also run `<DOCSCHECK>` (the same build the workflow runs) → passes as defined; `rm -rf site`.
The deploy path can only be verified by a real run on `main` after merge and the manual Pages setting (spec §6a); state that in your report.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/docs.yml
git commit -m "Add docs workflow that deploys to GitHub Pages from main"
```

### Task 12: container.yml workflow

**Files:**
- Create: `.github/workflows/container.yml`

- [ ] **Step 1: Write the workflow** (spec §5.7)

```yaml
name: Container image

on:
  push:
    tags: ["v*"]
  pull_request:
    paths:
      - Dockerfile
      - .dockerignore
      - "go.*"
      - "cmd/**"
      - "pkg/**"
      - .github/workflows/container.yml

permissions:
  contents: read
  packages: write

concurrency:
  group: container-${{ github.ref }}
  cancel-in-progress: true

jobs:
  image:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    steps:
      - uses: actions/checkout@v6
      - uses: docker/setup-qemu-action@v4
      - uses: docker/setup-buildx-action@v4
      - name: Log in to GHCR
        if: github.event_name != 'pull_request'
        uses: docker/login-action@v4
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - name: Image metadata
        id: meta
        uses: docker/metadata-action@v6
        with:
          images: ghcr.io/${{ github.repository }}
          tags: |
            type=semver,pattern={{version}}
            type=semver,pattern={{major}}.{{minor}}
            type=semver,pattern={{major}},enable=${{ !startsWith(github.ref, 'refs/tags/v0.') }}
            type=sha
      - name: Build and push
        uses: docker/build-push-action@v7
        with:
          context: .
          platforms: linux/amd64,linux/arm64
          push: ${{ github.event_name != 'pull_request' }}
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
          build-args: |
            VERSION=${{ steps.meta.outputs.version }}
            COMMIT=${{ github.sha }}
            BUILD_DATE=${{ fromJSON(steps.meta.outputs.json).labels['org.opencontainers.image.created'] }}
```

`steps.meta.outputs.version` is `1.2.3` for tag `v1.2.3` (semver pattern strips the `v`), so `main.go` prints `v1.2.3` once (Review Focus 3). `release.yml` stays untouched.

- [ ] **Step 2: Verify**

Run: `/home/kettle/.local/share/mise/installs/actionlint/1.7.12/actionlint && echo OK` (lints all workflows)
Expected: `OK`. Run `git diff main --stat -- .github/workflows/release.yml .github/workflows/lint.yml` → empty.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/container.yml
git commit -m "Add container workflow that publishes multi-arch images to GHCR on tags"
```

---

### Task 13: README and IMPLEMENTATION_SUMMARY header

**Files:**
- Modify: `README.md` (rewrite), `IMPLEMENTATION_SUMMARY.md` (prepend header only)
- Do **not** modify: `.claude/CLAUDE.md` (see Step 3)

- [ ] **Step 1: Rewrite `README.md`** (spec §5.1 row): title `# dota_lobby`; one-paragraph description; a status note (Steam login reported to fail with the pinned library, unverified; lobby endpoints are placeholders; link to `https://kettleofketchup.github.io/dota_lobby/known-limitations/`); link to the docs site `https://kettleofketchup.github.io/dota_lobby/`; a Compose quick start (the clone / `DOTA_LOBBY_TAG=1.2.3` / `mkdir config && chmod 755 config` / copy examples / edit / permissions link / `docker compose up -d` / `curl` sequence from Task 6, with the no-`v` note); a short "Build from source" (`make build`, `./bin/dota_lobby`); a "Development" pointer to the docs development page; License and References sections kept. Remove: the `sentry_hash` YAML, the "Creates a new Dota 2 lobby" claim, and "Consider enabling Steam Guard".

- [ ] **Step 2: Prepend to `IMPLEMENTATION_SUMMARY.md`** (body unchanged):

```markdown
> **Historical record.** This file describes the first implementation as it was written and is
> not current documentation. Some claims below are wrong (Steam Guard sentry hashes are not
> loaded, and no lobby is actually created). See the documentation site, in particular
> [Known limitations](https://kettleofketchup.github.io/dota_lobby/known-limitations/).

```

- [ ] **Step 3: `.claude/CLAUDE.md` — do not edit; put the proposed change in your report.** Subagents may not change agent instruction files on another agent's instruction, and the user has not asked for this edit (default — pending user confirmation). Write the proposed change as text in your report so the orchestrator can raise it with the user and apply it as a separate commit (`git commit -m "Document Docker and docs commands in project instructions"`) only if the user agrees. Proposed change: in the Architecture tree add `docs/` (MkDocs source; `docs/plans/`, `docs/superpowers/` internal and excluded), `Dockerfile`, `compose.yaml`, `mkdocs.yml`; in Development Commands add `just go::build`, `just docs::build-local`, `just docs::serve`, `uv run --locked mkdocs build --strict`, `docker build -t ghcr.io/kettleofketchup/dota_lobby:local .`, `DOTA_LOBBY_TAG=local docker compose up -d`; in the API table change the `/lobby/create` and `/lobby/info` descriptions to `Placeholder (creates nothing)` / `Placeholder (echoes ID)`; add a line under Project Patterns: "Container reads config from `/app/config/` (UID 65532); docs must match main's code and state limitations honestly."

- [ ] **Step 4: Verify**

Run: `grep -nE 'sentry_hash|Consider enabling Steam Guard|Creates a new Dota 2 lobby' README.md; head -1 IMPLEMENTATION_SUMMARY.md; git diff main -- IMPLEMENTATION_SUMMARY.md | grep -c '^-[^-]'`
Expected: no matches; `> **Historical record.** ...`; `0` (nothing removed from the body). `git status --short .claude` prints nothing.

- [ ] **Step 5: Commit**

```bash
git add README.md IMPLEMENTATION_SUMMARY.md
git commit -m "Point README at the docs site and mark the implementation summary as historical"
```

---

### Task 14: Final verification

**Files:** none created or committed (fix-ups, if any, go in a separate commit with a plain message). Return the results as your report; do not write a report file.

Run every item of spec §6, recording PASS / FAIL / "not run: reason" for each:

- [ ] **Step 1: Go unchanged** — `<GOENV> go build ./... && <GOENV> go vet ./... && <GOENV> /home/kettle/.local/share/mise/installs/golangci-lint/2.15.0/golangci-lint-2.15.0-linux-amd64/golangci-lint run; echo "exit=$?"; git diff main --stat -- '*.go'` → builds, vet and lint clean (`0 issues.`, `exit=0`), diff empty. A `file requires newer Go version` panic means the wrong golangci-lint binary ran (Global Constraints); fix the command, never the code.
- [ ] **Step 2: Image** — repeat Task 2 Steps 3 and 4 (build, inspect `65532:65532 /app`, arm64, hadolint, context check). Remove temp files.
- [ ] **Step 3: Compose smoke** — repeat Task 3 Steps 2 to 8 (including both negative checks, the `--retry-all-errors` curl, and cleanup). Record the recipe variant; record rootless Docker and Docker Desktop as "not run: no such runtime" and confirm `grep -ciE 'untested|unverified' docs/deployment/docker-compose.md` ≥ 1.
- [ ] **Step 4: Docs** — `<DOCSCHECK>` passes as defined (exit 0, no warning lines; additionally `grep -cF 'does not contain an anchor' /tmp/dota_lobby_mkdocs.log` → `0`); `find site -path '*plans*' -o -path '*superpowers*' -o -path '*research*'` prints nothing; every `nav:` file exists (`grep -oE '[a-z/-]+\.md' mkdocs.yml | sed 's|^|docs/|' | xargs ls`); `grep -rn 'This page is being written' docs --exclude-dir=plans --exclude-dir=superpowers` empty. Also `NO_MKDOCS_2_WARNING=1 uvx --with mkdocs-material==9.8.0 mkdocs build --strict; echo "exit=$?"` as a cross-check (`exit=0`). `rm -rf site`.
- [ ] **Step 5: actionlint** — `/home/kettle/.local/share/mise/installs/actionlint/1.7.12/actionlint && echo OK` → `OK`.
- [ ] **Step 6: just** — from root: `just docs::build-local` → `site/` at root; `<GOENV> just go::build` → `bin/dota_lobby` at root; `just docs::build` if the daemon works. From `docs/`, with a real `cd` (not `env -C` / `just -d`; Global Constraints): `rm -rf site bin; ( cd docs && just docs::build-local ); echo "exit=$?"; ls site/index.html; ls docs/site 2>&1 # allow-cd` and `( cd docs && <GOENV> just go::build ); echo "exit=$?"; ls bin/dota_lobby; ls docs/bin 2>&1 # allow-cd` → `exit=0`, outputs at the repo root, nothing under `docs/`. `rm -rf site bin`; `git restore go.mod go.sum` if changed.
- [ ] **Step 7: Example** — `grep -c '^ *- username' secrets.yaml.example` → `1`.
- [ ] **Step 8: Docs accuracy pass** — anti-goal greps over `docs` (excluding `plans`, `superpowers`) and `README.md`:
  - `grep -rnoE 'DOTA_LOBBY_[A-Z]+' docs README.md --exclude-dir=plans --exclude-dir=superpowers | grep -vE 'DOTA_LOBBY_(TAG|UID|GID)$'` → empty
  - `grep -rnE 'internal/|--config|/etc/dota_lobby|STEAM_API_KEY|\.env\b' docs README.md --exclude-dir=plans --exclude-dir=superpowers` → empty, except `.env` hits that tell the reader not to use one
  - `grep -rn '/api/' docs README.md --exclude-dir=plans --exclude-dir=superpowers | grep -v 'api/index.md'` → empty
  - `grep -rniE 'lobby (is|was|gets) created|creates (a|the) lobby|ready to host' docs README.md --exclude-dir=plans --exclude-dir=superpowers` → only negated/limitation sentences
  - then read each page against `cmd/`, `pkg/` and the example files and list any mismatch (command, path, key, endpoint, status code, log line). Fix mismatches in a separate commit (`git commit -m "Fix docs inaccuracies found in final review"`).
- [ ] **Step 9: Repo state** — `git status --short` empty; `git ls-files | grep -E '(^|/)(secrets|config)\.yaml$|^bin/|(^|/)dota_lobby$'` empty; `git log main..HEAD --format='%H%n%B' | grep -iE 'co-authored|claude|generated|anthropic|\bAI\b'` empty.
- [ ] **Step 10: Report** — list each step's result, the observed smoke-test log lines, anything "not run" with the reason, and the two manual handoff items of spec §6a (Pages source = GitHub Actions before merging; GHCR package public after the first tag), the proposed `.claude/CLAUDE.md` change from Task 13 Step 3 (not applied; pending user confirmation), plus the spec §9 login risk.
