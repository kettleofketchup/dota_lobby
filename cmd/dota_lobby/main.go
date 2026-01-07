package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/api"
	"github.com/kettleofketchup/dota_lobby/pkg/bot"
	"github.com/kettleofketchup/dota_lobby/pkg/config"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	log.Printf("Dota Lobby Manager v%s (commit: %s, built: %s)", version, commit, buildDate)

	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	log.Printf("Server configuration: %s:%d", cfg.Server.Host, cfg.Server.Port)

	// Create bot manager
	botManager := bot.NewManager()
	defer botManager.Shutdown()

	// Add bots from configuration
	if len(cfg.Secrets.Bots) == 0 {
		log.Println("WARNING: No bots configured in secrets.yaml")
	} else {
		for i, botCfg := range cfg.Secrets.Bots {
			if err := botManager.AddBot(botCfg); err != nil {
				log.Printf("Failed to add bot %d (%s): %v", i+1, botCfg.Username, err)
			} else {
				log.Printf("Added bot: %s", botCfg.Username)
			}
		}
	}

	// Create and start API server
	apiServer := api.NewServer(cfg.Server.Host, cfg.Server.Port, botManager)

	// Start server in a goroutine
	go func() {
		if err := apiServer.Start(); err != nil {
			log.Fatalf("Failed to start API server: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down gracefully...")

	// Shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := apiServer.Shutdown(ctx); err != nil {
		log.Printf("Error shutting down API server: %v", err)
	}

	log.Println("Shutdown complete")
}
