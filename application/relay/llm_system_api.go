package relay

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

type llmWorkerStatus struct {
	Running    bool    `json:"running"`
	PID        int     `json:"pid,omitempty"`
	ModelID    string  `json:"model_id"`
	Port       int     `json:"port"`
	Ready      bool    `json:"ready"`
	QueueDepth int     `json:"queue_depth,omitempty"`
	LastError  string  `json:"last_error,omitempty"`
	LatencyMS  float64 `json:"latency_ms,omitempty"`
}

func readLLMWorkerHealth() map[string]interface{} {
	base := strings.TrimSpace(os.Getenv("LLM_WORKER_URL"))
	if base == "" {
		base = "http://127.0.0.1:5003"
	}
	base = strings.TrimRight(base, "/")
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(base + "/health")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil
	}
	return body
}

func llmWorkerBaseURL() string {
	base := strings.TrimSpace(os.Getenv("LLM_WORKER_URL"))
	if base == "" {
		base = "http://127.0.0.1:5003"
	}
	return strings.TrimRight(base, "/")
}

func triggerLLMWarmup() {
	base := llmWorkerBaseURL()
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodPost, base+"/warmup", nil)
	if err != nil {
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
}

func buildLLMStatus(relay *Relay) llmWorkerStatus {
	statuses := relay.processManager.StatusAll()
	st := llmWorkerStatus{
		Running: false,
		ModelID: relay.processManager.LLMModelID(),
		Port:    5003,
	}
	if proc, ok := statuses["llm_query"]; ok {
		st.Running = proc.Running
		st.PID = proc.PID
	}
	if health := readLLMWorkerHealth(); health != nil {
		// External worker mode: treat healthy endpoint as running even when
		// there is no local llm_query process managed by ProcessManager.
		st.Running = true
		if ready, ok := health["ready"].(bool); ok {
			st.Ready = ready
		}
		if q, ok := health["queue_depth"].(float64); ok {
			st.QueueDepth = int(q)
		}
		if m, ok := health["model_id"].(string); ok && strings.TrimSpace(m) != "" {
			st.ModelID = strings.TrimSpace(m)
		}
		if e, ok := health["last_error"].(string); ok {
			st.LastError = e
		}
		if lat, ok := health["latency_ms"].(float64); ok {
			st.LatencyMS = lat
		}
	}
	return st
}

// handleSystemLLMStart starts (or restarts) the local Ask-AI LLM worker.
// POST /system/llm/start  body: {"model_id":"google/gemma-3-1b-it"}
func handleSystemLLMStart(w http.ResponseWriter, r *http.Request, relay *Relay) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !relay.processManager.IsAvailable() {
		http.Error(w, "VYRA_ROOT not configured", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		ModelID string `json:"model_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	modelID := strings.TrimSpace(req.ModelID)

	running := relay.processManager.IsRunning("llm_query")
	if !running {
		if health := readLLMWorkerHealth(); health != nil {
			// External worker mode (e.g. docker profile llm) — relay should not
			// force-spawn a local process when a healthy worker is already present.
			if modelID != "" {
				relay.processManager.SetLLMModelID(modelID)
			}
			go triggerLLMWarmup()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"started": true,
				"status":  buildLLMStatus(relay),
			})
			return
		}
	}
	if running && modelID != "" && modelID != relay.processManager.LLMModelID() {
		_ = relay.processManager.StopLLMWorker()
		running = false
	}

	if !running {
		if err := relay.processManager.StartLLMWorker(modelID); err != nil {
			http.Error(w, "Failed to start llm worker: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	go triggerLLMWarmup()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"started": true,
		"status":  buildLLMStatus(relay),
	})
}

// handleSystemLLMStop stops the local Ask-AI LLM worker.
// POST /system/llm/stop
func handleSystemLLMStop(w http.ResponseWriter, r *http.Request, relay *Relay) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !relay.processManager.IsAvailable() {
		http.Error(w, "VYRA_ROOT not configured", http.StatusServiceUnavailable)
		return
	}

	if relay.processManager.IsRunning("llm_query") {
		if err := relay.processManager.StopLLMWorker(); err != nil {
			http.Error(w, "Failed to stop llm worker: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"stopped": true,
		"status":  buildLLMStatus(relay),
	})
}

// handleSystemLLMStatus returns current Ask-AI worker status.
// GET /system/llm/status
func handleSystemLLMStatus(w http.ResponseWriter, r *http.Request, relay *Relay) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(buildLLMStatus(relay))
}
