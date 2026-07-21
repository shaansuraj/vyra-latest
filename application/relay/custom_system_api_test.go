package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/unblink/unblink/relay/cv"
)

func TestResolveCustomModelPathRelative(t *testing.T) {
	got, ok := resolveCustomModelPath("/tmp/app", "storage/models/custom-yolo/user-1/a.pt")
	if !ok {
		t.Fatalf("expected path to resolve")
	}
	if got != "/tmp/app/storage/models/custom-yolo/user-1/a.pt" {
		t.Fatalf("unexpected resolved path: %s", got)
	}
}

func TestResolveCustomModelPathRejectTraversal(t *testing.T) {
	if _, ok := resolveCustomModelPath("/tmp/app", "../outside.pt"); ok {
		t.Fatalf("expected traversal path to be rejected")
	}
}

func TestHandleSystemCustomWorkersWithoutProcessManager(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("new db: %v", err)
	}
	defer func() { _ = db.Close() }()

	authStore := NewAuthStore(db.DB)
	user, err := authStore.EnsureDevUser()
	if err != nil {
		t.Fatalf("ensure user: %v", err)
	}

	agentTable := NewAgentTable(db.DB)
	_, err = agentTable.CreateCustomYOLOAgent(
		"custom-agent-1",
		"Custom Agent",
		"vyra/custom_detection/custom-agent-1",
		[]string{},
		user.ID,
		&CustomYOLOConfig{
			Enabled:                   true,
			ModelRelPath:              "storage/models/custom-yolo/user-1/a.pt",
			ModelFilename:             "a.pt",
			RuntimeDefaultsEnableVLM:  true,
			RuntimeDefaultsEnableFace: false,
			RuntimeDesiredReplicas:    2,
		},
		"Detect custom objects",
	)
	if err != nil {
		t.Fatalf("create custom agent: %v", err)
	}

	relay := &Relay{
		agentTable:       agentTable,
		processManager:   NewProcessManager("", "", "9020", "8020", "5000", "secret"),
		cvWorkerRegistry: cv.NewCVWorkerRegistry(cv.NewCVEventBus(), nil),
	}

	req := httptest.NewRequest(http.MethodGet, "/system/custom/workers", nil)
	rec := httptest.NewRecorder()
	handleSystemCustomWorkers(rec, req, relay, user.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		ProcessManagerAvailable bool `json:"process_manager_available"`
		Agents                  []struct {
			AgentID string `json:"agent_id"`
			Desired int    `json:"desired"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ProcessManagerAvailable {
		t.Fatalf("expected process_manager_available=false")
	}
	if len(resp.Agents) != 1 {
		t.Fatalf("expected one custom agent entry, got %d", len(resp.Agents))
	}
	if resp.Agents[0].Desired != 2 {
		t.Fatalf("expected desired replicas from persisted default (2), got %d", resp.Agents[0].Desired)
	}
}
