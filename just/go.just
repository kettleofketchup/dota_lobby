BINARY_NAME := "dota_lobby"
BIN_DIR := env_var("PWD") / "bin"
VERSION := `git describe --tags --always --dirty 2>/dev/null || echo "dev"`
COMMIT := `git rev-parse --short HEAD 2>/dev/null || echo "unknown"`
DATE := `date -u +%Y-%m-%dT%H:%M:%SZ`
LDFLAGS := "-s -w -X main.version=" + VERSION + " -X main.commit=" + COMMIT + " -X main.buildDate=" + DATE

# Build the binary
[group('go')]
build: deps
    @echo "Building {{BINARY_NAME}}..."
    @mkdir -p {{BIN_DIR}}
    go build -trimpath -o {{BIN_DIR}}/{{BINARY_NAME}} -ldflags "{{LDFLAGS}}" ./cmd/dota_lobby

# Run the application
[group('go')]
run: build
    @echo "Version: {{VERSION}}, Commit: {{COMMIT}}, Date: {{DATE}}"
    @echo "Running {{BINARY_NAME}}..."
    {{BIN_DIR}}/{{BINARY_NAME}}

# Run tests
[group('go')]
test:
    @echo "Running tests..."
    go test -v ./...

# Run tests with race detection and coverage
[group('go')]
test-full:
    go test -race -cover ./...

# Format code
[group('go')]
format: deps
    @echo "Formatting code..."
    go fmt ./...

# Run linter
[group('go')]
lint: deps
    @echo "Linting..."
    golangci-lint run

# Install dependencies
[group('go')]
deps:
    @echo "Installing dependencies..."
    go mod tidy
    go mod download

# Clean build artifacts
[group('go')]
clean:
    @echo "Cleaning..."
    go clean
    rm -rf {{BIN_DIR}}
