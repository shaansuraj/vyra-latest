package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type processPolicyRequest struct {
	SelfHealEnabled       *bool `json:"self_heal_enabled"`
	AutoStartDependencies *bool `json:"auto_start_dependencies"`
	AutoStartEngine       *bool `json:"auto_start_engine"`
}

// handleSystemProcessPolicy returns/updates process lifecycle policy flags.
// GET  /system/process-policy
// POST /system/process-policy
func handleSystemProcessPolicy(w http.ResponseWriter, r *http.Request, relay *Relay) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(currentProcessPolicy(relay))
		return
	case http.MethodPost:
		var req processPolicyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}

		policy := currentProcessPolicy(relay)
		if req.SelfHealEnabled != nil {
			policy.SelfHealEnabled = *req.SelfHealEnabled
		}
		if req.AutoStartDependencies != nil {
			policy.AutoStartDependencies = *req.AutoStartDependencies
		}
		if req.AutoStartEngine != nil {
			policy.AutoStartEngine = *req.AutoStartEngine
		}

		relay.config.ProcessSelfHealEnabled = policy.SelfHealEnabled
		relay.config.ProcessAutoStartDependencies = policy.AutoStartDependencies
		relay.config.ProcessAutoStartEngine = policy.AutoStartEngine
		relay.processManager.SetProcessPolicy(
			policy.SelfHealEnabled,
			policy.AutoStartDependencies,
			policy.AutoStartEngine,
		)

		if err := upsertDotEnv(processPolicyEnvPath(relay.config), map[string]string{
			"PROCESS_SELF_HEAL_ENABLED":       strconv.FormatBool(policy.SelfHealEnabled),
			"PROCESS_AUTO_START_DEPENDENCIES": strconv.FormatBool(policy.AutoStartDependencies),
			"PROCESS_AUTO_START_ENGINE":       strconv.FormatBool(policy.AutoStartEngine),
		}); err != nil {
			http.Error(w, fmt.Sprintf("failed to persist process policy: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(policy)
		return
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

func currentProcessPolicy(relay *Relay) ProcessPolicy {
	if relay.processManager != nil {
		return relay.processManager.ProcessPolicy()
	}
	return ProcessPolicy{
		SelfHealEnabled:       relay.config.ProcessSelfHealEnabled,
		AutoStartDependencies: relay.config.ProcessAutoStartDependencies,
		AutoStartEngine:       relay.config.ProcessAutoStartEngine,
	}
}

func processPolicyEnvPath(cfg *Config) string {
	if root := strings.TrimSpace(cfg.RootDir); root != "" {
		return filepath.Join(root, "application", "relay", ".env")
	}
	cwd, err := os.Getwd()
	if err == nil {
		return filepath.Join(cwd, ".env")
	}
	return ".env"
}
