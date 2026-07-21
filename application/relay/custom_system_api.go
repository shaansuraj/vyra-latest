package relay

import (
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/unblink/unblink/relay/cv"
)

type customRuntimeAgentStatus struct {
	AgentID       string              `json:"agent_id"`
	AgentName     string              `json:"agent_name"`
	WorkerID      string              `json:"worker_id"`
	ModelFilename string              `json:"model_filename,omitempty"`
	Desired       int                 `json:"desired"`
	Workers       []WorkerReplicaInfo `json:"workers"`
	Adapters      []WorkerReplicaInfo `json:"adapters"`
	EnableVLM     bool                `json:"enable_vlm"`
	EnableFace    bool                `json:"enable_face"`
	Connected     int                 `json:"connected_workers"`
	TotalDropped  uint64              `json:"total_dropped"`
	QueueFillPct  int                 `json:"queue_fill_pct"`
}

type customRuntimeAggregateStatus struct {
	TotalDesired     int    `json:"total_desired"`
	RunningWorkers   int    `json:"running_workers"`
	RunningAdapters  int    `json:"running_adapters"`
	ConnectedWorkers int    `json:"connected_workers"`
	TotalDropped     uint64 `json:"total_dropped"`
	MaxQueuePressure int    `json:"max_queue_fill_pct"`
}

func runningReplicaCount(list []WorkerReplicaInfo) int {
	n := 0
	for _, it := range list {
		if it.Running {
			n++
		}
	}
	return n
}

func resolveCustomModelPath(appDir, relPath string) (string, bool) {
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return "", false
	}
	if filepath.IsAbs(relPath) {
		return relPath, true
	}
	cleanRel := filepath.Clean(relPath)
	if cleanRel == "." || strings.HasPrefix(cleanRel, "..") {
		return "", false
	}
	return filepath.Join(appDir, cleanRel), true
}

func getOwnedCustomAgent(relay *Relay, userID int64, agentID string) (*Agent, error) {
	_ = userID
	agent, err := relay.agentTable.GetAgentByID(agentID)
	if err != nil {
		return nil, errNotFound("agent not found")
	}
	if agent.Config.CustomYOLO == nil || !agent.Config.CustomYOLO.Enabled {
		return nil, errBadRequest("agent is not a custom YOLO agent")
	}
	return agent, nil
}

type customHTTPError struct {
	msg    string
	status int
}

func (e *customHTTPError) Error() string { return e.msg }

func errNotFound(msg string) error { return &customHTTPError{msg: msg, status: http.StatusNotFound} }
func errBadRequest(msg string) error {
	return &customHTTPError{msg: msg, status: http.StatusBadRequest}
}

func writeCustomErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if ce, ok := err.(*customHTTPError); ok {
		http.Error(w, ce.msg, ce.status)
		return true
	}
	return false
}

// handleSystemCustomStart starts/scales a dedicated custom runtime with persisted defaults.
// POST /system/custom/start  body: {"agent_id":"...","replicas":1,"enable_vlm":true,"enable_face":false}
func handleSystemCustomStart(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !relay.processManager.IsAvailable() {
		http.Error(w, processManagerUnavailableMessage(), http.StatusServiceUnavailable)
		return
	}

	var req struct {
		AgentID    string `json:"agent_id"`
		Replicas   int    `json:"replicas"`
		EnableVLM  bool   `json:"enable_vlm"`
		EnableFace bool   `json:"enable_face"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.AgentID = strings.TrimSpace(req.AgentID)
	if req.AgentID == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}
	if req.Replicas < 1 || req.Replicas > 16 {
		http.Error(w, "replicas must be between 1 and 16", http.StatusBadRequest)
		return
	}

	agent, err := getOwnedCustomAgent(relay, userID, req.AgentID)
	if writeCustomErr(w, err) {
		return
	}
	if err != nil {
		http.Error(w, "Failed to load agent", http.StatusInternalServerError)
		return
	}

	modelPath, ok := resolveCustomModelPath(relay.config.AppDir, agent.Config.CustomYOLO.ModelRelPath)
	if !ok {
		http.Error(w, "invalid custom model path", http.StatusBadRequest)
		return
	}

	pm := relay.processManager
	if !isEngineReady(pm) {
		if err := pm.ensureEngineReady(); err != nil {
			status := http.StatusInternalServerError
			if strings.Contains(err.Error(), "PROCESS_AUTO_START_ENGINE is disabled") {
				status = http.StatusConflict
			}
			http.Error(w, "Failed to start engine: "+err.Error(), status)
			return
		}
	}

	autoStarted := []string{}
	if req.EnableVLM && pm.DesiredScale("vlm") == 0 {
		if err := pm.ScaleWorkers("vlm", 1); err != nil {
			log.Printf("[SystemAPI] Auto-start dependency vlm failed: %v", err)
		} else {
			autoStarted = append(autoStarted, "vlm")
		}
	}
	if req.EnableFace && pm.DesiredScale("face") == 0 {
		if err := pm.ScaleWorkers("face", 1); err != nil {
			log.Printf("[SystemAPI] Auto-start dependency face failed: %v", err)
		} else {
			autoStarted = append(autoStarted, "face")
		}
	}

	opts := &CustomRuntimeOptions{
		ModelPath:  modelPath,
		EnableVLM:  req.EnableVLM,
		EnableFace: req.EnableFace,
	}
	if err := pm.ScaleCustomAgentRuntime(req.AgentID, req.Replicas, opts); err != nil {
		http.Error(w, "Failed to start custom runtime: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := waitForWorkerPoolReady(relay, "vyra/custom_detection/"+req.AgentID, req.Replicas, workerPoolReadyTimeout); err != nil {
		http.Error(w, "Custom runtime did not become ready: "+err.Error(), http.StatusGatewayTimeout)
		return
	}

	if err := relay.agentTable.UpdateCustomYOLORuntime(req.AgentID, req.EnableVLM, req.EnableFace, req.Replicas); err != nil {
		http.Error(w, "Failed to persist custom runtime defaults: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if updated, err := relay.agentTable.GetAgentByID(req.AgentID); err == nil {
		relay.agentRegistry.RegisterAgent(&AgentInfo{
			ID:          updated.ID,
			Name:        updated.Name,
			WorkerID:    updated.WorkerID,
			Instruction: updated.Config.Instruction,
			ServiceIDs:  updated.ServiceIDs,
			UserID:      updated.UserID,
			CustomYOLO:  updated.Config.CustomYOLO,
		})
		relay.syncFrameExtractorsForServices(updated.ServiceIDs...)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"agent_id":     req.AgentID,
		"replicas":     req.Replicas,
		"enable_vlm":   req.EnableVLM,
		"enable_face":  req.EnableFace,
		"auto_started": autoStarted,
	})
}

// handleSystemCustomScale scales a dedicated custom runtime.
// POST /system/custom/scale body: {"agent_id":"...","replicas":2}
func handleSystemCustomScale(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !relay.processManager.IsAvailable() {
		http.Error(w, processManagerUnavailableMessage(), http.StatusServiceUnavailable)
		return
	}

	var req struct {
		AgentID  string `json:"agent_id"`
		Replicas int    `json:"replicas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.AgentID = strings.TrimSpace(req.AgentID)
	if req.AgentID == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}
	if req.Replicas < 0 || req.Replicas > 16 {
		http.Error(w, "replicas must be between 0 and 16", http.StatusBadRequest)
		return
	}

	agent, err := getOwnedCustomAgent(relay, userID, req.AgentID)
	if writeCustomErr(w, err) {
		return
	}
	if err != nil {
		http.Error(w, "Failed to load agent", http.StatusInternalServerError)
		return
	}

	pm := relay.processManager
	if req.Replicas > 0 && !isEngineReady(pm) {
		if err := pm.ensureEngineReady(); err != nil {
			status := http.StatusInternalServerError
			if strings.Contains(err.Error(), "PROCESS_AUTO_START_ENGINE is disabled") {
				status = http.StatusConflict
			}
			http.Error(w, "Failed to start engine: "+err.Error(), status)
			return
		}
	}

	if _, ok := pm.CustomRuntimeOptions(req.AgentID); !ok && req.Replicas > 0 {
		modelPath, ok := resolveCustomModelPath(relay.config.AppDir, agent.Config.CustomYOLO.ModelRelPath)
		if !ok {
			http.Error(w, "invalid custom model path", http.StatusBadRequest)
			return
		}
		opts := &CustomRuntimeOptions{
			ModelPath:  modelPath,
			EnableVLM:  agent.Config.CustomYOLO.RuntimeDefaultsEnableVLM,
			EnableFace: agent.Config.CustomYOLO.RuntimeDefaultsEnableFace,
		}
		if err := pm.ScaleCustomAgentRuntime(req.AgentID, req.Replicas, opts); err != nil {
			http.Error(w, "Failed to scale custom runtime: "+err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		if err := pm.ScaleCustomAgentReplicas(req.AgentID, req.Replicas); err != nil {
			http.Error(w, "Failed to scale custom runtime: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.Replicas > 0 {
		if err := waitForWorkerPoolReady(relay, "vyra/custom_detection/"+req.AgentID, req.Replicas, workerPoolReadyTimeout); err != nil {
			http.Error(w, "Custom runtime did not become ready: "+err.Error(), http.StatusGatewayTimeout)
			return
		}
	}

	if err := relay.agentTable.UpdateCustomYOLORuntime(
		req.AgentID,
		agent.Config.CustomYOLO.RuntimeDefaultsEnableVLM,
		agent.Config.CustomYOLO.RuntimeDefaultsEnableFace,
		req.Replicas,
	); err != nil {
		http.Error(w, "Failed to persist custom runtime scale: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if updated, err := relay.agentTable.GetAgentByID(req.AgentID); err == nil {
		relay.agentRegistry.RegisterAgent(&AgentInfo{
			ID:          updated.ID,
			Name:        updated.Name,
			WorkerID:    updated.WorkerID,
			Instruction: updated.Config.Instruction,
			ServiceIDs:  updated.ServiceIDs,
			UserID:      updated.UserID,
			CustomYOLO:  updated.Config.CustomYOLO,
		})
		relay.syncFrameExtractorsForServices(updated.ServiceIDs...)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"agent_id": req.AgentID,
		"replicas": req.Replicas,
	})
}

// handleSystemCustomStop stops a custom runtime (desired replicas -> 0).
// POST /system/custom/stop body: {"agent_id":"..."}
func handleSystemCustomStop(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !relay.processManager.IsAvailable() {
		http.Error(w, processManagerUnavailableMessage(), http.StatusServiceUnavailable)
		return
	}

	var req struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.AgentID = strings.TrimSpace(req.AgentID)
	if req.AgentID == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}

	agent, err := getOwnedCustomAgent(relay, userID, req.AgentID)
	if writeCustomErr(w, err) {
		return
	}
	if err != nil {
		http.Error(w, "Failed to load agent", http.StatusInternalServerError)
		return
	}

	if err := relay.processManager.StopCustomAgentRuntime(req.AgentID); err != nil {
		http.Error(w, "Failed to stop custom runtime: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := relay.agentTable.UpdateCustomYOLORuntime(
		req.AgentID,
		agent.Config.CustomYOLO.RuntimeDefaultsEnableVLM,
		agent.Config.CustomYOLO.RuntimeDefaultsEnableFace,
		0,
	); err != nil {
		http.Error(w, "Failed to persist custom runtime scale: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if updated, err := relay.agentTable.GetAgentByID(req.AgentID); err == nil {
		relay.agentRegistry.RegisterAgent(&AgentInfo{
			ID:          updated.ID,
			Name:        updated.Name,
			WorkerID:    updated.WorkerID,
			Instruction: updated.Config.Instruction,
			ServiceIDs:  updated.ServiceIDs,
			UserID:      updated.UserID,
			CustomYOLO:  updated.Config.CustomYOLO,
		})
		relay.syncFrameExtractorsForServices(updated.ServiceIDs...)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"agent_id": req.AgentID,
		"replicas": 0,
	})
}

// handleSystemCustomWorkers lists per-custom-agent runtime inventory + aggregate telemetry.
// GET /system/custom/workers
func handleSystemCustomWorkers(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	agents, err := relay.agentTable.GetAgentsByUser(userID)
	if err != nil {
		http.Error(w, "Failed to list agents", http.StatusInternalServerError)
		return
	}
	pmAvailable := relay.processManager.IsAvailable()
	inv := map[string]CustomWorkerPoolInfo{}
	stats := map[string]cv.WorkerRuntimeStats{}
	if relay.cvWorkerRegistry != nil {
		stats = relay.cvWorkerRegistry.GetWorkerRuntimeStats()
	}
	if pmAvailable {
		inv = relay.processManager.CustomWorkerInventory()
	}

	out := make([]customRuntimeAgentStatus, 0)
	agg := customRuntimeAggregateStatus{}

	for _, a := range agents {
		if a.Config.CustomYOLO == nil || !a.Config.CustomYOLO.Enabled {
			continue
		}
		entry := inv[a.ID]
		workerID := "vyra/custom_detection/" + a.ID
		s := stats[workerID]
		desired := entry.Desired
		if desired == 0 && a.Config.CustomYOLO.RuntimeDesiredReplicas > 0 {
			desired = a.Config.CustomYOLO.RuntimeDesiredReplicas
		}
		enableVLM := entry.EnableVLM
		enableFace := entry.EnableFace
		if len(entry.Workers) == 0 && len(entry.Adapters) == 0 {
			enableVLM = a.Config.CustomYOLO.RuntimeDefaultsEnableVLM
			enableFace = a.Config.CustomYOLO.RuntimeDefaultsEnableFace
		}

		item := customRuntimeAgentStatus{
			AgentID:       a.ID,
			AgentName:     a.Name,
			WorkerID:      workerID,
			ModelFilename: a.Config.CustomYOLO.ModelFilename,
			Desired:       desired,
			Workers:       entry.Workers,
			Adapters:      entry.Adapters,
			EnableVLM:     enableVLM,
			EnableFace:    enableFace,
			Connected:     s.ConnectedWorkers,
			TotalDropped:  s.TotalDropped,
			QueueFillPct:  s.MaxQueueFillPct,
		}
		out = append(out, item)

		agg.TotalDesired += item.Desired
		agg.RunningWorkers += runningReplicaCount(item.Workers)
		agg.RunningAdapters += runningReplicaCount(item.Adapters)
		agg.ConnectedWorkers += item.Connected
		agg.TotalDropped += item.TotalDropped
		if item.QueueFillPct > agg.MaxQueuePressure {
			agg.MaxQueuePressure = item.QueueFillPct
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"agents":                    out,
		"aggregate":                 agg,
		"process_manager_available": pmAvailable,
	})
}
