package lobby

import (
	"fmt"
	"log"
	"sync"

	"github.com/kettleofketchup/dota_lobby/internal/config"
)

// Manager manages Dota 2 lobby creation and bot coordination
type Manager struct {
	config *config.Config
	bots   map[string]*Bot
	mu     sync.RWMutex
}

// Bot represents a Steam bot instance
type Bot struct {
	Name     string
	Username string
	Enabled  bool
}

// NewManager creates a new lobby manager
func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		config: cfg,
		bots:   make(map[string]*Bot),
	}
}

// Start initializes and starts the lobby manager
func (m *Manager) Start() error {
	log.Println("Initializing lobby manager...")

	// Initialize bots from configuration
	for _, botCfg := range m.config.Bots {
		if !botCfg.Enabled {
			log.Printf("Bot %s is disabled, skipping", botCfg.Name)
			continue
		}

		bot := &Bot{
			Name:     botCfg.Name,
			Username: botCfg.Username,
			Enabled:  botCfg.Enabled,
		}

		m.mu.Lock()
		m.bots[bot.Name] = bot
		m.mu.Unlock()

		log.Printf("Registered bot: %s (username: %s)", bot.Name, bot.Username)
	}

	if len(m.bots) == 0 {
		return fmt.Errorf("no enabled bots found")
	}

	log.Printf("Lobby manager started with %d bot(s)", len(m.bots))
	return nil
}

// GetBots returns all registered bots
func (m *Manager) GetBots() []*Bot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	bots := make([]*Bot, 0, len(m.bots))
	for _, bot := range m.bots {
		bots = append(bots, bot)
	}
	return bots
}
