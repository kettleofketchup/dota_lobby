package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/kettleofketchup/dota_lobby/pkg/bot"
)

const bearerPrefix = "Bearer "

// Server represents the HTTP API server
type Server struct {
	botManager *bot.Manager
	server     *http.Server
	apiKey     string
}

// NewServer creates a new API server
func NewServer(host string, port int, apiKey string, botManager *bot.Manager) *Server {
	return &Server{
		botManager: botManager,
		apiKey:     apiKey,
		server: &http.Server{
			Addr:         fmt.Sprintf("%s:%d", host, port),
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
			IdleTimeout:  60 * time.Second,
		},
	}
}

// Start starts the HTTP server
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// Health check endpoint (no auth required)
	mux.HandleFunc("/health", s.handleHealth)

	// Protected endpoints with authentication
	mux.HandleFunc("/bots", s.authMiddleware(s.handleBots))
	mux.HandleFunc("/lobby/create", s.authMiddleware(s.handleCreateLobby))
	mux.HandleFunc("/lobby/info", s.authMiddleware(s.handleLobbyInfo))

	s.server.Handler = mux

	log.Printf("Starting API server on %s", s.server.Addr)
	if s.apiKey != "" {
		log.Println("API key authentication enabled")
	} else {
		log.Println("WARNING: API key authentication disabled - all endpoints are public")
	}
	err := s.server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return err
	}

	log.Println("API server stopped")
	return nil
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	log.Println("Shutting down API server...")
	return s.server.Shutdown(ctx)
}

// authMiddleware provides API key authentication for protected endpoints
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If no API key is configured, allow all requests
		if s.apiKey == "" {
			next(w, r)
			return
		}

		// Check for API key in X-API-Key header
		apiKey := r.Header.Get("X-API-Key")
		if apiKey == "" {
			// Also check Authorization header with Bearer token
			authHeader := r.Header.Get("Authorization")
			if len(authHeader) > len(bearerPrefix) && authHeader[:len(bearerPrefix)] == bearerPrefix {
				apiKey = authHeader[len(bearerPrefix):]
			}
		}

		// Validate API key using constant-time comparison to prevent timing attacks
		if subtle.ConstantTimeCompare([]byte(apiKey), []byte(s.apiKey)) != 1 {
			http.Error(w, "Unauthorized: Invalid or missing API key", http.StatusUnauthorized)
			return
		}

		// API key is valid, proceed to handler
		next(w, r)
	}
}

// handleHealth returns the health status of the service
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	response := map[string]interface{}{
		"status": "healthy",
		"time":   time.Now().UTC(),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Error encoding health response: %v", err)
	}
}

// handleBots returns the status of all bots
func (s *Server) handleBots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bots := s.botManager.ListBots()

	response := map[string]interface{}{
		"bots": bots,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Error encoding bots response: %v", err)
	}
}

// LobbyCreateRequest represents a request to create a lobby
type LobbyCreateRequest struct {
	LobbyName    string `json:"lobby_name"`
	Password     string `json:"password,omitempty"`
	ServerRegion string `json:"server_region,omitempty"`
	GameMode     string `json:"game_mode,omitempty"`
}

// handleCreateLobby handles lobby creation requests
func (s *Server) handleCreateLobby(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req LobbyCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Validate lobby name
	if req.LobbyName == "" {
		http.Error(w, "lobby_name is required", http.StatusBadRequest)
		return
	}

	if len(req.LobbyName) > 100 {
		http.Error(w, "lobby_name must be 100 characters or less", http.StatusBadRequest)
		return
	}

	// Validate password if provided
	if len(req.Password) > 50 {
		http.Error(w, "password must be 50 characters or less", http.StatusBadRequest)
		return
	}

	// Get an available bot
	availableBot, err := s.botManager.GetAvailableBot()
	if err != nil {
		http.Error(w, fmt.Sprintf("No available bots: %v", err), http.StatusServiceUnavailable)
		return
	}

	// Verify bot has Dota client ready
	_, err = availableBot.GetDotaClient()
	if err != nil {
		http.Error(w, fmt.Sprintf("Bot not ready: %v", err), http.StatusServiceUnavailable)
		return
	}

	// TODO: Implement actual lobby creation using dotaClient
	// This is a placeholder response
	response := map[string]interface{}{
		"status":     "success",
		"message":    "Lobby creation initiated",
		"lobby_name": req.LobbyName,
		"bot_used":   availableBot.Username,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Error encoding lobby create response: %v", err)
	}
}

// LobbyInfoRequest represents a request to get lobby information
type LobbyInfoRequest struct {
	LobbyID string `json:"lobby_id"`
}

// handleLobbyInfo handles lobby information requests
func (s *Server) handleLobbyInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// For GET requests, read from query params
	var lobbyID string
	if r.Method == http.MethodGet {
		lobbyID = r.URL.Query().Get("lobby_id")
	} else {
		var req LobbyInfoRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
			return
		}
		lobbyID = req.LobbyID
	}

	if lobbyID == "" {
		http.Error(w, "lobby_id is required", http.StatusBadRequest)
		return
	}

	// TODO: Implement actual lobby info retrieval
	response := map[string]interface{}{
		"status":   "success",
		"lobby_id": lobbyID,
		"message":  "Lobby info retrieval not yet fully implemented",
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Error encoding lobby info response: %v", err)
	}
}
