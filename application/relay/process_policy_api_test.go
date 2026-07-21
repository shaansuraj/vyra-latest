package relay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHandleSystemProcessPolicyGet(t *testing.T) {
	root := t.TempDir()
	pm := NewProcessManager(root, root, "9020", "8020", "5000", "secret")
	pm.SetProcessPolicy(true, false, true)

	relay := &Relay{
		config: &Config{
			RootDir:                      root,
			ProcessSelfHealEnabled:       true,
			ProcessAutoStartDependencies: false,
			ProcessAutoStartEngine:       true,
		},
		processManager: pm,
	}

	req := httptest.NewRequest(http.MethodGet, "/system/process-policy", nil)
	rec := httptest.NewRecorder()
	handleSystemProcessPolicy(rec, req, relay)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got ProcessPolicy
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.SelfHealEnabled || got.AutoStartDependencies || !got.AutoStartEngine {
		t.Fatalf("unexpected policy: %+v", got)
	}
}

func TestHandleSystemProcessPolicyPostPersistsAndApplies(t *testing.T) {
	root := t.TempDir()
	relayEnvDir := filepath.Join(root, "application", "relay")
	if err := os.MkdirAll(relayEnvDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	relayEnvPath := filepath.Join(relayEnvDir, ".env")
	if err := os.WriteFile(relayEnvPath, []byte("APP_DIR=/tmp/vyra\n"), 0644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	pm := NewProcessManager(root, root, "9020", "8020", "5000", "secret")
	pm.SetProcessPolicy(false, false, true)

	relay := &Relay{
		config: &Config{
			RootDir:                      root,
			ProcessSelfHealEnabled:       false,
			ProcessAutoStartDependencies: false,
			ProcessAutoStartEngine:       true,
		},
		processManager: pm,
	}

	body := []byte(`{"self_heal_enabled":true,"auto_start_dependencies":true,"auto_start_engine":false}`)
	req := httptest.NewRequest(http.MethodPost, "/system/process-policy", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handleSystemProcessPolicy(rec, req, relay)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	if !relay.config.ProcessSelfHealEnabled || !relay.config.ProcessAutoStartDependencies || relay.config.ProcessAutoStartEngine {
		t.Fatalf("relay config not updated: %+v", relay.config)
	}

	policy := pm.ProcessPolicy()
	if !policy.SelfHealEnabled || !policy.AutoStartDependencies || policy.AutoStartEngine {
		t.Fatalf("process manager policy not updated: %+v", policy)
	}

	envMap := parseDotEnv(relayEnvPath)
	if envMap["PROCESS_SELF_HEAL_ENABLED"] != "true" {
		t.Fatalf("expected PROCESS_SELF_HEAL_ENABLED=true, got %q", envMap["PROCESS_SELF_HEAL_ENABLED"])
	}
	if envMap["PROCESS_AUTO_START_DEPENDENCIES"] != "true" {
		t.Fatalf("expected PROCESS_AUTO_START_DEPENDENCIES=true, got %q", envMap["PROCESS_AUTO_START_DEPENDENCIES"])
	}
	if envMap["PROCESS_AUTO_START_ENGINE"] != "false" {
		t.Fatalf("expected PROCESS_AUTO_START_ENGINE=false, got %q", envMap["PROCESS_AUTO_START_ENGINE"])
	}
}

func TestHandleSystemAutoscalePostPersistsAndApplies(t *testing.T) {
	root := t.TempDir()
	relayEnvDir := filepath.Join(root, "application", "relay")
	if err := os.MkdirAll(relayEnvDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	relayEnvPath := filepath.Join(relayEnvDir, ".env")
	if err := os.WriteFile(relayEnvPath, []byte("APP_DIR=/tmp/vyra\n"), 0644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	pm := NewProcessManager(root, root, "9020", "8020", "5000", "secret")
	pm.SetAutoScale(false)
	pm.SetAutoScaleMax(6)

	relay := &Relay{
		config: &Config{
			RootDir:                 root,
			ProcessAutoScaleEnabled: false,
			ProcessAutoScaleMax:     6,
			ProcessSelfHealEnabled:  true,
		},
		processManager: pm,
	}

	body := []byte(`{"enabled":true,"max_replicas":9}`)
	req := httptest.NewRequest(http.MethodPost, "/system/autoscale", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handleSystemAutoscale(rec, req, relay)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	if !relay.config.ProcessAutoScaleEnabled || relay.config.ProcessAutoScaleMax != 9 {
		t.Fatalf("relay config not updated: %+v", relay.config)
	}
	if !pm.AutoScaleEnabled() || pm.AutoScaleMax() != 9 {
		t.Fatalf("process manager autoscale not updated")
	}

	envMap := parseDotEnv(relayEnvPath)
	if envMap["PROCESS_AUTO_SCALE_ENABLED"] != "true" {
		t.Fatalf("expected PROCESS_AUTO_SCALE_ENABLED=true, got %q", envMap["PROCESS_AUTO_SCALE_ENABLED"])
	}
	if envMap["PROCESS_AUTO_SCALE_MAX"] != "9" {
		t.Fatalf("expected PROCESS_AUTO_SCALE_MAX=9, got %q", envMap["PROCESS_AUTO_SCALE_MAX"])
	}
}
