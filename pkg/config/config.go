package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// Config holds the application configuration
type Config struct {
	Server  ServerConfig
	Secrets SecretsConfig
}

// ServerConfig holds server-related configuration
type ServerConfig struct {
	Host string
	Port int
}

// SecretsConfig holds sensitive configuration (Steam credentials)
type SecretsConfig struct {
	Bots []BotConfig
}

// BotConfig holds configuration for a single Steam bot
type BotConfig struct {
	Username   string
	Password   string
	SentryHash string // Optional: for Steam Guard
}

// LoadConfig loads configuration from config files
func LoadConfig() (*Config, error) {
	// Load main config (non-sensitive settings)
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("./config")
	viper.AddConfigPath("$HOME/.config/dota_lobby")

	// Set defaults
	viper.SetDefault("server.host", "0.0.0.0")
	viper.SetDefault("server.port", 8080)

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("error reading config file: %w", err)
		}
		// Config file not found; use defaults
	}

	config := &Config{
		Server: ServerConfig{
			Host: viper.GetString("server.host"),
			Port: viper.GetInt("server.port"),
		},
	}

	// Unmarshal server config (will override defaults if present)
	if err := viper.UnmarshalKey("server", &config.Server); err != nil {
		return nil, fmt.Errorf("unable to decode server config: %w", err)
	}

	// Load secrets config separately
	secretsViper := viper.New()
	secretsViper.SetConfigName("secrets")
	secretsViper.SetConfigType("yaml")
	secretsViper.AddConfigPath(".")
	secretsViper.AddConfigPath("./config")
	secretsViper.AddConfigPath("$HOME/.config/dota_lobby")

	if err := secretsViper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			// Secrets file not found; log warning and continue without bots
			fmt.Println("WARNING: secrets.yaml not found - no bots will be configured")
		} else {
			return nil, fmt.Errorf("error reading secrets file: %w", err)
		}
	} else {
		if err := secretsViper.UnmarshalKey("secrets", &config.Secrets); err != nil {
			return nil, fmt.Errorf("unable to decode secrets config: %w", err)
		}
	}

	return config, nil
}
