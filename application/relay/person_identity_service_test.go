package relay

import (
	"strings"
	"testing"
	"time"
)

func TestPersonIdentityResolveUnknownMatchAcrossCameras(t *testing.T) {
	svc := NewPersonIdentityService(nil, nil)

	a := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:       "cam-1",
		TrackID:       1,
		Embedding:     []float64{1, 0, 0},
		ObservationTS: "2026-05-08T10:00:00Z",
	})
	if a.GlobalPersonID == "" {
		t.Fatalf("expected unknown global id for first sighting")
	}
	if !strings.HasPrefix(a.GlobalPersonID, "unknown:") {
		t.Fatalf("expected unknown:* id, got %q", a.GlobalPersonID)
	}

	b := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:       "cam-2",
		TrackID:       99,
		Embedding:     []float64{1, 0, 0},
		ObservationTS: "2026-05-08T10:00:30Z",
	})
	if b.GlobalPersonID != a.GlobalPersonID {
		t.Fatalf("expected embedding match to reuse global id, got %q != %q", b.GlobalPersonID, a.GlobalPersonID)
	}
}

func TestPersonIdentityAvoidsImmediateCrossCameraReuse(t *testing.T) {
	svc := NewPersonIdentityService(nil, nil)
	svc.crossCameraMinGap = 8 * time.Second

	a := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:       "cam-1",
		TrackID:       1,
		Embedding:     []float64{1, 0, 0},
		ObservationTS: "2026-05-08T10:00:00Z",
	})
	b := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:       "cam-2",
		TrackID:       2,
		Embedding:     []float64{1, 0, 0},
		ObservationTS: "2026-05-08T10:00:03Z",
	})
	if a.GlobalPersonID == b.GlobalPersonID {
		t.Fatalf("expected immediate cross-camera sighting to remain distinct, got %q", a.GlobalPersonID)
	}
}

func TestPersonIdentityDefaultAllowsImmediateCrossCameraReuse(t *testing.T) {
	svc := NewPersonIdentityService(nil, nil)

	a := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:       "cam-1",
		TrackID:       1,
		Embedding:     []float64{1, 0, 0},
		ObservationTS: "2026-05-08T10:00:00Z",
	})
	b := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:       "cam-2",
		TrackID:       2,
		Embedding:     []float64{1, 0, 0},
		ObservationTS: "2026-05-08T10:00:03Z",
	})
	if a.GlobalPersonID != b.GlobalPersonID {
		t.Fatalf("expected default cross-camera sighting to reuse global id, got %q != %q", b.GlobalPersonID, a.GlobalPersonID)
	}
}

func TestPersonIdentityDoesNotReuseUnknownForSimultaneousTracksOnSameCamera(t *testing.T) {
	svc := NewPersonIdentityService(nil, nil)

	a := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:       "cam-1",
		TrackID:       1,
		Embedding:     []float64{1, 0, 0},
		ObservationTS: "2026-05-08T10:00:00Z",
	})
	b := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:       "cam-1",
		TrackID:       2,
		Embedding:     []float64{1, 0, 0},
		ObservationTS: "2026-05-08T10:00:01Z",
	})
	if a.GlobalPersonID == "" || b.GlobalPersonID == "" {
		t.Fatalf("expected global ids, got %q and %q", a.GlobalPersonID, b.GlobalPersonID)
	}
	if a.GlobalPersonID == b.GlobalPersonID {
		t.Fatalf("expected active same-camera tracks to remain distinct, got %q", a.GlobalPersonID)
	}
}

func TestPersonIdentityDefaultAllowsOutOfOrderCrossCameraReuse(t *testing.T) {
	svc := NewPersonIdentityService(nil, nil)

	a := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:       "cam-1",
		TrackID:       1,
		Embedding:     []float64{1, 0, 0},
		ObservationTS: "2026-05-08T10:00:03Z",
	})
	b := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:       "cam-2",
		TrackID:       2,
		Embedding:     []float64{1, 0, 0},
		ObservationTS: "2026-05-08T10:00:00Z",
	})
	if a.GlobalPersonID != b.GlobalPersonID {
		t.Fatalf("expected out-of-order cross-camera sighting to reuse global id, got %q != %q", b.GlobalPersonID, a.GlobalPersonID)
	}
}

func TestPersonIdentityMergesExistingUnknownTracksOnLaterEmbedding(t *testing.T) {
	svc := NewPersonIdentityService(nil, nil)

	first := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:       "cam-1",
		TrackID:       1,
		ObservationTS: "2026-05-08T10:00:00Z",
	})
	second := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:       "cam-2",
		TrackID:       2,
		ObservationTS: "2026-05-08T10:00:01Z",
	})
	if first.GlobalPersonID == "" || second.GlobalPersonID == "" {
		t.Fatalf("expected initial unknown ids, got %q and %q", first.GlobalPersonID, second.GlobalPersonID)
	}
	if first.GlobalPersonID == second.GlobalPersonID {
		t.Fatalf("expected separate initial unknown ids without embeddings")
	}

	cam1 := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:       "cam-1",
		TrackID:       1,
		Embedding:     []float64{1, 0, 0},
		ObservationTS: "2026-05-08T10:00:05Z",
	})
	cam2 := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:       "cam-2",
		TrackID:       2,
		Embedding:     []float64{1, 0, 0},
		ObservationTS: "2026-05-08T10:00:06Z",
	})
	if cam1.GlobalPersonID != first.GlobalPersonID {
		t.Fatalf("expected cam-1 to keep first unknown id, got %q != %q", cam1.GlobalPersonID, first.GlobalPersonID)
	}
	if cam2.GlobalPersonID != first.GlobalPersonID {
		t.Fatalf("expected later embedding to merge cam-2 into first unknown id, got %q != %q", cam2.GlobalPersonID, first.GlobalPersonID)
	}
	if got := svc.canonicalID(second.GlobalPersonID); got != first.GlobalPersonID {
		t.Fatalf("expected second unknown id to alias to %q, got %q", first.GlobalPersonID, got)
	}

	byCam2 := svc.GetIdentities("cam-2")
	resolved := byCam2["2"]
	if resolved == nil || resolved.GlobalPersonID != first.GlobalPersonID {
		t.Fatalf("expected cam-2 track cache to use merged id %q, got %#v", first.GlobalPersonID, resolved)
	}
}

func TestPersonIdentityUnknownMergeKeepsLowerUnknownSequence(t *testing.T) {
	svc := NewPersonIdentityService(nil, nil)

	lower := svc.ResolveTrack(PersonIdentityResolveRequest{MediaID: "cam-1", TrackID: 1})
	higher := svc.ResolveTrack(PersonIdentityResolveRequest{MediaID: "cam-2", TrackID: 2})
	if lower.GlobalPersonID == "" || higher.GlobalPersonID == "" || lower.GlobalPersonID == higher.GlobalPersonID {
		t.Fatalf("expected distinct unknown ids, got %q and %q", lower.GlobalPersonID, higher.GlobalPersonID)
	}

	svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:       "cam-2",
		TrackID:       2,
		Embedding:     []float64{0, 1, 0},
		ObservationTS: "2026-05-08T10:00:05Z",
	})
	merged := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:       "cam-1",
		TrackID:       1,
		Embedding:     []float64{0, 1, 0},
		ObservationTS: "2026-05-08T10:00:06Z",
	})
	if merged.GlobalPersonID != lower.GlobalPersonID {
		t.Fatalf("expected lower unknown sequence to remain canonical, got %q != %q", merged.GlobalPersonID, lower.GlobalPersonID)
	}
	if got := svc.canonicalID(higher.GlobalPersonID); got != lower.GlobalPersonID {
		t.Fatalf("expected higher unknown id to alias to %q, got %q", lower.GlobalPersonID, got)
	}
}

func TestPersonIdentityMergeUnknownToKnown(t *testing.T) {
	svc := NewPersonIdentityService(nil, nil)

	unknown := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:   "cam-1",
		TrackID:   7,
		Embedding: []float64{0, 1, 0},
	})
	if unknown.GlobalPersonID == "" {
		t.Fatalf("expected unknown identity")
	}

	known := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:    "cam-1",
		TrackID:    7,
		PersonID:   "person-123",
		PersonName: "Abhishek",
		MatchScore: 0.97,
	})
	if got, want := known.GlobalPersonID, "person:person-123"; got != want {
		t.Fatalf("expected known global id %q, got %q", want, got)
	}

	if got, want := svc.canonicalID(unknown.GlobalPersonID), "person:person-123"; got != want {
		t.Fatalf("expected canonical merge to %q, got %q", want, got)
	}

	byTrack := svc.GetIdentities("cam-1")
	resolved := byTrack["7"]
	if resolved == nil {
		t.Fatalf("expected identity cache for track 7")
	}
	if got, want := resolved.GlobalPersonID, "person:person-123"; got != want {
		t.Fatalf("expected canonical track id %q, got %q", want, got)
	}
}

func TestPersonIdentityKeepsSameUnknownForTrackWhenEmbeddingChanges(t *testing.T) {
	svc := NewPersonIdentityService(nil, nil)

	first := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:   "cam-1",
		TrackID:   5,
		Embedding: []float64{1, 0, 0},
	})
	if first.GlobalPersonID == "" {
		t.Fatalf("expected first identity")
	}

	second := svc.ResolveTrack(PersonIdentityResolveRequest{
		MediaID:   "cam-1",
		TrackID:   5,
		Embedding: []float64{0, 1, 0},
	})
	if second.GlobalPersonID != first.GlobalPersonID {
		t.Fatalf("expected same global id for same track, got %q != %q", second.GlobalPersonID, first.GlobalPersonID)
	}
}

func TestCollectGlobalIDsRejectsNonCanonicalUnknownIDs(t *testing.T) {
	svc := NewPersonIdentityService(nil, nil)
	ids := svc.collectGlobalIDs(map[string]interface{}{
		"global_person_id": "unknown:cam-1:7",
		"global_person_ids": []interface{}{
			"unknown:9",
			"person:abc",
			"unknown:cam-2:3",
			"pending:track-12",
		},
		"bboxes": []interface{}{
			map[string]interface{}{"global_person_id": "unknown:11"},
			map[string]interface{}{"global_person_id": "unknown:cam-9:17"},
		},
	})

	if len(ids) != 3 {
		t.Fatalf("expected 3 canonical ids, got %d (%v)", len(ids), ids)
	}
	want := map[string]bool{"unknown:9": true, "unknown:11": true, "person:abc": true}
	for _, id := range ids {
		if !want[id] {
			t.Fatalf("unexpected id %q in %v", id, ids)
		}
	}
}
