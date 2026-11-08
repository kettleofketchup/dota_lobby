# 07 - Docker and Headless Deployment and Operations

> Track: running `dota_lobby` as an internal Go Docker image, headless, with multiple Steam bot
> accounts at once.
> Status: research and design only. No Go code was changed. Code snippets are proposals.
> Date: 2026-11-06. Pinned deps: `go-steam@v0.0.0-20250502043548-f167ff28a93a`,
> `go-dota2@v0.0.0-20250623204622-532cb1851217` (see `go.mod`).

Throughout this doc, **[V]** means verified in source, in docs, or by running something.
**[I]** means inferred from common practice or from other libraries' behavior, and still needs
confirming against a live account.

---

## 0. TL;DR

| Topic | Recommendation |
|---|---|
| Image | Multi-stage build. Stage 1 is `golang:1.24` (matching `go.mod`'s `go 1.24.11`) with `CGO_ENABLED=0 -trimpath` and the same `-ldflags` as `Makefile`. Stage 2 is `gcr.io/distroless/static-debian12:nonroot` (CA certs and tzdata included, UID 65532). |
| Replicas | **Exactly one process per Steam account, ever.** Use Compose with one service, or K8s with `replicas: 1` and `strategy: Recreate`. Never use `RollingUpdate` and never run two replicas against the same accounts. To scale out, give each replica its own set of bots. |
| Config layers | `config.yaml` (not secret, ConfigMap) < `secrets.yaml` (Secret, mounted file) < env `DOTA_LOBBY_*` (overrides) < `*_FILE` env indirection for secrets. Today Viper does **not** read env at all, so this needs to be added. |
| Persistent state | One volume at `/var/lib/dota_lobby`, with a per-bot directory holding the refresh/access token (modern auth), the legacy sentry hash, and a cached CM list. Files are `0600`, owned by 65532. |
| Auth | The pinned go-steam has **only legacy password, sentry and login-key logon** [V]. Valve stopped issuing login keys and moved to JWT refresh tokens in 2023 [V, web]. Upstream go-steam (Sep 2026) now has `LogOnDetails.AccessToken` and `Auth.Authenticate` [V, pkg.go.dev]. **Bumping go-steam is a deployment prerequisite.** |
| Probes | `/livez` checks only that the process and its event loops are alive. `/readyz` passes when at least `min_ready_bots` bots are at GC `HAVE_SESSION`. `/health` stays as is for compatibility and gains a detailed body. Liveness must **not** depend on Steam or the GC, or the Tuesday maintenance window turns into a restart loop and a login storm. |
| Shutdown | SIGTERM: stop accepting API calls, then per bot: `DestroyLobby` and `LeaveLobby` for lobbies still in `UI` state that we own (leave `RUN` lobbies alone), then `SetPlaying(false)`, then `steamClient.Disconnect()`. Total budget about 25s, with `terminationGracePeriodSeconds: 30`. |
| Egress | TCP 27017 to Valve CMs (what go-steam uses) and TCP 443 to `api.steampowered.com` (CM directory, Web API). Optionally allow TCP/UDP 27015-27050 and 443/27018 for WebSocket CMs if the library ever gains them. No inbound ports except the API. |
| Observability | JSON `log/slog`, using a `Secret.LogValue()` implementation. Prometheus metrics on a separate port: `dota_lobby_bot_state`, `..._gc_status`, `..._logon_failures_total{eresult}`, `..._lobbies_active`, `..._gc_request_duration_seconds`. |

---

## 1. Current-state audit (what exists today)

Repo files read: `Makefile`, `justfile` and `just/*.just`, `.github/workflows/{lint,release}.yml`,
`config.yaml.example`, `secrets.yaml.example`, `.gitignore`, `pkg/**`, `cmd/dota_lobby/main.go`,
`docs/plans/2026-01-22-status-system-design.md`.

| # | Finding | Evidence | Deployment impact |
|---|---|---|---|
| A1 | There is no `Dockerfile` for the service and no `.dockerignore`. `just/docs.just` refers to a `Dockerfile.docs` that is also not in git. | `git ls-files`; `just/docs.just:4,10` | Must be written (section 2). |
| A2 | The build already uses `-trimpath -ldflags "-s -w -X main.version... main.commit... main.buildDate"`. | `Makefile:12-17,31`; `just/go.just:6,13` | Reuse the same ldflags in the Dockerfile as build args. |
| A3 | CI has only lint and a GitHub release of a bare binary. Nothing builds or pushes an image. | `.github/workflows/release.yml:23-29` | Add an image job (section 2.4). |
| A4 | Viper reads `config.yaml` and `secrets.yaml` from `.`, `./config` and `$HOME/.config/dota_lobby`. There is **no env binding** (`AutomaticEnv`, `SetEnvPrefix` and `BindEnv` are all absent). | `pkg/config/config.go:58-62,89-94` | Containers need `/etc/dota_lobby` or a `--config-dir` flag, plus env overrides (section 3). |
| A5 | `BotConfig.SentryHash` is **never loaded and never used**. The field has no `mapstructure` tag, and mapstructure's case-insensitive name match does not strip underscores, so the `sentry_hash` key in `secrets.yaml.example` does not map to it (it needs `mapstructure:"sentry_hash"`). Even if it were populated, `connectBot` passes only Username and Password to `LogOn`. | `pkg/bot/manager.go:81-84`; `pkg/config/config.go:49-53`; `secrets.yaml.example:5,8` | Every restart is a fresh password login, which triggers Steam Guard and login throttling. |
| A6 | `dota2.New(...)` is created on **every** `LoggedOnEvent`. It calls `client.GC.RegisterPacketHandler(c)`, which **appends**, so every reconnect leaks another handler, and duplicate handlers process every GC packet. | `pkg/bot/manager.go:95`; go-dota2 `client.go:52-66`; go-steam `gamecoordinator.go:28-29` | Create the Dota2 client once per bot and reuse it across reconnects. |
| A7 | The code never calls `SetPlaying(true)` or `SayHello()`, so the GC session never starts. `connected=true` means only "Steam logged on". | `pkg/bot/manager.go:86-99`; go-dota2 `client_session.go:14-38` | Readiness must track GC status, not just Steam (section 5). |
| A8 | `steam.FatalErrorEvent` is not handled. On a **dial failure**, `ConnectToBind` calls `Fatalf` while `c.conn == nil`, so `Disconnect()` returns early and **no `DisconnectedEvent`** is emitted. The bot never reconnects. | go-steam `client.go:156-167,171-176,94-97`; `pkg/bot/manager.go:78-146` | A brief network blip at startup leaves a bot dead for good. A liveness or readiness signal plus a reconnect on `FatalErrorEvent` are needed. |
| A9 | The go-steam event channel is buffered (30) and `Emit` **blocks**. `Disconnect()` emits while holding `c.mutex`. After `m.ctx` is cancelled, `connectBot` stops reading events. | go-steam `client.go:64-66,89-91,171-190`; `pkg/bot/manager.go:72-76` | A shutdown hang is possible if more than 30 events are queued. Keep draining events until Disconnect returns. |
| A10 | The shutdown order in `main.go` is: API shutdown with a 10s ctx, then the deferred `botManager.Shutdown()` (Disconnect only). It never leaves or destroys lobbies and never calls `SetPlaying(false)`. | `cmd/dota_lobby/main.go:35,61-75`; `pkg/bot/manager.go:216-233` | See section 6. |
| A11 | `/health` always returns `healthy`, whatever the bot or GC state. | `pkg/api/server.go:105-120` | Split into livez, readyz and a detailed status (section 5). |
| A12 | Logging uses stdlib `log` for the app and a separate `logrus.New()` per bot for go-dota2, with no JSON and no bot field. | `pkg/bot/manager.go:92-95`; `main.go:23` | Unify on structured logging (section 7). |
| A13 | `Secret` redacts `%v`, `%s`, `%q`, JSON and slog Text/JSON, **but leaks under `%#v`**, including when nested in a struct. | Ran a standalone copy of `pkg/config/config.go:9-28` on Go 1.27.1. Output: `#v="hunter2"` and `main.BotConfig{Username:"u", Password:"hunter2"}`. | Add `GoString()` and `LogValue()` (section 9). |
| A14 | `Secret.MarshalJSON` always emits `"***REDACTED***"`. | `pkg/config/config.go:26-28` | **Never** use `Secret` in a struct you persist with `encoding/json` (for example the token store), or the token on disk is replaced by the literal redaction string. |

---

## 2. Container image

### 2.1 Dockerfile (proposed `Dockerfile`)

```dockerfile
# syntax=docker/dockerfile:1.7
ARG GO_VERSION=1.24
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

# Dependency layer, cached independently of source changes
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG TARGETOS TARGETARCH
ARG VERSION=dev COMMIT=unknown BUILD_DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
      -o /out/dota_lobby ./cmd/dota_lobby

# Pre-create the state dir with the right owner (distroless has no shell, so no mkdir later)
RUN mkdir -p /out/state && chown 65532:65532 /out/state

FROM gcr.io/distroless/static-debian12:nonroot
# distroless/static ships CA certificates and tzdata; nonroot = UID/GID 65532
COPY --from=build /out/dota_lobby /usr/local/bin/dota_lobby
COPY --from=build --chown=65532:65532 /out/state /var/lib/dota_lobby
WORKDIR /etc/dota_lobby
USER 65532:65532
EXPOSE 8080 9090
VOLUME ["/var/lib/dota_lobby"]
ENTRYPOINT ["/usr/local/bin/dota_lobby"]
```

Notes:

- **CGO off works.** The dependencies (go-steam, go-dota2, protobuf, logrus, viper) are pure Go [V, `go.mod`]. go-steam does its crypto in Go (`cryptoutil/`) [V, directory listing].
- **CA certs are required** for HTTPS to `api.steampowered.com`. go-steam's `InitializeSteamDirectory()` calls `https://api.steampowered.com/ISteamDirectory/GetCMList/v1/` [V, go-steam `steam_directory.go:30-34`]. `distroless/static` includes `/etc/ssl/certs/ca-certificates.crt`.
- **Distroless has no shell**, so `HEALTHCHECK CMD curl` cannot work. Use one of:
  - a `dota_lobby healthcheck` subcommand that does `GET 127.0.0.1:8080/livez` and sets the exit code, run as `HEALTHCHECK CMD ["/usr/local/bin/dota_lobby","healthcheck"]`, or
  - K8s HTTP probes, which need nothing inside the image.
- **Alpine alternative**: `alpine:3.20` with `apk add --no-cache ca-certificates tzdata` and `adduser -D -u 65532 app`. Use it if you need a shell for `docker exec` debugging. It is otherwise larger, with more CVE surface.
- **Interactive first login** (section 4.3) needs a TTY on stdin. That works on distroless because the binary itself reads the prompt.

### 2.2 `.dockerignore`

```gitignore
.git
bin/
site/
.venv/
docs/
*.md
config.yaml
secrets.yaml
.env
**/*_test.go
```

`config.yaml` and `secrets.yaml` are already in `.gitignore` [V, `.gitignore`]. Keeping them out of the
build context as well stops credentials from ever entering an image layer.

### 2.3 Build targets (to align with the existing Makefile and justfile)

```make
IMAGE ?= ghcr.io/kettleofketchup/dota_lobby
docker-build:
	docker buildx build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
	  --build-arg BUILD_DATE=$(DATE) -t $(IMAGE):$(VERSION) --load .
```

```just
# just/docker.just  (mod docker 'just/docker.just')
[group('docker')]
build:
    docker buildx build --build-arg VERSION={{VERSION}} --build-arg COMMIT={{COMMIT}} \
      --build-arg BUILD_DATE={{DATE}} -t dota_lobby:{{VERSION}} --load .
[group('docker')]
up:
    docker compose up -d
```

### 2.4 CI image job (add to `release.yml` or a new `image.yml`)

```yaml
  image:
    runs-on: ubuntu-latest
    permissions: { contents: read, packages: write }
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with: { registry: ghcr.io, username: ${{ github.actor }}, password: ${{ secrets.GITHUB_TOKEN }} }
      - uses: docker/build-push-action@v6
        with:
          push: true
          platforms: linux/amd64,linux/arm64
          tags: ghcr.io/${{ github.repository }}:${{ github.ref_name }}
          build-args: |
            VERSION=${{ github.ref_name }}
            COMMIT=${{ github.sha }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

The image is internal, so push to a private GHCR package or your internal registry.

---

## 3. Configuration: `config.yaml` vs `secrets.yaml` vs env

### 3.1 Layering (proposed)

| Layer | Source | Holds | K8s object | Precedence |
|---|---|---|---|---|
| 1 | Built-in defaults (`viper.SetDefault`) | port 8080, host 0.0.0.0, timeouts | - | lowest |
| 2 | `/etc/dota_lobby/config.yaml` | server, bots (username, enabled, league IDs, tags), state_dir, metrics, probes, rate limits | ConfigMap | |
| 3 | `/run/secrets/dota_lobby/secrets.yaml` | passwords, bootstrap tokens, API key, shared secrets (2FA) | Secret (file mount) | |
| 4 | Env `DOTA_LOBBY_*` | single-value overrides (`DOTA_LOBBY_SERVER_PORT`, `DOTA_LOBBY_LOG_LEVEL`) | Deployment env | |
| 5 | Env `DOTA_LOBBY_*_FILE` | path to a file whose content is the secret (Docker and K8s secrets convention) | Secret mount | highest for secrets |

Put the **bot list in `config.yaml`** (usernames are not secret) and key the secrets by username:

```yaml
# config.yaml
server:
  host: 0.0.0.0
  port: 8080
metrics:
  addr: ":9090"
state_dir: /var/lib/dota_lobby
log:
  level: info
  format: json
readiness:
  min_ready_bots: 1
steam:
  login_stagger: 10s          # delay between bot logins at startup
  reconnect_backoff_min: 5s
  reconnect_backoff_max: 5m
gc:
  hello_interval: 10s         # resend ClientHello until Welcome
bots:
  - username: league_bot_1
    enabled: true
  - username: league_bot_2
    enabled: true
```

```yaml
# secrets.yaml (mounted read-only, never committed)
secrets:
  api_key: "..."
  bots:
    league_bot_1:
      password: "..."
      # Optional, used only for the very first non-interactive bootstrap:
      shared_secret: ""      # TOTP seed if the account uses the mobile authenticator
    league_bot_2:
      password: "..."
```

The existing format (a `secrets.bots` list containing `username`, `password` and `sentry_hash`)
[V, `secrets.yaml.example`] can be kept for `username` and `password`, but its `sentry_hash`
does **not** work today: the untagged `BotConfig.SentryHash` field never receives it (see A5).
Keeping it as a fallback would require adding `mapstructure:"sentry_hash"` and passing it to
`LogOnDetails.SentryFileHash`. In any case `sentry_hash` should move out of secrets entirely, because it is **state** that Steam rewrites. It belongs in the volume (section 4).

### 3.2 Viper changes needed (proposal, not implemented)

```go
v := viper.New()
v.SetConfigName("config")
v.SetConfigType("yaml")
if dir := os.Getenv("DOTA_LOBBY_CONFIG_DIR"); dir != "" {
    v.AddConfigPath(dir)
}
v.AddConfigPath("/etc/dota_lobby")
v.AddConfigPath(".")
v.AddConfigPath("./config")
v.AddConfigPath("$HOME/.config/dota_lobby")

v.SetEnvPrefix("DOTA_LOBBY")
v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
v.AutomaticEnv()
// AutomaticEnv only affects Get*; Unmarshal only sees keys Viper already knows.
// Either SetDefault every key or BindEnv explicitly:
for _, k := range []string{"server.host", "server.port", "server.api_key", "log.level", "state_dir"} {
    _ = v.BindEnv(k)
}
```

- **[V, Viper docs]** `AutomaticEnv` makes `Get` check env. Long-standing Viper behavior is that `Unmarshal` only includes keys that are already registered (defaults, config file or `BindEnv`), which is why the explicit `BindEnv` loop is there. Confirm this against Viper v1.21.0 during implementation.
- `*_FILE` support: after loading, for each secret key `k`, if `DOTA_LOBBY_<K>_FILE` is set, read that file, trim it, and assign the result. This lets `DOTA_LOBBY_SERVER_API_KEY_FILE=/run/secrets/api_key` work with both Docker secrets and K8s.
- Add `DOTA_LOBBY_SECRETS_DIR` (default `/run/secrets/dota_lobby`) as the search path for the secrets Viper instance.
- Do **not** hot-reload secrets with `WatchConfig`. Changing passwords for a bot that is logged in has no effect until it next logs on. Hot reload is reasonable for log level only.
- Replace `fmt.Println("WARNING: ...")` (`config.go:99`) with the structured logger.

---

## 4. Persistent per-bot auth state

### 4.1 Auth reality check (most important deployment risk)

| Mechanism | Pinned go-steam (2025-05) | Valve today | Notes |
|---|---|---|---|
| Password in `CMsgClientLogon.password` | Yes. `LogOn` always sets `logon.Password` [V, `auth.go:60-62`] | Legacy. Steam moved clients to `IAuthenticationService` in 2023 [V, SteamKit #1125]. | May still work for some accounts. Not reliable for the long term. [I] |
| Sentry (`ShaSentryfile`) and `MachineAuthUpdateEvent` | Yes [V, `auth.go:71,168-182`] | Deprecated. SteamKit users report `UpdateMachineAuthCallback` no longer fires [V, SteamKit discussion #1258]. | Do not rely on it. |
| Login key (`ShouldRememberPassword` and `LoginKeyEvent`) | Yes [V, `auth.go:72-77,142-152`] | Steam stopped issuing login keys in Mar 2023 [V, SteamKit #1125 and the gg-cr/go-steam README] | Dead. |
| Refresh token in `CMsgClientLogon.access_token` | **No** [V, grep shows no such field in the logon path of the pinned module] | Current method. The JWT is issued for the `SteamClient` platform and lasts about 200 days [V, McKay forum, Wabbajack PR #3005] | Needs a newer library. |
| `LogOnDetails.AccessToken`, `Auth.Authenticate(ctx, details) (string, error)`, `ConfirmSteamGuard` callback, `AuthSessionState` | n/a | Upstream paralin/go-steam `v0.0.0-20260922065733-99f19aa307ad` [V, pkg.go.dev] | **Recommended upgrade.** Note that its `Auth.LogOn` signature changes to `LogOn(ctx, details) error`. |
| Alternative: `gg-cr/go-steam` fork | n/a | Adds refresh-token logon. Its README recommends getting the token from the HTTP `IAuthenticationService` because CM non-authed service calls caused EOF on some CMs [V, pkg.go.dev gg-cr] | Fallback if upstream misbehaves. |

> Deployment consequence: decide on the go-steam bump **before** building the state volume layout.
> The layout below handles both cases: a token file for modern auth and a sentry file for legacy.

### 4.2 Volume layout

```
/var/lib/dota_lobby/                     (PVC / named volume, owner 65532, mode 0700)
├── bots/
│   ├── league_bot_1/
│   │   ├── token.json        0600  {"access_token":"<jwt>","issued_at":..,"expires_at":..,"steam_id":..}
│   │   ├── sentry.bin        0600  legacy MachineAuth SHA1 (only if legacy auth is used)
│   │   └── lobby.json        0600  {"lobby_id":..,"created_by_service":true,"league_id":..} (crash recovery)
│   └── league_bot_2/...
├── cmlist.json               0644  last ClientCMListEvent / directory response (go-steam doc says to save it)
└── LOCK                      flock / lease-holder marker (section 10.3)
```

- go-steam documents that `ClientCMListEvent` addresses "should always" be saved and reused [V, `client_events.go`]. Load them with `ConnectTo(addr)` on boot to avoid depending on `api.steampowered.com` when the directory is down.
- Write files atomically: write to `token.json.tmp`, then `fsync`, then `rename`. A crash mid-write must not lose the only token, because recovering means an interactive Steam Guard login.
- The token struct must use a plain `string` and **not `config.Secret`**, because `Secret.MarshalJSON` redacts (A14). Wrap it in `Secret` only after loading, for logging.
- Optional: encrypt `token.json` at rest with a key from `secrets.yaml` (AES-GCM, `DOTA_LOBBY_STATE_KEY`). That way a leaked volume snapshot alone cannot be used to log in.
- On a token logon, `EResult_InvalidPassword` (5) / `AccessDenied` is treated as an expired or revoked token: delete `token.json`, mark the bot `needs_auth`, and do **not** loop on password logons [I, the pattern Wabbajack and ArchiSteamFarm use].
- Expiry alerting: metric `dota_lobby_bot_token_expiry_timestamp_seconds{bot}` with an alert at less than 14 days (refresh tokens last about 200 days [V, web]).

### 4.3 First-login bootstrap (headless)

Steam Guard needs a human the first time unless the account's TOTP `shared_secret` is available.

```bash
# Docker Compose: one-off interactive container using the same volume
docker compose run --rm -it dota_lobby auth --bot league_bot_1
#   -> prompts for email code / 2FA code or "approve in mobile app"
#   -> writes /var/lib/dota_lobby/bots/league_bot_1/token.json and exits
docker compose up -d

# Kubernetes: scale to 0 first (only one session per account!), then
kubectl -n dota scale deploy/dota-lobby --replicas=0
kubectl -n dota run dota-auth --rm -it --image=<image> --overrides='<pod spec mounting the same PVC + secret>' \
  -- auth --bot league_bot_1
kubectl -n dota scale deploy/dota-lobby --replicas=1
```

If `shared_secret` is configured, the service can generate the TOTP code itself (the Steam Guard
code algorithm is HMAC-SHA1 over a 30s time step with a 26-character Steam alphabet, the same as
`steam-totp` [I]), which makes the whole process fully headless. Treat `shared_secret` as the most
sensitive secret in the system, because it is equivalent to the account's second factor.

---

## 5. Health, readiness and status endpoints

### 5.1 Bot state machine (from the existing design, refined with GC status)

The states from `docs/plans/2026-01-22-status-system-design.md` are `disconnected`, `connecting`,
`steam_online`, `gc_connected`, `in_lobby` and `in_game`. Add `needs_auth` (needs a human) and
`gc_queued` (`NO_SESSION_IN_LOGON_QUEUE`).

GC statuses that exist in go-dota2 [V, `protocol/gcsdk_gcmessages.pb.go:141-150`]:

| `GCConnectionStatus` | Value | Map to bot state | Action |
|---|---|---|---|
| `HAVE_SESSION` | 0 | `gc_connected` (ready) | serve requests |
| `GC_GOING_DOWN` | 1 | `gc_down` | stop assigning new lobbies, keep the Steam connection |
| `NO_SESSION` | 2 | `steam_online` | keep calling `SayHello()` every `gc.hello_interval` |
| `NO_SESSION_IN_LOGON_QUEUE` | 3 | `gc_queued` | wait. `CMsgConnectionStatus` carries `queue_position`, `queue_size` and `estimated_wait_seconds_remaining` [V, `gcsdk_gcmessages.pb.go:2148-2158`]. Expose them in status. |
| `NO_STEAM` | 4 | `steam_online` / reconnecting | |
| `SUSPENDED` | 5 | `suspended` | alert. The account may be banned from the GC. |
| `STEAM_GOING_DOWN` | 6 | `steam_down` | expect a disconnect; back off |

Change detection: go-dota2 emits `*events.GCConnectionStatusChanged{OldState, NewState, Update}`
on the **steam client's event channel** [V, `client_session.go:98-116`, `client.go:121-123`], so the
same `for event := range steamClient.Events()` loop in `manager.go` can handle it. It also emits
`*events.ClientWelcomed` on welcome [V, `client_session.go:76-77`]. Every status change clears
lobby and party state (`ns.ClearState()`) [V, `client_session.go:117`]. After a GC blip the bot must
re-read the lobby from SOCache, because the lobby itself usually still exists on the GC side [I].

### 5.2 Endpoints

| Path | Auth | Port | 200 when | 503 when | Used by |
|---|---|---|---|---|---|
| `/livez` | none | api | the main goroutine and every bot event loop have ticked in the last 30s (watchdog) | an event loop is wedged (for example a blocked `Emit`, A9) | K8s livenessProbe, Docker HEALTHCHECK |
| `/readyz` | none | api | `count(bots in gc_connected or in_lobby) >= readiness.min_ready_bots` and not draining | otherwise | K8s readinessProbe, load balancer |
| `/health` | none | api | keep the current shape for backward compatibility, add `"ready": bool`, `"bots_ready": n` | never (always 200) | humans, legacy |
| `/status`, `/status/bots`, `/status/system` | API key | api | the design doc's shapes, plus `gc_status`, `gc_queue_position`, `token_expires_at`, `last_eresult` | - | dashboards |
| `/metrics` | none, network-restricted | metrics (9090) | always | - | Prometheus |

Why liveness must ignore Steam and the GC: during weekly maintenance, every bot drops to
`NO_SESSION` / `STEAM_GOING_DOWN`. If liveness failed then, Kubernetes would restart the pod, which
throws away in-memory state, abandons the SOCache, and re-logs-in every bot at once, which risks
`RateLimitExceeded` (84) and `AccountLoginDeniedThrottle` (87) [V, enum values in
go-steam `protocol/steamlang/enums.go:3555,3560`]. Readiness failing is correct instead. It stops
traffic while bots reconnect on their own.

---

## 6. Graceful shutdown

### 6.1 Ordering

```
SIGTERM / SIGINT
 1. mark draining=true                 -> /readyz 503, new /lobby/create -> 503 Retry-After
 2. http.Server.Shutdown(ctx 5s)       -> finish in-flight requests (existing code, main.go:71)
 3. for each bot in parallel (errgroup, ctx 15s total):
      a. lob := current lobby from SOCache (cso.Lobby)
      b. if lob != nil && we created it (lobby.json) :
           - if lob.State == CSODOTALobby_UI:   DestroyLobby(ctx) then LeaveLobby()
           - if lob.State == RUN/POSTGAME:       leave as-is (do NOT abandon a live league match);
                                                persist lobby.json so the next process re-adopts it
      c. dota.SetPlaying(false)            -> ClientGamesPlayed{} (client_session.go:14-23)
      d. dota.Close()                      -> cancels pending GC requests (client.go:74-80)
      e. steamClient.Disconnect()          -> emits DisconnectedEvent (client.go:171-190)
      f. keep draining steamClient.Events() until DisconnectedEvent or ctx timeout (avoid A9 deadlock)
 4. flush logs, exit 0
```

go-dota2 primitives used, all verified:

- `DestroyLobby(ctx) (*CMsgDOTADestroyLobbyResponse, error)`, a request/response with `k_EMsgDestroyLobbyRequest` [V, `client_generated.go:516-531`]
- `LeaveLobby()`, fire-and-forget `k_EMsgGCPracticeLobbyLeave` [V, `client_generated.go:2073-2076`]
- `AbandonLobby()` [V, `client_generated.go:14`]. go-dota2's own `LeaveCreateLobby` only calls it when `state != UI` [V, `client_lobby.go:50-53`]. Shutdown should **not** call it for live matches.
- go-steam has **no `LogOff`** method, neither in the pinned version (a grep matches only the social `LastLogOff` fields) nor in the current upstream (pkg.go.dev lists only `Disconnect`, plus an internal `handleLoggedOff`) [V]. So the question of whether to call LogOff is moot: shutdown uses `Disconnect()`, which closes the TCP connection, and that is sufficient.
- UNCERTAIN, probably wrong: an earlier draft said an explicit LogOff revokes the refresh token. No source was found for this. The documented revocation triggers are a password change, "sign out of all devices"/deauthorize, and an authenticator move (e.g. https://github.com/Rhgx/tf2-halloween-utils/pull/5), not a normal client logoff. It only matters if a future go-steam adds LogOff; resolve it then by logging off once and re-logging with the stored token.

### 6.2 Timeouts

| Setting | Value | Why |
|---|---|---|
| HTTP shutdown ctx | 5s | requests are short; `WriteTimeout` is already 15s (`server.go:33`) |
| Bot drain ctx | 15s | `DestroyLobby` is a GC round-trip. Steam disconnect is immediate. |
| Total in-process | at most 25s | |
| `terminationGracePeriodSeconds` / `stop_grace_period` | 30s | must exceed the in-process total |
| Replace `main.go:68`'s 10s | split into the stages above | today the bots get no shutdown budget at all |

### 6.3 Crash recovery (non-graceful exit)

On a `kill -9` or OOM, the GC keeps the lobby. On the next boot, after `ClientWelcomed`, the SOCache
reports the bot's current lobby (go-dota2 subscribes to caches from the welcome message [V,
`client_session.go:66-74`]). Reconcile it against `lobby.json`: either re-adopt the lobby (resume
tracking and report it through the API) or destroy it if it is stale and in `UI` state.

---

## 7. Structured logging

- Use stdlib `log/slog` (Go 1.21+, and `go.mod` is on 1.24) with `slog.NewJSONHandler(os.Stdout, ...)`. Containers log to stdout only, with no files.
- go-dota2 requires a `logrus.FieldLogger` [V, `client.go:52`]. Keep one shared `logrus.Logger` configured with `logrus.JSONFormatter`, and give each bot `logger.WithField("bot", username)` instead of `logrus.New()` per login (`manager.go:92`). Alternatively bridge logrus to slog with a small hook.
- Standard fields: `bot`, `steam_id`, `lobby_id`, `league_id`, `gc_status`, `eresult`, `request_id` (from the `X-Request-ID` header or generated), `component` (`api`, `steam`, `gc`, `lobby`).
- Levels: `info` for state transitions; `warn` for logon failures and GC queueing; `error` for `FatalErrorEvent` and `SUSPENDED`; `debug` for go-dota2 internals (it logs "sending hello to GC" and other messages at debug level [V, `client_session.go:28`]).
- **Never** log `LogOnDetails` with `%#v` (A13). Add the following to `pkg/config`:

```go
func (s Secret) GoString() string       { return `config.Secret("***REDACTED***")` }
func (s Secret) LogValue() slog.Value   { return slog.StringValue(s.String()) }
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }
```

---

## 8. Prometheus metrics

Serve them on a separate listener (`metrics.addr`, default `:9090`) so the API key middleware does
not apply and the port can be firewalled. Use `github.com/prometheus/client_golang` (it is not in
`go.mod` yet).

| Metric | Type | Labels | Source |
|---|---|---|---|
| `dota_lobby_build_info` | gauge=1 | `version`, `commit`, `go_version` | `main.version` / `commit` |
| `dota_lobby_bot_state` | gauge (0/1) | `bot`, `state` | state machine (section 5.1) |
| `dota_lobby_bot_gc_status` | gauge (enum value) | `bot` | `GCConnectionStatusChanged.NewState` |
| `dota_lobby_bot_gc_queue_position` | gauge | `bot` | `CMsgConnectionStatus.queue_position` |
| `dota_lobby_steam_logons_total` | counter | `bot`, `result` (EResult name) | `LoggedOnEvent`, `LogOnFailedEvent` |
| `dota_lobby_steam_disconnects_total` | counter | `bot`, `reason` (`disconnect`, `fatal`) | `DisconnectedEvent`, `FatalErrorEvent` |
| `dota_lobby_gc_sessions_total` | counter | `bot` | `ClientWelcomed` |
| `dota_lobby_gc_request_duration_seconds` | histogram | `msg` (bounded set), `outcome` | wrapper around `MakeRequest` |
| `dota_lobby_lobbies_active` | gauge | `bot`, `league_id` | SOCache lobby subscription |
| `dota_lobby_lobbies_created_total` | counter | `league_id`, `outcome` | API handler |
| `dota_lobby_bot_token_expiry_timestamp_seconds` | gauge | `bot` | token.json `exp` |
| `dota_lobby_api_requests_total` / `_duration_seconds` | counter / histogram | `route`, `code` | HTTP middleware |
| `dota_lobby_bot_leagues` | gauge | `bot` | count of leagues the bot can host (other tracks) |

Cardinality: `bot` is bounded by config (a handful), and `league_id` is bounded by the tickets
configured. **Never** use `lobby_id` or `match_id` as a label.

Suggested alerts: no ready bots for more than 10 minutes outside the Tuesday window; any bot in
`needs_auth` or `suspended`; `token_expiry - time() < 14d`; logon failures with
`result=~"RateLimitExceeded|AccountLoginDeniedThrottle"`.

---

## 9. Secrets handling

| Secret | Where | Notes |
|---|---|---|
| Bot passwords | `secrets.yaml` (file mount, read-only, 0400) | Used only for the bootstrap login once tokens exist. They could be removed after bootstrap. |
| Refresh/access tokens | state volume `token.json` (0600) | Strongest live credential; encrypt at rest if possible. |
| TOTP `shared_secret` | `secrets.yaml` or a separate K8s Secret | Optional; enables fully headless re-auth. |
| API key | `secrets.yaml` `server.api_key` or `DOTA_LOBBY_SERVER_API_KEY_FILE` | Already a `Secret` and compared in constant time (`server.go:94`). |
| State encryption key | env `_FILE` | Optional. |

Rules:

- Prefer files over env vars. Env vars show up in `docker inspect`, `/proc/<pid>/environ` and crash dumps.
- Every field holding sensitive data must use `config.Secret` (project rule in `.claude/CLAUDE.md`). Fix the `%#v` leak (A13). Keep raw strings in the persistence structs (A14).
- K8s: mount the Secret as a volume with `defaultMode: 0400`, not as `envFrom`. Consider External Secrets or SOPS in your GitOps flow.
- Never bake secrets into image layers (`.dockerignore`, section 2.2).

---

## 10. Topology: Compose, Kubernetes, and why only one replica

### 10.1 Why multiple replicas with the same accounts conflict

- Two distinct conflicts apply, according to node-steam-user and its maintainer [web, I for go-steam: https://github.com/DoctorMcKay/node-steam-user, https://dev.doctormckay.com/topic/1518-loggedinelsewhere/, https://github.com/DoctorMcKay/node-steam-user/issues/271]:
  - **Logon replacement.** A second logon from the **same public IP with the same logonID** (`obfuscated_private_ip`) kicks the first with `EResult_LogonSessionReplaced` (34) [V, enum at `enums.go:3502`]. The pinned go-steam never sets `CMsgClientLogon.ObfustucatedPrivateIp` (sic) in `LogOn` [V, `auth.go:61-76`], so every go-steam instance sends logonID 0 and two replicas on the same host or egress NAT *will* replace each other.
  - **Playing-session conflict.** Logons from different IPs or with different logonIDs can stay connected at the same time, but only one session can be in-game. When the second calls `SetPlaying` for Dota 2, one session gets `EResult_LoggedInElsewhere` (6) [V, enum at `enums.go:3474`] or is blocked from playing. Only the in-game session gets a GC session. So replicas on different nodes fight over the GC session, not necessarily over the logon.
  - Treat both 6 and 34 as "another instance owns this account": log loudly, stop, and back off for minutes. Do **not** reconnect in the 5s loop (`manager.go:123-143`), which makes two replicas knock each other off forever.
  - UNCERTAIN: the exact go-steam event and EResult seen in each case has not been tested here. A live test with two processes on one host and on two hosts would settle it.
- The GC session, the bot's SOCache and its **lobby membership** belong to the account. Two processes would both believe they own the bot's lobby.
- Rapid re-logins from two IPs raise the chance of Steam Guard challenges and throttling (`RateLimitExceeded` 84, `AccountLoginDeniedThrottle` 87).
- Lobby state is in memory plus SOCache. There is no shared store, so API requests sent to replica B cannot see replica A's lobbies.

So: **singleton per bot set.** HA means fast restart, not active/active.

### 10.2 docker-compose example

```yaml
# compose.yaml
services:
  dota_lobby:
    image: ghcr.io/kettleofketchup/dota_lobby:${TAG:-latest}
    container_name: dota_lobby           # also prevents accidental `--scale`
    restart: unless-stopped
    stop_grace_period: 30s
    read_only: true
    user: "65532:65532"
    cap_drop: ["ALL"]
    security_opt: ["no-new-privileges:true"]
    environment:
      DOTA_LOBBY_CONFIG_DIR: /etc/dota_lobby
      DOTA_LOBBY_SECRETS_DIR: /run/secrets/dota_lobby
      DOTA_LOBBY_LOG_LEVEL: info
      DOTA_LOBBY_SERVER_API_KEY_FILE: /run/secrets/api_key
    volumes:
      - ./config.yaml:/etc/dota_lobby/config.yaml:ro
      - ./secrets.yaml:/run/secrets/dota_lobby/secrets.yaml:ro
      - dota_state:/var/lib/dota_lobby
    secrets:
      - api_key
    ports:
      - "127.0.0.1:8080:8080"     # front with your internal reverse proxy
      - "127.0.0.1:9090:9090"     # metrics, scrape locally
    healthcheck:
      test: ["CMD", "/usr/local/bin/dota_lobby", "healthcheck"]
      interval: 15s
      timeout: 3s
      retries: 4
      start_period: 60s
    logging:
      driver: json-file
      options: { max-size: "20m", max-file: "5" }
secrets:
  api_key:
    file: ./api_key.txt
volumes:
  dota_state: {}
```

`read_only: true` is fine because the only writable path is the volume.

### 10.3 Kubernetes notes

```yaml
apiVersion: apps/v1
kind: Deployment            # or a StatefulSet with replicas: 1 (stable PVC binding)
metadata: { name: dota-lobby, namespace: dota }
spec:
  replicas: 1
  strategy: { type: Recreate }      # NEVER RollingUpdate: it would overlap two pods on the same accounts
  selector: { matchLabels: { app: dota-lobby } }
  template:
    metadata:
      labels: { app: dota-lobby }
      annotations: { prometheus.io/scrape: "true", prometheus.io/port: "9090" }
    spec:
      terminationGracePeriodSeconds: 30
      securityContext: { runAsNonRoot: true, runAsUser: 65532, fsGroup: 65532, seccompProfile: { type: RuntimeDefault } }
      containers:
        - name: dota-lobby
          image: ghcr.io/kettleofketchup/dota_lobby:v0.x.y
          args: []
          ports: [{ name: http, containerPort: 8080 }, { name: metrics, containerPort: 9090 }]
          env:
            - { name: DOTA_LOBBY_CONFIG_DIR, value: /etc/dota_lobby }
            - { name: DOTA_LOBBY_SECRETS_DIR, value: /run/secrets/dota_lobby }
          livenessProbe:  { httpGet: { path: /livez,  port: http }, periodSeconds: 15, failureThreshold: 4 }
          readinessProbe: { httpGet: { path: /readyz, port: http }, periodSeconds: 10, failureThreshold: 1 }
          startupProbe:   { httpGet: { path: /livez,  port: http }, periodSeconds: 5,  failureThreshold: 24 }
          resources: { requests: { cpu: 50m, memory: 64Mi }, limits: { memory: 256Mi } }
          securityContext: { readOnlyRootFilesystem: true, allowPrivilegeEscalation: false, capabilities: { drop: [ALL] } }
          volumeMounts:
            - { name: config,  mountPath: /etc/dota_lobby, readOnly: true }
            - { name: secrets, mountPath: /run/secrets/dota_lobby, readOnly: true }
            - { name: state,   mountPath: /var/lib/dota_lobby }
      volumes:
        - { name: config,  configMap: { name: dota-lobby-config } }
        - { name: secrets, secret: { secretName: dota-lobby-secrets, defaultMode: 0400 } }
        - { name: state,   persistentVolumeClaim: { claimName: dota-lobby-state } }   # ReadWriteOnce
```

- **`Recreate` plus an RWO PVC** gives two separate guards against overlap. Even so, an RWO volume can be attached to two pods **on the same node**, so this is not a perfect lock. Add an app-level lock as well (below).
- **App-level lock**:
  - Docker / single host: hold a `flock` on `/var/lib/dota_lobby/LOCK` for the process lifetime and refuse to start if it is taken.
  - Kubernetes: use `k8s.io/client-go/tools/leaderelection` with a `Lease` (`dota-lobby-<shard>`). Only the leader logs bots in, and losing the lease means disconnecting every bot immediately. This needs a ServiceAccount with `get/create/update` on `coordination.k8s.io/leases`. It allows **active/passive** with a warm standby that is not logged in. Failover then takes the lease duration (about 15s) plus logon time.
- **Scale-out by sharding** (when there are many bots): run N Deployments, each with its own `bots:` list, state PVC and Lease. Put a thin router or the existing league backend in front to choose the shard by `league_id`. Never let two shards share a username, and validate that at startup (the existing duplicate check `manager.go:50-52` covers only a single process).
- PodDisruptionBudget: `maxUnavailable: 1` is effectively the default with one replica. Drains cause about 30-60s of downtime. Schedule node maintenance away from match times.
- NetworkPolicy egress (section 11), plus ingress only from the league backend namespace and Prometheus.

---

## 11. Network requirements

### 11.1 What go-steam actually uses

- **TCP only.** `dialTCP` uses `net.DialTCP("tcp", ...)` with Valve's `VT01` framing [V, go-steam `connection.go:24-41`]. There is no WebSocket or UDP transport in the pinned version [V, grep]. The upstream 2026 version is still TCP (`*net.TCPAddr`) [V, pkg.go.dev].
- CM selection uses the built-in `CMServers` list, which is all `:27017` [V, `servers.go:14-33`], or `InitializeSteamDirectory()` hitting `GetCMList/v1` over HTTPS [V, `steam_directory.go:30-34`]. **The current `manager.go` never calls `InitializeSteamDirectory()`**, so it uses the hard-coded 2025 list, which can go stale.
- Live check (2026-11-06): `GetCMList/v1/?cellId=0` returned only `*:27017` entries. `GetCMListForConnect/v1` returned `netfilter` entries on `:27017` and `websockets` entries on `cmp*-*.steamserver.net:443` and `:27018` [V, ran `curl`].

### 11.2 Egress allow-list

| Dest | Proto/Port | Required? | Purpose |
|---|---|---|---|
| Valve CMs (AS32590 ranges such as 155.133.0.0/16, 162.254.192.0/21, 185.25.180.0/22, 205.196.6.0/24, 103.10.124.0/24, 103.28.54.0/24, 146.66.152.0/21, 45.121.184.0/22) | **TCP 27017** | Yes | Steam CM connection (go-steam) |
| `api.steampowered.com` | TCP 443 | Yes (directory, and HTTP `IAuthenticationService` when using the gg-cr approach) | CM list, auth, Web API |
| Valve CMs | TCP/UDP 27015-27050 | Recommended allowance | Valve's official "logging in" range [V, Valve support article as quoted]; future-proof |
| `*.steamserver.net` | TCP 443 / 27018 | Only if you switch to a WebSocket-capable library | WebSocket CMs |
| `steamcommunity.com` | TCP 443 | Only if web session features are used | `client.Web` |
| DNS | UDP/TCP 53 | Yes | |

Notes:

- The GC traffic (Dota 2 GC) is tunnelled **inside** the CM connection (`EMsg_ClientToGC`), so no separate ports are needed [V, go-steam `gamecoordinator.go:51-66` writes through `client.Write`].
- The bots never host game servers. Lobby servers are Valve's. **No inbound ports** are needed except the API.
- IP ranges change. Prefer an egress proxy or NAT with an FQDN or AS-based policy, or keep the CIDRs in config and refresh them from `GetCMList`. The CIDRs above are taken from today's CM list and `servers.go` and are **not** an authoritative Valve list [I].
- If several bots share one egress IP, Steam rate limits are applied per IP as well [I]. Stagger the logins (section 13).

---

## 12. GC weekly maintenance and outage handling

- Steam's weekly maintenance is on **Tuesday, US Pacific**. The most commonly reported time is **about 4 PM PT**, which is 23:00 UTC under PDT and 00:00 UTC Wednesday under PST (steamstatus.app, the third-party site steamdb.com (not SteamDB at steamdb.info), the Steam Community forum). Some outlets report between noon and 3 PM PT (esports.gg, CNET) or 1 PM PT (Windows Report). Reported durations range from 10-30 minutes to about an hour, and the Dota 2 GC goes down with Steam [web; Valve publishes no official schedule, so the time and length are UNCERTAIN and should be confirmed by logging actual GC disconnects over a few weeks].
- Expected sequence: `STEAM_GOING_DOWN` or `GC_GOING_DOWN` status, then a disconnect or `FatalErrorEvent`, then failed reconnects (`ServiceUnavailable` or `TryAnotherCM` produce a `SteamFailureEvent` [V, `auth.go:126-130`]), then recovery.
- Handling:
  1. **Reconnect backoff**: exponential with full jitter, from 5s up to a 5m cap, per bot. Today it is a fixed 5s or 30s (`manager.go:111,133`), which hammers Steam during outages. Reset it after a stable session of 2 minutes or more.
  2. **Handle `SteamFailureEvent` and `FatalErrorEvent`**, not just `DisconnectedEvent` (A8). On a `TryAnotherCM`, pick a different CM.
  3. **GC hello loop**: after `LoggedOnEvent`, call `SetPlaying(true)` and then `SayHello()` every `gc.hello_interval` until a `ClientWelcomed` arrives. This is the standard pattern in node-dota2 and ValvePython dota2, where the client re-sends hello until it gets a welcome [I].
  4. **API behaviour**: `POST /lobby/create` returns `503` with `Retry-After: 60` and `{"error":"gc_unavailable"}` when no bot has `HAVE_SESSION`. Callers (the league backend) should queue.
  5. **Maintenance-aware alerting**: silence "no ready bots" alerts on Tuesdays 19:00-02:00 UTC (this covers both the noon-3 PM PT and the 4 PM PT reports, in PDT and PST), or alert only after more than 45 minutes in that window.
  6. **Lobby survival**: Lobbies in `RUN` state are on Valve game servers and normally survive a GC blip, but `ns.ClearState()` wipes local knowledge [V, `client_session.go:117`]. Re-sync from SOCache after the next welcome.

---

## 13. Rate limits and pacing

Valve does not publish limits for Steam logon or GC calls. All numbers here are conservative
defaults [I] and should be made configurable.

| Area | Signal | Default policy |
|---|---|---|
| Steam logon | `EResult_RateLimitExceeded` (84), `AccountLoginDeniedThrottle` (87) [V, enum] | Stagger bot logins by `steam.login_stagger` (10s). On 84 or 87, back off for at least 30 minutes for that bot (and consider pausing the others on the same IP). Do not retry a password logon more than about 3 times per hour. |
| Wrong credentials | `InvalidPassword` (5), `AccountLogonDenied` (63, needs a Steam Guard code), `TwoFactorCodeMismatch` (88) [V, enum] | Stop retrying, set state `needs_auth`, and alert. Retrying is pointless and leads to a lock. |
| GC requests | GC may ignore spam; go-dota2 `MakeRequest` waits on ctx [I] | Serialize GC requests per bot with a worker queue, add a timeout per request (10s), and cap concurrency at 1. Lobby creation is per bot anyway, because a bot can be in only one lobby. |
| Lobby creation | One lobby per bot at a time [I, a bot account has one `CSODOTALobby` in SOCache, V `state.go:13`] | Bot pool scheduler: assign by league capability, then by idleness. Return 503 when every eligible bot is busy. |
| Inbound API | - | A per-API-key token bucket (for example 5 req/s, burst 20) via `golang.org/x/time/rate`. Limit `/lobby/create` to about 1 per 2s per league. |
| CM directory | HTTPS GET at startup | Call once and cache it in `cmlist.json`. Refresh every 24h. Fall back to the built-in list. |

---

## 14. Operations runbook (short)

| Task | Steps |
|---|---|
| First deploy | Create `config.yaml` and `secrets.yaml`, run `docker compose run --rm -it dota_lobby auth --bot X` for each bot, then `docker compose up -d`, and check `/readyz`. |
| Add a bot | Add it to `config.yaml` and its password to `secrets.yaml`, stop the service (only one session per account), run `auth --bot`, then start the service. With a hot `POST /admin/bots/reload` this could be done without downtime later. |
| Token expired (`needs_auth`) | Re-run `auth --bot X`. With `shared_secret` set, recovery is automatic. |
| Planned restart | Avoid times when lobbies are in `RUN`. Check that `/status` shows none. `docker compose restart` gets the 30s grace period. |
| GC down outside Tuesday | Check steamstat.us and steamstatus.app. Do not restart the service, because the backoff handles it. |
| Bot `suspended` | Check the account in a real client. It may have a matchmaking or GC ban. Remove it from the pool. |
| Upgrade image | `Recreate` only. Watch `dota_lobby_steam_logons_total{result!="OK"}` afterwards. |

---

## 15. Implementation checklist (ordered)

1. Bump `github.com/paralin/go-steam` to the 2026-09 version (or later) and adopt `AccessToken`. Adapt to `LogOn(ctx, details) error`.
2. Token and state store (section 4) with atomic writes, plus an `auth` subcommand.
3. Bot lifecycle fixes: create the Dota2 client once (A6), `SetPlaying` plus the hello loop (A7), handle `FatalErrorEvent` and `SteamFailureEvent` (A8), drain events on shutdown (A9), backoff with jitter.
4. Viper env and `_FILE` support, plus `/etc/dota_lobby` (section 3).
5. `/livez`, `/readyz`, extended `/status` (section 5).
6. Graceful drain (section 6).
7. slog and the `Secret` hardening (sections 7 and 9).
8. Prometheus (section 8).
9. Dockerfile, `.dockerignore`, the just/make targets, the CI image job (section 2).
10. Compose and K8s manifests (section 10), plus the lock or lease.

---

## 16. Verified vs Uncertain

### Verified (source, docs or execution)

- The pinned go-steam `LogOnDetails` has `Username, Password, AuthCode, TwoFactorCode, SentryFileHash, LoginKey, ShouldRememberPassword` and no access-token field. `LogOn` always sends the password. (`go-steam/auth.go:22-82`)
- go-steam is TCP-only with `VT01` framing, and its built-in CM list is all port 27017. (`connection.go:24-41`, `servers.go:14-33`)
- `InitializeSteamDirectory` calls `https://api.steampowered.com/ISteamDirectory/GetCMList/v1/?cellId=0`, and `manager.go` never calls it. (`steam_directory.go:30-34`, `pkg/bot/manager.go`)
- A dial failure emits `FatalErrorEvent` without a `DisconnectedEvent`, and the manager doesn't handle it, so the bot stops reconnecting. (`client.go:156-176`, `manager.go:78-146`)
- `Emit` blocks on a channel buffered at 30. `Disconnect` emits while holding the mutex. (`client.go:64-66,89-91,171-190`)
- `dota2.New` appends a GC packet handler, and the manager calls it on every logon. (`go-dota2/client.go:52-66`, `go-steam/gamecoordinator.go:28-29`, `manager.go:95`)
- The `GCConnectionStatus` enum values 0-6 and the `CMsgConnectionStatus` queue fields. (`go-dota2/protocol/gcsdk_gcmessages.pb.go:141-158,2148-2158`)
- `GCConnectionStatusChanged` and `ClientWelcomed` are emitted on the steam event channel, and every status change clears lobby state. (`client_session.go:76-130`, `client.go:121-123`)
- `DestroyLobby(ctx)`, `LeaveLobby()`, `AbandonLobby()`, `SetPlaying(bool)`, `SayHello()` and `Close()` exist with the signatures cited.
- `CMsgPracticeLobbySetDetails` has `Leagueid` (field 16) and `LeagueNodeId` (field 49). (`protocol/dota_gcmessages_client_match_management.pb.go:874,900`)
- The pinned go-steam has no `LogOff` method. (grep)
- The EResult codes 5, 6, 63, 84, 87 and 88 exist in `steamlang/enums.go:3473-3561`.
- `Secret` leaks under `%#v` and redacts under `%v`, `%s`, `%q`, json and slog (ran it on Go 1.27.1).
- Live CM directory: TCP `netfilter` CMs on 27017 and `websockets` CMs on 443 and 27018 (curl, 2026-11-06).
- Upstream paralin/go-steam `v0.0.0-20260922065733-99f19aa307ad` has `LogOnDetails.AccessToken`, `Auth.Authenticate` and `Auth.LogOn(ctx, details) error` (pkg.go.dev).
- Valve moved to refresh tokens and stopped issuing login keys in 2023 (SteamKit #1125, node-steam-user v5 guide).

### Inferred or uncertain

- Whether legacy password-only logon (the current code) still succeeds for these specific accounts in late 2026. It needs a live test, and is expected to be unreliable.
- That an explicit LogOff revokes refresh tokens. No source supports this and it is probably wrong. It is moot because no go-steam version has LogOff.
- How duplicate sessions fail with go-steam: same-IP logon replacement (`LogonSessionReplaced`, 34) versus a cross-host playing conflict (`LoggedInElsewhere`, 6). This is documented for node-steam-user but not tested with go-steam here.
- Steam and GC rate-limit thresholds. Valve does not publish them, so all numbers are conservative guesses.
- The exact weekly maintenance time and length (most reports say about 4 PM PT Tuesday, some say noon-3 PM PT; 10-60 minutes).
- Whether `RUN`-state lobbies fully survive the host bot disconnecting. The expectation is that they do, because the game server is Valve's, but leaguestat and match result reporting may depend on the lobby leader.
- That RWO PVC plus `Recreate` is enough on its own. It is not on a single node, hence the lock or lease recommendation.
- Valve CM CIDR ranges. They are taken from current directory output, not an official list.
- That the upstream 2026 go-steam's `Authenticate` works reliably over CM. The gg-cr fork reports EOF issues with CM-based non-authed service calls and recommends HTTP `IAuthenticationService`.
- That the Steam Guard TOTP algorithm matches `steam-totp`. This is a common community implementation, not checked against source here.

---

## 17. Sources

Local (file:line):

- `/home/kettle/git_repos/dota_lobby/pkg/bot/manager.go:46-233`
- `/home/kettle/git_repos/dota_lobby/pkg/api/server.go:26-120`
- `/home/kettle/git_repos/dota_lobby/pkg/config/config.go:9-110`
- `/home/kettle/git_repos/dota_lobby/cmd/dota_lobby/main.go:22-76`
- `/home/kettle/git_repos/dota_lobby/Makefile`, `justfile`, `just/go.just`, `just/docs.just`, `.github/workflows/release.yml`, `.github/workflows/lint.yml`, `config.yaml.example`, `secrets.yaml.example`, `.gitignore`
- `/home/kettle/git_repos/dota_lobby/docs/plans/2026-01-22-status-system-design.md`
- go-steam (`~/.local/share/go/pkg/mod/github.com/paralin/go-steam@v0.0.0-20250502043548-f167ff28a93a/`): `auth.go:22-182`, `client.go:63-270`, `client_events.go`, `connection.go:24-41`, `servers.go:14-45`, `steam_directory.go:17-76`, `gamecoordinator.go:28-79`, `protocol/steamlang/enums.go:3473-3561`
- go-dota2 (`~/.local/share/go/pkg/mod/github.com/paralin/go-dota2@v0.0.0-20250623204622-532cb1851217/`): `client.go:52-196`, `client_session.go:14-131`, `client_lobby.go:13-72`, `client_generated.go:14,516-531,2073-2076`, `events/session.go`, `state/state.go`, `protocol/gcsdk_gcmessages.pb.go:141-158,2148-2158`, `protocol/dota_gcmessages_client_match_management.pb.go:874,900`

Web:

- paralin/go-steam package docs (2026-09 version, AccessToken and Authenticate): https://pkg.go.dev/github.com/paralin/go-steam
- gg-cr/go-steam fork (refresh-token logon, HTTP auth recommendation): https://pkg.go.dev/github.com/gg-cr/go-steam
- SteamKit "New Steam login flow" #1125: https://github.com/SteamRE/SteamKit/issues/1125
- SteamKit discussion #1258 (UpdateMachineAuth deprecated): https://github.com/SteamRE/SteamKit/discussions/1258
- SteamKit SteamDirectory.cs (websockets and netfilter CM types): https://github.com/SteamRE/SteamKit/blob/master/SteamKit2/SteamKit2/Steam/WebAPI/SteamDirectory.cs
- node-steam-user v4 to v5 migration (loginKey becomes refreshToken): https://github.com/DoctorMcKay/node-steam-user/wiki/v4-%E2%86%92-v5-migration-guide
- McKay forum, login via refresh token (SteamClient-platform token): https://dev.doctormckay.com/topic/4228-login-via-refresh-token/
- IAuthenticationService reference: https://steamapi.xpaw.me/IAuthenticationService
- Wabbajack PR #3005 (refresh token lifetime, InvalidPassword means expired): https://github.com/wabbajack-tools/wabbajack/pull/3005
- ArchiSteamFarm bot lifecycle: https://deepwiki.com/JustArchiNET/ArchiSteamFarm/4.1-bot-lifecycle-and-management
- Valve "Required Ports for Steam": https://help.steampowered.com/en/faqs/view/2EA8-4D75-DA21-31EB (as quoted in https://steamcommunity.com/discussions/forum/10/4147320315758044430/)
- steam-for-linux #11217 (websocket CMs and GetCMListForConnect cmtype): https://github.com/ValveSoftware/steam-for-linux/issues/11217
- Steam CM directory endpoints (queried live): https://api.steampowered.com/ISteamDirectory/GetCMList/v1/?cellId=0, https://api.steampowered.com/ISteamDirectory/GetCMListForConnect/v1/?cellid=0
- Duplicate sessions (LogonSessionReplaced / LoggedInElsewhere): https://github.com/DoctorMcKay/node-steam-user, https://dev.doctormckay.com/topic/1518-loggedinelsewhere/, https://github.com/DoctorMcKay/node-steam-user/issues/271
- Refresh-token revocation triggers: https://github.com/Rhgx/tf2-halloween-utils/pull/5
- Steam Tuesday maintenance: https://steamstatus.app, https://steamdb.com/en/tools/steam-server-status (third-party, not steamdb.info), https://dotesports.com/dota-2/news/is-dota-2-down-heres-how-to-check-server-status, https://www.esports.net/news/dota/dota-2-game-coordinator/
- Distroless images: https://github.com/GoogleContainerTools/distroless
- Kubernetes leader election (client-go): https://pkg.go.dev/k8s.io/client-go/tools/leaderelection
- Viper: https://github.com/spf13/viper

---

## Verification notes

- **A5 / sentry hash**: corrected. `SentryHash` is not only unused but never loaded, because the untagged field does not match the `sentry_hash` key (`config.go:49-53`). The claim that the existing `sentry_hash` format "can keep working as a fallback" was withdrawn.
- **Section 10.1 (replicas)**: the mechanism was rewritten. A same-IP duplicate logon gets `LogonSessionReplaced` (34), because go-steam never sets `ObfustucatedPrivateIp` (`auth.go:61-76`). A cross-host duplicate gets `LoggedInElsewhere` (6) only on the playing/GC session. Both are handled as "owned elsewhere" with a long backoff. The single replica, `Recreate`, lock or lease, and sharding advice is unchanged. The go-steam behavior is marked UNCERTAIN until a live test.
- **Section 6.1 (shutdown)**: the "LogOff revokes refresh tokens" rationale is marked unsupported and probably wrong. No go-steam version (pinned or upstream) has LogOff, so `Disconnect()` is the only option and is sufficient. The DestroyLobby, LeaveLobby, AbandonLobby, SetPlaying and Close references were re-checked and are unchanged.
- **Section 12 (maintenance)**: the timing now uses the most common report (about 4 PM PT Tuesday, 23:00 or 00:00 UTC) and notes the noon-3 PM PT outliers and durations of 10-60 minutes. The figure is credited to steamdb.com and steamstatus.app, not SteamDB. The time is marked UNCERTAIN, since there is no official Valve schedule. The 19:00-02:00 UTC alert-silence window is kept, because it covers both reports. EResult 84 and 87 were re-checked (`enums.go:3555,3560`).
