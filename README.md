# Dota Lobby Manager

Creates and manages Dota 2 lobbies automatically using Steam bot accounts.

## Features

- 🤖 **Bot Management**: Configure and manage multiple Steam bot accounts
- ⚙️ **Viper Configuration**: Flexible configuration using Viper (YAML, environment variables)
- 🐳 **Docker Support**: Easy deployment with Docker and Docker Compose
- 🔒 **Secure**: Environment variable support for sensitive credentials
- 📦 **Multi-stage Builds**: Optimized Docker images for production
- 📚 **Comprehensive Documentation**: Full documentation with MkDocs

## Quick Start

### Using Docker

```bash
docker run -d \
  --name dota_lobby \
  -p 8080:8080 \
  -e DOTA_LOBBY_STEAM_API_KEY=your_api_key \
  -e DOTA_LOBBY_BOTS_0_USERNAME=your_username \
  -e DOTA_LOBBY_BOTS_0_PASSWORD=your_password \
  -e DOTA_LOBBY_BOTS_0_ENABLED=true \
  kettleofketchup/dota_lobby:latest
```

### Using Docker Compose

1. Create a `.env` file:
```bash
STEAM_API_KEY=your_steam_api_key
BOT1_USERNAME=your_steam_username
BOT1_PASSWORD=your_steam_password
```

2. Run with docker-compose:
```bash
docker-compose up -d
```

### Building from Source

```bash
git clone https://github.com/kettleofketchup/dota_lobby.git
cd dota_lobby
go build -o dota_lobby
./dota_lobby
```

## Configuration

Configuration can be provided via YAML file or environment variables:

### YAML Configuration

```yaml
server:
  host: "0.0.0.0"
  port: 8080

steam:
  api_key: "your_steam_api_key"

bots:
  - name: "bot1"
    username: "steam_username"
    password: "steam_password"
    enabled: true
```

### Environment Variables

```bash
DOTA_LOBBY_SERVER_PORT=8080
DOTA_LOBBY_STEAM_API_KEY=your_api_key
DOTA_LOBBY_BOTS_0_NAME=bot1
DOTA_LOBBY_BOTS_0_USERNAME=your_username
DOTA_LOBBY_BOTS_0_PASSWORD=your_password
DOTA_LOBBY_BOTS_0_ENABLED=true
```

See `.env.example` for a complete example.

## Documentation

📖 **Full documentation is available at**: [https://kettleofketchup.github.io/dota_lobby](https://kettleofketchup.github.io/dota_lobby)

- [Installation Guide](https://kettleofketchup.github.io/dota_lobby/getting-started/installation/)
- [Quick Start](https://kettleofketchup.github.io/dota_lobby/getting-started/quick-start/)
- [Bot Configuration](https://kettleofketchup.github.io/dota_lobby/configuration/bot-setup/)
- [Viper Integration](https://kettleofketchup.github.io/dota_lobby/configuration/viper/)
- [Docker Deployment](https://kettleofketchup.github.io/dota_lobby/deployment/docker/)
- [API Examples](https://kettleofketchup.github.io/dota_lobby/api/examples/)

## Docker Hub

Docker images are available on Docker Hub: [kettleofketchup/dota_lobby](https://hub.docker.com/r/kettleofketchup/dota_lobby)

```bash
# Pull latest version
docker pull kettleofketchup/dota_lobby:latest

# Pull specific version
docker pull kettleofketchup/dota_lobby:v1.0.0
```

Images are automatically built and pushed when version tags are created.

## Requirements

- Go 1.24+ (for building from source)
- Docker (for containerized deployment)
- Steam account(s) with Dota 2
- Steam API key ([Get one here](https://steamcommunity.com/dev/apikey))

## Security

⚠️ **Important Security Notes:**

- Never commit credentials to version control
- Use environment variables or secret management for sensitive data
- Always add `.env` to `.gitignore`
- Run behind authentication in production
- Use HTTPS in production environments

## Development

### Run Locally

```bash
# Install dependencies
go mod download

# Run with example config
cp config.example.yaml config.yaml
# Edit config.yaml with your settings
go run main.go
```

### Build Docker Image

```bash
docker build -t dota_lobby:local .
```

### Run Tests

```bash
go test ./...
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Support

- 📝 [Documentation](https://kettleofketchup.github.io/dota_lobby)
- 🐛 [Issue Tracker](https://github.com/kettleofketchup/dota_lobby/issues)
- 💬 [Discussions](https://github.com/kettleofketchup/dota_lobby/discussions)
