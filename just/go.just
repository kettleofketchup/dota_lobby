set working-directory := ".."

# Tools run through mise when it is installed, so the versions pinned in
# mise.toml (go, golangci-lint) are used.
RUN := `command -v mise >/dev/null 2>&1 && echo "mise exec --" || true`
GO := RUN + " go"
LINT := RUN + " golangci-lint"
BINARY_NAME := "dota_lobby"
BIN_DIR := justfile_directory() / "bin"
VERSION := `git describe --tags --always --dirty 2>/dev/null || echo "dev"`
COMMIT := `git rev-parse --short HEAD 2>/dev/null || echo "unknown"`
DATE := `date -u +%Y-%m-%dT%H:%M:%SZ`
LDFLAGS := "-s -w -X main.version=" + VERSION + " -X main.commit=" + COMMIT + " -X main.buildDate=" + DATE

# Build the binary
[group('go')]
build:
    @echo "Building {{BINARY_NAME}}..."
    @mkdir -p {{BIN_DIR}}
    {{GO}} build -trimpath -o {{BIN_DIR}}/{{BINARY_NAME}} -ldflags "{{LDFLAGS}}" ./cmd/dota_lobby

# Run the application
[group('go')]
run: build
    @echo "Version: {{VERSION}}, Commit: {{COMMIT}}, Date: {{DATE}}"
    @echo "Running {{BINARY_NAME}}..."
    {{BIN_DIR}}/{{BINARY_NAME}} serve

# Run tests
[group('go')]
test:
    @echo "Running tests..."
    {{GO}} test -race ./...

# Run tests with race detection and coverage
[group('go')]
test-full:
    {{GO}} test -race -cover ./...

# Format code
[group('go')]
format:
    @echo "Formatting code..."
    {{GO}} fmt ./...

# Run linter
[group('go')]
lint:
    @echo "Linting..."
    {{LINT}} run ./...

# Tidy and download dependencies
[group('go')]
deps:
    @echo "Installing dependencies..."
    {{GO}} mod tidy
    {{GO}} mod download

# Clean build artifacts
[group('go')]
clean:
    @echo "Cleaning..."
    {{GO}} clean
    rm -rf {{BIN_DIR}}
