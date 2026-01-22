# Status System Design

## Overview

Add comprehensive status tracking to the dota_lobby service, exposing bot-level, lobby-level, and system-level status information via REST API endpoints.

## Goals

- Real-time visibility into bot connection states and activity
- Track active lobbies and their current state
- Provide system-wide health and capacity metrics
- Enable monitoring and debugging of the lobby service

## Status Levels

### 1. Bot-Level Status

Track detailed state for each bot account.

**Bot States:**
```
Disconnected -> Connecting -> Connected (Steam) -> GC Connected -> In Lobby -> In Game
```

**Data Structure:**
```go
type BotStatus struct {
    Username        string           `json:"username"`
    State           BotState         `json:"state"`
    SteamConnected  bool             `json:"steam_connected"`
    GCConnected     bool             `json:"gc_connected"`
    CurrentLobbyID  *uint64          `json:"current_lobby_id,omitempty"`
    LastError       string           `json:"last_error,omitempty"`
    LastErrorTime   *time.Time       `json:"last_error_time,omitempty"`
    ConnectedSince  *time.Time       `json:"connected_since,omitempty"`
    LastActivity    time.Time        `json:"last_activity"`
}

type BotState string

const (
    BotStateDisconnected BotState = "disconnected"
    BotStateConnecting   BotState = "connecting"
    BotStateSteamOnline  BotState = "steam_online"
    BotStateGCConnected  BotState = "gc_connected"
    BotStateInLobby      BotState = "in_lobby"
    BotStateInGame       BotState = "in_game"
)
```

### 2. Lobby-Level Status

Track active lobbies managed by the service.

**Lobby States:**
```
Creating -> Waiting (for players) -> Ready -> In Game -> Finished
```

**Data Structure:**
```go
type LobbyStatus struct {
    LobbyID       uint64          `json:"lobby_id"`
    Name          string          `json:"name"`
    State         LobbyState      `json:"state"`
    BotUsername   string          `json:"bot_username"`
    ServerRegion  uint32          `json:"server_region"`
    GameMode      uint32          `json:"game_mode"`
    HasPassword   bool            `json:"has_password"`
    Players       []LobbyPlayer   `json:"players"`
    RadiantCount  int             `json:"radiant_count"`
    DireCount     int             `json:"dire_count"`
    SpectatorCount int            `json:"spectator_count"`
    CreatedAt     time.Time       `json:"created_at"`
    MatchID       *uint64         `json:"match_id,omitempty"`
}

type LobbyState string

const (
    LobbyStateCreating LobbyState = "creating"
    LobbyStateWaiting  LobbyState = "waiting"
    LobbyStateReady    LobbyState = "ready"
    LobbyStateInGame   LobbyState = "in_game"
    LobbyStateFinished LobbyState = "finished"
)

type LobbyPlayer struct {
    SteamID   uint64 `json:"steam_id"`
    Name      string `json:"name"`
    Team      string `json:"team"` // "radiant", "dire", "spectator", "unassigned"
    Slot      int    `json:"slot"`
}
```

### 3. System-Level Status

Aggregate metrics for the entire service.

**Data Structure:**
```go
type SystemStatus struct {
    Uptime          time.Duration     `json:"uptime_seconds"`
    StartedAt       time.Time         `json:"started_at"`
    TotalBots       int               `json:"total_bots"`
    AvailableBots   int               `json:"available_bots"`
    BusyBots        int               `json:"busy_bots"`
    DisconnectedBots int              `json:"disconnected_bots"`
    ActiveLobbies   int               `json:"active_lobbies"`
    TotalLobbiesCreated int64         `json:"total_lobbies_created"`
    BotStateSummary map[BotState]int  `json:"bot_state_summary"`
}
```

## API Endpoints

### GET /status

Returns complete system status with all bots and lobbies.

**Response:**
```json
{
  "system": {
    "uptime_seconds": 3600,
    "started_at": "2026-01-22T10:00:00Z",
    "total_bots": 3,
    "available_bots": 2,
    "busy_bots": 1,
    "disconnected_bots": 0,
    "active_lobbies": 1,
    "total_lobbies_created": 15,
    "bot_state_summary": {
      "gc_connected": 2,
      "in_lobby": 1
    }
  },
  "bots": [...],
  "lobbies": [...]
}
```

### GET /status/system

Returns only system-level metrics (lightweight).

### GET /status/bots

Returns all bot statuses.

### GET /status/bots/{username}

Returns status for a specific bot.

### GET /status/lobbies

Returns all active lobby statuses.

### GET /status/lobbies/{lobby_id}

Returns status for a specific lobby.

## Implementation

### Phase 1: Bot Status Tracking

1. **Extend Bot struct** in `pkg/bot/manager.go`:
   - Add `BotStatus` fields
   - Track state transitions
   - Record connection timestamps and errors

2. **Update event handlers** to update status:
   - `ConnectedEvent` -> `BotStateConnecting`
   - `LoggedOnEvent` -> `BotStateSteamOnline`
   - GC welcome -> `BotStateGCConnected`
   - Lobby join -> `BotStateInLobby`
   - `DisconnectedEvent` -> `BotStateDisconnected`

3. **Add Manager methods:**
   - `GetBotStatus(username) *BotStatus`
   - `GetAllBotStatuses() []BotStatus`

### Phase 2: Lobby Status Tracking

1. **Create lobby tracking** in `pkg/bot/manager.go` or new `pkg/lobby/tracker.go`:
   - Map of active lobbies by ID
   - Subscribe to SOCache lobby events
   - Update lobby state on GC events

2. **Track lobby lifecycle:**
   - Creation request -> `LobbyStateCreating`
   - SOCache lobby created -> `LobbyStateWaiting`
   - All players ready -> `LobbyStateReady`
   - Match started -> `LobbyStateInGame`
   - Match ended -> `LobbyStateFinished`

3. **Parse lobby members** from `CSODOTALobby`:
   - Extract player list with teams
   - Calculate team counts

### Phase 3: System Status & API

1. **Add system metrics** to Manager:
   - Track service start time
   - Count lobbies created
   - Compute derived metrics

2. **Add API endpoints** in `pkg/api/server.go`:
   - `/status` - Full status
   - `/status/system` - System only
   - `/status/bots` - All bots
   - `/status/bots/{username}` - Single bot
   - `/status/lobbies` - All lobbies
   - `/status/lobbies/{id}` - Single lobby

3. **Authentication:**
   - All status endpoints require API key (like existing `/bots`)
   - Consider read-only API key tier for monitoring

## File Changes

| File | Changes |
|------|---------|
| `pkg/bot/manager.go` | Add BotStatus, state tracking, status methods |
| `pkg/bot/status.go` | New file: status types and constants |
| `pkg/lobby/tracker.go` | New file: lobby tracking via SOCache |
| `pkg/api/server.go` | Add status endpoints |
| `pkg/api/status.go` | New file: status endpoint handlers |

## Testing

- Unit tests for state transitions
- Integration tests for status endpoints
- Test concurrent status updates (multiple bots)
- Test lobby status updates via mock SOCache events

## Future Considerations

- **WebSocket endpoint** for real-time status updates
- **Prometheus metrics** for monitoring integration
- **Status history** for debugging (last N state changes)
- **Alerting hooks** for critical state changes (all bots disconnected)
