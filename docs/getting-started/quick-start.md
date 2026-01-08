# Quick Start

Get your Dota Lobby Manager up and running in minutes!

## Step 1: Set Up Configuration

Create a `.env` file with your bot credentials:

```bash
# Server Configuration
DOTA_LOBBY_SERVER_HOST=0.0.0.0
DOTA_LOBBY_SERVER_PORT=8080

# Steam Configuration
DOTA_LOBBY_STEAM_API_KEY=your_steam_api_key_here

# Bot 1 Configuration
DOTA_LOBBY_BOTS_0_NAME=bot1
DOTA_LOBBY_BOTS_0_USERNAME=your_steam_username
DOTA_LOBBY_BOTS_0_PASSWORD=your_steam_password
DOTA_LOBBY_BOTS_0_ENABLED=true
```

!!! tip "Using Multiple Bots"
    To add more bots, increment the array index: `BOTS_1_`, `BOTS_2_`, etc.

## Step 2: Run with Docker

The easiest way to get started:

```bash
docker run -d \
  --name dota_lobby \
  --env-file .env \
  -p 8080:8080 \
  kettleofketchup/dota_lobby:latest
```

## Step 3: Run with Docker Compose

For a production setup:

```bash
# Create docker-compose.yml (see Deployment section)
docker-compose up -d
```

## Step 4: Verify It's Running

Check the logs:

```bash
docker logs dota_lobby
```

You should see output like:

```
Initializing lobby manager...
Registered bot: bot1 (username: your_username)
Lobby manager started with 1 bot(s)
Dota lobby manager started successfully
```

## Using Configuration Files

Alternatively, you can use a YAML configuration file:

1. Copy the example configuration:

```bash
cp config.example.yaml config.yaml
```

2. Edit `config.yaml` with your settings:

```yaml
server:
  host: "0.0.0.0"
  port: 8080

steam:
  api_key: "your_steam_api_key"

bots:
  - name: "bot1"
    username: "your_steam_username"
    password: "your_steam_password"
    enabled: true
```

3. Run with the config file:

```bash
./dota_lobby --config config.yaml
```

## Troubleshooting

### Bot Not Starting

- Verify Steam credentials are correct
- Check that Steam Guard is properly configured
- Ensure the Steam account has Dota 2 in its library

### Connection Issues

- Verify the Steam API key is valid
- Check firewall rules allow Steam connections
- Ensure the bot account isn't already logged in elsewhere

## Next Steps

- [Configure Multiple Bots](../configuration/bot-setup.md)
- [Learn About Viper Configuration](../configuration/viper.md)
- [Deploy to Production](../deployment/docker.md)
- [API Usage Examples](../api/examples.md)
