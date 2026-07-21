package relay

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPreferredPythonBinaryUsesVenv(t *testing.T) {
	t.Setenv("VYRA_PYTHON_BIN", "")
	tmp := t.TempDir()
	venvPy := filepath.Join(tmp, ".venv", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(venvPy), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(venvPy, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write venv python: %v", err)
	}

	got := preferredPythonBinary(tmp)
	if got != venvPy {
		t.Fatalf("expected venv python %q, got %q", venvPy, got)
	}
}

func TestPreferredPythonBinaryUsesConfiguredBinary(t *testing.T) {
	tmp := t.TempDir()
	customPy := filepath.Join(tmp, "custom-python")
	if err := os.WriteFile(customPy, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write custom python: %v", err)
	}
	t.Setenv("VYRA_PYTHON_BIN", customPy)

	got := preferredPythonBinary(tmp)
	if got != customPy {
		t.Fatalf("expected configured python %q, got %q", customPy, got)
	}
}

func TestWaitForHTTPReadyTimesOut(t *testing.T) {
	// Use a local closed port to verify timeout/error behavior.
	err := waitForHTTPReady("http://127.0.0.1:1", 500*time.Millisecond)
	if err == nil {
		t.Fatal("expected waitForHTTPReady to fail on unreachable endpoint")
	}
}

func TestWaitForPortListeningTimesOut(t *testing.T) {
	err := waitForPortListening("1", 500*time.Millisecond)
	if err == nil {
		t.Fatal("expected waitForPortListening to fail on unreachable port")
	}
}

func TestStartRespectsProcessBackoff(t *testing.T) {
	pm := NewProcessManager("", "", "9020", "8020", "5000", "secret")
	pm.onProcessExit("proc-a", os.ErrInvalid)

	err := pm.Start("proc-a", "echo", []string{"hello"}, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected start to fail during backoff")
	}
	if !strings.Contains(err.Error(), "backoff active") {
		t.Fatalf("expected backoff error, got: %v", err)
	}
}

func TestResetProcessFailureClearsBackoffState(t *testing.T) {
	pm := NewProcessManager("", "", "9020", "8020", "5000", "secret")
	pm.onProcessExit("proc-a", os.ErrInvalid)

	pm.mu.Lock()
	if pm.procFailCount["proc-a"] == 0 {
		pm.mu.Unlock()
		t.Fatal("expected fail count to be populated")
	}
	pm.mu.Unlock()

	pm.resetProcessFailure("proc-a")

	pm.mu.Lock()
	defer pm.mu.Unlock()
	if got := pm.procFailCount["proc-a"]; got != 0 {
		t.Fatalf("expected fail count reset to 0, got %d", got)
	}
	if got := pm.procNextRetry["proc-a"]; !got.IsZero() {
		t.Fatalf("expected next retry to be zero, got %v", got)
	}
}

func TestEnsureEngineReadyRequiresConfiguredRoot(t *testing.T) {
	pm := NewProcessManager("", "", "9020", "8020", "5000", "secret")
	if err := pm.ensureEngineReady(); err == nil {
		t.Fatal("expected ensureEngineReady to fail without VYRA_ROOT")
	}
}

func TestEnsureEngineReadyAcceptsExternalEngineListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("listener creation unavailable in this environment: %v", err)
	}
	defer ln.Close()
	port := strings.TrimPrefix(ln.Addr().String(), "127.0.0.1:")

	root := t.TempDir()
	pm := NewProcessManager(root, root, "9020", "8020", port, "secret")
	if err := pm.ensureEngineReady(); err != nil {
		t.Fatalf("ensureEngineReady should accept external listener: %v", err)
	}
	if pm.IsRunning("engine") {
		t.Fatalf("did not expect managed engine process to be started")
	}
}

func TestEnsureEngineReadyWaitsForManagedEnginePort(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("listener creation unavailable in this environment: %v", err)
	}
	port := strings.TrimPrefix(probe.Addr().String(), "127.0.0.1:")
	if err := probe.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}

	cmd := exec.Command("sleep", "2")
	if err := cmd.Start(); err != nil {
		t.Skipf("sleep command unavailable in this environment: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	root := t.TempDir()
	pm := NewProcessManager(root, root, "9020", "8020", port, "secret")
	pm.procs["engine"] = &managedProc{
		cmd:       cmd,
		name:      "engine",
		startedAt: time.Now(),
	}

	oldTimeout := engineReadyTimeout
	engineReadyTimeout = 2 * time.Second
	defer func() { engineReadyTimeout = oldTimeout }()

	lnReady := make(chan net.Listener, 1)
	go func() {
		time.Sleep(150 * time.Millisecond)
		ln, listenErr := net.Listen("tcp", "127.0.0.1:"+port)
		if listenErr == nil {
			lnReady <- ln
		}
	}()

	if err := pm.ensureEngineReady(); err != nil {
		t.Fatalf("ensureEngineReady should wait for managed engine port: %v", err)
	}

	select {
	case ln := <-lnReady:
		_ = ln.Close()
	default:
	}
}
