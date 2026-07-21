package relay

import "testing"

func TestShouldBroadcastEventFiltersBboxOnly(t *testing.T) {
	meta := map[string]interface{}{
		"agent_type": "ppe",
		"bboxes":     []interface{}{map[string]interface{}{"label": "person"}},
	}
	if shouldBroadcastEvent(meta) {
		t.Fatalf("expected bbox-only PPE event to be filtered from persisted broadcast")
	}
	if !shouldBroadcastOverlayEvent(meta) {
		t.Fatalf("expected bbox-only PPE event to be sent as overlay")
	}
}

func TestShouldBroadcastEventForMeaningfulAlert(t *testing.T) {
	meta := map[string]interface{}{
		"agent_type":      "person",
		"violation_count": float64(1),
	}
	if !shouldBroadcastEvent(meta) {
		t.Fatalf("expected meaningful person event to be broadcast")
	}
}

func TestShouldBroadcastEventForHasViolationsFlag(t *testing.T) {
	meta := map[string]interface{}{
		"agent_type":      "ppe",
		"has_violations":  true,
		"violation_count": 0,
	}
	if !shouldBroadcastEvent(meta) {
		t.Fatalf("expected has_violations=true event to be broadcast")
	}
}

func TestShouldBroadcastEventNumericCoercionInt(t *testing.T) {
	meta := map[string]interface{}{
		"agent_type":      "person",
		"violation_count": 1,
	}
	if !shouldBroadcastEvent(meta) {
		t.Fatalf("expected int violation_count to be treated as meaningful")
	}
}

func TestShouldBroadcastEventForCustomBBox(t *testing.T) {
	meta := map[string]interface{}{
		"agent_type": "custom",
		"bboxes":     []interface{}{map[string]interface{}{"label": "vehicle"}},
	}
	if !shouldBroadcastEvent(meta) {
		t.Fatalf("expected custom detections to be broadcast")
	}
}

func TestWorkerIDToTypePersonAlias(t *testing.T) {
	if got := workerIDToType("vyra/person_detection"); got != "objdet" {
		t.Fatalf("expected objdet for person_detection, got %q", got)
	}
}

func TestWorkerIDToTypeFaceAlias(t *testing.T) {
	if got := workerIDToType("vyra/face_recognition"); got != "face" {
		t.Fatalf("expected face for face_recognition, got %q", got)
	}
}

func TestStartAgentRuntimeTypesIncludeRealtimeDependenciesFirst(t *testing.T) {
	t.Setenv("PROCESS_AUTO_START_VLM_DEPENDENCIES", "")

	got := startAgentRuntimeTypes("objdet")
	want := []string{"face", "objdet"}
	if len(got) != len(want) {
		t.Fatalf("runtime count=%d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("runtime[%d]=%q, want %q; all=%#v", i, got[i], want[i], got)
		}
	}
}

func TestStartAgentRuntimeTypesIncludeVLMWhenEnabled(t *testing.T) {
	t.Setenv("PROCESS_AUTO_START_VLM_DEPENDENCIES", "true")

	got := startAgentRuntimeTypes("objdet")
	want := []string{"vlm", "face", "objdet"}
	if len(got) != len(want) {
		t.Fatalf("runtime count=%d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("runtime[%d]=%q, want %q; all=%#v", i, got[i], want[i], got)
		}
	}
}

func TestStartAgentRuntimeTypesDoNotDuplicateSelf(t *testing.T) {
	got := startAgentRuntimeTypes("face")
	if len(got) != 1 || got[0] != "face" {
		t.Fatalf("expected only face runtime, got %#v", got)
	}
}

func TestAgentRuntimeTypesForRestoreUsesAssignedStandardAgents(t *testing.T) {
	t.Setenv("PROCESS_AUTO_START_VLM_DEPENDENCIES", "")

	agents := []*Agent{
		{
			WorkerID:   "vyra/person_detection",
			ServiceIDs: []string{"cam-1"},
		},
		{
			WorkerID:   "vyra/ppe_detection",
			ServiceIDs: nil,
		},
		{
			WorkerID:   "vyra/vlm",
			ServiceIDs: []string{},
		},
	}

	got := agentRuntimeTypesForRestore(agents)
	want := []string{"face", "objdet"}
	if len(got) != len(want) {
		t.Fatalf("runtime count=%d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("runtime[%d]=%q, want %q; all=%#v", i, got[i], want[i], got)
		}
	}
}

func TestAgentRuntimeTypesForRestoreSkipsCustomYOLORuntimes(t *testing.T) {
	got := agentRuntimeTypesForRestore([]*Agent{
		{
			WorkerID:   "vyra/custom_detection/agent-1",
			ServiceIDs: []string{"cam-1"},
			Config: AgentConfig{CustomYOLO: &CustomYOLOConfig{
				Enabled: true,
			}},
		},
	})
	if len(got) != 0 {
		t.Fatalf("expected no shared runtime restore for custom YOLO, got %#v", got)
	}
}

func TestIsInternalFaceLookupAgent(t *testing.T) {
	if !isInternalFaceLookupAgent("face_lookup_cam-7", nil) {
		t.Fatalf("expected synthetic face lookup agent to be treated as internal")
	}
	if !isInternalFaceLookupAgent("any-agent", map[string]interface{}{"internal_only": true}) {
		t.Fatalf("expected internal_only metadata to force internal event handling")
	}
	if isInternalFaceLookupAgent("real-agent-id", nil) {
		t.Fatalf("did not expect real agent ids to be marked internal")
	}
}
