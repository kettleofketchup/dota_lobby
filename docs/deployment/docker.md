# Docker Deployment

Deploy Dota Lobby Manager using Docker for a consistent, isolated environment.

## Quick Start

Pull and run the latest image:

```bash
docker pull kettleofketchup/dota_lobby:latest

docker run -d \
  --name dota_lobby \
  -p 8080:8080 \
  -e DOTA_LOBBY_STEAM_API_KEY=your_api_key \
  -e DOTA_LOBBY_BOTS_0_USERNAME=your_username \
  -e DOTA_LOBBY_BOTS_0_PASSWORD=your_password \
  -e DOTA_LOBBY_BOTS_0_ENABLED=true \
  kettleofketchup/dota_lobby:latest
```

## Image Tags

| Tag | Description |
|-----|-------------|
| `latest` | Latest stable release |
| `v1.0.0` | Specific version (e.g., 1.0.0) |
| `main` | Latest from main branch (development) |

## Using Environment Files

Create a `.env` file:

```bash
DOTA_LOBBY_SERVER_HOST=0.0.0.0
DOTA_LOBBY_SERVER_PORT=8080
DOTA_LOBBY_STEAM_API_KEY=your_api_key
DOTA_LOBBY_BOTS_0_NAME=bot1
DOTA_LOBBY_BOTS_0_USERNAME=your_username
DOTA_LOBBY_BOTS_0_PASSWORD=your_password
DOTA_LOBBY_BOTS_0_ENABLED=true
```

Run with env file:

```bash
docker run -d \
  --name dota_lobby \
  -p 8080:8080 \
  --env-file .env \
  kettleofketchup/dota_lobby:latest
```

## Using Configuration Files

Mount a custom config file:

```bash
docker run -d \
  --name dota_lobby \
  -p 8080:8080 \
  -v $(pwd)/config.yaml:/app/config.yaml:ro \
  kettleofketchup/dota_lobby:latest
```

## Volume Mounts

### Data Persistence

Mount a volume for persistent data:

```bash
docker run -d \
  --name dota_lobby \
  -p 8080:8080 \
  -v dota_lobby_data:/app/data \
  --env-file .env \
  kettleofketchup/dota_lobby:latest
```

### Log Files

Mount a volume for logs:

```bash
docker run -d \
  --name dota_lobby \
  -p 8080:8080 \
  -v $(pwd)/logs:/app/logs \
  --env-file .env \
  kettleofketchup/dota_lobby:latest
```

## Multi-Architecture Support

The Docker image supports multiple architectures:

- `linux/amd64` (x86_64)
- `linux/arm64` (ARM 64-bit)

Docker automatically pulls the correct image for your architecture:

```bash
# Works on both AMD64 and ARM64
docker pull kettleofketchup/dota_lobby:latest
```

## Building from Source

Clone the repository and build:

```bash
git clone https://github.com/kettleofketchup/dota_lobby.git
cd dota_lobby

docker build -t dota_lobby:local .
```

### Multi-stage Build

The Dockerfile uses multi-stage builds for optimization:

```dockerfile
# Stage 1: Build
FROM golang:1.24-alpine AS builder
# ... build process ...

# Stage 2: Runtime
FROM alpine:latest
# ... minimal runtime image ...
```

Benefits:
- Smaller final image size
- No build tools in production image
- Faster deployment

### Build Arguments

Customize the build:

```bash
# Build with specific Go version
docker build --build-arg GO_VERSION=1.24 -t dota_lobby:custom .
```

## Container Management

### View Logs

```bash
# Follow logs
docker logs -f dota_lobby

# Last 100 lines
docker logs --tail 100 dota_lobby
```

### Restart Container

```bash
docker restart dota_lobby
```

### Stop Container

```bash
docker stop dota_lobby
```

### Remove Container

```bash
docker rm dota_lobby
```

### Update to Latest Version

```bash
# Stop and remove old container
docker stop dota_lobby
docker rm dota_lobby

# Pull latest image
docker pull kettleofketchup/dota_lobby:latest

# Run new container
docker run -d \
  --name dota_lobby \
  -p 8080:8080 \
  --env-file .env \
  kettleofketchup/dota_lobby:latest
```

## Health Checks

The Docker image includes health checks:

```bash
# Check container health
docker inspect --format='{{.State.Health.Status}}' dota_lobby
```

Health check runs every 30 seconds and verifies the binary exists.

## Resource Limits

Limit container resources:

```bash
docker run -d \
  --name dota_lobby \
  -p 8080:8080 \
  --memory="512m" \
  --cpus="1.0" \
  --env-file .env \
  kettleofketchup/dota_lobby:latest
```

## Security

### Non-root User

The container runs as a non-root user (`dota:dota`, UID/GID 1000):

```dockerfile
USER dota
```

### Read-only Root Filesystem

Run with read-only root filesystem:

```bash
docker run -d \
  --name dota_lobby \
  -p 8080:8080 \
  --read-only \
  --tmpfs /tmp \
  --env-file .env \
  kettleofketchup/dota_lobby:latest
```

### Network Security

Use custom networks:

```bash
# Create network
docker network create dota_network

# Run container
docker run -d \
  --name dota_lobby \
  --network dota_network \
  -p 8080:8080 \
  --env-file .env \
  kettleofketchup/dota_lobby:latest
```

## Troubleshooting

### Container Exits Immediately

Check logs for errors:

```bash
docker logs dota_lobby
```

Common issues:
- Missing required environment variables
- Invalid configuration
- Port already in use

### Permission Denied

If mounting volumes, ensure correct permissions:

```bash
# Set ownership to UID/GID 1000 (dota user)
sudo chown -R 1000:1000 ./data
```

### Port Already in Use

Change the host port mapping:

```bash
# Map to different host port
docker run -d \
  --name dota_lobby \
  -p 9090:8080 \
  --env-file .env \
  kettleofketchup/dota_lobby:latest
```

### Cannot Pull Image

Verify Docker Hub access:

```bash
# Check connectivity
docker pull hello-world

# Pull specific version
docker pull kettleofketchup/dota_lobby:v1.0.0
```

## Advanced Configuration

### Custom Entrypoint

Override the entrypoint:

```bash
docker run -it \
  --entrypoint /bin/sh \
  kettleofketchup/dota_lobby:latest
```

### Debugging

Run interactively for debugging:

```bash
docker run -it --rm \
  -p 8080:8080 \
  --env-file .env \
  kettleofketchup/dota_lobby:latest
```

## Next Steps

- [Docker Compose Deployment](docker-compose.md)
- [Configuration Guide](../configuration/bot-setup.md)
- [API Examples](../api/examples.md)
