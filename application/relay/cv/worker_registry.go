package cv

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// CVWorker represents a single connected worker process.
// Multiple instances with the same ID can connect simultaneously.
type CVWorker struct {
	ID           string // worker type (e.g., "vyra/ppe_detection"), shared across pool members
	connID       string // unique per TCP connection (UUID)
	Conn         *websocket.Conn
	RegisteredAt time.Time
	LastSeen     time.Time
	sendChan     chan []byte
	closeChan    chan struct{}
	closeOnce    sync.Once

	// Adaptive drop telemetry
	droppedFrames atomic.Uint64
	lastDropLog   atomic.Int64 // unix-second timestamp of last drop warning
}

// Close safely closes the worker's send loop.
func (w *CVWorker) Close() {
	w.closeOnce.Do(func() {
		close(w.closeChan)
	})
}

// workerPool holds all connected workers sharing the same worker_id and a
// lock-free round-robin counter.
type workerPool struct {
	workers []*CVWorker   // protected by outer registry r.mu
	next    atomic.Uint64 // monotonically incremented; no additional lock needed
}

// AgentRegistry interface for dependency injection
type AgentRegistry interface {
	GetAgentsForService(serviceID string) []*AgentInfo
}

// AgentInfo represents agent information from the registry
type AgentInfo struct {
	ID          string
	Name        string
	WorkerID    string
	Instruction string
	ServiceIDs  []string
	CustomYOLO  *CustomYOLOAgentInfo
}

// CustomYOLOAgentInfo carries custom model/runtime metadata sourced from relay agents.
type CustomYOLOAgentInfo struct {
	Enabled                   bool
	ModelRelPath              string
	ModelFilename             string
	ModelSizeBytes            int64
	RuntimeDefaultsEnableVLM  bool
	RuntimeDefaultsEnableFace bool
	RuntimeDesiredReplicas    int
}

// broadcastTarget represents a worker selected to receive a frame batch
type broadcastTarget struct {
	Worker    *CVWorker
	ServiceID string
	Agents    []*AgentEventInfo
}

// CVWorkerRegistry manages all connected worker pools and routes frame events.
type CVWorkerRegistry struct {
	pools      map[string]*workerPool // workerID → pool
	connWorker map[string]*CVWorker   // connID  → worker  (fast lookup for removal)
	workerKeys map[string]string      // auth key → connID
	mu         sync.RWMutex
	poolHookMu sync.RWMutex

	eventBus       *CVEventBus
	storageManager *StorageManager
	agentRegistry  AgentRegistry
	upgrader       websocket.Upgrader
	db             AgentEventStorage
	poolChangeHook func()

	// dbWriteChan serializes DB event writes off the receive-loop goroutine.
	// Capacity 256 absorbs short bursts; drops on overflow (non-critical data).
	dbWriteChan chan dbWriteRequest

	dbPersistSuccess atomic.Uint64
	dbPersistFailure atomic.Uint64
	dbQueueDropped   atomic.Uint64
}

type WorkerRuntimeStats struct {
	ConnectedWorkers int    `json:"connected_workers"`
	TotalDropped     uint64 `json:"total_dropped"`
	MaxQueueFillPct  int    `json:"max_queue_fill_pct"`
}

type PersistRuntimeStats struct {
	Success uint64 `json:"success"`
	Failure uint64 `json:"failure"`
	Dropped uint64 `json:"dropped"`
}

// AgentEventStorage interface for persisting agent events
type AgentEventStorage interface {
	StoreAgentEvent(agentID string, data map[string]interface{}, metadata map[string]interface{}, createdAt time.Time) error
}

// dbWriteRequest is queued by the receive loop and drained by a single
// background goroutine so that DB writes never block message processing.
type dbWriteRequest struct {
	db        AgentEventStorage
	agentID   string
	data      map[string]interface{}
	metadata  map[string]interface{}
	createdAt time.Time
}

// WebSocket message envelope
type WSMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

type RegisterMessage struct {
	WorkerID string `json:"worker_id"`
}

// NewCVWorkerRegistry creates a new worker registry.
func NewCVWorkerRegistry(eventBus *CVEventBus, storageManager *StorageManager) *CVWorkerRegistry {
	registry := &CVWorkerRegistry{
		pools:          make(map[string]*workerPool),
		connWorker:     make(map[string]*CVWorker),
		workerKeys:     make(map[string]string),
		eventBus:       eventBus,
		storageManager: storageManager,
		dbWriteChan:    make(chan dbWriteRequest, 256),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	go registry.dbWriter()

	eventBus.OnFrameBatchEvent(func(event *FrameBatchEvent) {
		registry.BroadcastFrameBatchEvent(event, time.Now())
	})

	return registry
}

// dbWriter drains dbWriteChan and persists events sequentially in a dedicated
// goroutine, keeping the worker receive loop free for real-time processing.
func (r *CVWorkerRegistry) dbWriter() {
	for req := range r.dbWriteChan {
		if err := req.db.StoreAgentEvent(req.agentID, req.data, req.metadata, req.createdAt); err != nil {
			r.dbPersistFailure.Add(1)
			log.Printf("[CVWorkerRegistry] Failed to store event: %v", err)
		} else {
			r.dbPersistSuccess.Add(1)
		}
	}
}

// generateWorkerKey generates a 256-bit random auth key.
func generateWorkerKey() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// inferAgentType maps a worker_id to the UI agent type string.
func inferAgentType(workerID string) string {
	id := strings.ToLower(workerID)
	if strings.HasPrefix(id, "vyra/custom_detection/") {
		return "custom"
	}
	if strings.Contains(id, "helmet_detection") || strings.Contains(id, "ppe") || strings.Contains(id, "helmet") {
		return "ppe"
	}
	if strings.Contains(id, "object_detection") || strings.Contains(id, "detect") || strings.Contains(id, "person") {
		return "person"
	}
	if strings.Contains(id, "face") {
		return "face"
	}
	if strings.Contains(id, "vlm") {
		return "situational"
	}
	return "unknown"
}

func metadataFloat(meta map[string]interface{}, key string) (float64, bool) {
	if meta == nil {
		return 0, false
	}
	v, ok := meta[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint64:
		return float64(n), true
	case uint32:
		return float64(n), true
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f, true
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

func metadataBool(meta map[string]interface{}, key string) bool {
	if meta == nil {
		return false
	}
	v, ok := meta[key]
	if !ok || v == nil {
		return false
	}
	switch b := v.(type) {
	case bool:
		return b
	case float64:
		return b > 0
	case float32:
		return b > 0
	case int:
		return b > 0
	case int64:
		return b > 0
	case json.Number:
		if f, err := b.Float64(); err == nil {
			return f > 0
		}
	case string:
		s := strings.TrimSpace(strings.ToLower(b))
		return s == "1" || s == "true" || s == "yes" || s == "on"
	}
	return false
}

// shouldPersistEvent returns true only for events worth writing to the database.
// Bbox-only frames (no violations, no VLM summary) arrive at 10+ FPS per camera;
// persisting every one would flood the DB with rows the Moments UI never needs.
func shouldPersistEvent(metadata map[string]interface{}) bool {
	if metadata == nil {
		return false
	}
	if metadataBool(metadata, "internal_only") {
		return false
	}
	agentType, _ := metadata["agent_type"].(string)
	agentType = strings.ToLower(strings.TrimSpace(agentType))

	// VLM / wellness events are already infrequent — always persist.
	// Detection-like streams (including custom YOLO) can arrive at high FPS and
	// should only be persisted when they carry meaningful signal.
	isDetectionLike := agentType == "ppe" || agentType == "person" || agentType == "face" || agentType == "custom"
	if !isDetectionLike {
		return true
	}

	// PPE/person/face detection: persist only when something noteworthy happened
	if snap, ok := metadata["snapshot_path"].(string); ok && snap != "" {
		return true
	}
	if summary, ok := metadata["vlm_summary"].(string); ok && summary != "" {
		return true
	}
	if metadataBool(metadata, "has_violations") {
		return true
	}
	if kind, ok := metadata["event_kind"].(string); ok {
		kind = strings.ToLower(strings.TrimSpace(kind))
		if kind == "ppe_violation" || kind == "person_visit" {
			return true
		}
	}
	if vc, ok := metadataFloat(metadata, "violation_count"); ok && vc > 0 {
		return true
	}
	if metadataBool(metadata, "fall_detected") {
		return true
	}
	if metadataBool(metadata, "first_detection") {
		return true
	}
	if mc, ok := metadataFloat(metadata, "matched_count"); ok && mc > 0 {
		return true
	}
	if sev, ok := metadata["severity"].(string); ok {
		sev = strings.ToLower(strings.TrimSpace(sev))
		if sev == "medium" || sev == "high" {
			return true
		}
	}
	return false
}

// ── Setter helpers ────────────────────────────────────────────────────────────

// GetWorkerIDByKey looks up the connection ID stored for an auth key.
func (r *CVWorkerRegistry) GetWorkerIDByKey(key string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	connID, exists := r.workerKeys[key]
	return connID, exists
}

func (r *CVWorkerRegistry) registerWorkerKey(key, connID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workerKeys[key] = connID
}

func (r *CVWorkerRegistry) SetStorageManager(sm *StorageManager) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.storageManager = sm
}

func (r *CVWorkerRegistry) SetAgentRegistry(ar AgentRegistry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agentRegistry = ar
}

func (r *CVWorkerRegistry) SetDatabase(db AgentEventStorage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.db = db
}

func (r *CVWorkerRegistry) SetPoolChangeHook(hook func()) {
	r.poolHookMu.Lock()
	defer r.poolHookMu.Unlock()
	r.poolChangeHook = hook
}

func (r *CVWorkerRegistry) notifyPoolChange() {
	r.poolHookMu.RLock()
	hook := r.poolChangeHook
	r.poolHookMu.RUnlock()
	if hook != nil {
		hook()
	}
}

// ── Routing helpers ───────────────────────────────────────────────────────────

func (r *CVWorkerRegistry) groupAgentsByWorker(agents []*AgentInfo) map[string][]*AgentEventInfo {
	workerAgents := make(map[string][]*AgentEventInfo)
	for _, agent := range agents {
		var custom *CustomYOLOEventInfo
		if agent.CustomYOLO != nil {
			custom = &CustomYOLOEventInfo{
				Enabled:                   agent.CustomYOLO.Enabled,
				ModelRelPath:              agent.CustomYOLO.ModelRelPath,
				ModelFilename:             agent.CustomYOLO.ModelFilename,
				ModelSizeBytes:            agent.CustomYOLO.ModelSizeBytes,
				RuntimeDefaultsEnableVLM:  agent.CustomYOLO.RuntimeDefaultsEnableVLM,
				RuntimeDefaultsEnableFace: agent.CustomYOLO.RuntimeDefaultsEnableFace,
				RuntimeDesiredReplicas:    agent.CustomYOLO.RuntimeDesiredReplicas,
			}
		}
		workerAgents[agent.WorkerID] = append(workerAgents[agent.WorkerID], &AgentEventInfo{
			ID:          agent.ID,
			Name:        agent.Name,
			Instruction: agent.Instruction,
			CustomYOLO:  custom,
		})
	}
	return workerAgents
}

// getTargetWorkers selects one worker per agent worker_id using round-robin.
// Must be called with r.mu.RLock held.
func (r *CVWorkerRegistry) getTargetWorkers(serviceID string) []*broadcastTarget {
	if r.agentRegistry == nil {
		return nil
	}
	agents := r.agentRegistry.GetAgentsForService(serviceID)
	if len(agents) == 0 {
		return nil
	}

	workerAgents := r.groupAgentsByWorker(agents)
	targets := make([]*broadcastTarget, 0, len(workerAgents))

	for workerID, agentList := range workerAgents {
		pool, exists := r.pools[workerID]
		if !exists || len(pool.workers) == 0 {
			// Compatibility aliases for migrated worker IDs.
			candidates := []string{}
			switch {
			case strings.EqualFold(workerID, "vyra/object_detection"):
				candidates = []string{"vyra/person_detection"}
			case strings.EqualFold(workerID, "vyra/person_detection"):
				candidates = []string{"vyra/object_detection"}
			case strings.EqualFold(workerID, "vyra/face_recognition"), strings.EqualFold(workerID, "vyra/face"):
				// Face recognition is embedded into person detection; route face agents
				// to the person/object worker pool when dedicated face workers are absent.
				candidates = []string{"vyra/person_detection", "vyra/object_detection"}
			}
			for _, candidate := range candidates {
				if p2, ok := r.pools[candidate]; ok && len(p2.workers) > 0 {
					pool = p2
					exists = true
					break
				}
			}
		}
		if !exists || len(pool.workers) == 0 {
			log.Printf("[CVWorkerRegistry] Agent needs worker %s but none connected", workerID)
			continue
		}
		// Round-robin: atomic increment, wrap by current pool size
		idx := pool.next.Add(1) % uint64(len(pool.workers))
		targets = append(targets, &broadcastTarget{
			Worker:    pool.workers[idx],
			ServiceID: serviceID,
			Agents:    agentList,
		})
	}
	return targets
}

// trySend sends data to a worker channel with adaptive drop logging.
func (r *CVWorkerRegistry) trySend(worker *CVWorker, data []byte, eventType string) {
	fill := len(worker.sendChan)
	capacity := cap(worker.sendChan)

	// Warn at ≥80% fill (rate-limited to once per 5 s per worker)
	if fill*10 >= capacity*8 {
		now := time.Now().Unix()
		if now-worker.lastDropLog.Load() >= 5 {
			worker.lastDropLog.Store(now)
			log.Printf("[CVWorkerRegistry] Worker %s channel %d/%d (%.0f%%) — consider adding worker processes (total drops: %d)",
				worker.ID, fill, capacity, float64(fill)/float64(capacity)*100,
				worker.droppedFrames.Load())
		}
	}

	select {
	case worker.sendChan <- data:
	default:
		n := worker.droppedFrames.Add(1)
		if n%50 == 1 {
			log.Printf("[CVWorkerRegistry] Worker %s (conn=%s) dropping %s (total: %d)",
				worker.ID, worker.connID[:8], eventType, n)
		}
	}
}

// ── WebSocket handling ────────────────────────────────────────────────────────

// HandleWebSocket upgrades an HTTP request to a worker WebSocket connection.
func (r *CVWorkerRegistry) HandleWebSocket(w http.ResponseWriter, req *http.Request) {
	conn, err := r.upgrader.Upgrade(w, req, nil)
	if err != nil {
		log.Printf("[CVWorkerRegistry] Failed to upgrade connection: %v", err)
		return
	}

	// Expect a "register" message first
	var msg WSMessage
	if err := conn.ReadJSON(&msg); err != nil {
		log.Printf("[CVWorkerRegistry] Failed to read registration: %v", err)
		conn.Close()
		return
	}
	if msg.Type != "register" {
		log.Printf("[CVWorkerRegistry] Expected register, got %s", msg.Type)
		conn.Close()
		return
	}

	var regMsg RegisterMessage
	if err := json.Unmarshal(msg.Data, &regMsg); err != nil || regMsg.WorkerID == "" {
		log.Printf("[CVWorkerRegistry] Invalid register message")
		conn.Close()
		return
	}

	connID := uuid.New().String()
	worker := &CVWorker{
		ID:           regMsg.WorkerID,
		connID:       connID,
		Conn:         conn,
		RegisteredAt: time.Now(),
		LastSeen:     time.Now(),
		sendChan:     make(chan []byte, 512),
		closeChan:    make(chan struct{}),
	}

	r.RegisterWorker(worker)
	go r.notifyPoolChange()

	// Generate per-connection auth key for HTTP frame downloads
	key := generateWorkerKey()
	r.registerWorkerKey(key, connID)

	// Confirm registration (includes auth key)
	regConfirm := map[string]interface{}{
		"type": "registered",
		"data": map[string]interface{}{
			"worker_id": regMsg.WorkerID,
			"key":       key,
		},
	}
	if data, err := json.Marshal(regConfirm); err == nil {
		worker.sendChan <- data
	}

	log.Printf("[CVWorkerRegistry] Worker %s connected (conn=%s)", regMsg.WorkerID, connID[:8])

	go r.sendLoop(worker)
	r.receiveLoop(worker) // blocks until disconnect

	// Clean up after the worker disconnects
	r.removeWorkerConn(connID)
	go r.notifyPoolChange()
}

func (r *CVWorkerRegistry) sendLoop(worker *CVWorker) {
	defer worker.Conn.Close()
	for {
		select {
		case <-worker.closeChan:
			return
		case data := <-worker.sendChan:
			if err := worker.Conn.WriteMessage(websocket.TextMessage, data); err != nil {
				log.Printf("[CVWorkerRegistry] Send error for worker %s: %v", worker.ID, err)
				return
			}
		}
	}
}

func (r *CVWorkerRegistry) receiveLoop(worker *CVWorker) {
	defer worker.Close()

	for {
		var msg WSMessage
		if err := worker.Conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("[CVWorkerRegistry] Worker %s read error: %v", worker.ID, err)
			}
			return
		}

		switch msg.Type {
		case "heartbeat":
			r.mu.Lock()
			worker.LastSeen = time.Now()
			r.mu.Unlock()

		case "event":
			var eventData map[string]interface{}
			if err := json.Unmarshal(msg.Data, &eventData); err != nil {
				log.Printf("[CVWorkerRegistry] Bad event from worker %s: %v", worker.ID, err)
				continue
			}

			agentID, _ := eventData["agent_id"].(string)
			data, _ := eventData["data"].(map[string]interface{})
			metadata, _ := eventData["metadata"].(map[string]interface{})
			createdAt := time.Now()
			eventID := uuid.New().String()

			if metadata == nil {
				metadata = map[string]interface{}{}
			}
			if existingID, ok := metadata["event_id"].(string); ok && strings.TrimSpace(existingID) != "" {
				eventID = strings.TrimSpace(existingID)
			} else {
				metadata["event_id"] = eventID
			}
			if _, ok := metadata["worker_id"]; !ok {
				metadata["worker_id"] = worker.ID
			}
			if _, ok := metadata["agent_type"]; !ok {
				metadata["agent_type"] = inferAgentType(worker.ID)
			}
			if sid, ok := eventData["service_id"].(string); ok && sid != "" {
				metadata["service_id"] = sid
			}
			if fid, ok := eventData["frame_id"].(string); ok && fid != "" {
				metadata["frame_id"] = fid
			}
			eventData["metadata"] = metadata

			// Write to DB only for meaningful events (violations / VLM results).
			// Uses the async channel so the receive loop is never blocked by SQLite.
			if r.db != nil && agentID != "" && data != nil && shouldPersistEvent(metadata) {
				req := dbWriteRequest{db: r.db, agentID: agentID, data: data, metadata: metadata, createdAt: createdAt}
				select {
				case r.dbWriteChan <- req:
				default:
					r.dbQueueDropped.Add(1)
					log.Printf("[CVWorkerRegistry] DB write queue full, dropping event for agent %s", agentID)
				}
			}

			r.eventBus.PublishWorkerEvent(&WorkerEvent{
				EventID:   fmt.Sprintf("%s-%d", worker.ID, createdAt.UnixNano()),
				WorkerID:  worker.ID,
				CreatedAt: createdAt,
				Data:      eventData,
			})

		default:
			log.Printf("[CVWorkerRegistry] Unknown message type from worker %s: %s", worker.ID, msg.Type)
		}
	}
}

// ── Pool management ───────────────────────────────────────────────────────────

// RegisterWorker adds a worker to its pool (multiple instances of the same
// worker_id are allowed and will receive frames in round-robin order).
func (r *CVWorkerRegistry) RegisterWorker(worker *CVWorker) {
	r.mu.Lock()
	defer r.mu.Unlock()

	pool, exists := r.pools[worker.ID]
	if !exists {
		pool = &workerPool{}
		r.pools[worker.ID] = pool
	}
	pool.workers = append(pool.workers, worker)
	r.connWorker[worker.connID] = worker

	log.Printf("[CVWorkerRegistry] Registered worker %s (conn=%s, pool_size=%d)",
		worker.ID, worker.connID[:8], len(pool.workers))
}

// removeWorkerConn removes a single connection from its pool and cleans up its auth key.
func (r *CVWorkerRegistry) removeWorkerConn(connID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	worker, exists := r.connWorker[connID]
	if !exists {
		return
	}

	worker.Close()
	delete(r.connWorker, connID)

	// Remove from pool slice
	if pool, ok := r.pools[worker.ID]; ok {
		for i, w := range pool.workers {
			if w.connID == connID {
				pool.workers = append(pool.workers[:i], pool.workers[i+1:]...)
				break
			}
		}
		if len(pool.workers) == 0 {
			delete(r.pools, worker.ID)
		}
	}

	// Remove auth key
	for k, cID := range r.workerKeys {
		if cID == connID {
			delete(r.workerKeys, k)
			break
		}
	}

	remaining := 0
	if pool, ok := r.pools[worker.ID]; ok {
		remaining = len(pool.workers)
	}
	log.Printf("[CVWorkerRegistry] Worker %s disconnected (conn=%s, pool_size=%d, total_dropped=%d)",
		worker.ID, connID[:8], remaining, worker.droppedFrames.Load())
}

// RemoveWorker closes and removes ALL connections for a given worker_id.
func (r *CVWorkerRegistry) RemoveWorker(workerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	pool, exists := r.pools[workerID]
	if !exists {
		return
	}
	for _, w := range pool.workers {
		w.Close()
		delete(r.connWorker, w.connID)
		for k, cID := range r.workerKeys {
			if cID == w.connID {
				delete(r.workerKeys, k)
				break
			}
		}
	}
	delete(r.pools, workerID)
	log.Printf("[CVWorkerRegistry] Removed all connections for worker %s", workerID)
}

// ── Broadcast ─────────────────────────────────────────────────────────────────

// BroadcastFrameEvent broadcasts a single-frame event (rarely used path).
func (r *CVWorkerRegistry) BroadcastFrameEvent(event *FrameEvent, timestamp time.Time) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.pools) == 0 {
		return
	}
	targets := r.getTargetWorkers(event.ServiceID)
	if len(targets) == 0 {
		return
	}

	msg := map[string]interface{}{
		"type":       "frame",
		"id":         uuid.New().String(),
		"created_at": timestamp.UTC().Format(time.RFC3339),
		"data":       event,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	for _, t := range targets {
		r.trySend(t.Worker, data, "frame")
	}
}

// BroadcastFrameBatchEvent routes a frame batch to one worker per agent (round-robin).
func (r *CVWorkerRegistry) BroadcastFrameBatchEvent(event *FrameBatchEvent, timestamp time.Time) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.pools) == 0 {
		return
	}
	targets := r.getTargetWorkers(event.ServiceID)
	if len(targets) == 0 {
		return
	}

	for _, target := range targets {
		eventCopy := *event
		eventCopy.Agents = target.Agents

		msg := map[string]interface{}{
			"type":       "frame_batch",
			"id":         uuid.New().String(),
			"created_at": timestamp.UTC().Format(time.RFC3339),
			"data": map[string]interface{}{
				"service_id": eventCopy.ServiceID,
				"frames":     eventCopy.Frames,
				"metadata":   eventCopy.Metadata,
				"agents":     eventCopy.Agents,
			},
		}

		data, err := json.Marshal(msg)
		if err != nil {
			log.Printf("[CVWorkerRegistry] Failed to marshal frame_batch: %v", err)
			continue
		}
		r.trySend(target.Worker, data, "frame_batch")
	}
}

// SendToWorkerPool sends a message to one worker in the named pool (round-robin).
// Returns true if a worker is connected and the message was queued, false otherwise.
// Used for relay-initiated VLM trigger messages.
func (r *CVWorkerRegistry) SendToWorkerPool(workerID string, data []byte) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	pool, exists := r.pools[workerID]
	if !exists || len(pool.workers) == 0 {
		return false
	}
	idx := pool.next.Add(1) % uint64(len(pool.workers))
	worker := pool.workers[idx]
	r.trySend(worker, data, "vlm_trigger")
	return true
}

// GetWorkerRuntimeStats returns pool telemetry keyed by worker_id.
func (r *CVWorkerRegistry) GetWorkerRuntimeStats() map[string]WorkerRuntimeStats {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make(map[string]WorkerRuntimeStats, len(r.pools))
	for workerID, pool := range r.pools {
		stats := WorkerRuntimeStats{ConnectedWorkers: len(pool.workers)}
		for _, w := range pool.workers {
			stats.TotalDropped += w.droppedFrames.Load()
			capacity := cap(w.sendChan)
			if capacity > 0 {
				fillPct := (len(w.sendChan) * 100) / capacity
				if fillPct > stats.MaxQueueFillPct {
					stats.MaxQueueFillPct = fillPct
				}
			}
		}
		out[workerID] = stats
	}
	return out
}

func (r *CVWorkerRegistry) GetPersistRuntimeStats() PersistRuntimeStats {
	return PersistRuntimeStats{
		Success: r.dbPersistSuccess.Load(),
		Failure: r.dbPersistFailure.Load(),
		Dropped: r.dbQueueDropped.Load(),
	}
}
