package bot

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
	"github.com/paralin/go-dota2"
	"github.com/paralin/go-steam"
	"github.com/paralin/go-steam/protocol/steamlang"
	"github.com/sirupsen/logrus"
)

// Bot represents a single Steam bot with Dota 2 capabilities
type Bot struct {
	Username    string
	steamClient *steam.Client
	dotaClient  *dota2.Dota2
	connected   bool
	mu          sync.RWMutex
}

// Manager manages multiple Steam bots
type Manager struct {
	bots   map[string]*Bot
	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
}

// NewManager creates a new bot manager
func NewManager() *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		bots:   make(map[string]*Bot),
		ctx:    ctx,
		cancel: cancel,
	}
}

// AddBot adds a bot to the manager
func (m *Manager) AddBot(cfg config.BotConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.bots[cfg.Username]; exists {
		return fmt.Errorf("bot with username %s already exists", cfg.Username)
	}

	bot := &Bot{
		Username:    cfg.Username,
		steamClient: steam.NewClient(),
	}

	m.bots[cfg.Username] = bot

	// Start bot connection in a goroutine
	go m.connectBot(bot, cfg)

	return nil
}

// connectBot handles the connection logic for a bot
func (m *Manager) connectBot(bot *Bot, cfg config.BotConfig) {
	bot.steamClient.Connect()

	for event := range bot.steamClient.Events() {
		select {
		case <-m.ctx.Done():
			return
		default:
		}

		switch e := event.(type) {
		case *steam.ConnectedEvent:
			log.Printf("[%s] Connected to Steam", bot.Username)
			bot.steamClient.Auth.LogOn(&steam.LogOnDetails{
				Username: cfg.Username,
				Password: cfg.Password,
			})

		case *steam.LoggedOnEvent:
			log.Printf("[%s] Logged on to Steam", bot.Username)
			bot.mu.Lock()
			bot.connected = true

			// Create a logrus logger for the dota client
			logger := logrus.New()
			logger.SetLevel(logrus.InfoLevel)

			bot.dotaClient = dota2.New(bot.steamClient, logger)
			bot.mu.Unlock()

			// Set online status
			bot.steamClient.Social.SetPersonaState(steamlang.EPersonaState_Online)

		case *steam.LogOnFailedEvent:
			log.Printf("[%s] Failed to log on: %v", bot.Username, e.Result)
			bot.mu.Lock()
			bot.connected = false
			bot.mu.Unlock()

			// Retry connection after delay
			time.Sleep(30 * time.Second)
			bot.steamClient.Connect()

		case *steam.DisconnectedEvent:
			log.Printf("[%s] Disconnected from Steam", bot.Username)
			bot.mu.Lock()
			bot.connected = false
			bot.mu.Unlock()

			// Reconnect after delay
			time.Sleep(5 * time.Second)
			bot.steamClient.Connect()

		case *steam.LoggedOffEvent:
			log.Printf("[%s] Logged off: %v", bot.Username, e.Result)
		}
	}
}

// GetBot retrieves a bot by username
func (m *Manager) GetBot(username string) (*Bot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	bot, exists := m.bots[username]
	if !exists {
		return nil, fmt.Errorf("bot with username %s not found", username)
	}

	return bot, nil
}

// GetAvailableBot returns the first available connected bot
func (m *Manager) GetAvailableBot() (*Bot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, bot := range m.bots {
		bot.mu.RLock()
		connected := bot.connected
		bot.mu.RUnlock()

		if connected {
			return bot, nil
		}
	}

	return nil, fmt.Errorf("no available bots")
}

// ListBots returns a list of all bot usernames and their connection status
func (m *Manager) ListBots() map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]bool)
	for username, bot := range m.bots {
		bot.mu.RLock()
		result[username] = bot.connected
		bot.mu.RUnlock()
	}

	return result
}

// GetDotaClient returns the Dota 2 client for a bot
func (b *Bot) GetDotaClient() (*dota2.Dota2, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if !b.connected || b.dotaClient == nil {
		return nil, fmt.Errorf("bot is not connected")
	}

	return b.dotaClient, nil
}

// IsConnected checks if the bot is connected
func (b *Bot) IsConnected() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.connected
}

// Shutdown gracefully shuts down the manager and all bots
func (m *Manager) Shutdown() {
	m.cancel()

	m.mu.Lock()
	defer m.mu.Unlock()

	for username, bot := range m.bots {
		log.Printf("Shutting down bot: %s", username)
		bot.steamClient.Disconnect()
	}
}
