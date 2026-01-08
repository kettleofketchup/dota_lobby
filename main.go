package main

import (
	"fmt"
	"log"
	"os"

	"github.com/kettleofketchup/dota_lobby/internal/config"
	"github.com/kettleofketchup/dota_lobby/internal/lobby"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize lobby manager
	manager := lobby.NewManager(cfg)

	// Start the lobby service
	if err := manager.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start lobby manager: %v\n", err)
		os.Exit(1)
	}

	log.Println("Dota lobby manager started successfully")
}
