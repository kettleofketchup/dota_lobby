BINARY_NAME := "dota_lobby"
BIN_DIR := justfile_directory() / "bin"
VERSION := `git describe --tags --always --dirty 2>/dev/null || echo "dev"`
COMMIT := `git rev-parse --short HEAD 2>/dev/null || echo "unknown"`
DATE := `date -u +%Y-%m-%dT%H:%M:%SZ`
LDFLAGS := "-s -w -X main.version=" + VERSION + " -X main.commit=" + COMMIT + " -X main.buildDate=" + DATE

# Build the binary
[group('go')]
build: deps
    @echo "Building {{BINARY_NAME}}..."
    @cd "{{justfile_directory()}}" && mkdir -p {{BIN_DIR}}
    cd "{{justfile_directory()}}" && go build -trimpath -o {{BIN_DIR}}/{{BINARY_NAME}} -ldflags "{{LDFLAGS}}" ./cmd/dota_lobby

# Run the application
[group('go')]
run: build
    @echo "Version: {{VERSION}}, Commit: {{COMMIT}}, Date: {{DATE}}"
    @echo "Running {{BINARY_NAME}}..."
    cd "{{justfile_directory()}}" && {{BIN_DIR}}/{{BINARY_NAME}}

# Run tests
[group('go')]
test:
    @echo "Running tests..."
    cd "{{justfile_directory()}}" && go test -v ./...

# Run tests with race detection and coverage
[group('go')]
test-full:
    cd "{{justfile_directory()}}" && go test -race -cover ./...

# Format code
[group('go')]
format: deps
    @echo "Formatting code..."
    cd "{{justfile_directory()}}" && go fmt ./...

# Run linter
[group('go')]
lint: deps
    @echo "Linting..."
    cd "{{justfile_directory()}}" && golangci-lint run

# Install dependencies
[group('go')]
deps:
    @echo "Installing dependencies..."
    cd "{{justfile_directory()}}" && go mod tidy
    cd "{{justfile_directory()}}" && go mod download

# Clean build artifacts
[group('go')]
clean:
    @echo "Cleaning..."
    cd "{{justfile_directory()}}" && go clean
    cd "{{justfile_directory()}}" && rm -rf {{BIN_DIR}}
