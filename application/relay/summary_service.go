package relay

import (
	"database/sql"
	"encoding/json"
	"log"
	"sort"
	"strings"
	"time"
)

// summaryLoop runs daily summary aggregation immediately on start, then every 30 minutes.
func (r *Relay) summaryLoop() {
	r.aggregateDailySummaries()
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-r.shutdown:
			return
		case <-ticker.C:
			r.aggregateDailySummaries()
		}
	}
}

// aggregateDailySummaries builds (or refreshes) today's shared daily summary.
func (r *Relay) aggregateDailySummaries() {
	today := time.Now().Local().Format("2006-01-02")
	if err := r.buildSummaryForUser(0, today); err != nil {
		log.Printf("[SummaryService] Error building shared summary: %v", err)
	}
}

// buildSummaryForUser aggregates today's events for the shared workspace and upserts the summary.
// userID is ignored to preserve call-site compatibility.
func (r *Relay) buildSummaryForUser(userID int64, date string) error {
	_ = userID
	evRows, err := r.db.DB.Query(`
		SELECT ae.id, ae.agent_id, ae.data, ae.metadata, ae.created_at
		FROM agent_events ae
		WHERE DATE(ae.created_at, 'localtime') = ?
		ORDER BY ae.created_at DESC
		LIMIT 1000
	`, date)
	if err != nil {
		return err
	}
	defer evRows.Close()

	type evRow struct {
		id        string
		agentID   string
		data      map[string]interface{}
		metadata  map[string]interface{}
		createdAt time.Time
	}
	var evs []evRow
	for evRows.Next() {
		var ev evRow
		var dataJSON string
		var metaJSON sql.NullString
		if err := evRows.Scan(&ev.id, &ev.agentID, &dataJSON, &metaJSON, &ev.createdAt); err != nil {
			continue
		}
		json.Unmarshal([]byte(dataJSON), &ev.data)
		if metaJSON.Valid {
			json.Unmarshal([]byte(metaJSON.String), &ev.metadata)
		}
		if ev.data == nil {
			ev.data = map[string]interface{}{}
		}
		if ev.metadata == nil {
			ev.metadata = map[string]interface{}{}
		}
		evs = append(evs, ev)
	}

	// ── Person sightings ───────────────────────────────────────────────────
	type personAgg struct {
		personID        string
		globalPersonID  string
		name            string
		count           int
		firstSeen       time.Time
		lastSeen        time.Time
		cameras         map[string]bool
		activitySummary map[string]int
	}
	pagg := map[string]*personAgg{}
	for _, ev := range evs {
		pid, _ := ev.metadata["person_id"].(string)
		if pid == "" {
			pid, _ = ev.data["person_id"].(string)
		}
		gpid, _ := ev.metadata["global_person_id"].(string)
		if gpid == "" {
			gpid, _ = ev.data["global_person_id"].(string)
		}
		name, _ := ev.metadata["person_name"].(string)
		if name == "" {
			name, _ = ev.data["person_name"].(string)
		}
		key := strings.TrimSpace(gpid)
		if key == "" {
			key = strings.TrimSpace(pid)
		}
		if key == "" {
			key = strings.TrimSpace(name)
		}
		if key == "" {
			continue
		}
		if name == "" {
			name = "Unknown"
		}
		cameraName, _ := ev.metadata["camera_name"].(string)
		if strings.TrimSpace(cameraName) == "" {
			cameraName, _ = ev.data["camera_name"].(string)
		}
		if strings.TrimSpace(cameraName) == "" {
			cameraName, _ = ev.metadata["service_id"].(string)
		}
		if strings.TrimSpace(cameraName) == "" {
			cameraName, _ = ev.data["service_id"].(string)
		}
		activity := ""
		for _, src := range []map[string]interface{}{ev.metadata, ev.data} {
			for _, k := range []string{"event_brief", "brief", "vlm_summary", "summary"} {
				if s, ok := src[k].(string); ok && strings.TrimSpace(s) != "" {
					activity = strings.TrimSpace(s)
					break
				}
			}
			if activity != "" {
				break
			}
		}
		if len(activity) > 160 {
			activity = activity[:160]
		}

		if a, ok := pagg[key]; ok {
			a.count++
			if ev.createdAt.Before(a.firstSeen) {
				a.firstSeen = ev.createdAt
			}
			if ev.createdAt.After(a.lastSeen) {
				a.lastSeen = ev.createdAt
			}
			if strings.TrimSpace(cameraName) != "" {
				a.cameras[cameraName] = true
			}
			if activity != "" {
				a.activitySummary[activity]++
			}
		} else {
			a := &personAgg{
				personID:        strings.TrimSpace(pid),
				globalPersonID:  strings.TrimSpace(gpid),
				name:            name,
				count:           1,
				firstSeen:       ev.createdAt,
				lastSeen:        ev.createdAt,
				cameras:         map[string]bool{},
				activitySummary: map[string]int{},
			}
			if strings.TrimSpace(cameraName) != "" {
				a.cameras[cameraName] = true
			}
			if activity != "" {
				a.activitySummary[activity]++
			}
			pagg[key] = a
		}
	}
	sightings := make([]DailySummaryPersonSighting, 0, len(pagg))
	for _, a := range pagg {
		cameras := make([]string, 0, len(a.cameras))
		for c := range a.cameras {
			cameras = append(cameras, c)
		}
		sort.Strings(cameras)
		bestActivity := ""
		bestCount := 0
		for brief, n := range a.activitySummary {
			if n > bestCount {
				bestCount = n
				bestActivity = brief
			}
		}
		sightings = append(sightings, DailySummaryPersonSighting{
			PersonID:       a.personID,
			GlobalPersonID: a.globalPersonID,
			PersonName:     a.name,
			Count:          a.count,
			FirstSeen:      a.firstSeen.UTC().Format(time.RFC3339),
			LastSeen:       a.lastSeen.UTC().Format(time.RFC3339),
			Cameras:        cameras,
			Activity:       bestActivity,
		})
	}
	sort.Slice(sightings, func(i, j int) bool { return sightings[i].Count > sightings[j].Count })

	// ── Incidents (medium + high with a usable brief) ─────────────────────
	incidents := make([]DailySummaryIncident, 0)
	for _, ev := range evs {
		if len(incidents) >= 20 {
			break
		}
		severity, _ := ev.metadata["severity"].(string)
		if severity != "high" && severity != "medium" {
			severity, _ = ev.data["severity"].(string)
		}
		if severity != "high" && severity != "medium" {
			continue
		}
		brief := ""
		for _, key := range []string{
			"vlm_summary", "ai_summary", "summary", "response",
			"brief", "event_brief", "message", "answer", "result",
		} {
			if s, ok := ev.metadata[key].(string); ok && s != "" {
				brief = s
				break
			}
			if s, ok := ev.data[key].(string); ok && s != "" {
				brief = s
				break
			}
		}
		if brief == "" {
			var labels []string
			for _, src := range []map[string]interface{}{ev.metadata, ev.data} {
				raw, ok := src["violation_labels"].([]interface{})
				if !ok {
					continue
				}
				for _, v := range raw {
					if s, ok := v.(string); ok && s != "" {
						labels = append(labels, s)
					}
				}
			}
			if len(labels) > 0 {
				brief = "Violation detected: " + labels[0]
			} else {
				continue
			}
		}
		snapshotPath, _ := ev.metadata["snapshot_path"].(string)
		if snapshotPath == "" {
			snapshotPath, _ = ev.data["snapshot_path"].(string)
		}
		serviceID, _ := ev.metadata["service_id"].(string)
		if serviceID == "" {
			serviceID, _ = ev.data["service_id"].(string)
		}
		personName, _ := ev.metadata["person_name"].(string)
		if personName == "" {
			personName, _ = ev.data["person_name"].(string)
		}
		incidents = append(incidents, DailySummaryIncident{
			EventID:      ev.id,
			Severity:     severity,
			Brief:        brief,
			SnapshotPath: snapshotPath,
			ServiceID:    serviceID,
			PersonName:   personName,
			CreatedAt:    ev.createdAt.UTC().Format(time.RFC3339),
		})
	}

	// ── Violation breakdown ────────────────────────────────────────────────
	violBreakdown := map[string]int{}
	for _, ev := range evs {
		for _, src := range []map[string]interface{}{ev.metadata, ev.data} {
			labels, ok := src["violation_labels"].([]interface{})
			if !ok {
				continue
			}
			for _, l := range labels {
				if label, ok := l.(string); ok && label != "" {
					violBreakdown[label]++
				}
			}
		}
	}

	ds := &DailySummary{
		Date:               date,
		UserID:             0,
		EventCount:         len(evs),
		PersonSightings:    sightings,
		Incidents:          incidents,
		ViolationBreakdown: violBreakdown,
	}
	return r.dailySummaryTable.UpsertDailySummary(ds)
}
