package relay

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// isPortListening returns true when a TCP listener is already bound to the given port on localhost.
func isPortListening(port string) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func waitForPortListening(port string, timeout time.Duration) error {
	port = strings.TrimSpace(port)
	if port == "" {
		return fmt.Errorf("port is required")
	}
	deadline := time.Now().Add(timeout)
	for {
		if isPortListening(port) {
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("port %s did not start listening within %s", port, timeout.Truncate(time.Second))
}

func isEngineReady(pm *ProcessManager) bool {
	if pm == nil {
		return false
	}
	if strings.TrimSpace(pm.enginePort) != "" {
		return isPortListening(pm.enginePort)
	}
	return pm.IsRunning("engine")
}

var workerPoolReadyTimeout = 20 * time.Second

func waitForWorkerPoolReady(relay *Relay, workerID string, minConnected int, timeout time.Duration) error {
	if relay == nil || relay.cvWorkerRegistry == nil || minConnected < 1 {
		return nil
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return nil
	}

	deadline := time.Now().Add(timeout)
	lastConnected := 0
	for {
		stats := relay.cvWorkerRegistry.GetWorkerRuntimeStats()
		if s, ok := stats[workerID]; ok {
			lastConnected = s.ConnectedWorkers
			if lastConnected >= minConnected {
				return nil
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("worker pool %s reached %d/%d connected adapters within %s", workerID, lastConnected, minConnected, timeout.Truncate(time.Second))
}

func processManagerUnavailableMessage() string {
	return "Process manager unavailable (VYRA_ROOT not configured). In Docker mode, start relay/engine/workers via docker compose profiles and run managed nodes outside the relay container."
}

// ── /system/* handlers ────────────────────────────────────────────────────────

// handleSystemStatus returns the status of all managed processes.
// GET /system/status
func handleSystemStatus(w http.ResponseWriter, r *http.Request, relay *Relay) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !relay.processManager.IsAvailable() {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"error": "VYRA_ROOT not configured"})
		return
	}
	statuses := relay.processManager.StatusAll()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(statuses)
}

// handleSystemStart starts a named component.
// POST /system/start/{name}
func handleSystemStart(w http.ResponseWriter, r *http.Request, relay *Relay, name string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !relay.processManager.IsAvailable() {
		http.Error(w, processManagerUnavailableMessage(), http.StatusServiceUnavailable)
		return
	}

	var err error
	switch name {
	case "engine":
		err = relay.processManager.StartEngine()
	case "llm", "llm_query":
		err = relay.processManager.StartLLMWorker("")
	case "worker-fast-embedding", "fast_embedding":
		err = relay.processManager.EnsureRAGWorkers()
	case "worker-rag-reranker", "rag_reranker":
		err = relay.processManager.EnsureRAGWorkers()
	case "worker-ppe", "worker-vlm", "worker-objdet", "worker-face":
		wtype := strings.TrimPrefix(name, "worker-")
		err = relay.processManager.StartWorker(wtype)
	case "adapter-ppe", "adapter-vlm", "adapter-objdet", "adapter-face":
		wtype := strings.TrimPrefix(name, "adapter-")
		err = relay.processManager.StartAdapter(wtype)
	default:
		http.Error(w, "Unknown component: "+name, http.StatusBadRequest)
		return
	}

	if err != nil {
		log.Printf("[SystemAPI] Start %q failed: %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"started": name})
}

// handleSystemWorkers returns worker/adapter replica inventory.
// GET /system/workers
func handleSystemWorkers(w http.ResponseWriter, r *http.Request, relay *Relay) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	inv := relay.processManager.WorkerInventory()
	stats := relay.cvWorkerRegistry.GetWorkerRuntimeStats()
	persistStats := relay.cvWorkerRegistry.GetPersistRuntimeStats()
	snapshotStats := map[string]uint64{"success": 0, "failure": 0}
	if relay.metrics != nil {
		snapshotStats = relay.metrics.SnapshotStats()
	}
	identityStats := map[string]interface{}{"resolve_count": uint64(0), "avg_latency_ms": 0.0}
	if relay.personIdentityService != nil {
		identityStats = relay.personIdentityService.RuntimeStats()
	}

	runningCount := func(list []WorkerReplicaInfo) int {
		n := 0
		for _, it := range list {
			if it.Running {
				n++
			}
		}
		return n
	}

	applyStats := func(p WorkerPoolInfo, workerIDs ...string) WorkerPoolInfo {
		for _, workerID := range workerIDs {
			s, ok := stats[workerID]
			if !ok {
				continue
			}
			if s.ConnectedWorkers > p.ConnectedWorkers {
				p.ConnectedWorkers = s.ConnectedWorkers
			}
			p.TotalDropped += s.TotalDropped
			if s.MaxQueueFillPct > p.QueueFillPct {
				p.QueueFillPct = s.MaxQueueFillPct
			}
		}
		return p
	}

	reasonFor := func(pool WorkerPoolInfo, workerID string) string {
		wRunning := runningCount(pool.Workers)
		aRunning := runningCount(pool.Adapters)
		if pool.Desired > 0 && (wRunning < pool.Desired || aRunning < pool.Desired) {
			return "Some desired replicas are not running (workers: " + strconv.Itoa(wRunning) + "/" + strconv.Itoa(pool.Desired) + ", adapters: " + strconv.Itoa(aRunning) + "/" + strconv.Itoa(pool.Desired) + "). Start the missing worker/adapter processes."
		}

		s, ok := stats[workerID]
		if !ok {
			return ""
		}
		if s.MaxQueueFillPct >= 80 || s.TotalDropped > 0 {
			if relay.processManager.AutoScaleEnabled() {
				return "Autoscaling is enabled and monitoring this bottleneck."
			}
			return "Bottleneck detected (queue pressure/drops). Increase replicas or enable autoscaling."
		}
		return ""
	}

	if p, ok := inv["ppe"]; ok {
		p = applyStats(p, "vyra/ppe_detection")
		p.Reason = reasonFor(p, "vyra/ppe_detection")
		inv["ppe"] = p
	}
	if p, ok := inv["objdet"]; ok {
		p = applyStats(p, "vyra/person_detection", "vyra/object_detection")
		// check both IDs for compatibility
		reason := reasonFor(p, "vyra/person_detection")
		if reason == "" {
			reason = reasonFor(p, "vyra/object_detection")
		}
		p.Reason = reason
		inv["objdet"] = p
	}
	if p, ok := inv["vlm"]; ok {
		p = applyStats(p, "vyra/vlm")
		p.Reason = reasonFor(p, "vyra/vlm")
		inv["vlm"] = p
	}
	if p, ok := inv["face"]; ok {
		p = applyStats(p, "vyra/face_recognition")
		p.Reason = reasonFor(p, "vyra/face_recognition")
		inv["face"] = p
	}

	w.Header().Set("Content-Type", "application/json")
	llmStatus := buildLLMStatus(relay)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"workers":      inv,
		"autoscaling":  relay.processManager.AutoScaleEnabled(),
		"autoscaleMax": relay.processManager.AutoScaleMax(),
		"llm":          llmStatus,
		"metrics": map[string]interface{}{
			"event_persist": map[string]uint64{
				"success": persistStats.Success,
				"failure": persistStats.Failure,
				"dropped": persistStats.Dropped,
			},
			"snapshot_upload":  snapshotStats,
			"identity_resolve": identityStats,
		},
	})
}

// handleSystemScale reconciles worker+adapter replica count for one type.
// POST /system/scale body: {"type":"ppe","replicas":2}
func handleSystemScale(w http.ResponseWriter, r *http.Request, relay *Relay) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !relay.processManager.IsAvailable() {
		http.Error(w, processManagerUnavailableMessage(), http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Type     string `json:"type"`
		Replicas int    `json:"replicas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	if req.Type == "" {
		http.Error(w, "type is required", http.StatusBadRequest)
		return
	}
	// Accept numeric replicas encoded as string via query fallback.
	if req.Replicas == 0 {
		if q := r.URL.Query().Get("replicas"); q != "" {
			if n, err := strconv.Atoi(q); err == nil {
				req.Replicas = n
			}
		}
	}
	if err := relay.processManager.ScaleWorkers(req.Type, req.Replicas); err != nil {
		http.Error(w, "Failed to scale: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Auto-start dependency worker pools when scaling up from 0.
	autoStarted := []string{}
	if req.Replicas > 0 && relay.processManager.AutoStartDependenciesEnabled() {
		for _, dep := range agentDependencyTypes(req.Type) {
			if relay.processManager.DesiredScale(dep) == 0 {
				if err := relay.processManager.ScaleWorkers(dep, 1); err != nil {
					log.Printf("[SystemAPI] Auto-start dependency %s failed: %v", dep, err)
				} else {
					autoStarted = append(autoStarted, dep)
					log.Printf("[SystemAPI] Auto-started dependency pool: %s (1 replica)", dep)
				}
			}
		}
	}
	if req.Replicas > 0 {
		if workerID := workerTypeToRuntimeID(req.Type); workerID != "" {
			if err := waitForWorkerPoolReady(relay, workerID, req.Replicas, workerPoolReadyTimeout); err != nil {
				http.Error(w, "Worker pool did not become ready: "+err.Error(), http.StatusGatewayTimeout)
				return
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"type":         req.Type,
		"replicas":     req.Replicas,
		"auto_started": autoStarted,
	})
}

// handleSystemAutoscale updates autoscaling mode.
// POST /system/autoscale body: {"enabled":true,"max_replicas":6}
func handleSystemAutoscale(w http.ResponseWriter, r *http.Request, relay *Relay) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Enabled     bool `json:"enabled"`
		MaxReplicas int  `json:"max_replicas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	relay.processManager.SetAutoScale(req.Enabled)
	if req.MaxReplicas > 0 {
		relay.processManager.SetAutoScaleMax(req.MaxReplicas)
	}
	relay.config.ProcessAutoScaleEnabled = relay.processManager.AutoScaleEnabled()
	relay.config.ProcessAutoScaleMax = relay.processManager.AutoScaleMax()
	if err := upsertDotEnv(processPolicyEnvPath(relay.config), map[string]string{
		"PROCESS_AUTO_SCALE_ENABLED": strconv.FormatBool(relay.config.ProcessAutoScaleEnabled),
		"PROCESS_AUTO_SCALE_MAX":     strconv.Itoa(relay.config.ProcessAutoScaleMax),
	}); err != nil {
		http.Error(w, fmt.Sprintf("failed to persist autoscale settings: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"enabled":            relay.processManager.AutoScaleEnabled(),
		"max_replicas":       relay.processManager.AutoScaleMax(),
		"self_heal_enabled":  relay.config.ProcessSelfHealEnabled,
		"effective_enabled":  relay.processManager.AutoScaleEnabled(),
		"policy_requirement": "autoscale reacts to queue pressure and drops; self-heal only controls restart/reconcile behavior",
	})
}

func systemResourcesHealthy() bool {
	// CPU load check via loadavg (Linux). Fallback to true when unavailable.
	okCPU := true
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		parts := strings.Fields(string(b))
		if len(parts) > 0 {
			if load1, err := strconv.ParseFloat(parts[0], 64); err == nil {
				cpus := float64(runtime.NumCPU())
				okCPU = load1 <= cpus*0.85
			}
		}
	}

	// Memory availability check via /proc/meminfo. Fallback to true.
	okMem := true
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, available float64
		lines := strings.Split(string(b), "\n")
		for _, ln := range lines {
			if strings.HasPrefix(ln, "MemTotal:") {
				fs := strings.Fields(ln)
				if len(fs) >= 2 {
					total, _ = strconv.ParseFloat(fs[1], 64)
				}
			}
			if strings.HasPrefix(ln, "MemAvailable:") {
				fs := strings.Fields(ln)
				if len(fs) >= 2 {
					available, _ = strconv.ParseFloat(fs[1], 64)
				}
			}
		}
		if total > 0 {
			okMem = (available / total) >= 0.2
		}
	}

	return okCPU && okMem
}

// handleSystemStop stops a named component.
// POST /system/stop/{name}
func handleSystemStop(w http.ResponseWriter, r *http.Request, relay *Relay, name string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !relay.processManager.IsAvailable() {
		http.Error(w, processManagerUnavailableMessage(), http.StatusServiceUnavailable)
		return
	}
	target := strings.TrimSpace(strings.ToLower(name))
	if target == "llm" {
		target = "llm_query"
	}

	stopped := target
	var err error
	switch {
	case target == "engine":
		err = stopEngineAuthoritative(relay.processManager)
	case target == "llm_query":
		err = relay.processManager.StopLLMWorker()
		if err != nil && strings.Contains(err.Error(), "not found") {
			err = nil
		}
	case target == "worker-fast-embedding", target == "fast_embedding":
		stopped = "worker-fast-embedding"
		err = relay.processManager.Stop("worker-fast-embedding")
		if err != nil && strings.Contains(err.Error(), "not found") {
			err = nil
		}
	case target == "worker-rag-reranker", target == "rag_reranker":
		stopped = "worker-rag-reranker"
		err = relay.processManager.Stop("worker-rag-reranker")
		if err != nil && strings.Contains(err.Error(), "not found") {
			err = nil
		}
	case strings.HasPrefix(target, "worker-"):
		wtype, ok := pooledTypeFromStopName(target, "worker-")
		if !ok {
			http.Error(w, "Unknown component: "+name, http.StatusBadRequest)
			return
		}
		stopped = "worker-" + wtype
		err = relay.processManager.ScaleWorkers(wtype, 0)
	case strings.HasPrefix(target, "adapter-"):
		wtype, ok := pooledTypeFromStopName(target, "adapter-")
		if !ok {
			http.Error(w, "Unknown component: "+name, http.StatusBadRequest)
			return
		}
		stopped = "adapter-" + wtype
		err = relay.processManager.ScaleWorkers(wtype, 0)
	default:
		err = relay.processManager.Stop(target)
	}

	if err != nil {
		log.Printf("[SystemAPI] Stop %q failed: %v", target, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"stopped": stopped})
}

func pooledTypeFromStopName(name, prefix string) (string, bool) {
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(name, prefix)
	if rest == "" {
		return "", false
	}
	parts := strings.SplitN(rest, "-", 2)
	wtype := parts[0]
	switch wtype {
	case "ppe", "vlm", "objdet", "face":
		return wtype, true
	default:
		return "", false
	}
}

func stopEngineAuthoritative(pm *ProcessManager) error {
	for _, wtype := range []string{"ppe", "objdet", "vlm", "face"} {
		if err := pm.ScaleWorkers(wtype, 0); err != nil {
			return err
		}
	}

	for agentID := range pm.CustomWorkerInventory() {
		if err := pm.ScaleCustomAgentReplicas(agentID, 0); err != nil {
			return err
		}
	}

	pm.ClearDesiredRuntimeIntents()
	_ = pm.StopLLMWorker()
	_ = pm.Stop("query_service")
	if err := pm.Stop("engine"); err != nil && !strings.Contains(err.Error(), "not found") {
		return err
	}
	return nil
}

// handleStartAgent starts the engine + matching worker + adapter for a worker_id.
// POST /system/start/agent  body: {"worker_id": "vyra/ppe_detection"}
func handleStartAgent(w http.ResponseWriter, r *http.Request, relay *Relay) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !relay.processManager.IsAvailable() {
		http.Error(w, processManagerUnavailableMessage(), http.StatusServiceUnavailable)
		return
	}

	var req struct {
		WorkerID string `json:"worker_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	wtype := workerIDToType(req.WorkerID)
	if wtype == "" {
		http.Error(w, "Unrecognised worker_id: "+req.WorkerID, http.StatusBadRequest)
		return
	}

	var started []string
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
		started = append(started, "engine")
	}

	for _, runtimeType := range startAgentRuntimeTypes(wtype) {
		workerName := canonicalReplicaName("worker", runtimeType, 1)
		adapterName := canonicalReplicaName("adapter", runtimeType, 1)
		workerWasRunning := pm.IsRunning(workerName)
		adapterWasRunning := pm.IsRunning(adapterName)

		replicas := pm.DesiredScale(runtimeType)
		if replicas < 1 {
			replicas = 1
		}
		if err := pm.ScaleWorkers(runtimeType, replicas); err != nil {
			http.Error(w, "Failed to start "+runtimeType+" runtime: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if !workerWasRunning && pm.IsRunning(workerName) {
			started = append(started, workerName)
		}
		if !adapterWasRunning && pm.IsRunning(adapterName) {
			started = append(started, adapterName)
		}
	}

	readyWorkerID := workerTypeToRuntimeID(wtype)
	if readyWorkerID == "" {
		readyWorkerID = req.WorkerID
	}
	if err := waitForWorkerPoolReady(relay, readyWorkerID, 1, workerPoolReadyTimeout); err != nil {
		http.Error(w, "Agent runtime did not become ready: "+err.Error(), http.StatusGatewayTimeout)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"started": started, "worker_type": wtype})
}

// handleFrameInterval gets or updates the detection FPS target stored in camera settings.
// GET  /system/frame-interval                   → {fps, interval_ms}
// PATCH /system/frame-interval  body: {fps: N}  → applies processing_fps to all cameras
func handleFrameInterval(w http.ResponseWriter, r *http.Request, relay *Relay) {
	switch r.Method {
	case http.MethodGet:
		fps := 0.0
		if relay.config.FrameInterval > 0 {
			fps = 1.0 / relay.config.FrameInterval.Seconds()
		}
		mixed := false
		if relay.cameraTable != nil {
			if cameras, err := relay.cameraTable.GetAllCameras(); err == nil && len(cameras) > 0 {
				base := cameras[0].ProcessingFPS
				if base <= 0 {
					base = defaultProcessingFPS
				}
				fps = base
				for _, cam := range cameras[1:] {
					cur := cam.ProcessingFPS
					if cur <= 0 {
						cur = defaultProcessingFPS
					}
					if cur < base-0.001 || cur > base+0.001 {
						mixed = true
						fps = defaultProcessingFPS
						break
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		intervalMillis := int64(0)
		if fps > 0 {
			intervalMillis = time.Duration(float64(time.Second) / fps).Milliseconds()
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"fps":         fps,
			"interval_ms": intervalMillis,
			"mixed":       mixed,
		})

	case http.MethodPatch, http.MethodPost:
		var req struct {
			FPS float64 `json:"fps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.FPS < defaultProcessingFPS || req.FPS > 60 {
			http.Error(w, fmt.Sprintf("fps must be between %.0f and 60", defaultProcessingFPS), http.StatusBadRequest)
			return
		}
		if relay.cameraTable == nil {
			http.Error(w, "camera settings unavailable", http.StatusServiceUnavailable)
			return
		}
		newInterval := time.Duration(float64(time.Second) / req.FPS)
		cameras, err := relay.cameraTable.GetAllCameras()
		if err != nil {
			http.Error(w, "failed to list cameras: "+err.Error(), http.StatusInternalServerError)
			return
		}

		updatedCameras := 0
		updatedStreams := 0
		restartedStreams := 0
		for _, cam := range cameras {
			prevFPS := cam.ProcessingFPS
			if prevFPS <= 0 {
				prevFPS = defaultProcessingFPS
			}
			updatedCam, err := relay.cameraTable.UpdateCameraFPS(cam.ID, req.FPS, cam.VLMFPS)
			if err != nil {
				log.Printf("[SystemAPI] Failed to update camera %s fps: %v", cam.ID, err)
				continue
			}
			updatedCameras++

			if relay.realtimeStreamManager == nil {
				continue
			}

			// Raising extraction FPS needs a stream restart so FFmpeg restarts at the
			// new configured camera rate. Lowering can be applied in-place.
			if updatedCam.ProcessingFPS > prevFPS+0.001 {
				streamNodeID := ""
				for _, nodeID := range updatedCam.NodeIDs {
					if relay.GetNode(nodeID) != nil {
						streamNodeID = nodeID
						break
					}
				}
				if streamNodeID != "" && relay.config.AutoRequestRealtimeStream && (updatedCam.Type == "rtsp" || updatedCam.Type == "mjpeg") {
					svc := relay.cameraToService(updatedCam)
					relay.realtimeStreamManager.OnServiceRemoved(updatedCam.ID)
					go relay.realtimeStreamManager.OnServiceAnnounced(svc, streamNodeID)
					restartedStreams++
					continue
				}
			}

			if stream, err := relay.realtimeStreamManager.GetStream(updatedCam.ID); err == nil {
				if extractor := stream.GetFrameExtractor(); extractor != nil {
					extractor.SetInterval(newInterval)
					updatedStreams++
				}
			}
		}

		log.Printf("[SystemAPI] Applied detection fps %.2f to %d camera(s); updated %d running extractor(s), restarted %d stream(s)", req.FPS, updatedCameras, updatedStreams, restartedStreams)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"fps":             req.FPS,
			"interval_ms":     newInterval.Milliseconds(),
			"updated_cameras": updatedCameras,
			"updated_streams": updatedStreams,
			"restarted":       restartedStreams,
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// workerIDToType maps a worker_id string to the short type used by ProcessManager.
func workerIDToType(workerID string) string {
	id := strings.ToLower(workerID)
	if strings.Contains(id, "ppe") || strings.Contains(id, "helmet") {
		return "ppe"
	}
	if strings.Contains(id, "vlm") || strings.Contains(id, "wellness") {
		return "vlm"
	}
	if strings.Contains(id, "face") || strings.Contains(id, "recognition") {
		return "face"
	}
	if strings.Contains(id, "person") || strings.Contains(id, "object") || strings.Contains(id, "security") || strings.Contains(id, "objdet") {
		return "objdet"
	}
	return ""
}

func workerTypeToRuntimeID(wtype string) string {
	switch strings.TrimSpace(strings.ToLower(wtype)) {
	case "ppe":
		return "vyra/ppe_detection"
	case "vlm":
		return "vyra/vlm"
	case "face":
		return "vyra/face_recognition"
	case "objdet":
		return "vyra/person_detection"
	default:
		return ""
	}
}

func startAgentRuntimeTypes(wtype string) []string {
	wtype = strings.TrimSpace(strings.ToLower(wtype))
	seen := map[string]bool{}
	out := []string{}
	add := func(runtimeType string) {
		runtimeType = strings.TrimSpace(strings.ToLower(runtimeType))
		if runtimeType == "" || seen[runtimeType] {
			return
		}
		seen[runtimeType] = true
		out = append(out, runtimeType)
	}
	for _, dep := range agentDependencyTypes(wtype) {
		add(dep)
	}
	add(wtype)
	return out
}

// ── /managed-nodes/* handlers ─────────────────────────────────────────────────

// ManagedNodeResponse is the JSON shape returned for a managed node.
type ManagedNodeResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	Running   bool   `json:"running"`
	Online    bool   `json:"online"`
}

// handleListManagedNodes returns all managed nodes for the user.
// GET /managed-nodes
func handleListManagedNodes(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nodes, err := relay.managedNodeTable.GetAllManagedNodes()
	if err != nil {
		http.Error(w, "Failed to list managed nodes", http.StatusInternalServerError)
		return
	}
	resp := make([]ManagedNodeResponse, 0, len(nodes))
	for _, mn := range nodes {
		running := relay.processManager.IsRunning("node-" + mn.ID)
		online := relay.GetNode(mn.ID) != nil
		resp = append(resp, ManagedNodeResponse{
			ID:        mn.ID,
			Name:      mn.Name,
			CreatedAt: mn.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			Running:   running,
			Online:    online,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleCreateManagedNode creates a new managed node record.
// POST /managed-nodes  body: {"name": "..."}
func handleCreateManagedNode(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	mn, err := relay.managedNodeTable.CreateManagedNode(req.Name, userID)
	if err != nil {
		log.Printf("[SystemAPI] CreateManagedNode: %v", err)
		http.Error(w, "Failed to create managed node", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ManagedNodeResponse{
		ID:        mn.ID,
		Name:      mn.Name,
		CreatedAt: mn.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
}

// handleManagedNodeAction routes sub-path actions: /managed-nodes/{id}/...
func handleManagedNodeAction(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64, nodeID, action string) {
	switch action {
	case "name":
		handleRenameManagedNode(w, r, relay, nodeID)
	case "start":
		handleStartManagedNode(w, r, relay, nodeID)
	case "stop":
		handleStopManagedNode(w, r, relay, nodeID)
	case "cameras":
		// Expecting /managed-nodes/{id}/cameras/{cameraId}
		http.Error(w, "Use /managed-nodes/{id}/cameras/{cameraId} directly", http.StatusBadRequest)
	default:
		http.Error(w, "Unknown action: "+action, http.StatusNotFound)
	}
}

// handleRenameManagedNode renames a managed node.
// PATCH /managed-nodes/{id}/name  body: {"name": "..."}
func handleRenameManagedNode(w http.ResponseWriter, r *http.Request, relay *Relay, nodeID string) {
	if r.Method != http.MethodPatch && r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := relay.managedNodeTable.UpdateManagedNodeName(nodeID, req.Name); err != nil {
		http.Error(w, "Failed to rename", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"name": req.Name})
}

// handleDeleteManagedNode deletes a managed node and kills its process.
// DELETE /managed-nodes/{id}
func handleDeleteManagedNode(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64, nodeID string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Kill process if running
	_ = relay.processManager.StopNode(nodeID)

	if err := relay.managedNodeTable.DeleteManagedNode(nodeID); err != nil {
		http.Error(w, "Failed to delete", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// handleStartManagedNode generates a config and starts the node process.
// POST /managed-nodes/{id}/start
func handleStartManagedNode(w http.ResponseWriter, r *http.Request, relay *Relay, nodeID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !relay.processManager.IsAvailable() {
		http.Error(w, processManagerUnavailableMessage(), http.StatusServiceUnavailable)
		return
	}

	// Get managed node record (need name + userID for auth)
	mn, err := relay.managedNodeTable.GetManagedNode(nodeID)
	if err != nil {
		http.Error(w, "Managed node not found", http.StatusNotFound)
		return
	}

	// Ensure the node has a pre-issued token so it connects directly without
	// requiring the user to visit an authorization URL.
	token, err := relay.EnsureManagedNodeAuthorized(nodeID, mn.Name, mn.UserID)
	if err != nil {
		log.Printf("[SystemAPI] EnsureManagedNodeAuthorized %s: %v", nodeID, err)
		http.Error(w, "Failed to authorize node: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Get cameras assigned to this node
	cameras, err := relay.cameraTable.GetCamerasByManagedNode(nodeID)
	if err != nil {
		http.Error(w, "Failed to query cameras", http.StatusInternalServerError)
		return
	}

	// Register cameras in ServiceRegistry so relay can serve them
	for _, cam := range cameras {
		svc := relay.cameraToService(cam)
		relay.services.Register(svc, nodeID)
	}

	if err := relay.processManager.StartNode(nodeID, cameras, token); err != nil {
		log.Printf("[SystemAPI] StartNode %s: %v", nodeID, err)
		http.Error(w, "Failed to start node: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"started": true, "camera_count": len(cameras)})
}

// handleStopManagedNode stops the node process.
// POST /managed-nodes/{id}/stop
func handleStopManagedNode(w http.ResponseWriter, r *http.Request, relay *Relay, nodeID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := relay.processManager.StopNode(nodeID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Force relay-side cleanup immediately so /nodes, /nodes/{id}/services and
	// active Command Center tiles reflect the stopped state without waiting for
	// the node's websocket loop to notice process exit on its own.
	if nc := relay.GetNode(nodeID); nc != nil {
		relay.removeNode(nc)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"stopped": true})
}

// handleAssignCamera assigns a camera to a managed node.
// POST /managed-nodes/{nodeId}/cameras/{cameraId}
func handleAssignCamera(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64, nodeID, cameraID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Existence checks (shared model: no per-user ownership restriction).
	if _, err := relay.managedNodeTable.GetManagedNode(nodeID); err != nil {
		http.Error(w, "Managed node not found", http.StatusNotFound)
		return
	}
	if _, err := relay.cameraTable.GetCameraByID(cameraID); err != nil {
		http.Error(w, "Camera not found", http.StatusNotFound)
		return
	}
	if err := relay.cameraTable.AssignCameraToNode(cameraID, nodeID); err != nil {
		http.Error(w, "Failed to assign camera", http.StatusInternalServerError)
		return
	}
	// If node is running, regenerate config + restart
	if relay.processManager.IsRunning("node-" + nodeID) {
		cameras, _ := relay.cameraTable.GetCamerasByManagedNode(nodeID)
		_ = relay.processManager.StopNode(nodeID)
		time.Sleep(500 * time.Millisecond)
		if mn, err := relay.managedNodeTable.GetManagedNode(nodeID); err == nil {
			if tok, err := relay.EnsureManagedNodeAuthorized(nodeID, mn.Name, mn.UserID); err == nil {
				_ = relay.processManager.StartNode(nodeID, cameras, tok)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// handleUnassignCamera removes a camera from a managed node.
// DELETE /managed-nodes/{nodeId}/cameras/{cameraId}
func handleUnassignCamera(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64, nodeID, cameraID string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, err := relay.managedNodeTable.GetManagedNode(nodeID); err != nil {
		http.Error(w, "Managed node not found", http.StatusNotFound)
		return
	}
	if _, err := relay.cameraTable.GetCameraByID(cameraID); err != nil {
		http.Error(w, "Camera not found", http.StatusNotFound)
		return
	}
	if err := relay.cameraTable.UnassignCameraFromSpecificNode(cameraID, nodeID); err != nil {
		http.Error(w, "Failed to unassign camera", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}
