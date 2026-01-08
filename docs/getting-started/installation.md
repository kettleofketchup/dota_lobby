# Installation

This guide will help you install and set up the Dota Lobby Manager.

## Prerequisites

- Go 1.24 or higher (for building from source)
- Docker (for containerized deployment)
- Steam account(s) with Dota 2
- Steam API key

## Installation Methods

### Method 1: Using Docker (Recommended)

Pull the latest image from Docker Hub:

```bash
docker pull kettleofketchup/dota_lobby:latest
```

### Method 2: Using Docker Compose

1. Download the `docker-compose.yml` file:

```bash
wget https://raw.githubusercontent.com/kettleofketchup/dota_lobby/main/docker-compose.yml
```

2. Create a `.env` file with your configuration:

```bash
cp .env.example .env
# Edit .env with your credentials
```

### Method 3: Building from Source

1. Clone the repository:

```bash
git clone https://github.com/kettleofketchup/dota_lobby.git
cd dota_lobby
```

2. Install dependencies:

```bash
go mod download
```

3. Build the application:

```bash
go build -o dota_lobby
```

## Getting a Steam API Key

1. Go to [Steam Web API Key](https://steamcommunity.com/dev/apikey)
2. Sign in with your Steam account
3. Fill in the domain name (can be anything for local use)
4. Copy your API key

!!! warning "Security Notice"
    Never commit your Steam API key or bot credentials to version control. Always use environment variables or secret management systems.

## Next Steps

- [Quick Start Guide](quick-start.md) - Run your first lobby
- [Bot Setup](../configuration/bot-setup.md) - Configure your bots
- [Docker Deployment](../deployment/docker.md) - Deploy with Docker
