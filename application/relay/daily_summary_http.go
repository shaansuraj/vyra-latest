package relay

import (
	"encoding/json"
	"net/http"
	"time"
)

// handleGetDailySummary returns the shared daily summary.
// GET /daily-summary?date=YYYY-MM-DD   (defaults to today in local time)
func handleGetDailySummary(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	_ = userID
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Local().Format("2006-01-02")
	}

	ds, err := relay.dailySummaryTable.GetDailySummary(userID, date)
	if err != nil {
		// Return an empty summary rather than a 404 so the frontend always gets
		// a valid shape even before the first aggregation run.
		ds = &DailySummary{
			Date:               date,
			UserID:             0,
			EventCount:         0,
			PersonSightings:    []DailySummaryPersonSighting{},
			Incidents:          []DailySummaryIncident{},
			ViolationBreakdown: map[string]int{},
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ds)
}
