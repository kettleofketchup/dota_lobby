# Docker Compose Deployment

Deploy Dota Lobby Manager using Docker Compose for easy multi-container orchestration.

## Quick Start

1. Download the `docker-compose.yml` file:

```bash
wget https://raw.githubusercontent.com/kettleofketchup/dota_lobby/main/docker-compose.yml
```

2. Create a `.env` file:

```bash
# .env
STEAM_API_KEY=your_steam_api_key
BOT1_USERNAME=your_steam_username
BOT1_PASSWORD=your_steam_password
BOT2_USERNAME=second_bot_username
BOT2_PASSWORD=second_bot_password
```

3. Start the service:

```bash
docker-compose up -d
```

## docker-compose.yml

Here's the complete configuration:

```yaml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:latest
    container_name: dota_lobby
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      # Server Configuration
      - DOTA_LOBBY_SERVER_HOST=0.0.0.0
      - DOTA_LOBBY_SERVER_PORT=8080
      
      # Steam Configuration
      - DOTA_LOBBY_STEAM_API_KEY=${STEAM_API_KEY}
      
      # Bot Configuration
      - DOTA_LOBBY_BOTS_0_NAME=bot1
      - DOTA_LOBBY_BOTS_0_USERNAME=${BOT1_USERNAME}
      - DOTA_LOBBY_BOTS_0_PASSWORD=${BOT1_PASSWORD}
      - DOTA_LOBBY_BOTS_0_ENABLED=true
      
      - DOTA_LOBBY_BOTS_1_NAME=bot2
      - DOTA_LOBBY_BOTS_1_USERNAME=${BOT2_USERNAME}
      - DOTA_LOBBY_BOTS_1_PASSWORD=${BOT2_PASSWORD}
      - DOTA_LOBBY_BOTS_1_ENABLED=false
    
    volumes:
      - dota_lobby_data:/app/data
    
    healthcheck:
      test: ["CMD", "/bin/sh", "-c", "test -f /app/dota_lobby"]
      interval: 30s
      timeout: 3s
      start_period: 5s
      retries: 3

volumes:
  dota_lobby_data:
    driver: local
```

## Common Operations

### Start Services

```bash
# Start in background
docker-compose up -d

# Start with logs
docker-compose up

# Rebuild and start
docker-compose up -d --build
```

### View Logs

```bash
# Follow all logs
docker-compose logs -f

# Follow specific service
docker-compose logs -f dota_lobby

# Last 100 lines
docker-compose logs --tail=100
```

### Stop Services

```bash
# Stop services (keeps containers)
docker-compose stop

# Stop and remove containers
docker-compose down

# Stop, remove, and delete volumes
docker-compose down -v
```

### Restart Services

```bash
# Restart all services
docker-compose restart

# Restart specific service
docker-compose restart dota_lobby
```

### Check Status

```bash
docker-compose ps
```

## Advanced Configuration

### Using Config Files

Mount a custom configuration file:

```yaml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:latest
    volumes:
      - ./config.yaml:/app/config.yaml:ro
      - dota_lobby_data:/app/data
```

### Multiple Bots

Configure multiple bots:

```yaml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:latest
    environment:
      - DOTA_LOBBY_STEAM_API_KEY=${STEAM_API_KEY}
      
      # Bot 1
      - DOTA_LOBBY_BOTS_0_NAME=bot_us_east
      - DOTA_LOBBY_BOTS_0_USERNAME=${BOT1_USERNAME}
      - DOTA_LOBBY_BOTS_0_PASSWORD=${BOT1_PASSWORD}
      - DOTA_LOBBY_BOTS_0_ENABLED=true
      
      # Bot 2
      - DOTA_LOBBY_BOTS_1_NAME=bot_eu_west
      - DOTA_LOBBY_BOTS_1_USERNAME=${BOT2_USERNAME}
      - DOTA_LOBBY_BOTS_1_PASSWORD=${BOT2_PASSWORD}
      - DOTA_LOBBY_BOTS_1_ENABLED=true
      
      # Bot 3
      - DOTA_LOBBY_BOTS_2_NAME=bot_testing
      - DOTA_LOBBY_BOTS_2_USERNAME=${BOT3_USERNAME}
      - DOTA_LOBBY_BOTS_2_PASSWORD=${BOT3_PASSWORD}
      - DOTA_LOBBY_BOTS_2_ENABLED=false
```

### Custom Port

Change the exposed port:

```yaml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:latest
    ports:
      - "9090:8080"  # Host:Container
```

### Resource Limits

Set resource constraints:

```yaml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:latest
    deploy:
      resources:
        limits:
          cpus: '1.0'
          memory: 512M
        reservations:
          cpus: '0.5'
          memory: 256M
```

### Networks

Create custom networks:

```yaml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:latest
    networks:
      - dota_network

networks:
  dota_network:
    driver: bridge
```

## Production Deployment

### Using Specific Version

Pin to a specific version instead of `latest`:

```yaml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:v1.0.0  # Use specific version
```

### Logging Configuration

Configure logging:

```yaml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:latest
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "3"
```

### Restart Policy

Configure restart behavior:

```yaml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:latest
    restart: unless-stopped
    # Options: no, always, on-failure, unless-stopped
```

### Health Checks

Enhanced health check:

```yaml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:latest
    healthcheck:
      test: ["CMD", "/bin/sh", "-c", "test -f /app/dota_lobby"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s
```

## Environment File

Create `.env` file for sensitive data:

```bash
# .env
STEAM_API_KEY=XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
BOT1_USERNAME=steam_user1
BOT1_PASSWORD=secure_password_1
BOT2_USERNAME=steam_user2
BOT2_PASSWORD=secure_password_2
```

!!! warning "Security"
    Add `.env` to `.gitignore` to prevent committing secrets.

## Multiple Environments

### Development

```yaml
# docker-compose.dev.yml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:latest
    volumes:
      - .:/app:ro  # Mount source for development
    environment:
      - LOG_LEVEL=debug
```

Run with:
```bash
docker-compose -f docker-compose.yml -f docker-compose.dev.yml up
```

### Production

```yaml
# docker-compose.prod.yml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:v1.0.0
    restart: always
    deploy:
      resources:
        limits:
          cpus: '2.0'
          memory: 1G
```

Run with:
```bash
docker-compose -f docker-compose.yml -f docker-compose.prod.yml up -d
```

## Updating

### Update to Latest Version

```bash
# Pull latest image
docker-compose pull

# Recreate containers with new image
docker-compose up -d
```

### Zero-downtime Update

```bash
# Pull new image
docker-compose pull

# Recreate with no downtime
docker-compose up -d --no-deps --build dota_lobby
```

## Backup and Restore

### Backup Volumes

```bash
# Backup data volume
docker run --rm \
  -v dota_lobby_dota_lobby_data:/data \
  -v $(pwd):/backup \
  alpine tar czf /backup/dota_lobby_backup.tar.gz /data
```

### Restore Volumes

```bash
# Restore data volume
docker run --rm \
  -v dota_lobby_dota_lobby_data:/data \
  -v $(pwd):/backup \
  alpine tar xzf /backup/dota_lobby_backup.tar.gz -C /
```

## Troubleshooting

### Services Not Starting

```bash
# Check logs
docker-compose logs

# Check configuration
docker-compose config

# Validate compose file
docker-compose config --quiet && echo "Valid" || echo "Invalid"
```

### Port Conflicts

```bash
# Find what's using the port
sudo lsof -i :8080

# Change port in docker-compose.yml
ports:
  - "9090:8080"
```

### Volume Permission Issues

```bash
# Create volume with correct permissions
docker-compose run --rm --user root dota_lobby chown -R 1000:1000 /app/data
```

### Environment Variables Not Loading

```bash
# Verify .env file location (same directory as docker-compose.yml)
ls -la .env

# Check if variables are loaded
docker-compose config | grep STEAM_API_KEY
```

## Monitoring

### Resource Usage

```bash
# Monitor resource usage
docker stats dota_lobby

# Or with compose
docker-compose top
```

### Container Inspection

```bash
# Inspect container
docker-compose exec dota_lobby /bin/sh

# Check processes
docker-compose exec dota_lobby ps aux
```

## Integration Examples

### With Reverse Proxy (nginx)

```yaml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:latest
    networks:
      - internal
    # Don't expose port publicly

  nginx:
    image: nginx:alpine
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf:ro
    networks:
      - internal

networks:
  internal:
    driver: bridge
```

### With Monitoring (Prometheus)

```yaml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:latest
    ports:
      - "8080:8080"
    
  prometheus:
    image: prom/prometheus
    ports:
      - "9090:9090"
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml:ro
```

## Next Steps

- [Configuration Guide](../configuration/bot-setup.md)
- [Docker Deployment](docker.md)
- [API Examples](../api/examples.md)
