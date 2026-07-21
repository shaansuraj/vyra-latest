package relay

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/unblink/unblink/node"
	"github.com/unblink/unblink/relay/cv"
	"github.com/unblink/unblink/relay/realtime"
)

// Relay manages node connections and the service registry
type Relay struct {
	nodes         map[string]*NodeConn // node_id -> connection
	nodesMu       sync.RWMutex
	services      *ServiceRegistry
	db            *Database
	nodeTable     *NodeTable
	agentTable    *AgentTable
	cameraTable   *CameraTable
	agentRegistry *AgentRegistry // In-memory agent cache
	config        *Config        // Centralized configuration
	shutdown      chan struct{}
	wg            sync.WaitGroup

	// Realtime streaming and CV processing
	realtimeStreamManager *realtime.RealtimeStreamManager
	cvEventBus            *cv.CVEventBus
	cvWorkerRegistry      *cv.CVWorkerRegistry
	storageManager        *cv.StorageManager
	snapshotManager       *cv.SnapshotManager
	clientEvents          *ClientEventRegistry

	// Process management and multi-node support
	processManager    *ProcessManager
	managedNodeTable  *ManagedNodeTable
	personTable       *PersonTable
	dailySummaryTable *DailySummaryTable
	lastDropCount     map[string]uint64
	frameSyncMu       sync.Mutex

	// Relay-driven VLM trigger: rate-limit to avoid redundant analysis per (service+context)
	vlmTriggerCooldowns map[string]time.Time
	vlmTriggerMu        sync.Mutex

	// Shared face identity cache — detection workers query this instead of embedding face rec
	faceIdentityService *FaceIdentityService
	// Shared cross-camera person identity service (recognized + unknown IDs)
	personIdentityService *PersonIdentityService
	metrics               *RuntimeMetrics
}

// NewRelay creates a new relay server
func NewRelay() *Relay {
	// Load centralized configuration
	config, err := LoadConfig()
	if err != nil {
		log.Fatalf("[Relay] Failed to load configuration: %v", err)
	}

	// Initialize database
	db, err := NewDatabase(config.DatabasePath)
	if err != nil {
		log.Fatalf("[Relay] Failed to initialize database: %v", err)
	}

	pm := NewProcessManager(
		config.RootDir,
		config.AppDir,
		config.RelayPort,
		config.APIPort,
		config.EnginePort,
		config.WorkerSecret,
	)
	pm.SetProcessPolicy(
		config.ProcessSelfHealEnabled,
		config.ProcessAutoStartDependencies,
		config.ProcessAutoStartEngine,
	)
	pm.SetAutoScale(config.ProcessAutoScaleEnabled)
	pm.SetAutoScaleMax(config.ProcessAutoScaleMax)
	if config.ProcessCleanupStaleWorkers {
		killed, err := pm.CleanupStaleManagedProcesses()
		if err != nil {
			log.Printf("[Relay] Warning: stale process cleanup failed: %v", err)
		} else if killed > 0 {
			log.Printf("[Relay] Cleaned up %d stale managed process group(s) from previous relay runs", killed)
		}
	}

	relay := &Relay{
		nodes:               make(map[string]*NodeConn),
		services:            NewServiceRegistry(),
		shutdown:            make(chan struct{}),
		db:                  db,
		nodeTable:           NewNodeTable(db.DB),
		agentTable:          NewAgentTable(db.DB),
		cameraTable:         NewCameraTable(db.DB),
		agentRegistry:       NewAgentRegistry(),
		config:              config,
		managedNodeTable:    NewManagedNodeTable(db.DB),
		personTable:         NewPersonTable(db.DB),
		dailySummaryTable:   NewDailySummaryTable(db.DB),
		processManager:      pm,
		lastDropCount:       make(map[string]uint64),
		vlmTriggerCooldowns: make(map[string]time.Time),
		metrics:             NewRuntimeMetrics(),
	}

	if err := relay.agentTable.MigrateLegacyFaceAgents(); err != nil {
		log.Printf("[Relay] Warning: Failed to migrate legacy face-only agents: %v", err)
	}

	// Load agents from database into registry
	if err := relay.agentRegistry.LoadFromDatabase(relay.agentTable); err != nil {
		log.Printf("[Relay] Warning: Failed to load agents into registry: %v", err)
	}

	relay.initializeCV()

	// Shared face identity service — detection workers query cached identities
	// instead of embedding InsightFace.  Results come from the face worker pool.
	relay.faceIdentityService = NewFaceIdentityService(relay.cvWorkerRegistry, relay.storageManager)
	go relay.faceIdentityService.RunCleanup(relay.shutdown)
	relay.personIdentityService = NewPersonIdentityService(relay.db, relay.agentTable)
	go relay.personIdentityService.RunCleanup(relay.shutdown)
	go func() {
		if strings.EqualFold(strings.TrimSpace(os.Getenv("TIMELINE_BACKFILL_ON_START")), "0") {
			return
		}
		time.Sleep(1200 * time.Millisecond)
		n, err := relay.db.BackfillPersonSightingsFromEvents(envInt("TIMELINE_BACKFILL_LIMIT", 50000, 1, 500000))
		if err != nil {
			log.Printf("[Timeline] backfill failed: %v", err)
			return
		}
		if n > 0 {
			log.Printf("[Timeline] backfill processed %d timeline rows", n)
		}
	}()

	go relay.autoScaleLoop()
	go relay.summaryLoop()
	go func() {
		// Let the API and websocket listeners come up first; startup restore is
		// best-effort and should never make the relay look down.
		time.Sleep(750 * time.Millisecond)
		relay.restoreAgentRuntimeIntents()
		relay.restoreManagedNodeIntents()
	}()
	go relay.autoIndexKnowledgeDocsOnStart()
	return relay
}

func (r *Relay) restoreAgentRuntimeIntents() {
	if r == nil || r.processManager == nil || r.agentTable == nil || r.config == nil {
		return
	}
	if !r.config.ProcessSelfHealEnabled || !r.config.ProcessAutoStartDependencies {
		return
	}

	agents, err := r.agentTable.GetAgentsByUser(0)
	if err != nil {
		log.Printf("[Relay][SelfHeal] Failed to load agents for runtime restore: %v", err)
		return
	}
	runtimes := agentRuntimeTypesForRestore(agents)
	if len(runtimes) == 0 {
		return
	}

	for _, runtimeType := range runtimes {
		replicas := r.processManager.DesiredScale(runtimeType)
		if replicas < 1 {
			replicas = 1
		}
		if err := r.processManager.ScaleWorkers(runtimeType, replicas); err != nil {
			log.Printf("[Relay][SelfHeal] Failed to restore %s runtime to %d replica(s): %v", runtimeType, replicas, err)
			continue
		}
		log.Printf("[Relay][SelfHeal] Restored %s runtime to %d replica(s) from assigned agents", runtimeType, replicas)
	}
}

func (r *Relay) restoreManagedNodeIntents() {
	if r == nil || r.processManager == nil || r.managedNodeTable == nil || r.cameraTable == nil || r.config == nil {
		return
	}
	if !r.config.ProcessSelfHealEnabled {
		return
	}
	if !r.processManager.IsAvailable() {
		return
	}

	nodes, err := r.managedNodeTable.GetAllManagedNodes()
	if err != nil {
		log.Printf("[Relay][SelfHeal] Failed to load managed nodes for restore: %v", err)
		return
	}

	for _, mn := range nodes {
		if mn == nil || strings.TrimSpace(mn.ID) == "" {
			continue
		}
		if r.GetNode(mn.ID) != nil || r.processManager.IsRunning("node-"+mn.ID) {
			continue
		}

		cameras, err := r.cameraTable.GetCamerasByManagedNode(mn.ID)
		if err != nil {
			log.Printf("[Relay][SelfHeal] Failed to load cameras for managed node %s: %v", mn.ID, err)
			continue
		}
		if len(cameras) == 0 {
			continue
		}

		token, err := r.EnsureManagedNodeAuthorized(mn.ID, mn.Name, mn.UserID)
		if err != nil {
			log.Printf("[Relay][SelfHeal] Failed to authorize managed node %s: %v", mn.ID, err)
			continue
		}

		if err := r.processManager.StartNode(mn.ID, cameras, token); err != nil {
			log.Printf("[Relay][SelfHeal] Failed to restore managed node %s: %v", mn.ID, err)
			continue
		}
		log.Printf("[Relay][SelfHeal] Restored managed node %s with %d camera(s)", mn.ID, len(cameras))
	}
}

func agentRuntimeTypesForRestore(agents []*Agent) []string {
	seen := map[string]bool{}
	add := func(runtimeType string) {
		runtimeType = strings.TrimSpace(strings.ToLower(runtimeType))
		if runtimeType == "" || seen[runtimeType] {
			return
		}
		seen[runtimeType] = true
	}

	for _, agent := range agents {
		if agent == nil || len(agent.ServiceIDs) == 0 {
			continue
		}
		if agent.Config.CustomYOLO != nil && agent.Config.CustomYOLO.Enabled {
			continue
		}
		wtype := workerIDToType(agent.WorkerID)
		if wtype == "" {
			continue
		}
		for _, runtimeType := range startAgentRuntimeTypes(wtype) {
			add(runtimeType)
		}
	}

	order := []string{"vlm", "face", "ppe", "objdet"}
	out := make([]string, 0, len(seen))
	for _, runtimeType := range order {
		if seen[runtimeType] {
			out = append(out, runtimeType)
		}
	}
	return out
}

func (r *Relay) autoScaleLoop() {
	interval := r.config.ProcessAutoScaleInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-r.shutdown:
			return
		case <-t.C:
			if r.config.ProcessSelfHealEnabled {
				if err := r.processManager.EnsureLLMWorker(); err != nil {
					log.Printf("[Relay][SelfHeal] Failed to reconcile llm_query: %v", err)
				}
				r.restoreManagedNodeIntents()
				// Keep desired worker/adapter replicas alive even when autoscaling is off.
				// This heals "Partially running" states after worker crashes.
				for _, wtype := range []string{"ppe", "objdet", "vlm", "face"} {
					desired := r.processManager.DesiredScale(wtype)
					if desired <= 0 {
						continue
					}
					if err := r.processManager.ScaleWorkers(wtype, desired); err != nil {
						log.Printf("[Relay][SelfHeal] Failed to reconcile %s to %d replicas: %v", wtype, desired, err)
					}
				}
				for agentID, inv := range r.processManager.CustomWorkerInventory() {
					if inv.Desired <= 0 {
						continue
					}
					if err := r.processManager.ScaleCustomAgentReplicas(agentID, inv.Desired); err != nil {
						log.Printf("[Relay][SelfHeal] Failed to reconcile custom %s to %d replicas: %v", agentID, inv.Desired, err)
					}
				}
			}

			if !r.processManager.AutoScaleEnabled() || r.cvWorkerRegistry == nil {
				continue
			}
			if !systemResourcesHealthy() {
				continue
			}
			stats := r.cvWorkerRegistry.GetWorkerRuntimeStats()
			maxReplicas := r.processManager.AutoScaleMax()

			decide := func(wtype, workerID string) {
				s, ok := stats[workerID]
				if !ok {
					return
				}
				prev := r.lastDropCount[workerID]
				r.lastDropCount[workerID] = s.TotalDropped
				dropsIncreased := s.TotalDropped > prev
				highQueue := s.MaxQueueFillPct >= 80
				if !dropsIncreased && !highQueue {
					return
				}
				cur := r.processManager.DesiredScale(wtype)
				if cur >= maxReplicas {
					return
				}
				if err := r.processManager.ScaleWorkers(wtype, cur+1); err == nil {
					log.Printf("[Relay][Autoscale] Scaled %s from %d to %d due to queue pressure/drops", wtype, cur, cur+1)
				}
			}

			decide("ppe", "vyra/ppe_detection")
			// person detection supports both IDs
			if _, ok := stats["vyra/person_detection"]; ok {
				decide("objdet", "vyra/person_detection")
			} else {
				decide("objdet", "vyra/object_detection")
			}
			decide("vlm", "vyra/vlm")
			decide("face", "vyra/face_recognition")

			// Autoscale dedicated custom runtimes based on their per-agent queue pressure/drop telemetry.
			for agentID, inv := range r.processManager.CustomWorkerInventory() {
				if inv.Desired <= 0 || inv.Desired >= maxReplicas {
					continue
				}
				workerID := strings.TrimSpace(inv.WorkerID)
				if workerID == "" {
					workerID = "vyra/custom_detection/" + agentID
				}
				s, ok := stats[workerID]
				if !ok {
					continue
				}
				prev := r.lastDropCount[workerID]
				r.lastDropCount[workerID] = s.TotalDropped
				dropsIncreased := s.TotalDropped > prev
				highQueue := s.MaxQueueFillPct >= 80
				if !dropsIncreased && !highQueue {
					continue
				}
				if err := r.processManager.ScaleCustomAgentReplicas(agentID, inv.Desired+1); err == nil {
					log.Printf("[Relay][Autoscale] Scaled custom agent %s from %d to %d due to queue pressure/drops", agentID, inv.Desired, inv.Desired+1)
				}
			}
		}
	}
}

// initializeCV initializes the CV processing subsystems
func (r *Relay) initializeCV() {
	config := r.config

	r.cvEventBus = cv.NewCVEventBus()

	// Worker registry (event bus first)
	r.cvWorkerRegistry = cv.NewCVWorkerRegistry(r.cvEventBus, nil)

	// Storage manager
	r.storageManager = cv.NewStorageManager(config.StorageDir, r.cvWorkerRegistry)

	// Snapshot manager for violation/event snapshots
	snapshotDir := filepath.Join(config.StorageDir, "snapshots")
	os.MkdirAll(snapshotDir, 0755)
	r.snapshotManager = cv.NewSnapshotManager(snapshotDir)
	log.Printf("[Relay] Snapshot storage initialized: %s", snapshotDir)

	// Close the circular deps
	r.cvWorkerRegistry.SetStorageManager(r.storageManager)
	r.cvWorkerRegistry.SetAgentRegistry(r.agentRegistry)
	r.cvWorkerRegistry.SetDatabase(r.db)
	r.cvWorkerRegistry.SetPoolChangeHook(func() {
		r.syncAllFrameExtractors()
	})

	// Browser WS registry
	r.clientEvents = NewClientEventRegistry(r.agentTable, r.agentRegistry, r.db, r.config)

	// Forward worker events to browser clients.
	// Meaningful events are broadcast as `agent_event` and persisted by worker registry.
	// High-frequency bbox-only frames are broadcast as transient `agent_overlay_event`.
	r.cvEventBus.OnWorkerEvent(func(event *cv.WorkerEvent) {
		if agentID, ok := event.Data["agent_id"].(string); ok {
			data, _ := event.Data["data"].(map[string]interface{})
			metadata, _ := event.Data["metadata"].(map[string]interface{})
			if metadata != nil && r.faceIdentityService != nil {
				r.faceIdentityService.OnFaceWorkerEvent(event.Data)
			}
			if isInternalFaceLookupAgent(agentID, metadata) {
				return
			}
			if !legacyModeEnabled() && shouldBroadcastOverlayEvent(metadata) {
				r.clientEvents.BroadcastOverlayEvent(agentID, data, metadata, event.CreatedAt)
			}
			if !shouldBroadcastEvent(metadata) {
				return
			}
			r.clientEvents.BroadcastAgentEvent(agentID, data, metadata, event.CreatedAt)

			// Capture face worker results into the shared identity cache.
			if metadata != nil {
				if r.personIdentityService != nil {
					r.personIdentityService.OnAgentEvent(agentID, metadata, data, event.CreatedAt)
				}
			}

			// Relay-driven VLM trigger: when a PPE/person/custom event with snapshot is detected,
			// send a vlm_trigger to any connected VLM adapter so the scene gets analyzed.
			// This decouples VLM from the YOLO pipeline, removing GPU contention.
			if metadata != nil {
				agentType, _ := metadata["agent_type"].(string)
				if agentType == "ppe" || agentType == "person" {
					if snapshotPath, ok := metadata["snapshot_path"].(string); ok && snapshotPath != "" {
						serviceID, _ := metadata["service_id"].(string)
						context := buildViolationContext(metadata)
						go r.maybeVLMTrigger("", serviceID, snapshotPath, context)
					}
				}
				if agentType == "custom" {
					customEnableVLM, _ := metadata["custom_enable_vlm"].(bool)
					if customEnableVLM {
						if snapshotPath, ok := metadata["snapshot_path"].(string); ok && snapshotPath != "" {
							serviceID, _ := metadata["service_id"].(string)
							context := buildViolationContext(metadata)
							go r.maybeVLMTrigger(agentID, serviceID, snapshotPath, context)
						}
					}
				}
			}
		}
	})

	// Realtime stream manager
	nodeConnGetter := func(nodeID string) realtime.NodeConn {
		nc := r.GetNode(nodeID)
		if nc == nil {
			return nil
		}
		return nc
	}

	bridgeProxyFactory := func(nc realtime.NodeConn, bridgeID string, service node.Service) (realtime.BridgeTCPProxy, error) {
		nodeConn, ok := nc.(*NodeConn)
		if !ok {
			return nil, fmt.Errorf("invalid node connection type")
		}
		return NewBridgeTCPProxy(nodeConn, bridgeID, service)
	}

	r.realtimeStreamManager = realtime.NewRealtimeStreamManager(nodeConnGetter, bridgeProxyFactory)

	// When stream demand changes, frame extraction is started/stopped lazily.
	r.realtimeStreamManager.OnStreamCreated(func(stream *realtime.RealtimeStream) {
		service := stream.GetService().(node.Service)
		r.syncFrameExtractorForService(service.ID)
	})

	log.Printf("[Relay] Initialized CV processing (storage=%s, frameInterval=%v)",
		config.StorageDir, config.FrameInterval)
}

func (r *Relay) syncAllFrameExtractors() {
	if r == nil || r.realtimeStreamManager == nil {
		return
	}
	seen := make(map[string]struct{})
	for _, stream := range r.realtimeStreamManager.ListStreams() {
		service, ok := stream.GetService().(node.Service)
		if !ok {
			continue
		}
		seen[service.ID] = struct{}{}
		r.syncFrameExtractorForService(service.ID)
	}
	if r.agentRegistry == nil {
		return
	}
	for _, serviceID := range r.agentRegistry.MonitoredServiceIDs() {
		if _, ok := seen[serviceID]; ok {
			continue
		}
		r.syncFrameExtractorForService(serviceID)
	}
}

func (r *Relay) syncFrameExtractorsForServices(serviceIDs ...string) {
	seen := make(map[string]struct{}, len(serviceIDs))
	for _, serviceID := range serviceIDs {
		serviceID = strings.TrimSpace(serviceID)
		if serviceID == "" {
			continue
		}
		if _, exists := seen[serviceID]; exists {
			continue
		}
		seen[serviceID] = struct{}{}
		r.syncFrameExtractorForService(serviceID)
	}
}

func (r *Relay) syncFrameExtractorForService(serviceID string) {
	if r == nil || r.realtimeStreamManager == nil {
		return
	}
	shouldRun := r.shouldRunFrameExtractor(serviceID)
	stream, err := r.realtimeStreamManager.GetStream(serviceID)
	if err != nil {
		if !shouldRun {
			return
		}
		if !r.ensureRealtimeStreamForService(serviceID) {
			return
		}
		stream, err = r.realtimeStreamManager.GetStream(serviceID)
		if err != nil {
			return
		}
	}

	r.frameSyncMu.Lock()
	defer r.frameSyncMu.Unlock()

	extractor := stream.GetFrameExtractor()
	if shouldRun {
		if extractor == nil {
			r.startFrameExtractorForStream(stream)
		}
		return
	}
	if extractor != nil {
		extractor.Close()
		stream.SetFrameExtractor(nil)
		log.Printf("[Relay] Stopped frame extraction for service %s (no active CV demand)", serviceID)
	}
}

func (r *Relay) ensureRealtimeStreamForService(serviceID string) bool {
	if r == nil || r.realtimeStreamManager == nil || r.config == nil || !r.config.AutoRequestRealtimeStream {
		return false
	}
	if _, err := r.realtimeStreamManager.GetStream(serviceID); err == nil {
		return true
	}

	if r.services != nil {
		if reg := r.services.Get(serviceID); reg != nil && isRealtimeCameraType(reg.Service.Type) && r.GetNode(reg.NodeID) != nil {
			log.Printf("[Relay] Creating missing realtime stream for service %s from registered node %s", serviceID, reg.NodeID)
			r.realtimeStreamManager.OnServiceAnnounced(reg.Service, reg.NodeID)
			return true
		}
	}

	if r.cameraTable == nil {
		return false
	}
	cam, err := r.cameraTable.GetCameraByID(serviceID)
	if err != nil || !isRealtimeCameraType(cam.Type) {
		return false
	}
	svc := r.cameraToService(cam)
	for _, nodeID := range cam.NodeIDs {
		if r.GetNode(nodeID) == nil {
			continue
		}
		if r.services != nil {
			r.services.Register(svc, nodeID)
		}
		log.Printf("[Relay] Creating missing realtime stream for camera %s from assigned node %s", serviceID, nodeID)
		r.realtimeStreamManager.OnServiceAnnounced(svc, nodeID)
		return true
	}
	return false
}

func isRealtimeCameraType(cameraType string) bool {
	switch strings.ToLower(strings.TrimSpace(cameraType)) {
	case "rtsp", "mjpeg":
		return true
	default:
		return false
	}
}

func (r *Relay) shouldRunFrameExtractor(serviceID string) bool {
	if r == nil || r.agentRegistry == nil || r.cvWorkerRegistry == nil {
		return false
	}

	agents := r.agentRegistry.GetAgentsForService(serviceID)
	if len(agents) == 0 {
		return false
	}

	stats := r.cvWorkerRegistry.GetWorkerRuntimeStats()
	for _, agent := range agents {
		if workerPoolConnected(stats, agent.WorkerID) {
			return true
		}
	}
	return false
}

func workerPoolConnected(stats map[string]cv.WorkerRuntimeStats, workerID string) bool {
	candidates := []string{strings.TrimSpace(workerID)}
	switch {
	case strings.EqualFold(workerID, "vyra/object_detection"):
		candidates = append(candidates, "vyra/person_detection")
	case strings.EqualFold(workerID, "vyra/person_detection"):
		candidates = append(candidates, "vyra/object_detection")
	case strings.EqualFold(workerID, "vyra/face_recognition"), strings.EqualFold(workerID, "vyra/face"):
		candidates = append(candidates, "vyra/person_detection", "vyra/object_detection")
	}

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if s, ok := stats[candidate]; ok && s.ConnectedWorkers > 0 {
			return true
		}
	}
	return false
}

func (r *Relay) startFrameExtractorForStream(stream *realtime.RealtimeStream) {
	if stream == nil || stream.GetFrameExtractor() != nil {
		return
	}
	select {
	case <-stream.CloseChan():
		return
	default:
	}

	service, ok := stream.GetService().(node.Service)
	if !ok {
		return
	}

	// Camera processing_fps is the source of truth for detection extraction.
	// The relay-level FrameInterval is only a fallback for cameras without an
	// explicit configured processing_fps.
	extractInterval := r.config.FrameInterval
	vlmFPS := 5.0
	if cam, err := r.cameraTable.GetCameraByID(service.ID); err == nil {
		if cam.ProcessingFPS > 0 {
			extractInterval = time.Duration(float64(time.Second) / cam.ProcessingFPS)
		}
		if cam.VLMFPS > 0 {
			vlmFPS = cam.VLMFPS
		}
	}

	frameExtractor := cv.NewCVFrameExtractor(
		service.ID,
		extractInterval,
		r.cvEventBus,
		r.storageManager,
		r.config.BatchSize,
	)
	frameExtractor.SetFPSHints(vlmFPS)

	cameraDisplayName := service.Name
	if cam, err := r.cameraTable.GetCameraByID(service.ID); err == nil && cam.Name != "" {
		cameraDisplayName = cam.Name
	}
	if cameraDisplayName != "" {
		frameExtractor.SetServiceName(cameraDisplayName)
	}

	if err := frameExtractor.Start(stream); err != nil {
		log.Printf("[Relay] Failed to start frame extractor for service %s: %v", service.ID, err)
		return
	}

	stream.SetFrameExtractor(frameExtractor)

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		select {
		case <-stream.CloseChan():
			frameExtractor.Close()
		case <-r.shutdown:
			frameExtractor.Close()
		case <-frameExtractor.DoneChan():
			return
		}
	}()

	log.Printf("[Relay] Started frame extraction for service %s (batchSize=%d, extractInterval=%v, vlm_fps=%.2f)",
		service.ID, r.config.BatchSize, extractInterval, vlmFPS)
}

// shouldBroadcastEvent mirrors the DB-persistence filter from cv/worker_registry.go.
// Returns false for bbox-only frames (no violations, no VLM summary) that arrive
// at 10+ FPS per camera — broadcasting those would flood every connected browser.
func shouldBroadcastEvent(metadata map[string]interface{}) bool {
	if metadata == nil {
		return false
	}
	agentType, _ := metadata["agent_type"].(string)
	agentType = strings.ToLower(strings.TrimSpace(agentType))
	// VLM / wellness events are infrequent — always broadcast
	if agentType != "ppe" && agentType != "person" && agentType != "face" {
		return true
	}
	// PPE/person/face detection: only broadcast when something noteworthy happened
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

func isInternalFaceLookupAgent(agentID string, metadata map[string]interface{}) bool {
	if metadataBool(metadata, "internal_only") {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(agentID)), "face_lookup_")
}

func shouldBroadcastOverlayEvent(metadata map[string]interface{}) bool {
	if metadata == nil {
		return false
	}
	agentType, _ := metadata["agent_type"].(string)
	agentType = strings.ToLower(strings.TrimSpace(agentType))
	switch agentType {
	case "person", "ppe", "face", "custom":
	default:
		return false
	}

	switch bboxes := metadata["bboxes"].(type) {
	case []interface{}:
		return len(bboxes) > 0
	case []map[string]interface{}:
		return len(bboxes) > 0
	default:
		return false
	}
}

// registerNode adds a node to the registry
func (r *Relay) registerNode(nodeID string, nc *NodeConn) {
	r.nodesMu.Lock()
	defer r.nodesMu.Unlock()

	if old, exists := r.nodes[nodeID]; exists && old != nc {
		old.Close()
	}

	r.nodes[nodeID] = nc
	nc.nodeID = nodeID
	log.Printf("[Relay] Node registered: %s", nodeID)
}

// removeNode removes a node and its services
func (r *Relay) removeNode(nc *NodeConn) {
	if nc.nodeID == "" {
		return
	}

	r.nodesMu.Lock()
	delete(r.nodes, nc.nodeID)
	r.nodesMu.Unlock()

	// Close the node connection (this also closes all bridges)
	nc.Close()

	// Remove all services from this node
	r.services.RemoveByNode(nc.nodeID)

	// Stop realtime streams for this node
	if r.realtimeStreamManager != nil {
		r.realtimeStreamManager.OnNodeDisconnected(nc.nodeID)
	}

	log.Printf("[Relay] Node removed: %s", nc.nodeID)
}

// GetNode returns a node connection by ID
func (r *Relay) GetNode(nodeID string) *NodeConn {
	r.nodesMu.RLock()
	defer r.nodesMu.RUnlock()
	return r.nodes[nodeID]
}

// ListNodes returns a list of connected node IDs
func (r *Relay) ListNodes() []string {
	r.nodesMu.RLock()
	defer r.nodesMu.RUnlock()

	result := make([]string, 0, len(r.nodes))
	for id := range r.nodes {
		result = append(result, id)
	}
	return result
}

// Services returns the service registry
func (r *Relay) Services() *ServiceRegistry {
	return r.services
}

// GetDB returns the database
func (r *Relay) GetDB() *Database {
	return r.db
}

// getFirstConnectedNodeID returns the ID of the first connected node, or empty string
func (r *Relay) getFirstConnectedNodeID() string {
	r.nodesMu.RLock()
	defer r.nodesMu.RUnlock()
	for id := range r.nodes {
		return id
	}
	return ""
}

// registerCamerasForNode loads only cameras explicitly assigned to the given node
// and registers them in ServiceRegistry under that node.
func (r *Relay) registerCamerasForNode(nodeID string) {
	cameras, err := r.cameraTable.GetCamerasByManagedNode(nodeID)
	if err != nil {
		log.Printf("[Relay] Warning: Failed to load cameras for node %s: %v", nodeID, err)
		return
	}

	for _, cam := range cameras {
		svc := r.cameraToService(cam)
		r.services.Register(svc, nodeID)
		log.Printf("[Relay] Registered DB camera as service: %s (%s:%d) under node %s", cam.Name, cam.Addr, cam.Port, nodeID)

		// Start realtime stream for camera services (if enabled)
		if r.config.AutoRequestRealtimeStream && r.realtimeStreamManager != nil && (cam.Type == "rtsp" || cam.Type == "mjpeg") {
			go r.realtimeStreamManager.OnServiceAnnounced(svc, nodeID)
		}
	}

	log.Printf("[Relay] Registered %d assigned DB camera(s) under node %s", len(cameras), nodeID)
}

// EnsureManagedNodeAuthorized ensures a managed node has a valid token in the nodes table.
// If the node doesn't exist in the nodes table yet, it is created and authorized for the user.
// Returns the token that the node config should use.
func (r *Relay) EnsureManagedNodeAuthorized(nodeID, name string, userID int64) (string, error) {
	// Check if node already exists in the relay nodes table
	existing, err := r.nodeTable.GetNodeByID(nodeID)
	if err == nil && existing.Token != "" {
		// Already authorized — return existing token
		return existing.Token, nil
	}

	// Generate a fresh secure token
	token, err := generateSecureToken()
	if err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}

	// Insert / update the node record and mark it as authorized
	if err := r.nodeTable.AuthorizeNode(nodeID, userID, name, token); err != nil {
		return "", fmt.Errorf("authorize managed node: %w", err)
	}

	log.Printf("[Relay] Authorized managed node %s for user %d", nodeID, userID)
	return token, nil
}

// cameraToService converts a Camera DB record to a node.Service
func (r *Relay) cameraToService(cam *Camera) node.Service {
	svc := node.Service{
		ID:   cam.ID,
		Name: cam.Name,
		Type: cam.Type,
		Addr: cam.Addr,
		Port: cam.Port,
		Path: cam.Path,
	}
	if cam.AuthUsername != "" {
		svc.Auth = &node.Auth{
			Type:     "username_and_password",
			Username: cam.AuthUsername,
			Password: cam.AuthPassword,
		}
	}
	return svc
}

// buildViolationContext constructs a human-readable context string from event metadata.
// Used to label VLM trigger requests so the model gets targeted guidance.
func buildViolationContext(metadata map[string]interface{}) string {
	personNames := func() []string {
		raw, ok := metadata["person_names"].([]interface{})
		if !ok || len(raw) == 0 {
			return nil
		}
		out := make([]string, 0, len(raw))
		for _, it := range raw {
			if s, ok := it.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}

	withNames := func(base string) string {
		names := personNames()
		if len(names) == 0 {
			return base
		}
		return fmt.Sprintf("%s (persons: %s)", base, strings.Join(names, ", "))
	}

	if vls, ok := metadata["violation_labels"].([]interface{}); ok && len(vls) > 0 {
		parts := make([]string, 0, len(vls))
		for _, v := range vls {
			if s, ok := v.(string); ok {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			return withNames(strings.Join(parts, ", "))
		}
	}
	if fd, ok := metadata["fall_detected"].(bool); ok && fd {
		return withNames("Fall-Detected")
	}
	names := personNames()
	if len(names) > 0 {
		return "Person detected: " + strings.Join(names, ", ")
	}
	return ""
}

// vlmTriggerCooldownDuration is the minimum interval between relay-driven VLM calls
// for the same service + violation context combination.
const vlmTriggerCooldownDuration = 30 * time.Second

// maybeVLMTrigger triggers a VLM analysis for a PPE/person/custom snapshot.
// It is rate-limited per (serviceID + context) to avoid redundant VLM calls.
// Runs in its own goroutine — must not block the event handler.
// preferredAgentID is used for custom-agent enrichment; when empty, relay picks
// the VLM agent assigned to the service.
func (r *Relay) maybeVLMTrigger(preferredAgentID, serviceID, snapshotRelPath, context string) {
	if serviceID == "" || snapshotRelPath == "" {
		return
	}

	// Rate-limit: skip if we triggered VLM for same service+context recently
	cooldownKey := serviceID + ":" + context
	r.vlmTriggerMu.Lock()
	if last, exists := r.vlmTriggerCooldowns[cooldownKey]; exists && time.Since(last) < vlmTriggerCooldownDuration {
		r.vlmTriggerMu.Unlock()
		return
	}
	r.vlmTriggerCooldowns[cooldownKey] = time.Now()
	r.vlmTriggerMu.Unlock()

	vlmAgentID := strings.TrimSpace(preferredAgentID)
	if vlmAgentID == "" {
		// Find a VLM agent that monitors this service
		agents := r.agentRegistry.GetAgentsForService(serviceID)
		for _, a := range agents {
			if a.WorkerID == "vyra/vlm" {
				vlmAgentID = a.ID
				break
			}
		}
		if vlmAgentID == "" {
			return // No VLM agent assigned to this camera
		}
	}

	// Read snapshot bytes from disk
	snapshotFullPath := r.snapshotManager.GetSnapshotPath(snapshotRelPath)
	snapBytes, err := os.ReadFile(snapshotFullPath)
	if err != nil {
		log.Printf("[Relay][VLMTrigger] Cannot read snapshot %s: %v", snapshotRelPath, err)
		return
	}

	// Register snapshot bytes as a temporary in-memory frame
	// so the VLM adapter can download it via /worker/frames/{uuid}
	frameUUID := uuid.New().String()
	if err := r.storageManager.RegisterFrameData(frameUUID, serviceID, snapBytes, time.Now()); err != nil {
		log.Printf("[Relay][VLMTrigger] Cannot store snapshot as frame: %v", err)
		return
	}

	// Build the vlm_trigger message
	msgBytes, err := json.Marshal(map[string]interface{}{
		"type": "vlm_trigger",
		"data": map[string]interface{}{
			"agent_id":   vlmAgentID,
			"service_id": serviceID,
			"frame_id":   frameUUID,
			"context":    context,
		},
	})
	if err != nil {
		log.Printf("[Relay][VLMTrigger] Cannot marshal trigger: %v", err)
		return
	}

	if !r.cvWorkerRegistry.SendToWorkerPool("vyra/vlm", msgBytes) {
		log.Printf("[Relay][VLMTrigger] No VLM worker connected (service=%s)", serviceID)
		return
	}

	log.Printf("[Relay][VLMTrigger] VLM triggered: service=%s agent=%s context=%q", serviceID, vlmAgentID, context)
}

// Shutdown gracefully shuts down the relay
func (r *Relay) Shutdown() {
	close(r.shutdown)

	if r.processManager != nil {
		if err := r.processManager.StopAllManaged(); err != nil {
			log.Printf("[Relay] StopAllManaged warning: %v", err)
		}
	}

	// Close all node connections
	r.nodesMu.Lock()
	for _, nc := range r.nodes {
		nc.Close()
	}
	r.nodesMu.Unlock()

	// Wait for relay-owned goroutines (extractor lifecycles, etc.)
	r.wg.Wait()

	log.Printf("[Relay] Shutdown complete")
}

// SendTokenToNode sends an AUTH_TOKEN message to a node
func (r *Relay) SendTokenToNode(nodeID, token string) {
	r.nodesMu.RLock()
	nc, exists := r.nodes[nodeID]
	connectedNodes := make([]string, 0, len(r.nodes))
	for id := range r.nodes {
		connectedNodes = append(connectedNodes, id)
	}
	r.nodesMu.RUnlock()

	if !exists {
		log.Printf("[Relay] Node %s not connected. Connected nodes: %v", nodeID, connectedNodes)
		return
	}

	msg := node.NewAuthTokenMsg(uuid.New().String(), token)
	if err := nc.conn.WriteMessage(msg); err != nil {
		log.Printf("[Relay] Failed to send token to node %s: %v", nodeID, err)
		return
	}

	log.Printf("[Relay] Sent token to node %s", nodeID)
}
