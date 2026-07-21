package relay

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSharedEventQueriesIncludeEventsWithoutAgentRow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "relay.db")
	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase failed: %v", err)
	}
	defer db.Close()

	_, err = db.DB.Exec(
		`INSERT INTO agent_events (id, agent_id, data, metadata, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		"ev-orphan-1",
		"missing-agent-1",
		`{"event_brief":"orphan legacy event","severity":"high"}`,
		`{"service_id":"cam-legacy-1","severity":"high"}`,
		"2026-02-21 10:15:00",
	)
	if err != nil {
		t.Fatalf("insert orphan event failed: %v", err)
	}

	filtered, err := db.GetAgentEventsForUserFiltered(1, "2026-02-21", 100, 0)
	if err != nil {
		t.Fatalf("GetAgentEventsForUserFiltered failed: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != "ev-orphan-1" {
		t.Fatalf("expected filtered query to return orphan event, got %d events", len(filtered))
	}

	recent, err := db.GetRecentAgentEventsForUser(1, 100, 0)
	if err != nil {
		t.Fatalf("GetRecentAgentEventsForUser failed: %v", err)
	}
	if len(recent) != 1 || recent[0].ID != "ev-orphan-1" {
		t.Fatalf("expected recent query to return orphan event, got %d events", len(recent))
	}

	search, err := db.SearchAgentEventsForUser(1, "orphan legacy", 100, 0)
	if err != nil {
		t.Fatalf("SearchAgentEventsForUser failed: %v", err)
	}
	if len(search) != 1 || search[0].ID != "ev-orphan-1" {
		t.Fatalf("expected search query to return orphan event, got %d events", len(search))
	}
}

func TestQueryAgentEventsForAIFiltersDateCameraAndKeywordInSQL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "relay.db")
	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase failed: %v", err)
	}
	defer db.Close()

	_, err = db.DB.Exec(`INSERT INTO agents (id, name, worker_id, config, user_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"agent-1", "PPE Monitor", "worker-1", `{}`, 1, time.Now().UTC(), time.Now().UTC())
	if err != nil {
		t.Fatalf("insert agent failed: %v", err)
	}
	events := []struct {
		id       string
		brief    string
		service  string
		created  time.Time
		severity string
	}{
		{"match", "Helmet violation near entry", "cam-main", time.Date(2026, 2, 21, 10, 15, 0, 0, time.UTC), "high"},
		{"wrong-camera", "Helmet violation near entry", "cam-side", time.Date(2026, 2, 21, 10, 20, 0, 0, time.UTC), "high"},
		{"wrong-keyword", "Person walking normally", "cam-main", time.Date(2026, 2, 21, 10, 25, 0, 0, time.UTC), "low"},
		{"wrong-date", "Helmet violation near entry", "cam-main", time.Date(2026, 2, 22, 10, 15, 0, 0, time.UTC), "high"},
	}
	for _, ev := range events {
		_, err = db.DB.Exec(
			`INSERT INTO agent_events (id, agent_id, data, metadata, created_at) VALUES (?, ?, ?, ?, ?)`,
			ev.id,
			"agent-1",
			`{"event_brief":"`+ev.brief+`","severity":"`+ev.severity+`","service_id":"`+ev.service+`"}`,
			`{"service_id":"`+ev.service+`","severity":"`+ev.severity+`"}`,
			ev.created,
		)
		if err != nil {
			t.Fatalf("insert event %s failed: %v", ev.id, err)
		}
	}

	got, err := db.QueryAgentEventsForAI(1, AgentEventAIQueryFilter{
		StartAt:    time.Date(2026, 2, 21, 0, 0, 0, 0, time.UTC),
		EndAt:      time.Date(2026, 2, 22, 0, 0, 0, 0, time.UTC),
		ServiceIDs: []string{"cam-main"},
		Keywords:   []string{"helmet"},
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("QueryAgentEventsForAI failed: %v", err)
	}
	if len(got) != 1 || got[0].ID != "match" {
		t.Fatalf("expected only matching event, got %#v", got)
	}
}
