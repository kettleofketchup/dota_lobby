# Bootstrap development environment
dev:
    @echo "==> Setting up dota_lobby development environment..."
    @echo "Checking Go..."
    @command -v go >/dev/null || (echo "ERROR: Go not found. Install from https://go.dev/dl/" && exit 1)
    @echo "Checking Docker..."
    @command -v docker >/dev/null || (echo "ERROR: Docker not found. Install from https://docker.com" && exit 1)
    @echo "Installing Go dependencies..."
    @go mod tidy
    @go mod download
    @echo "==> Development environment ready!"
    @echo ""
    @echo "Available commands:"
    @just --list

# Clean all build artifacts
clean:
    rm -rf bin/ site/ .venv/

# Full rebuild
rebuild: clean
    just go::build
    just docs::build
