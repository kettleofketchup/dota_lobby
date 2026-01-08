# Viper Integration

The Dota Lobby Manager uses [Viper](https://github.com/spf13/viper) for configuration management, providing a flexible and powerful way to configure your application.

## What is Viper?

Viper is a complete configuration solution for Go applications. It supports:

- Reading from JSON, TOML, YAML, HCL, and Java properties files
- Reading from environment variables
- Reading from command line flags
- Setting explicit defaults
- Watching and re-reading config files

## Configuration Priority

Viper loads configuration in the following order (highest to lowest priority):

1. **Explicit calls** (if manually set in code)
2. **Command line flags**
3. **Environment variables**
4. **Configuration file**
5. **Defaults**

This means environment variables override configuration file values.

## Configuration File Locations

Viper searches for configuration files in:

1. Current directory (`./config.yaml`)
2. Config directory (`./config/config.yaml`)
3. System directory (`/etc/dota_lobby/config.yaml`)

You can use any of these locations for your `config.yaml` file.

## Environment Variable Mapping

Environment variables are automatically mapped to configuration keys:

| Configuration Key | Environment Variable |
|------------------|---------------------|
| `server.host` | `DOTA_LOBBY_SERVER_HOST` |
| `server.port` | `DOTA_LOBBY_SERVER_PORT` |
| `steam.api_key` | `DOTA_LOBBY_STEAM_API_KEY` |
| `bots[0].name` | `DOTA_LOBBY_BOTS_0_NAME` |
| `bots[0].username` | `DOTA_LOBBY_BOTS_0_USERNAME` |
| `bots[0].password` | `DOTA_LOBBY_BOTS_0_PASSWORD` |
| `bots[0].enabled` | `DOTA_LOBBY_BOTS_0_ENABLED` |

**Pattern**: `DOTA_LOBBY_` + uppercase key path with `.` replaced by `_`

## Configuration Structure

### Complete Configuration Example

```yaml
# config.yaml
server:
  host: "0.0.0.0"
  port: 8080

steam:
  api_key: "your_steam_api_key"

bots:
  - name: "bot1"
    username: "steam_user1"
    password: "password1"
    enabled: true
    
  - name: "bot2"
    username: "steam_user2"
    password: "password2"
    enabled: false
```

### Environment Variable Example

```bash
# Equivalent to above YAML
export DOTA_LOBBY_SERVER_HOST="0.0.0.0"
export DOTA_LOBBY_SERVER_PORT="8080"
export DOTA_LOBBY_STEAM_API_KEY="your_steam_api_key"

export DOTA_LOBBY_BOTS_0_NAME="bot1"
export DOTA_LOBBY_BOTS_0_USERNAME="steam_user1"
export DOTA_LOBBY_BOTS_0_PASSWORD="password1"
export DOTA_LOBBY_BOTS_0_ENABLED="true"

export DOTA_LOBBY_BOTS_1_NAME="bot2"
export DOTA_LOBBY_BOTS_1_USERNAME="steam_user2"
export DOTA_LOBBY_BOTS_1_PASSWORD="password2"
export DOTA_LOBBY_BOTS_1_ENABLED="false"
```

## Hybrid Configuration

You can combine YAML and environment variables:

**config.yaml:**
```yaml
server:
  host: "0.0.0.0"
  port: 8080

steam:
  api_key: ""  # Will be overridden by env var

bots:
  - name: "bot1"
    username: ""  # Will be overridden by env var
    password: ""  # Will be overridden by env var
    enabled: true
```

**Environment:**
```bash
export DOTA_LOBBY_STEAM_API_KEY="secret_key"
export DOTA_LOBBY_BOTS_0_USERNAME="my_user"
export DOTA_LOBBY_BOTS_0_PASSWORD="my_pass"
```

This approach allows you to:
- Keep structure in YAML
- Keep secrets in environment variables
- Version control the YAML (without secrets)

## Best Practices

### For Development

Use a local config file:

```bash
# Copy example config
cp config.example.yaml config.yaml

# Edit with your development values
vim config.yaml

# Run application (config.yaml is in .gitignore)
./dota_lobby
```

### For Production

Use environment variables with secret management:

```bash
# Set via system environment or container orchestration
export DOTA_LOBBY_STEAM_API_KEY="$(aws secretsmanager get-secret-value ...)"
export DOTA_LOBBY_BOTS_0_PASSWORD="$(vault kv get ...)"

# Run application
./dota_lobby
```

### For Docker

Use environment variables in docker-compose.yml:

```yaml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:latest
    environment:
      - DOTA_LOBBY_STEAM_API_KEY=${STEAM_API_KEY}
      - DOTA_LOBBY_BOTS_0_USERNAME=${BOT_USERNAME}
      - DOTA_LOBBY_BOTS_0_PASSWORD=${BOT_PASSWORD}
```

Or mount a config file:

```yaml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:latest
    volumes:
      - ./config.yaml:/app/config.yaml:ro
```

## Validation

Configuration is validated on startup:

```go
// Validation rules:
- Server port must be between 1-65535
- At least one bot must be configured
- Each bot must have username and password
- Bot names should be unique (recommended)
```

If validation fails, the application will exit with an error message.

## Advanced Usage

### Custom Config Path

Specify a custom config file location:

```bash
./dota_lobby --config /path/to/config.yaml
```

### Config Reload

Currently, configuration is loaded once at startup. For config changes to take effect, restart the application:

```bash
# Docker
docker restart dota_lobby

# Docker Compose
docker-compose restart

# Binary
# Stop and restart ./dota_lobby
```

## Troubleshooting

### Config Not Found

**Error**: `Config file not found`

**Solution**: 
- Ensure config.yaml exists in current directory, ./config/, or /etc/dota_lobby/
- Or use environment variables instead

### Environment Variables Not Working

**Issue**: Env vars don't override config file

**Check**:
1. Correct prefix: `DOTA_LOBBY_`
2. Correct format: uppercase, `.` → `_`
3. Example: `server.port` → `DOTA_LOBBY_SERVER_PORT`

### Validation Errors

**Error**: `invalid configuration: bot 0: username is required`

**Solution**: Ensure all required fields are set:
- Bot username (cannot be empty)
- Bot password (cannot be empty)
- At least one bot must be configured

## See Also

- [Bot Setup Guide](bot-setup.md)
- [Environment Variables Reference](environment.md)
- [Viper Documentation](https://github.com/spf13/viper)
