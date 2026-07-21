package relay

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const customYOLOMaxUploadBytes int64 = 512 * 1024 * 1024 // 512 MB

func defaultCustomYOLOInstruction(name string) string {
	if strings.TrimSpace(name) == "" {
		return "Run custom YOLO object detection and emit bounding boxes."
	}
	return fmt.Sprintf("Run custom YOLO object detection for %s and emit bounding boxes.", strings.TrimSpace(name))
}

// handleCreateCustomYOLOAgent handles:
// POST /agents/custom-yolo (multipart/form-data)
// fields: name, instruction(optional), service_ids(JSON array string), model(.pt file)
func handleCreateCustomYOLOAgent(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "Invalid multipart form: "+err.Error(), http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	instruction := strings.TrimSpace(r.FormValue("instruction"))
	serviceIDsRaw := strings.TrimSpace(r.FormValue("service_ids"))

	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if len(name) > 255 {
		http.Error(w, "Name too long (max 255 characters)", http.StatusBadRequest)
		return
	}
	if instruction == "" {
		instruction = defaultCustomYOLOInstruction(name)
	}
	if len(instruction) > 5000 {
		http.Error(w, "Instruction too long (max 5000 characters)", http.StatusBadRequest)
		return
	}

	serviceIDs := []string{}
	if serviceIDsRaw != "" {
		if err := json.Unmarshal([]byte(serviceIDsRaw), &serviceIDs); err != nil {
			http.Error(w, "service_ids must be a JSON array of strings", http.StatusBadRequest)
			return
		}
	}

	modelFile, header, err := r.FormFile("model")
	if err != nil {
		http.Error(w, "model file is required", http.StatusBadRequest)
		return
	}
	defer modelFile.Close()

	origName := strings.TrimSpace(header.Filename)
	if origName == "" {
		http.Error(w, "model filename is required", http.StatusBadRequest)
		return
	}
	if strings.ToLower(filepath.Ext(origName)) != ".pt" {
		http.Error(w, "only .pt model files are supported", http.StatusBadRequest)
		return
	}
	if header.Size > customYOLOMaxUploadBytes {
		http.Error(w, "model exceeds max upload size (512 MB)", http.StatusBadRequest)
		return
	}

	agentID := uuid.New().String()
	workerID := "vyra/custom_detection/" + agentID
	modelFileName := uuid.New().String() + ".pt"
	relDir := filepath.Join("storage", "models", "custom-yolo", fmt.Sprintf("user-%d", userID), fmt.Sprintf("agent-%s", agentID))
	absDir := filepath.Join(relay.config.AppDir, relDir)
	if err := os.MkdirAll(absDir, 0755); err != nil {
		http.Error(w, "failed to create model directory", http.StatusInternalServerError)
		return
	}
	absPath := filepath.Join(absDir, modelFileName)
	relPath := filepath.Join(relDir, modelFileName)

	dst, err := os.Create(absPath)
	if err != nil {
		http.Error(w, "failed to create model file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	written, err := io.Copy(dst, io.LimitReader(modelFile, customYOLOMaxUploadBytes+1))
	if err != nil {
		_ = os.Remove(absPath)
		http.Error(w, "failed to save model file", http.StatusInternalServerError)
		return
	}
	if written > customYOLOMaxUploadBytes {
		_ = os.Remove(absPath)
		http.Error(w, "model exceeds max upload size (512 MB)", http.StatusBadRequest)
		return
	}

	customCfg := &CustomYOLOConfig{
		Enabled:                   true,
		ModelRelPath:              filepath.ToSlash(relPath),
		ModelFilename:             origName,
		ModelSizeBytes:            written,
		RuntimeDefaultsEnableVLM:  false,
		RuntimeDefaultsEnableFace: false,
		RuntimeDesiredReplicas:    0,
	}

	agent, err := relay.agentTable.CreateCustomYOLOAgent(agentID, name, workerID, serviceIDs, userID, customCfg, instruction)
	if err != nil {
		_ = os.Remove(absPath)
		http.Error(w, "Failed to create custom agent", http.StatusInternalServerError)
		return
	}

	relay.agentRegistry.RegisterAgent(&AgentInfo{
		ID:          agent.ID,
		Name:        agent.Name,
		WorkerID:    agent.WorkerID,
		Instruction: agent.Config.Instruction,
		ServiceIDs:  agent.ServiceIDs,
		UserID:      userID,
		CustomYOLO:  agent.Config.CustomYOLO,
	})
	relay.syncFrameExtractorsForServices(agent.ServiceIDs...)

	log.Printf("[CustomYOLO] Created agent %s (user=%d model=%s size=%d)", agent.ID, userID, relPath, written)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"id":      agent.ID,
		"agent":   toAgentResponse(agent),
	})
}
