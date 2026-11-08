# Dota Lobby Service - Claude Code Instructions

## Project Overview

This is a **Go** backend service for managing Dota 2 lobbies via Steam bot accounts. It provides a REST API for creating and managing game lobbies for league play.

## Architecture

```
dota_lobby/
├── cmd/dota_lobby/      # Application entry point
├── pkg/
│   ├── api/             # HTTP API server (handlers, middleware)
│   ├── auth/            # Token store, TOTP, EResult classifier, state-dir lock
│   ├── bot/             # Per-bot supervisor, GC worker, manager
│   ├── gc/              # Only importer of go-steam/go-dota2 (+ fake/)
│   └── config/          # Configuration loading with Viper
```

**Key dependencies:**
- [go-dota2](https://github.com/paralin/go-dota2) - Dota 2 client
- [go-steam](https://github.com/paralin/go-steam) - Steam client
- [Viper](https://github.com/spf13/viper) - Configuration management

## Development Commands

```bash
make build    # Build the binary to ./bin/dota_lobby
make run      # Build and run the service
make test     # Run tests
make lint     # Run golangci-lint
make clean    # Clean build artifacts
```

## Code Style Guidelines

### Go Conventions
- Follow standard Go formatting (`gofmt`)
- Use `golangci-lint` for linting (see `.golangci.yml`)
- Error handling: wrap errors with context using `fmt.Errorf("context: %w", err)`
- Use the `Secret` type from `pkg/config` for sensitive values to prevent logging

### Project Patterns
- **Configuration**: Use Viper for config loading, separate `config.yaml` (settings) from `secrets.yaml` (credentials)
- **API Authentication**: Optional API key auth via `X-API-Key` or `Authorization: Bearer` headers
- **Security**: Use `Secret` type for sensitive fields - they redact automatically in logs/JSON

## Configuration Files

- `config.yaml` - Application settings (host, port, state_dir, bots)
- `secrets.yaml` - API key and Steam bot credentials (**never commit**)
- `.golangci.yml` - Linter configuration

## API Endpoints

| Method | Endpoint | Auth Required | Description |
|--------|----------|---------------|-------------|
| GET | /livez | No | Liveness (process + bot loop watchdog) |
| GET | /readyz | No | Readiness (min_ready_bots in gc_ready, not draining) |
| GET | /health | No | Detailed health, always 200 |
| GET | /v1/bots, /v1/bots/{name} | Yes* | Bot states |
| any | /v1/lobbies/... | Yes* | 501 until Phase 3 |

*Only if `secrets.api_key` is configured

## Testing Notes

- Run `make test` before committing
- CI runs `golangci-lint` on all PRs and pushes to main

## Security Considerations

- Never commit `secrets.yaml` or expose bot credentials
- Use `Secret` type for any sensitive configuration fields
- API key validation uses constant-time comparison
- Enable API key authentication in production
