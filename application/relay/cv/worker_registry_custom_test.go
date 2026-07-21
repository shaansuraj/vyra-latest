package cv

import "testing"

func TestInferAgentTypeCustomDetection(t *testing.T) {
	got := inferAgentType("vyra/custom_detection/123e4567-e89b-12d3-a456-426614174000")
	if got != "custom" {
		t.Fatalf("expected custom, got %q", got)
	}
}

func TestShouldPersistEventCustomBBoxOnlySkipped(t *testing.T) {
	meta := map[string]interface{}{
		"agent_type": "custom",
		"bboxes":     []interface{}{map[string]interface{}{"label": "tank"}},
	}
	if shouldPersistEvent(meta) {
		t.Fatalf("expected custom bbox-only event to be skipped")
	}
}

func TestShouldPersistEventHasViolationsFlag(t *testing.T) {
	meta := map[string]interface{}{
		"agent_type":     "ppe",
		"has_violations": true,
	}
	if !shouldPersistEvent(meta) {
		t.Fatalf("expected has_violations=true event to be persisted")
	}
}

func TestShouldPersistEventIntViolationCount(t *testing.T) {
	meta := map[string]interface{}{
		"agent_type":      "person",
		"violation_count": 1,
	}
	if !shouldPersistEvent(meta) {
		t.Fatalf("expected int violation_count to be persisted")
	}
}
