package relay

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Client represents a connected browser client
type Client struct {
	ID          string
	UserID      int64
	Conn        *websocket.Conn
	sendChan    chan []byte // important events: agent_event, system messages (1024 cap)
	overlayChan chan []byte // high-frequency bbox overlay events (2048 cap, droppable)
	closeChan   chan struct{}
	closeOnce   sync.Once
}

// Close safely closes the client's close channel
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.closeChan)
	})
}

// ClientEventRegistry manages browser WebSocket connections
type ClientEventRegistry struct {
	clients       map[string]*Client // clientID → client
	mu            sync.RWMutex
	agentTable    *AgentTable
	agentRegistry *AgentRegistry // in-memory cache — avoids DB reads on the hot path
	db            *Database
	cfg           *Config
	upgrader      websocket.Upgrader
}

// WSClientMessage represents WebSocket messages from client
type WSClientMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// RegisterClientMessage is sent by client to authenticate
type RegisterClientMessage struct {
	Token string `json:"token"`
}

// RegisteredClientMessage confirms successful registration
type RegisteredClientMessage struct {
	UserID int64 `json:"user_id"`
}

// ErrorClientMessage represents an error response
type ErrorClientMessage struct {
	Message string `json:"message"`
}

// RequestAgentEventsMessage is sent by client to request historical events
type RequestAgentEventsMessage struct {
	AgentID   string `json:"agent_id,omitempty"`
	ServiceID string `json:"service_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// NewClientEventRegistry creates a new client event registry
func NewClientEventRegistry(agentTable *AgentTable, agentRegistry *AgentRegistry, db *Database, cfg *Config) *ClientEventRegistry {
	return &ClientEventRegistry{
		clients:       make(map[string]*Client),
		agentTable:    agentTable,
		agentRegistry: agentRegistry,
		db:            db,
		cfg:           cfg,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				return true // TODO: Add origin validation for production
			},
		},
	}
}

// HandleWebSocket handles browser WebSocket connections at /client/connect
func (r *ClientEventRegistry) HandleWebSocket(w http.ResponseWriter, req *http.Request) {
	// Upgrade to WebSocket
	conn, err := r.upgrader.Upgrade(w, req, nil)
	if err != nil {
		log.Printf("[ClientEvents] Failed to upgrade: %v", err)
		return
	}

	log.Printf("[ClientEvents] Client connected from %s, awaiting registration...", req.RemoteAddr)

	// Handle registration in a goroutine with timeout
	registered := make(chan *Client, 1)
	go r.handleRegistration(conn, req.RemoteAddr, registered)

	// Wait for registration (with 10 second timeout)
	select {
	case client := <-registered:
		if client != nil {
			// Registration successful, start send/receive loops
			go r.sendLoop(client)
			go r.receiveLoop(client)
		}
		// If client is nil, registration failed and connection was already closed
	case <-time.After(10 * time.Second):
		log.Printf("[ClientEvents] Registration timeout from %s", req.RemoteAddr)
		conn.Close()
	}
}

func (r *ClientEventRegistry) handleRegistration(conn *websocket.Conn, remoteAddr string, registered chan<- *Client) {
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// Read registration message
	_, msgBytes, err := conn.ReadMessage()
	if err != nil {
		log.Printf("[ClientEvents] Failed to read registration from %s: %v", remoteAddr, err)
		r.sendError(conn, "Failed to read registration message")
		conn.Close()
		registered <- nil
		return
	}

	var msg WSClientMessage
	if err := json.Unmarshal(msgBytes, &msg); err != nil {
		log.Printf("[ClientEvents] Failed to parse registration from %s: %v", remoteAddr, err)
		r.sendError(conn, "Invalid message format")
		conn.Close()
		registered <- nil
		return
	}

	if msg.Type != "register" {
		log.Printf("[ClientEvents] Expected 'register' message, got '%s' from %s", msg.Type, remoteAddr)
		r.sendError(conn, "Expected registration message")
		conn.Close()
		registered <- nil
		return
	}

	var registerMsg RegisterClientMessage
	if err := json.Unmarshal(msg.Data, &registerMsg); err != nil {
		log.Printf("[ClientEvents] Failed to parse register data from %s: %v", remoteAddr, err)
		r.sendError(conn, "Invalid registration data")
		conn.Close()
		registered <- nil
		return
	}

	if registerMsg.Token == "" {
		log.Printf("[ClientEvents] Empty token from %s", remoteAddr)
		r.sendError(conn, "Missing token")
		conn.Close()
		registered <- nil
		return
	}

	// Validate JWT and get userID
	claims, err := ValidateToken(registerMsg.Token, r.cfg.JWTSecret)
	if err != nil {
		log.Printf("[ClientEvents] Invalid token from %s: %v", remoteAddr, err)
		r.sendError(conn, "Invalid token")
		conn.Close()
		registered <- nil
		return
	}

	// Create client
	client := &Client{
		ID:          uuid.New().String(),
		UserID:      claims.UserID,
		Conn:        conn,
		sendChan:    make(chan []byte, 1024),
		overlayChan: make(chan []byte, 2048),
		closeChan:   make(chan struct{}),
	}

	// Register client
	r.mu.Lock()
	r.clients[client.ID] = client
	r.mu.Unlock()

	log.Printf("[ClientEvents] Client registered: %s (user %d) from %s", client.ID, client.UserID, remoteAddr)

	// Send registered confirmation
	registeredMsg := map[string]interface{}{
		"type": "registered",
		"data": RegisteredClientMessage{
			UserID: claims.UserID,
		},
	}
	if msgBytes, err := json.Marshal(registeredMsg); err == nil {
		conn.WriteMessage(websocket.TextMessage, msgBytes)
	}

	// Clear read deadline for normal operation
	conn.SetReadDeadline(time.Time{})

	// Signal successful registration
	registered <- client
}

func (r *ClientEventRegistry) sendError(conn *websocket.Conn, message string) {
	errorMsg := map[string]interface{}{
		"type": "error",
		"data": ErrorClientMessage{
			Message: message,
		},
	}
	if msgBytes, err := json.Marshal(errorMsg); err == nil {
		conn.WriteMessage(websocket.TextMessage, msgBytes)
	}
}

func (r *ClientEventRegistry) sendLoop(client *Client) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	writeMsg := func(msg []byte) bool {
		if err := client.Conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			log.Printf("[ClientEvents] Write error for client %s: %v", client.ID, err)
			r.closeClient(client)
			return false
		}
		return true
	}

	for {
		// Priority drain: send all pending important events before overlay events.
		// This prevents bbox storms from delaying agent_event / system messages.
		select {
		case msg := <-client.sendChan:
			if !writeMsg(msg) {
				return
			}
			continue
		default:
		}

		// No important messages pending — block on either channel.
		select {
		case msg := <-client.sendChan:
			if !writeMsg(msg) {
				return
			}
		case msg := <-client.overlayChan:
			if !writeMsg(msg) {
				return
			}
		case <-ticker.C:
			// Ping to keep connection alive
			if err := client.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				r.closeClient(client)
				return
			}
		case <-client.closeChan:
			return
		}
	}
}

func (r *ClientEventRegistry) receiveLoop(client *Client) {
	client.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	client.Conn.SetPongHandler(func(string) error {
		client.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, msgBytes, err := client.Conn.ReadMessage()
		if err != nil {
			r.closeClient(client)
			return
		}

		// Parse message
		var msg WSClientMessage
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			log.Printf("[ClientEvents] Failed to parse message from client %s: %v", client.ID, err)
			continue
		}

		// Handle message types
		switch msg.Type {
		case "req_agent_events":
			r.handleRequestAgentEvents(client, msg.Data)
		default:
			log.Printf("[ClientEvents] Unknown message type from client %s: %s", client.ID, msg.Type)
		}
	}
}

func (r *ClientEventRegistry) handleRequestAgentEvents(client *Client, data json.RawMessage) {
	var req RequestAgentEventsMessage
	if err := json.Unmarshal(data, &req); err != nil {
		log.Printf("[ClientEvents] Failed to parse req_agent_events: %v", err)
		r.sendErrorToClient(client, "Invalid request format")
		return
	}

	// Set default and max limit
	if req.Limit <= 0 {
		req.Limit = 100
	}
	if req.Limit > 1000 {
		req.Limit = 1000
	}

	var dbEvents []*AgentEvent
	var err error

	if req.ServiceID != "" {
		// Get events by service
		dbEvents, err = r.db.GetAgentEventsByService(req.ServiceID, client.UserID, req.Limit)
	} else if req.AgentID != "" {
		// Verify agent exists.
		agent, agentErr := r.agentTable.GetAgentByID(req.AgentID)
		if agentErr != nil {
			log.Printf("[ClientEvents] Agent %s not found: %v", req.AgentID, agentErr)
			r.sendErrorToClient(client, "Agent not found")
			return
		}
		_ = agent

		// Get events by agent
		dbEvents, err = r.db.GetAgentEvents(req.AgentID, req.Limit)
	} else {
		// No filter specified - return recent workspace events.
		dbEvents, err = r.db.GetRecentAgentEventsForUser(client.UserID, req.Limit, 0)
	}

	if err != nil {
		log.Printf("[ClientEvents] Failed to get agent events: %v", err)
		r.sendErrorToClient(client, "Failed to retrieve events")
		return
	}

	// Convert database events to response format.
	// Agent info is read from the in-memory registry (O(1), no DB) to avoid
	// N+1 queries. Only falls back to DB when the registry has no record.
	events := make([]map[string]interface{}, 0, len(dbEvents))
	for _, dbEvent := range dbEvents {
		var agentName string
		var serviceIDs []string

		if info := r.agentRegistry.GetAgentByID(dbEvent.AgentID); info != nil {
			agentName = info.Name
			serviceIDs = info.ServiceIDs
		} else {
			// Cold path: registry miss — fall back to DB
			agent, err := r.agentTable.GetAgentByID(dbEvent.AgentID)
			if err != nil {
				log.Printf("[ClientEvents] Failed to get agent %s: %v", dbEvent.AgentID, err)
				continue
			}
			agentName = agent.Name
			serviceIDs = agent.ServiceIDs
		}

		event := map[string]interface{}{
			"id":          dbEvent.ID,
			"agent_id":    dbEvent.AgentID,
			"agent_name":  agentName,
			"service_ids": serviceIDs,
			"data":        dbEvent.Data,
			"metadata":    dbEvent.Metadata,
			"created_at":  dbEvent.CreatedAt.Format(time.RFC3339),
		}
		events = append(events, event)
	}

	// Send response
	response := map[string]interface{}{
		"type": "res_agent_events",
		"data": map[string]interface{}{
			"events": events,
		},
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		log.Printf("[ClientEvents] Failed to marshal response: %v", err)
		return
	}

	select {
	case client.sendChan <- responseBytes:
		log.Printf("[ClientEvents] Sent %d historical events to client %s", len(events), client.ID)
	default:
		log.Printf("[ClientEvents] Client %s buffer full, dropping response", client.ID)
	}
}

func (r *ClientEventRegistry) sendErrorToClient(client *Client, message string) {
	errorMsg := map[string]interface{}{
		"type": "error",
		"data": ErrorClientMessage{
			Message: message,
		},
	}
	if msgBytes, err := json.Marshal(errorMsg); err == nil {
		select {
		case client.sendChan <- msgBytes:
		default:
			log.Printf("[ClientEvents] Client %s buffer full, dropping error", client.ID)
		}
	}
}

func (r *ClientEventRegistry) closeClient(client *Client) {
	client.Close()
	client.Conn.Close()

	r.mu.Lock()
	delete(r.clients, client.ID)
	r.mu.Unlock()

	log.Printf("[ClientEvents] Client disconnected: %s", client.ID)
}

// BroadcastToAll sends a system message to every connected browser client.
func (r *ClientEventRegistry) BroadcastToAll(msgType string, payload map[string]interface{}) {
	msg := map[string]interface{}{
		"type": msgType,
		"data": payload,
	}
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[ClientEvents] BroadcastToAll marshal error: %v", err)
		return
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, client := range r.clients {
		select {
		case client.sendChan <- msgBytes:
		default:
			log.Printf("[ClientEvents] Client %s buffer full, dropping broadcast", client.ID)
		}
	}
}

// BroadcastAgentEvent sends event to browsers whose users own the agent.
// Agent lookup uses the in-memory registry (O(1), no DB) to avoid SQLITE_BUSY
// on the hot path (called on every frame event at 10+ FPS per camera).
func (r *ClientEventRegistry) BroadcastAgentEvent(agentID string, data map[string]interface{}, metadata map[string]interface{}, createdAt time.Time) {
	var agentName string
	var serviceIDs []string

	// Fast path: in-memory registry (no DB access)
	if info := r.agentRegistry.GetAgentByID(agentID); info != nil {
		agentName = info.Name
		serviceIDs = info.ServiceIDs
	} else {
		// Cold path: registry miss (agent just created and not yet registered, or
		// relay restarted mid-session). Fall back to DB — infrequent.
		agent, err := r.agentTable.GetAgentByID(agentID)
		if err != nil {
			log.Printf("[ClientEvents] Failed to get agent %s: %v", agentID, err)
			return
		}
		agentName = agent.Name
		serviceIDs = agent.ServiceIDs
	}

	// Build message
	message := map[string]interface{}{
		"type":       "agent_event",
		"id":         uuid.New().String(),
		"created_at": createdAt.Format(time.RFC3339),
		"data": map[string]interface{}{
			"id":          uuid.New().String(),
			"agent_id":    agentID,
			"agent_name":  agentName,
			"service_ids": serviceIDs,
			"data":        data,
			"metadata":    metadata,
			"created_at":  createdAt.Format(time.RFC3339),
		},
	}

	msgBytes, err := json.Marshal(message)
	if err != nil {
		log.Printf("[ClientEvents] Failed to marshal message: %v", err)
		return
	}

	// Send to all connected clients in the shared workspace.
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, client := range r.clients {
		select {
		case client.sendChan <- msgBytes:
		default:
			log.Printf("[ClientEvents] Client %s buffer full, dropping message", client.ID)
		}
	}
}

// BroadcastOverlayEvent sends transient high-frequency overlay events to browsers.
// These are not persisted and are intended for live bounding-box rendering.
// Uses overlayChan (separate high-capacity channel) so bbox storms never delay
// important agent_event messages in sendChan.
func (r *ClientEventRegistry) BroadcastOverlayEvent(agentID string, data map[string]interface{}, metadata map[string]interface{}, createdAt time.Time) {
	var agentName string
	var serviceIDs []string

	if info := r.agentRegistry.GetAgentByID(agentID); info != nil {
		agentName = info.Name
		serviceIDs = info.ServiceIDs
	} else {
		agent, err := r.agentTable.GetAgentByID(agentID)
		if err != nil {
			log.Printf("[ClientEvents] BroadcastOverlayEvent: agent %s not found in registry or DB: %v", agentID, err)
			return
		}
		agentName = agent.Name
		serviceIDs = agent.ServiceIDs
	}

	overlayMeta := map[string]interface{}{}
	for k, v := range metadata {
		overlayMeta[k] = v
	}
	overlayMeta["realtime_only"] = true

	message := map[string]interface{}{
		"type":       "agent_overlay_event",
		"id":         uuid.New().String(),
		"created_at": createdAt.Format(time.RFC3339),
		"data": map[string]interface{}{
			"id":          uuid.New().String(),
			"agent_id":    agentID,
			"agent_name":  agentName,
			"service_ids": serviceIDs,
			"data":        data,
			"metadata":    overlayMeta,
			"created_at":  createdAt.Format(time.RFC3339),
		},
	}

	msgBytes, err := json.Marshal(message)
	if err != nil {
		log.Printf("[ClientEvents] Failed to marshal overlay message: %v", err)
		return
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	sent := 0
	for _, client := range r.clients {
		select {
		case client.overlayChan <- msgBytes:
			sent++
		default:
			log.Printf("[ClientEvents] Client %s overlay buffer full, dropping bbox event", client.ID)
		}
	}
	if sent == 0 && len(r.clients) > 0 {
		log.Printf("[ClientEvents] BroadcastOverlayEvent: agent %s — no clients accepted overlay (connected: %d)", agentID, len(r.clients))
	}
}
