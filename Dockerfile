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
