package cv

import "testing"

func TestShouldPersistEvent_DetectionLikeAndCustom(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]interface{}
		want     bool
	}{
		{
			name: "custom bbox-only event is skipped",
			metadata: map[string]interface{}{
				"agent_type":   "custom",
				"event_brief":  "Detections - person:2",
				"person_count": 2,
			},
			want: false,
		},
		{
			name: "custom event with snapshot persists",
			metadata: map[string]interface{}{
				"agent_type":    "custom",
				"snapshot_path": "2026-02-27/snap.jpg",
			},
			want: true,
		},
		{
			name: "custom event with medium severity persists",
			metadata: map[string]interface{}{
				"agent_type": "custom",
				"severity":   "medium",
			},
			want: true,
		},
		{
			name: "ppe empty payload is skipped",
			metadata: map[string]interface{}{
				"agent_type": "ppe",
			},
			want: false,
		},
		{
			name: "internal face lookup event is never persisted",
			metadata: map[string]interface{}{
				"agent_type":    "face",
				"internal_only": true,
				"matched_count": 1,
			},
			want: false,
		},
		{
			name: "ppe first detection persists",
			metadata: map[string]interface{}{
				"agent_type":      "ppe",
				"first_detection": true,
			},
			want: true,
		},
		{
			name: "vlm payload persists by default",
			metadata: map[string]interface{}{
				"agent_type": "situational",
			},
			want: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldPersistEvent(tc.metadata); got != tc.want {
				t.Fatalf("shouldPersistEvent() = %v, want %v", got, tc.want)
			}
		})
	}
}
