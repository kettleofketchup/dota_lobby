# Environment Variables Reference

Complete reference for all environment variables supported by Dota Lobby Manager.

## Server Configuration

### DOTA_LOBBY_SERVER_HOST

**Type**: String  
**Default**: `0.0.0.0`  
**Description**: Host address to bind the server to

```bash
DOTA_LOBBY_SERVER_HOST=0.0.0.0
```

### DOTA_LOBBY_SERVER_PORT

**Type**: Integer  
**Default**: `8080`  
**Range**: 1-65535  
**Description**: Port number for the server to listen on

```bash
DOTA_LOBBY_SERVER_PORT=8080
```

## Steam Configuration

### DOTA_LOBBY_STEAM_API_KEY

**Type**: String  
**Required**: Yes  
**Description**: Steam Web API key for accessing Steam services

```bash
DOTA_LOBBY_STEAM_API_KEY=XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
```

Get your API key at: [https://steamcommunity.com/dev/apikey](https://steamcommunity.com/dev/apikey)

## Bot Configuration

Bot configuration uses indexed environment variables. Each bot requires its own set of variables with an incrementing index starting from 0.

### DOTA_LOBBY_BOTS_{N}_NAME

**Type**: String  
**Required**: Yes  
**Description**: Unique identifier for the bot

```bash
DOTA_LOBBY_BOTS_0_NAME=bot1
DOTA_LOBBY_BOTS_1_NAME=bot2
```

### DOTA_LOBBY_BOTS_{N}_USERNAME

**Type**: String  
**Required**: Yes  
**Description**: Steam username for the bot account

```bash
DOTA_LOBBY_BOTS_0_USERNAME=my_steam_username
```

!!! warning "Security"
    Never hardcode Steam usernames in public repositories. Use environment variables or secret management.

### DOTA_LOBBY_BOTS_{N}_PASSWORD

**Type**: String  
**Required**: Yes  
**Description**: Steam password for the bot account

```bash
DOTA_LOBBY_BOTS_0_PASSWORD=my_secure_password
```

!!! danger "Critical Security"
    Always use secret management for passwords. Never commit passwords to version control.

### DOTA_LOBBY_BOTS_{N}_ENABLED

**Type**: Boolean  
**Default**: `true`  
**Values**: `true`, `false`  
**Description**: Whether this bot is enabled

```bash
DOTA_LOBBY_BOTS_0_ENABLED=true
DOTA_LOBBY_BOTS_1_ENABLED=false
```

## Complete Examples

### Single Bot Configuration

```bash
# Server
DOTA_LOBBY_SERVER_HOST=0.0.0.0
DOTA_LOBBY_SERVER_PORT=8080

# Steam
DOTA_LOBBY_STEAM_API_KEY=XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX

# Bot 1
DOTA_LOBBY_BOTS_0_NAME=primary_bot
DOTA_LOBBY_BOTS_0_USERNAME=steam_user
DOTA_LOBBY_BOTS_0_PASSWORD=secure_password
DOTA_LOBBY_BOTS_0_ENABLED=true
```

### Multiple Bots Configuration

```bash
# Server
DOTA_LOBBY_SERVER_HOST=0.0.0.0
DOTA_LOBBY_SERVER_PORT=8080

# Steam
DOTA_LOBBY_STEAM_API_KEY=XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX

# Bot 1
DOTA_LOBBY_BOTS_0_NAME=bot_us_east
DOTA_LOBBY_BOTS_0_USERNAME=steam_user1
DOTA_LOBBY_BOTS_0_PASSWORD=password1
DOTA_LOBBY_BOTS_0_ENABLED=true

# Bot 2
DOTA_LOBBY_BOTS_1_NAME=bot_eu_west
DOTA_LOBBY_BOTS_1_USERNAME=steam_user2
DOTA_LOBBY_BOTS_1_PASSWORD=password2
DOTA_LOBBY_BOTS_1_ENABLED=true

# Bot 3 (Disabled)
DOTA_LOBBY_BOTS_2_NAME=bot_testing
DOTA_LOBBY_BOTS_2_USERNAME=steam_user3
DOTA_LOBBY_BOTS_2_PASSWORD=password3
DOTA_LOBBY_BOTS_2_ENABLED=false
```

## Using .env Files

Create a `.env` file for local development:

```bash
# .env file
DOTA_LOBBY_SERVER_PORT=8080
DOTA_LOBBY_STEAM_API_KEY=your_api_key
DOTA_LOBBY_BOTS_0_USERNAME=your_username
DOTA_LOBBY_BOTS_0_PASSWORD=your_password
DOTA_LOBBY_BOTS_0_ENABLED=true
```

Load with Docker:

```bash
docker run --env-file .env kettleofketchup/dota_lobby:latest
```

## Secret Management Integration

### AWS Secrets Manager

```bash
#!/bin/bash
export DOTA_LOBBY_STEAM_API_KEY=$(aws secretsmanager get-secret-value \
  --secret-id dota_lobby/steam_api_key \
  --query SecretString \
  --output text)

export DOTA_LOBBY_BOTS_0_PASSWORD=$(aws secretsmanager get-secret-value \
  --secret-id dota_lobby/bot1_password \
  --query SecretString \
  --output text)

./dota_lobby
```

### HashiCorp Vault

```bash
#!/bin/bash
export DOTA_LOBBY_STEAM_API_KEY=$(vault kv get -field=api_key secret/dota_lobby/steam)
export DOTA_LOBBY_BOTS_0_PASSWORD=$(vault kv get -field=password secret/dota_lobby/bot1)

./dota_lobby
```

### Kubernetes Secrets

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: dota-lobby-secrets
type: Opaque
stringData:
  steam-api-key: "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
  bot1-username: "steam_user1"
  bot1-password: "secure_password"
---
apiVersion: v1
kind: Pod
metadata:
  name: dota-lobby
spec:
  containers:
  - name: dota-lobby
    image: kettleofketchup/dota_lobby:latest
    env:
    - name: DOTA_LOBBY_STEAM_API_KEY
      valueFrom:
        secretKeyRef:
          name: dota-lobby-secrets
          key: steam-api-key
    - name: DOTA_LOBBY_BOTS_0_USERNAME
      valueFrom:
        secretKeyRef:
          name: dota-lobby-secrets
          key: bot1-username
    - name: DOTA_LOBBY_BOTS_0_PASSWORD
      valueFrom:
        secretKeyRef:
          name: dota-lobby-secrets
          key: bot1-password
```

## Validation

Environment variables are validated at startup:

| Validation | Error Message |
|-----------|---------------|
| Port range | `invalid server port: X` |
| Bot configured | `at least one bot must be configured` |
| Bot username | `bot X: username is required` |
| Bot password | `bot X: password is required` |

## Troubleshooting

### Variables Not Being Read

1. Check the prefix: must be `DOTA_LOBBY_`
2. Check the format: uppercase with underscores
3. Check for typos in variable names
4. Verify variables are exported: `export VARIABLE_NAME=value`

### Array Index Issues

Bot configuration uses sequential indices starting from 0:

```bash
# ✓ Correct
DOTA_LOBBY_BOTS_0_NAME=bot1
DOTA_LOBBY_BOTS_1_NAME=bot2

# ✗ Incorrect (skips index)
DOTA_LOBBY_BOTS_0_NAME=bot1
DOTA_LOBBY_BOTS_2_NAME=bot2  # Missing index 1
```

### Docker Environment Variables

Pass environment variables to Docker:

```bash
# Single variable
docker run -e DOTA_LOBBY_SERVER_PORT=9090 kettleofketchup/dota_lobby:latest

# Multiple variables
docker run \
  -e DOTA_LOBBY_STEAM_API_KEY=xxx \
  -e DOTA_LOBBY_BOTS_0_USERNAME=user \
  kettleofketchup/dota_lobby:latest

# From file
docker run --env-file .env kettleofketchup/dota_lobby:latest
```

## See Also

- [Bot Setup](bot-setup.md)
- [Viper Integration](viper.md)
- [Docker Deployment](../deployment/docker.md)
