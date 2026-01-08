# API Examples

Practical examples for using the Dota Lobby Manager API.

## Prerequisites

These examples assume:

- Dota Lobby Manager is running on `localhost:8080`
- You have configured at least one bot
- You have `curl` or similar HTTP client installed

## Basic Examples

### Check Service Health

=== "curl"
    ```bash
    curl http://localhost:8080/health
    ```

=== "Python"
    ```python
    import requests
    
    response = requests.get('http://localhost:8080/health')
    data = response.json()
    
    if data['success']:
        print(f"Service is {data['data']['status']}")
    ```

=== "JavaScript"
    ```javascript
    fetch('http://localhost:8080/health')
      .then(res => res.json())
      .then(data => {
        if (data.success) {
          console.log(`Service is ${data.data.status}`);
        }
      });
    ```

=== "Go"
    ```go
    package main
    
    import (
        "encoding/json"
        "fmt"
        "net/http"
    )
    
    type HealthResponse struct {
        Success bool `json:"success"`
        Data    struct {
            Status string `json:"status"`
        } `json:"data"`
    }
    
    func main() {
        resp, err := http.Get("http://localhost:8080/health")
        if err != nil {
            panic(err)
        }
        defer resp.Body.Close()
        
        var health HealthResponse
        json.NewDecoder(resp.Body).Decode(&health)
        
        if health.Success {
            fmt.Printf("Service is %s\n", health.Data.Status)
        }
    }
    ```

### List All Bots

=== "curl"
    ```bash
    curl http://localhost:8080/api/bots
    ```

=== "Python"
    ```python
    import requests
    
    response = requests.get('http://localhost:8080/api/bots')
    data = response.json()
    
    if data['success']:
        for bot in data['data']['bots']:
            print(f"Bot: {bot['name']}, Status: {bot['status']}")
    ```

=== "JavaScript"
    ```javascript
    fetch('http://localhost:8080/api/bots')
      .then(res => res.json())
      .then(data => {
        if (data.success) {
          data.data.bots.forEach(bot => {
            console.log(`Bot: ${bot.name}, Status: ${bot.status}`);
          });
        }
      });
    ```

=== "Go"
    ```go
    package main
    
    import (
        "encoding/json"
        "fmt"
        "net/http"
    )
    
    type Bot struct {
        Name     string `json:"name"`
        Username string `json:"username"`
        Enabled  bool   `json:"enabled"`
        Status   string `json:"status"`
    }
    
    type BotsResponse struct {
        Success bool `json:"success"`
        Data    struct {
            Bots []Bot `json:"bots"`
        } `json:"data"`
    }
    
    func main() {
        resp, err := http.Get("http://localhost:8080/api/bots")
        if err != nil {
            panic(err)
        }
        defer resp.Body.Close()
        
        var result BotsResponse
        json.NewDecoder(resp.Body).Decode(&result)
        
        if result.Success {
            for _, bot := range result.Data.Bots {
                fmt.Printf("Bot: %s, Status: %s\n", bot.Name, bot.Status)
            }
        }
    }
    ```

## Bot Management Examples

### Get Specific Bot

=== "curl"
    ```bash
    curl http://localhost:8080/api/bots/bot1
    ```

=== "Python"
    ```python
    import requests
    
    bot_name = "bot1"
    response = requests.get(f'http://localhost:8080/api/bots/{bot_name}')
    data = response.json()
    
    if data['success']:
        bot = data['data']
        print(f"Name: {bot['name']}")
        print(f"Username: {bot['username']}")
        print(f"Enabled: {bot['enabled']}")
        print(f"Status: {bot['status']}")
    ```

=== "JavaScript"
    ```javascript
    const botName = 'bot1';
    
    fetch(`http://localhost:8080/api/bots/${botName}`)
      .then(res => res.json())
      .then(data => {
        if (data.success) {
          const bot = data.data;
          console.log(`Name: ${bot.name}`);
          console.log(`Username: ${bot.username}`);
          console.log(`Enabled: ${bot.enabled}`);
          console.log(`Status: ${bot.status}`);
        }
      });
    ```

### Enable/Disable Bot

=== "curl"
    ```bash
    # Enable bot
    curl -X POST http://localhost:8080/api/bots/bot1/enable
    
    # Disable bot
    curl -X POST http://localhost:8080/api/bots/bot1/disable
    ```

=== "Python"
    ```python
    import requests
    
    # Enable bot
    response = requests.post('http://localhost:8080/api/bots/bot1/enable')
    print(response.json())
    
    # Disable bot
    response = requests.post('http://localhost:8080/api/bots/bot1/disable')
    print(response.json())
    ```

=== "JavaScript"
    ```javascript
    // Enable bot
    fetch('http://localhost:8080/api/bots/bot1/enable', {
      method: 'POST'
    })
      .then(res => res.json())
      .then(data => console.log(data));
    
    // Disable bot
    fetch('http://localhost:8080/api/bots/bot1/disable', {
      method: 'POST'
    })
      .then(res => res.json())
      .then(data => console.log(data));
    ```

## Lobby Management Examples

### Create a Lobby

=== "curl"
    ```bash
    curl -X POST \
      -H "Content-Type: application/json" \
      -d '{"name":"Custom Lobby","bot":"bot1","settings":{"game_mode":"captains_mode"}}' \
      http://localhost:8080/api/lobbies
    ```

=== "Python"
    ```python
    import requests
    
    lobby_data = {
        "name": "Custom Lobby",
        "bot": "bot1",
        "settings": {
            "game_mode": "captains_mode"
        }
    }
    
    response = requests.post(
        'http://localhost:8080/api/lobbies',
        json=lobby_data
    )
    
    if response.json()['success']:
        lobby_id = response.json()['data']['id']
        print(f"Created lobby: {lobby_id}")
    ```

=== "JavaScript"
    ```javascript
    const lobbyData = {
      name: 'Custom Lobby',
      bot: 'bot1',
      settings: {
        game_mode: 'captains_mode'
      }
    };
    
    fetch('http://localhost:8080/api/lobbies', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json'
      },
      body: JSON.stringify(lobbyData)
    })
      .then(res => res.json())
      .then(data => {
        if (data.success) {
          console.log(`Created lobby: ${data.data.id}`);
        }
      });
    ```

=== "Go"
    ```go
    package main
    
    import (
        "bytes"
        "encoding/json"
        "fmt"
        "net/http"
    )
    
    type LobbyRequest struct {
        Name     string                 `json:"name"`
        Bot      string                 `json:"bot"`
        Settings map[string]interface{} `json:"settings"`
    }
    
    func main() {
        lobbyReq := LobbyRequest{
            Name: "Custom Lobby",
            Bot:  "bot1",
            Settings: map[string]interface{}{
                "game_mode": "captains_mode",
            },
        }
        
        jsonData, _ := json.Marshal(lobbyReq)
        
        resp, err := http.Post(
            "http://localhost:8080/api/lobbies",
            "application/json",
            bytes.NewBuffer(jsonData),
        )
        if err != nil {
            panic(err)
        }
        defer resp.Body.Close()
        
        var result map[string]interface{}
        json.NewDecoder(resp.Body).Decode(&result)
        
        if result["success"].(bool) {
            data := result["data"].(map[string]interface{})
            fmt.Printf("Created lobby: %s\n", data["id"])
        }
    }
    ```

### List Active Lobbies

=== "curl"
    ```bash
    curl http://localhost:8080/api/lobbies
    ```

=== "Python"
    ```python
    import requests
    
    response = requests.get('http://localhost:8080/api/lobbies')
    data = response.json()
    
    if data['success']:
        for lobby in data['data']['lobbies']:
            print(f"ID: {lobby['id']}")
            print(f"Name: {lobby['name']}")
            print(f"Bot: {lobby['bot']}")
            print(f"Status: {lobby['status']}")
            print("---")
    ```

=== "JavaScript"
    ```javascript
    fetch('http://localhost:8080/api/lobbies')
      .then(res => res.json())
      .then(data => {
        if (data.success) {
          data.data.lobbies.forEach(lobby => {
            console.log(`ID: ${lobby.id}`);
            console.log(`Name: ${lobby.name}`);
            console.log(`Bot: ${lobby.bot}`);
            console.log(`Status: ${lobby.status}`);
            console.log('---');
          });
        }
      });
    ```

### Close a Lobby

=== "curl"
    ```bash
    curl -X DELETE http://localhost:8080/api/lobbies/lobby123
    ```

=== "Python"
    ```python
    import requests
    
    lobby_id = "lobby123"
    response = requests.delete(f'http://localhost:8080/api/lobbies/{lobby_id}')
    
    if response.json()['success']:
        print(f"Closed lobby: {lobby_id}")
    ```

=== "JavaScript"
    ```javascript
    const lobbyId = 'lobby123';
    
    fetch(`http://localhost:8080/api/lobbies/${lobbyId}`, {
      method: 'DELETE'
    })
      .then(res => res.json())
      .then(data => {
        if (data.success) {
          console.log(`Closed lobby: ${lobbyId}`);
        }
      });
    ```

## Advanced Examples

### Monitoring Bot Status

=== "Python"
    ```python
    import requests
    import time
    
    def monitor_bots(interval=10):
        """Monitor bot status every interval seconds"""
        while True:
            response = requests.get('http://localhost:8080/api/bots')
            data = response.json()
            
            if data['success']:
                print(f"\n=== Bot Status at {time.strftime('%Y-%m-%d %H:%M:%S')} ===")
                for bot in data['data']['bots']:
                    status_emoji = "✓" if bot['enabled'] else "✗"
                    print(f"{status_emoji} {bot['name']}: {bot['status']}")
            
            time.sleep(interval)
    
    # Run monitoring
    monitor_bots(interval=10)
    ```

=== "JavaScript"
    ```javascript
    const monitorBots = (interval = 10000) => {
      setInterval(async () => {
        const response = await fetch('http://localhost:8080/api/bots');
        const data = await response.json();
        
        if (data.success) {
          console.log(`\n=== Bot Status at ${new Date().toISOString()} ===`);
          data.data.bots.forEach(bot => {
            const statusEmoji = bot.enabled ? '✓' : '✗';
            console.log(`${statusEmoji} ${bot.name}: ${bot.status}`);
          });
        }
      }, interval);
    };
    
    // Run monitoring (every 10 seconds)
    monitorBots(10000);
    ```

### Automatic Lobby Creation

=== "Python"
    ```python
    import requests
    
    class LobbyManager:
        def __init__(self, base_url='http://localhost:8080'):
            self.base_url = base_url
        
        def create_lobby(self, name, bot_name, game_mode='captains_mode'):
            """Create a new lobby with specified settings"""
            lobby_data = {
                "name": name,
                "bot": bot_name,
                "settings": {
                    "game_mode": game_mode
                }
            }
            
            response = requests.post(
                f'{self.base_url}/api/lobbies',
                json=lobby_data
            )
            
            data = response.json()
            if data['success']:
                return data['data']['id']
            else:
                raise Exception(data['error']['message'])
        
        def list_active_lobbies(self):
            """Get all active lobbies"""
            response = requests.get(f'{self.base_url}/api/lobbies')
            data = response.json()
            
            if data['success']:
                return data['data']['lobbies']
            return []
        
        def close_lobby(self, lobby_id):
            """Close a specific lobby"""
            response = requests.delete(f'{self.base_url}/api/lobbies/{lobby_id}')
            return response.json()['success']
    
    # Usage
    manager = LobbyManager()
    lobby_id = manager.create_lobby("Tournament Game 1", "bot1", "captains_mode")
    print(f"Created lobby: {lobby_id}")
    
    active = manager.list_active_lobbies()
    print(f"Active lobbies: {len(active)}")
    ```

### Error Handling Wrapper

=== "Python"
    ```python
    import requests
    from typing import Optional, Dict, Any
    
    class DotaLobbyClient:
        def __init__(self, base_url='http://localhost:8080'):
            self.base_url = base_url
        
        def _make_request(self, method: str, endpoint: str, 
                         data: Optional[Dict] = None) -> Dict[Any, Any]:
            """Make HTTP request with error handling"""
            url = f"{self.base_url}{endpoint}"
            
            try:
                if method == 'GET':
                    response = requests.get(url)
                elif method == 'POST':
                    response = requests.post(url, json=data)
                elif method == 'DELETE':
                    response = requests.delete(url)
                else:
                    raise ValueError(f"Unsupported method: {method}")
                
                response.raise_for_status()
                result = response.json()
                
                if not result.get('success', False):
                    error = result.get('error', {})
                    raise Exception(f"API Error: {error.get('message', 'Unknown error')}")
                
                return result.get('data', {})
                
            except requests.RequestException as e:
                raise Exception(f"Request failed: {str(e)}")
        
        def get_bots(self):
            """Get all bots"""
            return self._make_request('GET', '/api/bots')
        
        def get_bot(self, name):
            """Get specific bot"""
            return self._make_request('GET', f'/api/bots/{name}')
        
        def create_lobby(self, lobby_data):
            """Create new lobby"""
            return self._make_request('POST', '/api/lobbies', lobby_data)
    
    # Usage
    client = DotaLobbyClient()
    
    try:
        bots = client.get_bots()
        print(f"Found {len(bots['bots'])} bots")
    except Exception as e:
        print(f"Error: {e}")
    ```

## Integration Examples

### Docker Compose with Application

```yaml
# docker-compose.yml
services:
  dota_lobby:
    image: kettleofketchup/dota_lobby:latest
    ports:
      - "8080:8080"
    environment:
      - DOTA_LOBBY_STEAM_API_KEY=${STEAM_API_KEY}
      - DOTA_LOBBY_BOTS_0_USERNAME=${BOT_USERNAME}
      - DOTA_LOBBY_BOTS_0_PASSWORD=${BOT_PASSWORD}
      - DOTA_LOBBY_BOTS_0_ENABLED=true
  
  my_app:
    build: .
    depends_on:
      - dota_lobby
    environment:
      - LOBBY_API_URL=http://dota_lobby:8080
```

### Kubernetes Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dota-lobby
spec:
  replicas: 1
  selector:
    matchLabels:
      app: dota-lobby
  template:
    metadata:
      labels:
        app: dota-lobby
    spec:
      containers:
      - name: dota-lobby
        image: kettleofketchup/dota_lobby:latest
        ports:
        - containerPort: 8080
        env:
        - name: DOTA_LOBBY_STEAM_API_KEY
          valueFrom:
            secretKeyRef:
              name: dota-lobby-secrets
              key: steam-api-key
---
apiVersion: v1
kind: Service
metadata:
  name: dota-lobby-service
spec:
  selector:
    app: dota-lobby
  ports:
  - protocol: TCP
    port: 80
    targetPort: 8080
```

## See Also

- [API Overview](overview.md) - API documentation
- [Configuration](../configuration/bot-setup.md) - Setup guide
- [Docker Deployment](../deployment/docker.md) - Deploy guide
