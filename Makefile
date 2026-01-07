# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOMOD=$(GOCMD) mod

# Binary name
BINARY_NAME=dota_lobby

# Setup the -ldflags option for go build here, conditionally
# adding the version information.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -ldflags "-s -w \
    -X main.version=$(VERSION) \
    -X main.commit=$(COMMIT) \
    -X main.buildDate=$(DATE)"

# Phony targets are not real files
.PHONY: all build run test clean deps install lint format

# Default target
all: build

# Build the binary
build: deps
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p bin
	$(GOBUILD) -trimpath -o bin/$(BINARY_NAME) $(LDFLAGS) ./cmd/dota_lobby

# Run the application
run: build
	@echo "Version: $(VERSION), Commit: $(COMMIT), Date: $(DATE)"
	@echo "Running $(BINARY_NAME)..."
	./bin/$(BINARY_NAME)

# Run tests
test:
	@echo "Running tests..."
	$(GOTEST) -v ./...

# Clean up build artifacts
clean:
	@echo "Cleaning..."
	$(GOCLEAN)
	rm -rf bin/

format: deps
	@echo "Formatting code..."
	$(GOCMD) fmt ./...

lint: format
	@echo "Linting..."
	golangci-lint run

# Install dependencies
deps:
	@echo "Installing dependencies..."
	$(GOMOD) tidy
	$(GOMOD) download
