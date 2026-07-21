package relay

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func startSleepProcess(t *testing.T, pm *ProcessManager, name string) {
	t.Helper()
	if err := pm.Start(name, "bash", []string{"-lc", "sleep 60"}, t.TempDir(), nil); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
}

func TestStopAllManagedStopsProcessesAndClearsDesiredState(t *testing.T) {
	root := t.TempDir()
	pm := NewProcessManager(root, root, "9020", "8020", "5000", "secret")

	for _, name := range []string{
		"engine",
		"worker-ppe",
		"adapter-ppe",
		"node-node1",
		"llm_query",
		"query_service",
		"worker-custom-agent1-1",
		"adapter-custom-agent1-1",
	} {
		startSleepProcess(t, pm, name)
	}

	pm.mu.Lock()
	pm.desiredScale["ppe"] = 2
	pm.desiredScale["objdet"] = 1
	pm.customRuntime["agent1"] = &customRuntimeState{
		Desired: 2,
		Options: CustomRuntimeOptions{ModelPath: "/tmp/model.pt"},
	}
	pm.llmDesired = true
	pm.mu.Unlock()

	if err := pm.StopAllManaged(); err != nil {
		t.Fatalf("StopAllManaged failed: %v", err)
	}

	for _, name := range []string{
		"engine",
		"worker-ppe",
		"adapter-ppe",
		"node-node1",
		"llm_query",
		"query_service",
		"worker-custom-agent1-1",
		"adapter-custom-agent1-1",
	} {
		if pm.IsRunning(name) {
			t.Fatalf("expected %s to be stopped", name)
		}
	}

	for _, wtype := range []string{"ppe", "objdet", "vlm", "face"} {
		if got := pm.DesiredScale(wtype); got != 0 {
			t.Fatalf("expected desired scale for %s to be 0, got %d", wtype, got)
		}
	}
	if got := pm.CustomDesiredScale("agent1"); got != 0 {
		t.Fatalf("expected custom desired scale for agent1 to be 0, got %d", got)
	}
	if pm.LLMDesired() {
		t.Fatalf("expected llm desired state to be false")
	}
}

func TestRelayShutdownStopsManagedChildren(t *testing.T) {
	root := t.TempDir()
	pm := NewProcessManager(root, root, "9020", "8020", "5000", "secret")
	startSleepProcess(t, pm, "engine")

	r := &Relay{
		nodes:          map[string]*NodeConn{},
		shutdown:       make(chan struct{}),
		processManager: pm,
	}

	r.Shutdown()

	if pm.IsRunning("engine") {
		t.Fatalf("expected managed engine process to stop during relay shutdown")
	}
}

func TestStopKillsManagedProcessGroupChildren(t *testing.T) {
	root := t.TempDir()
	pm := NewProcessManager(root, root, "9020", "8020", "5000", "secret")
	workDir := t.TempDir()
	childPIDPath := workDir + "/child.pid"

	if err := pm.Start(
		"worker-ppe",
		"bash",
		[]string{"-lc", "sleep 60 & echo $! > child.pid; wait"},
		workDir,
		nil,
	); err != nil {
		t.Fatalf("start worker-ppe: %v", err)
	}

	var childPID int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(childPIDPath)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(b)))
			if parseErr == nil && pid > 0 {
				childPID = pid
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if childPID <= 0 {
		t.Fatalf("failed to capture child pid")
	}

	if err := pm.Stop("worker-ppe"); err != nil {
		t.Fatalf("stop worker-ppe: %v", err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(childPID) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("expected child pid %d to stop with managed process group", childPID)
}

func TestStaleManagedProcessTargetsOnlyPreviousVYRAProcesses(t *testing.T) {
	root := "/opt/vyra"
	appDir := "/srv/vyra-data"
	currentPID := 50
	entries := []processTableEntry{
		{PID: currentPID, PPID: 1, PGID: currentPID, Cmdline: "/opt/vyra/application/relay/relay", Cwd: "/opt/vyra/application/relay"},
		{PID: 100, PPID: 1, PGID: 100, Cmdline: "/opt/vyra/engine/py/.venv/bin/python -m worker_vlm", Cwd: "/opt/vyra/engine/py"},
		{PID: 101, PPID: 100, PGID: 100, Cmdline: "bun run src/relay_worker_adapter.ts", Cwd: "/opt/vyra/engine"},
		{PID: 200, PPID: currentPID, PGID: 200, Cmdline: "/opt/vyra/engine/py/.venv/bin/python -m worker_face_recognition", Cwd: "/opt/vyra/engine/py"},
		{PID: 300, PPID: 1, PGID: 300, Cmdline: "/usr/bin/python -m worker_vlm", Cwd: "/tmp"},
		{PID: 400, PPID: 1, PGID: 400, Cmdline: "go run ./cmd/unblink/ --config /srv/vyra-data/nodes/node1/config.json", Cwd: "/opt/vyra/application/node"},
		{PID: 500, PPID: 1, PGID: 500, Cmdline: "/opt/vyra/engine/py/.venv/bin/python worker_query_llm.py", Cwd: "/opt/vyra/engine/py"},
	}

	targets := staleManagedProcessTargets(entries, root, appDir, currentPID)
	gotGroups := map[int]bool{}
	for _, target := range targets {
		gotGroups[target.PGID] = true
	}

	if len(gotGroups) != 3 || !gotGroups[100] || !gotGroups[400] || !gotGroups[500] {
		t.Fatalf("expected stale groups 100, 400, and 500, got targets=%+v", targets)
	}
	if gotGroups[200] {
		t.Fatalf("did not expect current relay descendant to be targeted: %+v", targets)
	}
	if gotGroups[300] {
		t.Fatalf("did not expect unrelated worker-like process to be targeted: %+v", targets)
	}
}

func TestAgentDependencyTypesSkipsVLMByDefault(t *testing.T) {
	t.Setenv("PROCESS_AUTO_START_VLM_DEPENDENCIES", "")

	got := agentDependencyTypes("objdet")
	if len(got) != 1 || got[0] != "face" {
		t.Fatalf("expected only face dependency by default, got %#v", got)
	}
}

func TestAgentDependencyTypesIncludesVLMWhenEnabled(t *testing.T) {
	t.Setenv("PROCESS_AUTO_START_VLM_DEPENDENCIES", "true")

	got := agentDependencyTypes("objdet")
	if len(got) != 2 || got[0] != "vlm" || got[1] != "face" {
		t.Fatalf("expected vlm and face dependencies when enabled, got %#v", got)
	}
}
