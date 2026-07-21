package relay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestMigrateLegacyFaceAgentsMergesIntoPersonDetection(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("new db: %v", err)
	}
	defer func() { _ = db.Close() }()

	agentTable := NewAgentTable(db.DB)
	personAgent, err := agentTable.CreateAgent(
		"Person Detection",
		"old instruction",
		"vyra/object_detection",
		[]string{"cam-person"},
		1,
	)
	if err != nil {
		t.Fatalf("create person agent: %v", err)
	}
	if _, err := agentTable.CreateAgent(
		"Face Recognition",
		"legacy face pipeline",
		"vyra/face_recognition",
		[]string{"cam-face-1"},
		1,
	); err != nil {
		t.Fatalf("create legacy face agent: %v", err)
	}
	if _, err := agentTable.CreateAgent(
		"Face Recognition",
		"legacy face pipeline",
		"vyra/face",
		[]string{"cam-face-2"},
		1,
	); err != nil {
		t.Fatalf("create second legacy face agent: %v", err)
	}

	if err := agentTable.MigrateLegacyFaceAgents(); err != nil {
		t.Fatalf("migrate legacy face agents: %v", err)
	}

	agents, err := agentTable.GetAgentsByUser(1)
	if err != nil {
		t.Fatalf("get agents: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected only canonical agent to remain, got %d agents", len(agents))
	}
	got := agents[0]
	if got.ID != personAgent.ID {
		t.Fatalf("expected person detection agent %s to remain, got %s", personAgent.ID, got.ID)
	}
	if got.WorkerID != "vyra/person_detection" {
		t.Fatalf("expected worker_id to be canonicalized, got %q", got.WorkerID)
	}
	if got.Name != "Person Detection" {
		t.Fatalf("expected canonical name, got %q", got.Name)
	}
	if got.Config.Instruction != predefinedAgentTemplates[1].Instruction {
		t.Fatalf("expected canonical instruction, got %q", got.Config.Instruction)
	}

	wantServices := map[string]struct{}{
		"cam-person": {},
		"cam-face-1": {},
		"cam-face-2": {},
	}
	if len(got.ServiceIDs) != len(wantServices) {
		t.Fatalf("expected %d merged services, got %v", len(wantServices), got.ServiceIDs)
	}
	for _, sid := range got.ServiceIDs {
		if _, ok := wantServices[sid]; !ok {
			t.Fatalf("unexpected merged service id %q", sid)
		}
		delete(wantServices, sid)
	}
	if len(wantServices) != 0 {
		t.Fatalf("missing merged services: %+v", wantServices)
	}
}

func TestHandleCreateAgentRejectsLegacyFaceWorkerID(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("new db: %v", err)
	}
	defer func() { _ = db.Close() }()

	relay := &Relay{
		agentTable:    NewAgentTable(db.DB),
		agentRegistry: NewAgentRegistry(),
	}

	body, _ := json.Marshal(map[string]any{
		"name":        "Face Recognition",
		"instruction": "legacy face agent",
		"worker_id":   "vyra/face_recognition",
		"service_ids": []string{"cam-1"},
	})
	req := httptest.NewRequest(http.MethodPost, "/agents", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handleCreateAgent(rec, req, relay, 1)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	agents, err := relay.agentTable.GetAgentsByUser(1)
	if err != nil {
		t.Fatalf("get agents: %v", err)
	}
	if len(agents) != 0 {
		t.Fatalf("expected no agents to be created, got %d", len(agents))
	}
}
