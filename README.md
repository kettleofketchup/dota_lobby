# dota_lobby

A Dota 2 lobby management service that provides an API endpoint for curating Dota 2 lobbies for league play. This service manages multiple Steam bot accounts to create and manage game lobbies.

## Features

- **Multi-Bot Management**: Support for multiple Steam bot accounts
- **REST API**: HTTP API for lobby creation and management
- **Configuration Management**: Separate configs for application settings and sensitive credentials using Viper
- **GitHub Automations**: Integrated CI/CD with golangci-lint and automated binary releases
- **Go-Dota2 Integration**: Uses the [go-dota2](https://github.com/paralin/go-dota2) API for bot operations

## Quick Start

### Prerequisites

- Go 1.24 or higher
- Steam accounts for bots
- Make (optional, for using Makefile commands)

### Installation

1. Clone the repository:
```bash
git clone https://github.com/kettleofketchup/dota_lobby.git
cd dota_lobby
```

2. Build the binary:
```bash
make build
```

3. Create configuration files:
```bash
cp config.yaml.example config.yaml
cp secrets.yaml.example secrets.yaml
```

4. Edit `secrets.yaml` with your Steam bot credentials:
```yaml
secrets:
  bots:
    - username: "your_steam_username"
      password: "your_steam_password"
      sentry_hash: ""  # Optional
```

5. (Optional) Customize `config.yaml`:
```yaml
server:
  host: "0.0.0.0"
  port: 8080
  # Optional: Enable API key authentication (recommended for production)
  api_key: "your-secret-api-key-here"
```

6. Run the service:
```bash
./bin/dota_lobby
```

## Configuration

The service uses two configuration files:

### config.yaml (Application Settings)
Contains non-sensitive application settings:
- `server.host`: Server bind address (default: "0.0.0.0")
- `server.port`: Server port (default: 8080)
- `server.api_key`: Optional API key for authentication (recommended for production)

### secrets.yaml (Bot Credentials)
Contains sensitive Steam bot credentials. **Keep this file secure and never commit it to version control.**

Configuration files are loaded from:
1. Current directory
2. `./config/` directory
3. `$HOME/.config/dota_lobby/` directory

## Authentication

The API supports optional API key authentication to restrict access to protected endpoints.

**When enabled** (by setting `server.api_key` in config.yaml):
- Protected endpoints require authentication
- API key can be provided via:
  - `X-API-Key` header: `X-API-Key: your-api-key`
  - `Authorization` header: `Authorization: Bearer your-api-key`
- Health endpoint (`/health`) remains public

**When disabled** (no `api_key` configured):
- All endpoints are public
- A warning is logged on startup

**Protected endpoints:**
- `GET /bots` - Bot status
- `POST /lobby/create` - Create lobby
- `GET|POST /lobby/info` - Lobby information

**Example authenticated request:**
```bash
# Using X-API-Key header
curl -H "X-API-Key: your-secret-key" http://localhost:8080/bots

# Using Authorization Bearer
curl -H "Authorization: Bearer your-secret-key" http://localhost:8080/bots
```

## API Endpoints

### Health Check
```
GET /health
```
Returns the service health status. **No authentication required.**

### Bot Status
```
GET /bots
```
Returns the list of configured bots and their connection status. **Requires authentication if API key is configured.**

### Create Lobby
```
POST /lobby/create
Content-Type: application/json
X-API-Key: your-api-key  # If authentication is enabled

{
  "lobby_name": "My League Lobby",
  "password": "optional_password",
  "server_region": "USE",
  "game_mode": "AP"
}
```
Creates a new Dota 2 lobby using an available bot. **Requires authentication if API key is configured.**

### Lobby Info
```
GET /lobby/info?lobby_id=12345
X-API-Key: your-api-key  # If authentication is enabled
```
or
```
POST /lobby/info
Content-Type: application/json
X-API-Key: your-api-key  # If authentication is enabled

{
  "lobby_id": "12345"
}
```
Retrieves information about a specific lobby. **Requires authentication if API key is configured.**

## Development

### Building
```bash
make build
```

### Running
```bash
make run
```

### Testing
```bash
make test
```

### Linting
```bash
make lint
```

### Cleaning
```bash
make clean
```

## GitHub Workflows

### Linting
Automatically runs `golangci-lint` on every push to main and on pull requests.

### Release
Creates a GitHub release with the compiled binary when a version tag (e.g., `v1.0.0`) is pushed:
```bash
git tag v1.0.0
git push origin v1.0.0
```

## Architecture

```
dota_lobby/
├── cmd/dota_lobby/      # Main application entry point
├── pkg/
│   ├── api/             # HTTP API server
│   ├── bot/             # Bot manager and Steam client handling
│   └── config/          # Configuration loading with Viper
├── .github/workflows/   # CI/CD workflows
├── config.yaml.example  # Example configuration
└── secrets.yaml.example # Example secrets configuration
```

## Security Notes

- Never commit `secrets.yaml` to version control
- **Enable API key authentication** in production by setting `server.api_key` in config.yaml
- Use strong, randomly generated API keys (e.g., 32+ characters)
- Use strong passwords for bot accounts
- Consider enabling Steam Guard for additional security
- Run the service behind a reverse proxy with HTTPS in production
- Rotate API keys periodically
- Monitor access logs for unauthorized attempts

## License

See LICENSE file for details.

## References

- [go-dota2](https://github.com/paralin/go-dota2) - Dota 2 client implementation
- [go-steam](https://github.com/paralin/go-steam) - Steam client implementation
- [Viper](https://github.com/spf13/viper) - Configuration management

