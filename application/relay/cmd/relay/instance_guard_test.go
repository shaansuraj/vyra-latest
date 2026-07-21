package main

import (
	"strings"
	"testing"
)

func TestAcquireRelayInstanceGuardWritesMetadata(t *testing.T) {
	appDir := t.TempDir()
	guard, err := acquireRelayInstanceGuard(appDir, []string{"relay", "--test"}, "test-launcher")
	if err != nil {
		t.Fatalf("acquire guard: %v", err)
	}
	defer func() { _ = guard.Release() }()

	meta, err := readRelayPIDMetadata(guard.pidPath)
	if err != nil {
		t.Fatalf("read pid metadata: %v", err)
	}
	if meta.PID <= 0 {
		t.Fatalf("expected pid > 0, got %d", meta.PID)
	}
	if meta.LauncherTag != "test-launcher" {
		t.Fatalf("expected launcher tag test-launcher, got %q", meta.LauncherTag)
	}
	if !strings.Contains(meta.Command, "--test") {
		t.Fatalf("expected command metadata to include args, got %q", meta.Command)
	}
}

func TestAcquireRelayInstanceGuardRejectsSecondInstance(t *testing.T) {
	appDir := t.TempDir()
	first, err := acquireRelayInstanceGuard(appDir, []string{"relay"}, "first")
	if err != nil {
		t.Fatalf("acquire first guard: %v", err)
	}
	defer func() { _ = first.Release() }()

	second, err := acquireRelayInstanceGuard(appDir, []string{"relay"}, "second")
	if err == nil {
		_ = second.Release()
		t.Fatalf("expected second guard acquisition to fail")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected already running error, got %v", err)
	}
}
