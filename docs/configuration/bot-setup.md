# Bot Setup

This guide explains how to configure Steam bots for the Dota Lobby Manager.

## Bot Configuration Overview

Each bot requires:

- A unique name (identifier)
- Steam username
- Steam password
- Enable/disable flag

## Configuration Methods

### Using Environment Variables

Environment variables follow the pattern: `DOTA_LOBBY_BOTS_{index}_{field}`

```bash
# Bot 0
DOTA_LOBBY_BOTS_0_NAME=lobby_bot_1
DOTA_LOBBY_BOTS_0_USERNAME=steam_user1
DOTA_LOBBY_BOTS_0_PASSWORD=secret_password_1
DOTA_LOBBY_BOTS_0_ENABLED=true

# Bot 1
DOTA_LOBBY_BOTS_1_NAME=lobby_bot_2
DOTA_LOBBY_BOTS_1_USERNAME=steam_user2
DOTA_LOBBY_BOTS_1_PASSWORD=secret_password_2
DOTA_LOBBY_BOTS_1_ENABLED=false
```

### Using YAML Configuration

```yaml
bots:
  - name: "lobby_bot_1"
    username: "steam_user1"
    password: "secret_password_1"
    enabled: true
    
  - name: "lobby_bot_2"
    username: "steam_user2"
    password: "secret_password_2"
    enabled: false
```

## Best Practices

### Security

!!! danger "Never Hardcode Credentials"
    Always use environment variables or secret management for credentials. Never commit passwords to version control.

**Recommended Approach:**

1. Use environment variables in production
2. Use secret management services (e.g., AWS Secrets Manager, HashiCorp Vault)
3. Use `.env` files for local development (added to `.gitignore`)

### Bot Account Setup

1. **Create Dedicated Steam Accounts**: Use separate accounts for bots
2. **Enable Steam Guard**: Configure Mobile Authenticator
3. **Own Dota 2**: Each bot account needs Dota 2 in its library
4. **Verify Email**: Ensure accounts are verified

### Naming Convention

Use descriptive names for your bots:

```yaml
bots:
  - name: "lobby_bot_us_east"
    username: "..."
    # US East server bot
    
  - name: "lobby_bot_eu_west"
    username: "..."
    # EU West server bot
```

## Configuration Validation

The application validates:

- ✓ Username is not empty
- ✓ Password is not empty
- ✓ At least one bot is configured
- ✓ Port is in valid range (1-65535)

## Managing Multiple Bots

### Example: 3 Bots Configuration

```yaml
bots:
  - name: "primary_bot"
    username: "bot1_steam"
    password: "${BOT1_PASSWORD}"  # From environment
    enabled: true
    
  - name: "backup_bot"
    username: "bot2_steam"
    password: "${BOT2_PASSWORD}"
    enabled: true
    
  - name: "testing_bot"
    username: "bot3_steam"
    password: "${BOT3_PASSWORD}"
    enabled: false  # Disabled for production
```

### Enabling/Disabling Bots

To temporarily disable a bot without removing its configuration:

```yaml
bots:
  - name: "maintenance_bot"
    username: "bot_user"
    password: "bot_pass"
    enabled: false  # Set to false to disable
```

Or via environment variable:

```bash
DOTA_LOBBY_BOTS_0_ENABLED=false
```

## Troubleshooting

### Bot Fails to Start

**Symptoms**: Bot doesn't appear in logs as "Registered"

**Solutions**:
1. Verify credentials are correct
2. Check `enabled: true` is set
3. Review logs for validation errors

### Multiple Bots Conflict

**Symptoms**: Bots interfere with each other

**Solutions**:
1. Ensure each bot has unique Steam account
2. Don't log in to same account from multiple places
3. Use different bot names

### Configuration Not Loading

**Symptoms**: Bots don't start, "at least one bot must be configured" error

**Solutions**:
1. Check environment variable naming (DOTA_LOBBY_BOTS_0_...)
2. Verify config.yaml is in the correct location
3. Check file permissions

## Next Steps

- [Viper Integration](viper.md) - Learn about Viper configuration
- [Environment Variables](environment.md) - Complete env var reference
- [API Examples](../api/examples.md) - Use the API
