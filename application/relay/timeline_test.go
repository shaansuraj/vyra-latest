package relay

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func newTimelineTestDB(t *testing.T) *Database {
	t.Helper()
	db, err := NewDatabase(filepath.Join(t.TempDir(), "timeline-test.db"))
	if err != nil {
		t.Fatalf("NewDatabase failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestTimelineRecordsFromAgentEventExtractsMultiPersonBBoxes(t *testing.T) {
	createdAt := time.Date(2026, 6, 29, 12, 57, 0, 0, time.UTC)
	ev := &AgentEvent{
		ID:        "event-1",
		AgentID:   "agent-1",
		CreatedAt: createdAt,
		Metadata: map[string]interface{}{
			"event_id":       "event-1",
			"service_id":     "cam-main",
			"camera_name":    "Main",
			"snapshot_path":  "snapshots/main.jpg",
			"activity_brief": "Two people detected on Main.",
			"severity":       "medium",
			"bboxes": []interface{}{
				map[string]interface{}{
					"label":               "person",
					"global_person_id":    "unknown:2",
					"person_name":         "Unknown #2",
					"track_id":            7,
					"identity_confidence": 0.61,
					"x":                   10,
					"y":                   12,
					"w":                   40,
					"h":                   90,
				},
				map[string]interface{}{
					"label":               "person",
					"global_person_id":    "unknown:old",
					"person_id":           "alice-id",
					"person_name":         "Alice",
					"track_id":            8,
					"identity_confidence": 0.91,
				},
				map[string]interface{}{
					"label": "chair",
				},
			},
		},
		Data: map[string]interface{}{},
	}

	records := TimelineRecordsFromAgentEvent(42, ev)
	if len(records) != 2 {
		t.Fatalf("expected 2 person records, got %d: %#v", len(records), records)
	}
	byID := map[string]PersonTimelineRecord{}
	for _, record := range records {
		byID[record.GlobalPersonID] = record
	}
	unknown := byID["unknown:2"]
	if unknown.CameraName != "Main" || unknown.ServiceID != "cam-main" {
		t.Fatalf("expected camera metadata on unknown record, got %#v", unknown)
	}
	if unknown.TrackID != 7 {
		t.Fatalf("unexpected unknown track: %#v", unknown)
	}
	known := byID["person:alice-id"]
	if known.PersonName != "Alice" {
		t.Fatalf("expected known person canonical record, got %#v", known)
	}
}

func TestPersonTimelineStorageAndIdentityRetrievalAreIdempotent(t *testing.T) {
	db := newTimelineTestDB(t)
	createdAt := time.Date(2026, 6, 29, 12, 57, 0, 0, time.UTC)

	record := PersonTimelineRecord{
		UserID:             42,
		GlobalPersonID:     "unknown:2",
		PersonName:         "Unknown #2",
		IdentityConfidence: 0.55,
		ServiceID:          "cam-main",
		CameraName:         "Main",
		EventID:            "event-1",
		TrackID:            7,
		SnapshotPath:       "main.jpg",
		ActivityBrief:      "Unknown #2 detected on Main.",
		EventKind:          "person_detection",
		Severity:           "medium",
		DedupeKey:          "event-1:unknown:2:7",
		CreatedAt:          createdAt,
	}
	if err := db.StorePersonSightingRecord(record); err != nil {
		t.Fatalf("store first sighting failed: %v", err)
	}
	record.IdentityConfidence = 0.72
	if err := db.StorePersonSightingRecord(record); err != nil {
		t.Fatalf("store duplicate sighting failed: %v", err)
	}
	if err := db.StorePersonSightingRecord(PersonTimelineRecord{
		UserID:         42,
		GlobalPersonID: "person:alice-id",
		PersonID:       "alice-id",
		PersonName:     "Alice",
		ServiceID:      "cam-extra",
		CameraName:     "Extra",
		EventID:        "event-2",
		TrackID:        1,
		DedupeKey:      "event-2:person:alice-id:1",
		CreatedAt:      createdAt.Add(time.Minute),
	}); err != nil {
		t.Fatalf("store known sighting failed: %v", err)
	}

	unknownMoments, err := db.QueryPersonTimeline(42, PersonTimelineFilter{GlobalPersonID: "unknown:2", Limit: 10})
	if err != nil {
		t.Fatalf("query unknown moments failed: %v", err)
	}
	if len(unknownMoments) != 1 {
		t.Fatalf("expected deduped unknown moment, got %d", len(unknownMoments))
	}
	if unknownMoments[0].IdentityConfidence != 0.72 {
		t.Fatalf("expected confidence update, got %.2f", unknownMoments[0].IdentityConfidence)
	}

	unknownIdentities, err := db.QueryTimelineIdentities(42, PersonTimelineFilter{Known: "unknown", Limit: 10})
	if err != nil {
		t.Fatalf("query unknown identities failed: %v", err)
	}
	if len(unknownIdentities) != 1 || unknownIdentities[0].GlobalPersonID != "unknown:2" {
		t.Fatalf("unexpected unknown identities: %#v", unknownIdentities)
	}

	knownIdentities, err := db.QueryTimelineIdentities(42, PersonTimelineFilter{Known: "known", Limit: 10})
	if err != nil {
		t.Fatalf("query known identities failed: %v", err)
	}
	if len(knownIdentities) != 1 || knownIdentities[0].PersonName != "Alice" {
		t.Fatalf("unexpected known identities: %#v", knownIdentities)
	}
}

func TestTimelineJourneyReviewCasesLocksAndExport(t *testing.T) {
	db := newTimelineTestDB(t)
	userID := insertTestUser(t, db, "timeline-journey@example.com", "timeline")
	base := time.Date(2026, 7, 8, 9, 0, 0, 0, time.UTC)
	records := []PersonTimelineRecord{
		{UserID: userID, GlobalPersonID: "unknown:journey", ServiceID: "cam-a", CameraName: "Lobby", EventID: "ev-a", TrackID: 1, SnapshotPath: "2026-07-08/snapshot_a.jpg", ActivityBrief: "Unknown entered lobby", EventKind: "person_detection", Severity: "medium", DedupeKey: "ev-a:unknown:journey:1", IdentityConfidence: 0.72, CreatedAt: base},
		{UserID: userID, GlobalPersonID: "unknown:journey", ServiceID: "cam-b", CameraName: "Hall", EventID: "ev-b", TrackID: 1, SnapshotPath: "2026-07-08/snapshot_b.jpg", ActivityBrief: "Unknown crossed hall", EventKind: "person_detection", Severity: "medium", DedupeKey: "ev-b:unknown:journey:1", IdentityConfidence: 0.76, CreatedAt: base.Add(2 * time.Minute)},
		{UserID: userID, GlobalPersonID: "unknown:journey", ServiceID: "cam-c", CameraName: "Exit", EventID: "ev-c", TrackID: 1, SnapshotPath: "2026-07-08/snapshot_c.jpg", ActivityBrief: "Unknown reached exit", EventKind: "person_detection", Severity: "high", DedupeKey: "ev-c:unknown:journey:1", IdentityConfidence: 0.8, CreatedAt: base.Add(8 * time.Minute)},
		{UserID: userID, GlobalPersonID: "unknown:nearby", ServiceID: "cam-c", CameraName: "Exit", EventID: "ev-near", TrackID: 2, SnapshotPath: "2026-07-08/snapshot_near.jpg", ActivityBrief: "Another unknown near exit", EventKind: "person_detection", Severity: "low", DedupeKey: "ev-near:unknown:nearby:2", IdentityConfidence: 0.5, CreatedAt: base.Add(8*time.Minute + 30*time.Second)},
	}
	for _, record := range records {
		if err := db.StorePersonSightingRecord(record); err != nil {
			t.Fatalf("store record failed: %v", err)
		}
	}
	if _, err := db.UpsertTimelineTopology(userID, []TimelineTopologyNode{
		{ServiceID: "cam-a", Label: "Lobby", X: 0.1, Y: 0.5, AdjacentServiceIDs: []string{"cam-b"}},
		{ServiceID: "cam-b", Label: "Hall", X: 0.5, Y: 0.5, AdjacentServiceIDs: []string{"cam-c"}},
		{ServiceID: "cam-c", Label: "Exit", X: 0.9, Y: 0.5, AdjacentServiceIDs: []string{"cam-d"}},
	}); err != nil {
		t.Fatalf("upsert topology failed: %v", err)
	}

	journey, err := db.BuildTimelineJourney(userID, "unknown:journey", PersonTimelineFilter{AnchorMomentID: "", Limit: 20})
	if err != nil {
		t.Fatalf("BuildTimelineJourney failed: %v", err)
	}
	if len(journey.Moments) != 3 || journey.Moments[0].ServiceID != "cam-a" || journey.Moments[2].ServiceID != "cam-c" {
		t.Fatalf("journey moments should be chronological, got %#v", journey.Moments)
	}
	if journey.Stats.GapsOver120Sec != 1 || len(journey.GhostTrail) != 1 {
		t.Fatalf("expected one ghost trail gap, stats=%#v ghost=%#v", journey.Stats, journey.GhostTrail)
	}
	if len(journey.NearbyActivity) != 3 || journey.NearbyActivity[0].Count == 0 {
		t.Fatalf("expected nearby activity around last sighting, got %#v", journey.NearbyActivity)
	}
	if len(journey.PredictedNext) != 1 || journey.PredictedNext[0].ServiceID != "cam-d" {
		t.Fatalf("expected topology fallback prediction to cam-d, got %#v", journey.PredictedNext)
	}

	lastMoment := journey.Moments[2]
	if err := db.ReviewTimelineMoment(userID, lastMoment.ID, "false_match", "bad match", true); err != nil {
		t.Fatalf("review moment failed: %v", err)
	}
	reviewed, err := db.QueryPersonTimeline(userID, PersonTimelineFilter{ReviewStatus: "false_match", Limit: 10})
	if err != nil || len(reviewed) != 1 || !reviewed[0].IsFalseMatch {
		t.Fatalf("expected false match review, moments=%#v err=%v", reviewed, err)
	}
	if err := db.AuditTimelineReveal(userID, lastMoment.ID); err != nil {
		t.Fatalf("audit reveal failed: %v", err)
	}

	timelineCase, err := db.CreateTimelineCase(userID, "Exit investigation", "operator", "review exit sequence")
	if err != nil {
		t.Fatalf("create case failed: %v", err)
	}
	items, err := db.AddTimelineCaseItems(userID, timelineCase.ID, []string{lastMoment.ID}, "pin exit")
	if err != nil || len(items) != 1 {
		t.Fatalf("add case item failed items=%#v err=%v", items, err)
	}
	lock, err := db.CreateTimelineEvidenceLock(userID, "Exit lock", timelineCase.ID, []string{lastMoment.ID}, time.Time{})
	if err != nil || lock.ID == "" || len(lock.MomentIDs) != 1 {
		t.Fatalf("create lock failed lock=%#v err=%v", lock, err)
	}
	manifest, err := db.ExportTimelineCase(userID, timelineCase.ID)
	if err != nil {
		t.Fatalf("export case failed: %v", err)
	}
	if len(manifest.Moments) != 1 || len(manifest.CSVRows) != 1 || manifest.CSVRows[0]["snapshot_path"] == "" {
		t.Fatalf("unexpected export manifest: %#v", manifest)
	}
	var auditCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM timeline_audit_log WHERE user_id = ?`, userID).Scan(&auditCount); err != nil {
		t.Fatalf("count audit failed: %v", err)
	}
	if auditCount < 4 {
		t.Fatalf("expected audit rows for review/case/lock/export, got %d", auditCount)
	}
}

func TestTimelineIdentityCorrectionAssignSplitMerge(t *testing.T) {
	db := newTimelineTestDB(t)
	userID := insertTestUser(t, db, "timeline-correction@example.com", "timeline")
	person, err := NewPersonTable(db.DB).CreatePerson("Alice", userID)
	if err != nil {
		t.Fatalf("create person failed: %v", err)
	}
	base := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if err := db.StorePersonSightingRecord(PersonTimelineRecord{
			UserID:             userID,
			GlobalPersonID:     "unknown:correct",
			ServiceID:          "cam-main",
			CameraName:         "Main",
			EventID:            fmt.Sprintf("ev-correct-%d", i),
			TrackID:            i,
			DedupeKey:          fmt.Sprintf("ev-correct-%d:unknown:correct:%d", i, i),
			IdentityConfidence: 0.4,
			CreatedAt:          base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("store correction sighting failed: %v", err)
		}
	}
	if err := db.StorePersonSightingRecord(PersonTimelineRecord{
		UserID:             userID,
		GlobalPersonID:     "unknown:similar",
		ServiceID:          "cam-side",
		CameraName:         "Side",
		EventID:            "ev-similar",
		TrackID:            3,
		DedupeKey:          "ev-similar:unknown:similar:3",
		IdentityConfidence: 0.45,
		CreatedAt:          base.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("store similar sighting failed: %v", err)
	}
	if err := db.UpsertPersonGlobalIdentityRecord(PersonGlobalIdentityRecord{
		GlobalPersonID:     "unknown:correct",
		BodyPrototype:      serializeEmbedding([]float64{1, 0}),
		BodyEmbeddingCount: 2,
		SeenAt:             base,
		LastObservationAt:  base,
	}); err != nil {
		t.Fatalf("upsert target prototype failed: %v", err)
	}
	if err := db.UpsertPersonGlobalIdentityRecord(PersonGlobalIdentityRecord{
		GlobalPersonID:     "unknown:similar",
		BodyPrototype:      serializeEmbedding([]float64{0.95, 0.05}),
		BodyEmbeddingCount: 2,
		SeenAt:             base,
		LastObservationAt:  base,
	}); err != nil {
		t.Fatalf("upsert similar prototype failed: %v", err)
	}
	matches, err := db.SearchTimelineAppearance(userID, "unknown:correct", "body", 10)
	if err != nil || len(matches) == 0 || matches[0].GlobalPersonID != "unknown:similar" || matches[0].Score <= 0.9 {
		t.Fatalf("appearance search mismatch matches=%#v err=%v", matches, err)
	}
	targetID, err := db.AssignTimelineIdentity(userID, "unknown:correct", person.ID)
	if err != nil {
		t.Fatalf("assign timeline identity failed: %v", err)
	}
	if targetID != "person:"+person.ID {
		t.Fatalf("unexpected target id %s", targetID)
	}
	assigned, err := db.QueryPersonTimeline(userID, PersonTimelineFilter{GlobalPersonID: targetID, Limit: 10})
	if err != nil || len(assigned) != 2 || assigned[0].PersonName != "Alice" {
		t.Fatalf("assigned moments mismatch assigned=%#v err=%v", assigned, err)
	}
	newID, err := db.SplitTimelineMoments(userID, targetID, []string{assigned[0].ID})
	if err != nil {
		t.Fatalf("split timeline moments failed: %v", err)
	}
	split, err := db.QueryPersonTimeline(userID, PersonTimelineFilter{GlobalPersonID: newID, Limit: 10})
	if err != nil || len(split) != 1 || split[0].PersonID != "" {
		t.Fatalf("split moment mismatch split=%#v err=%v", split, err)
	}
	if err := db.MergeTimelineIdentities(userID, newID, targetID); err != nil {
		t.Fatalf("merge timeline identities failed: %v", err)
	}
	merged, err := db.QueryPersonTimeline(userID, PersonTimelineFilter{GlobalPersonID: targetID, Known: "known", Limit: 10})
	if err != nil || len(merged) != 2 {
		t.Fatalf("merged known moments mismatch merged=%#v err=%v", merged, err)
	}
}

func TestBackfillPersonSightingsFromEventsDedupe(t *testing.T) {
	db := newTimelineTestDB(t)
	userID := insertTestUser(t, db, "timeline@example.com", "timeline")
	agent, err := NewAgentTable(db.DB).CreateAgent("Person Detection", "detect people", "person_worker", []string{"cam-main"}, userID)
	if err != nil {
		t.Fatalf("create agent failed: %v", err)
	}
	createdAt := time.Date(2026, 6, 29, 12, 57, 0, 0, time.UTC)
	metadata := map[string]interface{}{
		"event_id":       "event-backfill",
		"service_id":     "cam-main",
		"camera_name":    "Main",
		"snapshot_path":  "main.jpg",
		"activity_brief": "Two people detected.",
		"bboxes": []interface{}{
			map[string]interface{}{"label": "person", "global_person_id": "unknown:1", "track_id": 1},
			map[string]interface{}{"label": "person", "global_person_id": "unknown:2", "track_id": 2},
		},
	}
	if err := db.StoreAgentEvent(agent.ID, map[string]interface{}{"brief": "Two people detected."}, metadata, createdAt); err != nil {
		t.Fatalf("store agent event failed: %v", err)
	}

	if _, err := db.BackfillPersonSightingsFromEvents(100); err != nil {
		t.Fatalf("first backfill failed: %v", err)
	}
	if _, err := db.BackfillPersonSightingsFromEvents(100); err != nil {
		t.Fatalf("second backfill failed: %v", err)
	}

	moments, err := db.QueryPersonTimeline(userID, PersonTimelineFilter{ServiceID: "cam-main", Limit: 10})
	if err != nil {
		t.Fatalf("query timeline after backfill failed: %v", err)
	}
	if len(moments) != 2 {
		t.Fatalf("expected 2 deduped backfilled moments, got %d: %#v", len(moments), moments)
	}
}

func TestNewDatabaseMigratesOldPersonSightingsBeforeIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-relay.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open raw sqlite failed: %v", err)
	}
	_, err = raw.Exec(`
		CREATE TABLE person_sightings (
			id TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			global_person_id TEXT NOT NULL,
			person_id TEXT,
			service_id TEXT,
			event_id TEXT,
			snapshot_path TEXT,
			activity_brief TEXT,
			created_at DATETIME NOT NULL
		);
	`)
	if err != nil {
		t.Fatalf("create old person_sightings failed: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite failed: %v", err)
	}

	db, err := NewDatabase(path)
	if err != nil {
		t.Fatalf("NewDatabase should migrate old person_sightings before indexes: %v", err)
	}
	defer db.Close()

	if err := db.StorePersonSightingRecord(PersonTimelineRecord{
		UserID:         1,
		GlobalPersonID: "unknown:legacy",
		EventID:        "event-legacy",
		TrackID:        1,
		DedupeKey:      "event-legacy:unknown:legacy:1",
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("store migrated sighting failed: %v", err)
	}
}

func TestNewDatabaseMigratesOldQueryTracesBeforeIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-query-traces.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open raw sqlite failed: %v", err)
	}
	_, err = raw.Exec(`
		CREATE TABLE kb_query_traces (
			id TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			question TEXT NOT NULL,
			rewritten_query TEXT DEFAULT '',
			filters_json TEXT DEFAULT '{}',
			retrieved_chunks_json TEXT DEFAULT '[]',
			retrieved_events_json TEXT DEFAULT '[]',
			prompt_digest TEXT DEFAULT '',
			answer TEXT DEFAULT '',
			confidence TEXT DEFAULT '',
			latency_ms INTEGER DEFAULT 0,
			created_at DATETIME NOT NULL
		);
	`)
	if err != nil {
		t.Fatalf("create old kb_query_traces failed: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite failed: %v", err)
	}

	db, err := NewDatabase(path)
	if err != nil {
		t.Fatalf("NewDatabase should migrate old kb_query_traces before indexes: %v", err)
	}
	defer db.Close()

	if _, err := db.StoreKnowledgeTrace(KnowledgeTraceInput{
		UserID:     1,
		ChatID:     "chat-1",
		Intent:     "camera_activity",
		Question:   "Who entered Main today?",
		Answer:     "No matching timeline evidence.",
		Confidence: "low",
	}); err != nil {
		t.Fatalf("store migrated trace failed: %v", err)
	}
}
