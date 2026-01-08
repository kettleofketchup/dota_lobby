# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o dota_lobby .

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk --no-cache add ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1000 dota && \
    adduser -D -u 1000 -G dota dota

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/dota_lobby .

# Copy example config
COPY config.example.yaml ./config.example.yaml

# Change ownership
RUN chown -R dota:dota /app

# Switch to non-root user
USER dota

# Expose port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/bin/sh", "-c", "test -f /app/dota_lobby"]

# Run the application
ENTRYPOINT ["/app/dota_lobby"]
