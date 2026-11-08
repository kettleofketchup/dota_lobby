# syntax=docker/dockerfile:1

# Keep the Go version in step with mise.toml. go.mod asks for 1.26.0 or newer.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.1-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
WORKDIR /src
ENV GOTOOLCHAIN=local
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
# An empty state dir, copied below with the runtime user as owner. A new named
# volume mounted on /app/state starts as a copy of it, so the service can
# write there without a chown on the host.
RUN mkdir -m 0700 /out/state

FROM gcr.io/distroless/static-debian13:nonroot
LABEL org.opencontainers.image.source="https://github.com/kettleofketchup/dota_lobby" \
      org.opencontainers.image.description="Dota 2 lobby service driven by Steam bot accounts" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/dota_lobby /usr/local/bin/dota_lobby
COPY --from=build --chown=65532:65532 /out/state /app/state
# Config is looked up relative to the working directory, so with WORKDIR /app
# it lives at /app/config/config.yaml (and optionally /app/config/secrets.yaml).
# Secrets are also looked up in /run/secrets/dota_lobby/secrets.yaml.
WORKDIR /app
# The token store and LOCK file live here; mount a volume on it. This env
# value overrides state_dir from config.yaml.
ENV DOTA_LOBBY_STATE_DIR=/app/state
USER 65532:65532
EXPOSE 8080
# GET /livez on 127.0.0.1:${DOTA_LOBBY_SERVER_PORT:-8080}. Liveness only: it
# does not depend on Steam, so a bot that cannot log on never restarts the
# container. Readiness is /readyz.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/dota_lobby", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/dota_lobby"]
CMD ["serve"]
