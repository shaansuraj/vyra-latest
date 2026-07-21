package relay

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// DailySummaryPersonSighting records how often a known person was seen on a given day.
type DailySummaryPersonSighting struct {
	PersonID       string   `json:"person_id"`
	GlobalPersonID string   `json:"global_person_id,omitempty"`
	PersonName     string   `json:"person_name"`
	Count          int      `json:"count"`
	FirstSeen      string   `json:"first_seen"`
	LastSeen       string   `json:"last_seen"`
	Cameras        []string `json:"cameras,omitempty"`
	Activity       string   `json:"activity,omitempty"`
}

// DailySummaryIncident is a medium/high severity event with a human-readable brief.
type DailySummaryIncident struct {
	EventID      string `json:"event_id"`
	Severity     string `json:"severity"`
	Brief        string `json:"brief"`
	SnapshotPath string `json:"snapshot_path,omitempty"`
	ServiceID    string `json:"service_id,omitempty"`
	PersonName   string `json:"person_name,omitempty"`
	CreatedAt    string `json:"created_at"`
}

// DailySummary holds the aggregated safety data for a single day.
type DailySummary struct {
	ID                 string                       `json:"id"`
	Date               string                       `json:"date"`
	UserID             int64                        `json:"user_id"`
	EventCount         int                          `json:"event_count"`
	PersonSightings    []DailySummaryPersonSighting `json:"person_sightings"`
	Incidents          []DailySummaryIncident       `json:"incidents"`
	ViolationBreakdown map[string]int               `json:"violation_breakdown"`
	UpdatedAt          string                       `json:"updated_at"`
}

// DailySummaryTable handles persistence of daily summaries.
type DailySummaryTable struct {
	db *sql.DB
}

// NewDailySummaryTable creates a new DailySummaryTable.
func NewDailySummaryTable(db *sql.DB) *DailySummaryTable {
	return &DailySummaryTable{db: db}
}

// UpsertDailySummary inserts or updates the daily summary for a date key.
func (t *DailySummaryTable) UpsertDailySummary(ds *DailySummary) error {
	if ds.ID == "" {
		ds.ID = uuid.New().String()
	}
	sightingsJSON, _ := json.Marshal(ds.PersonSightings)
	incidentsJSON, _ := json.Marshal(ds.Incidents)
	breakdownJSON, _ := json.Marshal(ds.ViolationBreakdown)
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := t.db.Exec(`
		INSERT INTO daily_summaries
		  (id, date, user_id, event_count, person_sightings, incidents, violation_breakdown, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(date, user_id) DO UPDATE SET
		  event_count=excluded.event_count,
		  person_sightings=excluded.person_sightings,
		  incidents=excluded.incidents,
		  violation_breakdown=excluded.violation_breakdown,
		  updated_at=excluded.updated_at
	`, ds.ID, ds.Date, ds.UserID, ds.EventCount,
		string(sightingsJSON), string(incidentsJSON), string(breakdownJSON), now)
	return err
}

// GetDailySummary retrieves the shared daily summary for a specific date (YYYY-MM-DD).
// userID is ignored to preserve call-site compatibility.
func (t *DailySummaryTable) GetDailySummary(userID int64, date string) (*DailySummary, error) {
	_ = userID
	var ds DailySummary
	var sightingsJSON, incidentsJSON, breakdownJSON string
	err := t.db.QueryRow(`
		SELECT id, date, user_id, event_count,
		       COALESCE(person_sightings,'[]'), COALESCE(incidents,'[]'),
		       COALESCE(violation_breakdown,'{}'), updated_at
		FROM daily_summaries WHERE date=?
		ORDER BY updated_at DESC
		LIMIT 1
	`, date).Scan(
		&ds.ID, &ds.Date, &ds.UserID, &ds.EventCount,
		&sightingsJSON, &incidentsJSON, &breakdownJSON, &ds.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	parseDailySummaryJSON(&ds, sightingsJSON, incidentsJSON, breakdownJSON)
	return &ds, nil
}

// GetRecentDailySummaries returns the most recent N shared daily summaries.
// userID is ignored to preserve call-site compatibility.
func (t *DailySummaryTable) GetRecentDailySummaries(userID int64, days int) ([]*DailySummary, error) {
	_ = userID
	rows, err := t.db.Query(`
		SELECT id, date, user_id, event_count,
		       COALESCE(person_sightings,'[]'), COALESCE(incidents,'[]'),
		       COALESCE(violation_breakdown,'{}'), updated_at
		FROM daily_summaries
		ORDER BY date DESC, updated_at DESC
		LIMIT ?
	`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*DailySummary
	for rows.Next() {
		var ds DailySummary
		var sightingsJSON, incidentsJSON, breakdownJSON string
		if err := rows.Scan(
			&ds.ID, &ds.Date, &ds.UserID, &ds.EventCount,
			&sightingsJSON, &incidentsJSON, &breakdownJSON, &ds.UpdatedAt,
		); err != nil {
			continue
		}
		parseDailySummaryJSON(&ds, sightingsJSON, incidentsJSON, breakdownJSON)
		out = append(out, &ds)
	}
	return out, nil
}

func parseDailySummaryJSON(ds *DailySummary, sightingsJSON, incidentsJSON, breakdownJSON string) {
	json.Unmarshal([]byte(sightingsJSON), &ds.PersonSightings)
	json.Unmarshal([]byte(incidentsJSON), &ds.Incidents)
	json.Unmarshal([]byte(breakdownJSON), &ds.ViolationBreakdown)
	if ds.PersonSightings == nil {
		ds.PersonSightings = []DailySummaryPersonSighting{}
	}
	if ds.Incidents == nil {
		ds.Incidents = []DailySummaryIncident{}
	}
	if ds.ViolationBreakdown == nil {
		ds.ViolationBreakdown = map[string]int{}
	}
}
