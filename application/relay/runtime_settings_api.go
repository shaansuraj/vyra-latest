package relay

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type runtimeSettingsResponse struct {
	PPERequirePersonAssociation bool    `json:"ppe_require_person_association"`
	SnapshotCooldownSeconds     float64 `json:"snapshot_cooldown_seconds"`
	PersonReIDMatchThreshold    float64 `json:"person_reid_match_threshold"`
	LLMModelID                  string  `json:"llm_model_id"`
}

func parseDotEnv(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		out[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return out
}

func upsertDotEnv(path string, values map[string]string) error {
	lines := []string{}
	if data, err := os.ReadFile(path); err == nil {
		lines = strings.Split(string(data), "\n")
	}
	used := map[string]bool{}
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		parts := strings.SplitN(trim, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k := strings.TrimSpace(parts[0])
		if v, ok := values[k]; ok {
			lines[i] = k + "=" + v
			used[k] = true
		}
	}
	for k, v := range values {
		if used[k] {
			continue
		}
		lines = append(lines, k+"="+v)
	}
	if len(lines) == 0 {
		lines = append(lines, "")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
}

func parseBool(s string, fallback bool) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return fallback
	}
	return s == "1" || s == "true" || s == "yes" || s == "on"
}

func parseFloat(s string, fallback float64) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}
	return fallback
}

func buildRuntimeSettings(relay *Relay) runtimeSettingsResponse {
	engineEnv := map[string]string{}
	relayEnv := map[string]string{}
	if strings.TrimSpace(relay.config.RootDir) != "" {
		engineEnv = parseDotEnv(filepath.Join(relay.config.RootDir, "engine", ".env"))
		relayEnv = parseDotEnv(filepath.Join(relay.config.RootDir, "application", "relay", ".env"))
	}

	ppeRequirePersonAssociation := parseBool(os.Getenv("PPE_REQUIRE_PERSON_ASSOCIATION"), true)
	if v := engineEnv["PPE_REQUIRE_PERSON_ASSOCIATION"]; strings.TrimSpace(v) != "" {
		ppeRequirePersonAssociation = parseBool(v, ppeRequirePersonAssociation)
	}

	snapshotCooldownSeconds := parseFloat(os.Getenv("SNAPSHOT_COOLDOWN_SECONDS"), 30.0)
	if v := engineEnv["SNAPSHOT_COOLDOWN_SECONDS"]; strings.TrimSpace(v) != "" {
		snapshotCooldownSeconds = parseFloat(v, snapshotCooldownSeconds)
	}

	personReIDThreshold := parseFloat(os.Getenv("PERSON_REID_MATCH_THRESHOLD"), 0.72)
	if v := relayEnv["PERSON_REID_MATCH_THRESHOLD"]; strings.TrimSpace(v) != "" {
		personReIDThreshold = parseFloat(v, personReIDThreshold)
	}
	if relay.personIdentityService != nil {
		personReIDThreshold = relay.personIdentityService.UnknownMatchThreshold()
	}

	modelID := strings.TrimSpace(relay.processManager.LLMModelID())
	if modelID == "" {
		modelID = strings.TrimSpace(os.Getenv("LLM_MODEL_ID"))
	}
	if modelID == "" {
		if v := strings.TrimSpace(engineEnv["LLM_MODEL_ID"]); v != "" {
			modelID = v
		}
	}
	if modelID == "" {
		modelID = "google/gemma-3-1b-it"
	}

	return runtimeSettingsResponse{
		PPERequirePersonAssociation: ppeRequirePersonAssociation,
		SnapshotCooldownSeconds:     snapshotCooldownSeconds,
		PersonReIDMatchThreshold:    personReIDThreshold,
		LLMModelID:                  modelID,
	}
}

// handleSystemRuntimeSettings manages runtime tunables used by workers and relay services.
// GET /system/runtime-settings
// POST /system/runtime-settings
func handleSystemRuntimeSettings(w http.ResponseWriter, r *http.Request, relay *Relay) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildRuntimeSettings(relay))
		return
	case http.MethodPost:
		if !relay.processManager.IsAvailable() {
			http.Error(w, "Runtime settings update requires VYRA_ROOT process-manager mode. In Docker mode, update environment variables and redeploy.", http.StatusConflict)
			return
		}

		var req struct {
			PPERequirePersonAssociation *bool    `json:"ppe_require_person_association"`
			SnapshotCooldownSeconds     *float64 `json:"snapshot_cooldown_seconds"`
			PersonReIDMatchThreshold    *float64 `json:"person_reid_match_threshold"`
			LLMModelID                  *string  `json:"llm_model_id"`
			RestartLLMIfRunning         *bool    `json:"restart_llm_if_running"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}

		engineEnvPath := filepath.Join(relay.config.RootDir, "engine", ".env")
		relayEnvPath := filepath.Join(relay.config.RootDir, "application", "relay", ".env")
		engineUpdates := map[string]string{}
		relayUpdates := map[string]string{}

		if req.PPERequirePersonAssociation != nil {
			if *req.PPERequirePersonAssociation {
				engineUpdates["PPE_REQUIRE_PERSON_ASSOCIATION"] = "1"
			} else {
				engineUpdates["PPE_REQUIRE_PERSON_ASSOCIATION"] = "0"
			}
		}
		if req.SnapshotCooldownSeconds != nil {
			if *req.SnapshotCooldownSeconds < 0 {
				http.Error(w, "snapshot_cooldown_seconds must be >= 0", http.StatusBadRequest)
				return
			}
			engineUpdates["SNAPSHOT_COOLDOWN_SECONDS"] = strconv.FormatFloat(*req.SnapshotCooldownSeconds, 'f', 3, 64)
		}
		if req.PersonReIDMatchThreshold != nil {
			if *req.PersonReIDMatchThreshold <= 0 || *req.PersonReIDMatchThreshold > 1 {
				http.Error(w, "person_reid_match_threshold must be within (0,1]", http.StatusBadRequest)
				return
			}
			if relay.personIdentityService != nil {
				relay.personIdentityService.SetUnknownMatchThreshold(*req.PersonReIDMatchThreshold)
			}
			relayUpdates["PERSON_REID_MATCH_THRESHOLD"] = strconv.FormatFloat(*req.PersonReIDMatchThreshold, 'f', 3, 64)
		}
		if req.LLMModelID != nil {
			trimmed := strings.TrimSpace(*req.LLMModelID)
			if trimmed == "" {
				http.Error(w, "llm_model_id cannot be empty", http.StatusBadRequest)
				return
			}
			engineUpdates["LLM_MODEL_ID"] = trimmed
			relayUpdates["LLM_MODEL_ID"] = trimmed
			relay.processManager.SetLLMModelID(trimmed)
			restart := true
			if req.RestartLLMIfRunning != nil {
				restart = *req.RestartLLMIfRunning
			}
			if restart && relay.processManager.IsRunning("llm_query") {
				_ = relay.processManager.StopLLMWorker()
				_ = relay.processManager.StartLLMWorker(trimmed)
			}
		}

		if len(engineUpdates) > 0 {
			if err := upsertDotEnv(engineEnvPath, engineUpdates); err != nil {
				http.Error(w, fmt.Sprintf("failed to persist engine settings: %v", err), http.StatusInternalServerError)
				return
			}
		}
		if len(relayUpdates) > 0 {
			if err := upsertDotEnv(relayEnvPath, relayUpdates); err != nil {
				http.Error(w, fmt.Sprintf("failed to persist relay settings: %v", err), http.StatusInternalServerError)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildRuntimeSettings(relay))
		return
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
}
