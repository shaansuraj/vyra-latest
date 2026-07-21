package relay

import "testing"

func faceWorkerEvent(mediaID string, trackID int, det map[string]interface{}) map[string]interface{} {
	if det == nil {
		det = map[string]interface{}{}
	}
	det["track_id"] = float64(trackID)
	return map[string]interface{}{
		"metadata": map[string]interface{}{
			"agent_type": "face",
			"service_id": mediaID,
		},
		"data": map[string]interface{}{
			"detections": []interface{}{det},
		},
	}
}

func TestFaceIdentityServicePromotesRepeatedWeakCandidate(t *testing.T) {
	t.Setenv("FACE_WEAK_PROMOTE_HITS", "2")
	svc := NewFaceIdentityService(nil, nil)

	weak := map[string]interface{}{
		"confidence_tier":       "weak",
		"candidate_person_id":   "person-1",
		"candidate_person_name": "Known Person",
		"candidate_score":       float64(0.44),
	}

	svc.OnFaceWorkerEvent(faceWorkerEvent("cam-a", 7, weak))
	first := svc.GetIdentities("cam-a")[7]
	if first == nil {
		t.Fatalf("expected weak candidate identity to be cached")
	}
	if first.PersonID != "" {
		t.Fatalf("first weak hit should not promote immediately, got person_id=%q", first.PersonID)
	}
	if first.CandidatePersonID != "person-1" || first.EvidenceHits != 1 {
		t.Fatalf("unexpected first weak state: %+v", first)
	}

	svc.OnFaceWorkerEvent(faceWorkerEvent("cam-a", 7, weak))
	second := svc.GetIdentities("cam-a")[7]
	if second == nil {
		t.Fatalf("expected promoted identity to be cached")
	}
	if second.PersonID != "person-1" || second.PersonName != "Known Person" {
		t.Fatalf("expected weak candidate to promote to known person, got %+v", second)
	}
	if second.ConfidenceTier != "weak_promoted" || second.EvidenceHits != 2 {
		t.Fatalf("unexpected promoted weak state: %+v", second)
	}
}

func TestFaceWeakPromoteHitsAcceptsTrackEnvAlias(t *testing.T) {
	t.Setenv("FACE_WEAK_PROMOTE_HITS", "")
	t.Setenv("FACE_TRACK_WEAK_PROMOTE_HITS", "3")

	if got := faceWeakPromoteHits(); got != 3 {
		t.Fatalf("expected FACE_TRACK_WEAK_PROMOTE_HITS fallback, got %d", got)
	}
}

func TestFaceIdentityServiceDoesNotClearKnownIdentityWithUnknownResult(t *testing.T) {
	svc := NewFaceIdentityService(nil, nil)

	svc.OnFaceWorkerEvent(faceWorkerEvent("cam-a", 8, map[string]interface{}{
		"person_id":       "person-2",
		"person_name":     "Known Person",
		"match_score":     float64(0.62),
		"confidence_tier": "strong",
	}))
	svc.OnFaceWorkerEvent(faceWorkerEvent("cam-a", 8, map[string]interface{}{
		"confidence_tier": "unknown",
		"candidate_score": float64(0.12),
	}))

	got := svc.GetIdentities("cam-a")[8]
	if got == nil {
		t.Fatalf("expected known identity to remain cached")
	}
	if got.PersonID != "person-2" || got.PersonName != "Known Person" {
		t.Fatalf("unknown result cleared known identity: %+v", got)
	}
}
