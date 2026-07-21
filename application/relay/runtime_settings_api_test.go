package relay

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeSettingsApplyPersonReIDThresholdLive(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "engine"), 0755); err != nil {
		t.Fatalf("mkdir engine: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "application", "relay"), 0755); err != nil {
		t.Fatalf("mkdir relay: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "engine", ".env"), []byte("PPE_REQUIRE_PERSON_ASSOCIATION=1\nSNAPSHOT_COOLDOWN_SECONDS=30\n"), 0644); err != nil {
		t.Fatalf("write engine .env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "application", "relay", ".env"), []byte("PERSON_REID_MATCH_THRESHOLD=0.840\n"), 0644); err != nil {
		t.Fatalf("write relay .env: %v", err)
	}

	relay := &Relay{
		config:                &Config{RootDir: root},
		processManager:        NewProcessManager(root, root, "9020", "8020", "5000", "secret"),
		personIdentityService: NewPersonIdentityService(nil, nil),
	}

	body := []byte(`{"person_reid_match_threshold":0.73}`)
	req := httptest.NewRequest(http.MethodPost, "/system/runtime-settings", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handleSystemRuntimeSettings(rec, req, relay)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := relay.personIdentityService.UnknownMatchThreshold(); math.Abs(got-0.73) > 1e-9 {
		t.Fatalf("expected in-memory threshold 0.73, got %.6f", got)
	}

	var resp struct {
		PersonReIDMatchThreshold float64 `json:"person_reid_match_threshold"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if math.Abs(resp.PersonReIDMatchThreshold-0.73) > 1e-9 {
		t.Fatalf("expected response threshold 0.73, got %.6f", resp.PersonReIDMatchThreshold)
	}
}

func TestRuntimeSettingsGetWorksWithoutProcessManager(t *testing.T) {
	relay := &Relay{
		config:         &Config{RootDir: ""},
		processManager: NewProcessManager("", "", "9020", "8020", "5000", "secret"),
	}

	req := httptest.NewRequest(http.MethodGet, "/system/runtime-settings", nil)
	rec := httptest.NewRecorder()
	handleSystemRuntimeSettings(rec, req, relay)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp runtimeSettingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.LLMModelID == "" {
		t.Fatalf("expected llm_model_id to be populated")
	}
}
