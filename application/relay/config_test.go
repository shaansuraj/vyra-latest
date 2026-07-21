package relay

import (
	"testing"
	"time"
)

func setMinimalConfigEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_DIR", t.TempDir())
	t.Setenv("FRAME_INTERVAL_SECONDS", "1")
	t.Setenv("RELAY_PORT", "9020")
	t.Setenv("API_PORT", "8020")
	t.Setenv("DASHBOARD_URL", "http://localhost:3000")
	t.Setenv("JWT_SECRET", "test-secret")
}

func TestLoadConfigProcessPolicyDefaults(t *testing.T) {
	setMinimalConfigEnv(t)
	t.Setenv("PROCESS_SELF_HEAL_ENABLED", "")
	t.Setenv("PROCESS_AUTO_START_DEPENDENCIES", "")
	t.Setenv("PROCESS_AUTO_START_ENGINE", "")
	t.Setenv("SIGNUP_ENABLED", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.ProcessSelfHealEnabled {
		t.Fatalf("expected PROCESS_SELF_HEAL_ENABLED default false")
	}
	if cfg.ProcessAutoStartDependencies {
		t.Fatalf("expected PROCESS_AUTO_START_DEPENDENCIES default false")
	}
	if !cfg.ProcessAutoStartEngine {
		t.Fatalf("expected PROCESS_AUTO_START_ENGINE default true")
	}
	if cfg.SignupEnabled {
		t.Fatalf("expected SIGNUP_ENABLED default false")
	}
	if cfg.ProcessAutoScaleEnabled {
		t.Fatalf("expected PROCESS_AUTO_SCALE_ENABLED default false")
	}
	if cfg.ProcessAutoScaleMax != 6 {
		t.Fatalf("expected PROCESS_AUTO_SCALE_MAX default 6, got %d", cfg.ProcessAutoScaleMax)
	}
	if cfg.ProcessAutoScaleInterval != 3*time.Second {
		t.Fatalf("expected PROCESS_AUTO_SCALE_INTERVAL default 3s, got %v", cfg.ProcessAutoScaleInterval)
	}
}

func TestLoadConfigProcessPolicyOverrides(t *testing.T) {
	setMinimalConfigEnv(t)
	t.Setenv("PROCESS_SELF_HEAL_ENABLED", "true")
	t.Setenv("PROCESS_AUTO_START_DEPENDENCIES", "1")
	t.Setenv("PROCESS_AUTO_START_ENGINE", "false")
	t.Setenv("PROCESS_AUTO_SCALE_ENABLED", "true")
	t.Setenv("PROCESS_AUTO_SCALE_MAX", "8")
	t.Setenv("PROCESS_AUTO_SCALE_INTERVAL_SECONDS", "2")
	t.Setenv("SIGNUP_ENABLED", "true")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if !cfg.ProcessSelfHealEnabled {
		t.Fatalf("expected PROCESS_SELF_HEAL_ENABLED true")
	}
	if !cfg.ProcessAutoStartDependencies {
		t.Fatalf("expected PROCESS_AUTO_START_DEPENDENCIES true")
	}
	if cfg.ProcessAutoStartEngine {
		t.Fatalf("expected PROCESS_AUTO_START_ENGINE false")
	}
	if !cfg.SignupEnabled {
		t.Fatalf("expected SIGNUP_ENABLED true")
	}
	if !cfg.ProcessAutoScaleEnabled {
		t.Fatalf("expected PROCESS_AUTO_SCALE_ENABLED true")
	}
	if cfg.ProcessAutoScaleMax != 8 {
		t.Fatalf("expected PROCESS_AUTO_SCALE_MAX 8, got %d", cfg.ProcessAutoScaleMax)
	}
	if cfg.ProcessAutoScaleInterval != 2*time.Second {
		t.Fatalf("expected PROCESS_AUTO_SCALE_INTERVAL 2s, got %v", cfg.ProcessAutoScaleInterval)
	}
}
