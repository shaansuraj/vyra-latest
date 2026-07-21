package relay

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type PersonTimelineRecord struct {
	UserID             int64
	GlobalPersonID     string
	PersonID           string
	PersonName         string
	IdentitySource     string
	IdentityConfidence float64
	ServiceID          string
	CameraName         string
	EventID            string
	TrackID            int
	BBoxJSON           string
	SnapshotPath       string
	ActivityBrief      string
	EventKind          string
	Severity           string
	MetadataJSON       string
	DedupeKey          string
	CreatedAt          time.Time
}

type PersonTimelineFilter struct {
	Date           string
	StartAt        time.Time
	EndAt          time.Time
	ServiceID      string
	CameraGroupID  string
	GlobalPersonID string
	PersonID       string
	AnchorMomentID string
	Known          string
	EventKind      string
	Severity       string
	ReviewStatus   string
	Query          string
	Limit          int
}

type TimelineIdentity struct {
	GlobalPersonID     string    `json:"global_person_id"`
	PersonID           string    `json:"person_id,omitempty"`
	PersonName         string    `json:"person_name,omitempty"`
	IdentitySource     string    `json:"identity_source,omitempty"`
	IdentityConfidence float64   `json:"identity_confidence,omitempty"`
	LastServiceID      string    `json:"last_service_id,omitempty"`
	LastCameraName     string    `json:"last_camera_name,omitempty"`
	FirstSeenAt        time.Time `json:"first_seen_at"`
	LastSeenAt         time.Time `json:"last_seen_at"`
	MomentCount        int       `json:"moment_count"`
	SnapshotPath       string    `json:"snapshot_path,omitempty"`
}

func (db *Database) StorePersonSightingRecord(record PersonTimelineRecord) error {
	if db == nil || record.UserID <= 0 || strings.TrimSpace(record.GlobalPersonID) == "" {
		return nil
	}
	record.GlobalPersonID = strings.TrimSpace(record.GlobalPersonID)
	record.PersonID = strings.TrimSpace(record.PersonID)
	record.PersonName = strings.TrimSpace(record.PersonName)
	record.IdentitySource = strings.TrimSpace(record.IdentitySource)
	record.ServiceID = strings.TrimSpace(record.ServiceID)
	record.CameraName = strings.TrimSpace(record.CameraName)
	record.EventID = strings.TrimSpace(record.EventID)
	record.SnapshotPath = strings.TrimSpace(record.SnapshotPath)
	record.ActivityBrief = strings.TrimSpace(record.ActivityBrief)
	record.EventKind = strings.TrimSpace(record.EventKind)
	record.Severity = strings.TrimSpace(record.Severity)
	record.BBoxJSON = strings.TrimSpace(record.BBoxJSON)
	if strings.TrimSpace(record.MetadataJSON) == "" {
		record.MetadataJSON = "{}"
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}
	record.DedupeKey = strings.TrimSpace(record.DedupeKey)
	if record.DedupeKey == "" {
		record.DedupeKey = buildTimelineDedupeKey(record.EventID, record.GlobalPersonID, record.TrackID, record.CreatedAt)
	}

	_, err := db.execWrite(`
		INSERT INTO person_sightings (
			id, user_id, global_person_id, person_id, person_name, identity_source,
			identity_confidence, service_id, camera_name, event_id, track_id,
			bbox_json, snapshot_path, activity_brief, event_kind, severity,
			metadata_json, dedupe_key, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(dedupe_key) DO UPDATE SET
			person_id = CASE WHEN excluded.person_id != '' THEN excluded.person_id ELSE person_sightings.person_id END,
			person_name = CASE WHEN excluded.person_name != '' THEN excluded.person_name ELSE person_sightings.person_name END,
			identity_source = CASE WHEN excluded.identity_source != '' THEN excluded.identity_source ELSE person_sightings.identity_source END,
			identity_confidence = CASE WHEN excluded.identity_confidence > person_sightings.identity_confidence THEN excluded.identity_confidence ELSE person_sightings.identity_confidence END,
			service_id = CASE WHEN excluded.service_id != '' THEN excluded.service_id ELSE person_sightings.service_id END,
			camera_name = CASE WHEN excluded.camera_name != '' THEN excluded.camera_name ELSE person_sightings.camera_name END,
			event_id = CASE WHEN excluded.event_id != '' THEN excluded.event_id ELSE person_sightings.event_id END,
			track_id = CASE WHEN excluded.track_id >= 0 THEN excluded.track_id ELSE person_sightings.track_id END,
			bbox_json = CASE WHEN excluded.bbox_json != '' THEN excluded.bbox_json ELSE person_sightings.bbox_json END,
			snapshot_path = CASE WHEN excluded.snapshot_path != '' THEN excluded.snapshot_path ELSE person_sightings.snapshot_path END,
			activity_brief = CASE WHEN excluded.activity_brief != '' THEN excluded.activity_brief ELSE person_sightings.activity_brief END,
			event_kind = CASE WHEN excluded.event_kind != '' THEN excluded.event_kind ELSE person_sightings.event_kind END,
			severity = CASE WHEN excluded.severity != '' THEN excluded.severity ELSE person_sightings.severity END,
			metadata_json = CASE WHEN excluded.metadata_json != '{}' THEN excluded.metadata_json ELSE person_sightings.metadata_json END
	`, uuid.New().String(), record.UserID, record.GlobalPersonID, record.PersonID, record.PersonName,
		record.IdentitySource, record.IdentityConfidence, record.ServiceID, record.CameraName, record.EventID,
		record.TrackID, record.BBoxJSON, record.SnapshotPath, record.ActivityBrief, record.EventKind, record.Severity,
		record.MetadataJSON, record.DedupeKey, record.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert person_sighting: %w", err)
	}
	return nil
}

func (db *Database) StorePersonSighting(
	userID int64,
	globalPersonID string,
	personID string,
	serviceID string,
	eventID string,
	snapshotPath string,
	activityBrief string,
	createdAt time.Time,
) error {
	return db.StorePersonSightingRecord(PersonTimelineRecord{
		UserID:         userID,
		GlobalPersonID: globalPersonID,
		PersonID:       personID,
		ServiceID:      serviceID,
		EventID:        eventID,
		TrackID:        -1,
		SnapshotPath:   snapshotPath,
		ActivityBrief:  activityBrief,
		CreatedAt:      createdAt,
	})
}

func buildTimelineDedupeKey(eventID, globalPersonID string, trackID int, createdAt time.Time) string {
	eventID = strings.TrimSpace(eventID)
	globalPersonID = strings.TrimSpace(globalPersonID)
	if eventID == "" {
		if createdAt.IsZero() {
			createdAt = time.Now()
		}
		eventID = strconv.FormatInt(createdAt.UnixNano(), 10)
	}
	if trackID >= 0 {
		return eventID + ":" + globalPersonID + ":" + strconv.Itoa(trackID)
	}
	return eventID + ":" + globalPersonID
}

func TimelineRecordsFromAgentEvent(userID int64, ev *AgentEvent) []PersonTimelineRecord {
	if ev == nil || userID <= 0 {
		return nil
	}
	meta := ev.Metadata
	if meta == nil {
		meta = map[string]interface{}{}
	}
	data := ev.Data
	if data == nil {
		data = map[string]interface{}{}
	}
	eventID := firstTimelineString(meta["event_id"], data["event_id"], ev.ID)
	serviceID := firstTimelineString(meta["service_id"], data["service_id"], meta["media_id"], data["media_id"])
	cameraName := firstTimelineString(meta["camera_name"], data["camera_name"])
	snapshotPath := firstTimelineString(meta["snapshot_path"], data["snapshot_path"])
	activityBrief := firstTimelineString(meta["activity_brief"], meta["event_brief"], data["activity_brief"], data["event_brief"], data["brief"], meta["brief"])
	eventKind := firstTimelineString(meta["event_kind"], data["event_kind"])
	severity := firstTimelineString(meta["severity"], data["severity"])
	metadataJSON := mustJSON(meta, "{}")
	createdAt := ev.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	var records []PersonTimelineRecord
	if boxes := timelineBBoxList(firstNonNil(meta["bboxes"], data["bboxes"])); len(boxes) > 0 {
		for i, box := range boxes {
			if !timelineIsPersonBox(box) {
				continue
			}
			gid := timelineCanonicalGlobalID(box)
			if gid == "" {
				continue
			}
			trackID := timelineInt(box["track_id"], -1)
			bboxJSON := mustJSON(box, "{}")
			rec := PersonTimelineRecord{
				UserID:             userID,
				GlobalPersonID:     gid,
				PersonID:           firstTimelineString(box["person_id"]),
				PersonName:         firstTimelineString(box["person_name"]),
				IdentitySource:     firstTimelineString(box["identity_source"]),
				IdentityConfidence: timelineFloat(firstNonNil(box["identity_confidence"], box["match_score"]), 0),
				ServiceID:          serviceID,
				CameraName:         cameraName,
				EventID:            eventID,
				TrackID:            trackID,
				BBoxJSON:           bboxJSON,
				SnapshotPath:       snapshotPath,
				ActivityBrief:      activityBrief,
				EventKind:          eventKind,
				Severity:           severity,
				MetadataJSON:       metadataJSON,
				CreatedAt:          createdAt,
			}
			rec.DedupeKey = buildTimelineDedupeKey(eventID, gid, trackIDOrIndex(trackID, i), createdAt)
			records = append(records, rec)
		}
	}

	if len(records) == 0 {
		ids := collectTimelineGlobalIDs(meta)
		if len(ids) == 0 {
			ids = collectTimelineGlobalIDs(data)
		}
		personID := firstTimelineString(meta["person_id"], data["person_id"])
		personName := firstTimelineString(meta["person_name"], data["person_name"])
		identitySource := firstTimelineString(meta["identity_source"], data["identity_source"])
		identityConfidence := timelineFloat(firstNonNil(meta["identity_confidence"], data["identity_confidence"], meta["match_score"], data["match_score"]), 0)
		for _, gid := range ids {
			rec := PersonTimelineRecord{
				UserID:             userID,
				GlobalPersonID:     gid,
				PersonID:           personID,
				PersonName:         personName,
				IdentitySource:     identitySource,
				IdentityConfidence: identityConfidence,
				ServiceID:          serviceID,
				CameraName:         cameraName,
				EventID:            eventID,
				TrackID:            -1,
				SnapshotPath:       snapshotPath,
				ActivityBrief:      activityBrief,
				EventKind:          eventKind,
				Severity:           severity,
				MetadataJSON:       metadataJSON,
				CreatedAt:          createdAt,
			}
			rec.DedupeKey = buildTimelineDedupeKey(eventID, gid, -1, createdAt)
			records = append(records, rec)
		}
	}

	sort.SliceStable(records, func(i, j int) bool {
		return records[i].DedupeKey < records[j].DedupeKey
	})
	return records
}

func trackIDOrIndex(trackID, idx int) int {
	if trackID >= 0 {
		return trackID
	}
	return -100000 - idx
}

func collectTimelineGlobalIDs(m map[string]interface{}) []string {
	seen := map[string]bool{}
	add := func(raw interface{}) {
		id := strings.TrimSpace(firstTimelineString(raw))
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
	}
	add(m["global_person_id"])
	switch ids := m["global_person_ids"].(type) {
	case []interface{}:
		for _, raw := range ids {
			add(raw)
		}
	case []string:
		for _, raw := range ids {
			add(raw)
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func timelineBBoxList(raw interface{}) []map[string]interface{} {
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

func timelineIsPersonBox(box map[string]interface{}) bool {
	label := strings.ToLower(strings.TrimSpace(firstTimelineString(box["label"])))
	return label == "" || label == "person"
}

func timelineCanonicalGlobalID(box map[string]interface{}) string {
	personID := strings.TrimSpace(firstTimelineString(box["person_id"]))
	globalID := strings.TrimSpace(firstTimelineString(box["global_person_id"]))
	if personID != "" && (globalID == "" || strings.HasPrefix(strings.ToLower(globalID), "unknown:")) {
		return "person:" + personID
	}
	return globalID
}

func firstTimelineString(values ...interface{}) string {
	for _, value := range values {
		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case fmt.Stringer:
			if strings.TrimSpace(v.String()) != "" {
				return strings.TrimSpace(v.String())
			}
		}
	}
	return ""
}

func firstNonNil(values ...interface{}) interface{} {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func timelineFloat(raw interface{}, fallback float64) float64 {
	switch v := raw.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return fallback
}

func timelineInt(raw interface{}, fallback int) int {
	switch v := raw.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return fallback
}

func (db *Database) QueryPersonTimeline(userID int64, filter PersonTimelineFilter) ([]*PersonMoment, error) {
	if db == nil || userID <= 0 {
		return nil, nil
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	where := []string{"user_id = ?"}
	args := []interface{}{userID}
	if !filter.StartAt.IsZero() || !filter.EndAt.IsZero() {
		if !filter.StartAt.IsZero() {
			where = append(where, "created_at >= ?")
			args = append(args, filter.StartAt.UTC())
		}
		if !filter.EndAt.IsZero() {
			where = append(where, "created_at < ?")
			args = append(args, filter.EndAt.UTC())
		}
	} else if strings.TrimSpace(filter.Date) != "" {
		where = append(where, "DATE(created_at, 'localtime') = ?")
		args = append(args, strings.TrimSpace(filter.Date))
	}
	if strings.TrimSpace(filter.ServiceID) != "" {
		where = append(where, "service_id = ?")
		args = append(args, strings.TrimSpace(filter.ServiceID))
	}
	if strings.TrimSpace(filter.GlobalPersonID) != "" {
		where = append(where, "global_person_id = ?")
		args = append(args, strings.TrimSpace(filter.GlobalPersonID))
	}
	if strings.TrimSpace(filter.CameraGroupID) != "" {
		where = append(where, `service_id IN (
			SELECT cgm.service_id
			FROM camera_group_members cgm
			JOIN camera_groups cg ON cg.id = cgm.group_id
			WHERE cg.user_id = ? AND cgm.group_id = ?
		)`)
		args = append(args, userID, strings.TrimSpace(filter.CameraGroupID))
	}
	if strings.TrimSpace(filter.PersonID) != "" {
		where = append(where, "person_id = ?")
		args = append(args, strings.TrimSpace(filter.PersonID))
	}
	if strings.TrimSpace(filter.EventKind) != "" {
		where = append(where, "event_kind = ?")
		args = append(args, strings.TrimSpace(filter.EventKind))
	}
	if strings.TrimSpace(filter.Severity) != "" {
		where = append(where, "severity = ?")
		args = append(args, strings.TrimSpace(filter.Severity))
	}
	if strings.TrimSpace(filter.ReviewStatus) != "" {
		where = append(where, "COALESCE(review_status,'unreviewed') = ?")
		args = append(args, strings.TrimSpace(filter.ReviewStatus))
	}
	if query := strings.ToLower(strings.TrimSpace(filter.Query)); query != "" {
		needle := "%" + query + "%"
		where = append(where, `(LOWER(COALESCE(activity_brief,'')) LIKE ? OR LOWER(COALESCE(person_name,'')) LIKE ? OR LOWER(COALESCE(camera_name,'')) LIKE ? OR LOWER(COALESCE(global_person_id,'')) LIKE ? OR LOWER(COALESCE(event_kind,'')) LIKE ? OR LOWER(COALESCE(severity,'')) LIKE ?)`)
		args = append(args, needle, needle, needle, needle, needle, needle)
	}
	switch strings.ToLower(strings.TrimSpace(filter.Known)) {
	case "known":
		where = append(where, "person_id IS NOT NULL AND person_id != ''")
	case "unknown":
		where = append(where, "(person_id IS NULL OR person_id = '')")
	}
	args = append(args, limit)

	rows, err := db.DB.Query(`
		SELECT id, global_person_id, COALESCE(person_id,''), COALESCE(person_name,''),
		       COALESCE(identity_source,''), COALESCE(identity_confidence,0),
		       COALESCE(service_id,''), COALESCE(camera_name,''), COALESCE(event_id,''),
		       COALESCE(track_id,-1), COALESCE(bbox_json,''), COALESCE(snapshot_path,''),
		       COALESCE(activity_brief,''), COALESCE(event_kind,''), COALESCE(severity,''),
		       COALESCE(metadata_json,'{}'), COALESCE(dedupe_key,''),
		       COALESCE(review_status,'unreviewed'), COALESCE(operator_note,''),
		       COALESCE(is_false_match,0), reviewed_at, created_at
		FROM person_sightings
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY created_at DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPersonMoments(rows, limit)
}

func scanPersonMoments(rows *sql.Rows, limit int) ([]*PersonMoment, error) {
	out := make([]*PersonMoment, 0, limit)
	for rows.Next() {
		var pm PersonMoment
		var isFalseMatch int
		var reviewedAtRaw interface{}
		if err := rows.Scan(
			&pm.ID,
			&pm.GlobalPersonID,
			&pm.PersonID,
			&pm.PersonName,
			&pm.IdentitySource,
			&pm.IdentityConfidence,
			&pm.ServiceID,
			&pm.CameraName,
			&pm.EventID,
			&pm.TrackID,
			&pm.BBoxJSON,
			&pm.SnapshotPath,
			&pm.ActivityBrief,
			&pm.EventKind,
			&pm.Severity,
			&pm.MetadataJSON,
			&pm.DedupeKey,
			&pm.ReviewStatus,
			&pm.OperatorNote,
			&isFalseMatch,
			&reviewedAtRaw,
			&pm.CreatedAt,
		); err != nil {
			return nil, err
		}
		pm.IsFalseMatch = isFalseMatch != 0
		pm.ReviewedAt = dbValueToTime(reviewedAtRaw)
		out = append(out, &pm)
	}
	return out, rows.Err()
}

func (db *Database) QueryTimelineIdentities(userID int64, filter PersonTimelineFilter) ([]TimelineIdentity, error) {
	if db == nil || userID <= 0 {
		return nil, nil
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	where := []string{"user_id = ?"}
	args := []interface{}{userID}
	if !filter.StartAt.IsZero() || !filter.EndAt.IsZero() {
		if !filter.StartAt.IsZero() {
			where = append(where, "created_at >= ?")
			args = append(args, filter.StartAt.UTC())
		}
		if !filter.EndAt.IsZero() {
			where = append(where, "created_at < ?")
			args = append(args, filter.EndAt.UTC())
		}
	} else if strings.TrimSpace(filter.Date) != "" {
		where = append(where, "DATE(created_at, 'localtime') = ?")
		args = append(args, strings.TrimSpace(filter.Date))
	}
	if strings.TrimSpace(filter.ServiceID) != "" {
		where = append(where, "service_id = ?")
		args = append(args, strings.TrimSpace(filter.ServiceID))
	}
	if strings.TrimSpace(filter.GlobalPersonID) != "" {
		where = append(where, "global_person_id = ?")
		args = append(args, strings.TrimSpace(filter.GlobalPersonID))
	}
	if strings.TrimSpace(filter.CameraGroupID) != "" {
		where = append(where, `service_id IN (
			SELECT cgm.service_id
			FROM camera_group_members cgm
			JOIN camera_groups cg ON cg.id = cgm.group_id
			WHERE cg.user_id = ? AND cgm.group_id = ?
		)`)
		args = append(args, userID, strings.TrimSpace(filter.CameraGroupID))
	}
	if strings.TrimSpace(filter.EventKind) != "" {
		where = append(where, "event_kind = ?")
		args = append(args, strings.TrimSpace(filter.EventKind))
	}
	if strings.TrimSpace(filter.Severity) != "" {
		where = append(where, "severity = ?")
		args = append(args, strings.TrimSpace(filter.Severity))
	}
	if strings.TrimSpace(filter.ReviewStatus) != "" {
		where = append(where, "COALESCE(review_status,'unreviewed') = ?")
		args = append(args, strings.TrimSpace(filter.ReviewStatus))
	}
	if query := strings.ToLower(strings.TrimSpace(filter.Query)); query != "" {
		needle := "%" + query + "%"
		where = append(where, `(LOWER(COALESCE(activity_brief,'')) LIKE ? OR LOWER(COALESCE(person_name,'')) LIKE ? OR LOWER(COALESCE(camera_name,'')) LIKE ? OR LOWER(COALESCE(global_person_id,'')) LIKE ? OR LOWER(COALESCE(event_kind,'')) LIKE ? OR LOWER(COALESCE(severity,'')) LIKE ?)`)
		args = append(args, needle, needle, needle, needle, needle, needle)
	}
	switch strings.ToLower(strings.TrimSpace(filter.Known)) {
	case "known":
		where = append(where, "person_id IS NOT NULL AND person_id != ''")
	case "unknown":
		where = append(where, "(person_id IS NULL OR person_id = '')")
	}
	args = append(args, limit)

	rows, err := db.DB.Query(`
		WITH ranked AS (
			SELECT *,
			       ROW_NUMBER() OVER (PARTITION BY global_person_id ORDER BY created_at DESC) AS rn,
			       COUNT(*) OVER (PARTITION BY global_person_id) AS moment_count,
			       MIN(created_at) OVER (PARTITION BY global_person_id) AS first_seen_at,
			       MAX(created_at) OVER (PARTITION BY global_person_id) AS last_seen_at
			FROM person_sightings
			WHERE `+strings.Join(where, " AND ")+`
		)
		SELECT global_person_id, COALESCE(person_id,''), COALESCE(person_name,''),
		       COALESCE(identity_source,''), COALESCE(identity_confidence,0),
		       COALESCE(service_id,''), COALESCE(camera_name,''), first_seen_at,
		       last_seen_at, moment_count, COALESCE(snapshot_path,'')
		FROM ranked
		WHERE rn = 1
		ORDER BY last_seen_at DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]TimelineIdentity, 0, limit)
	for rows.Next() {
		var item TimelineIdentity
		var firstSeenRaw interface{}
		var lastSeenRaw interface{}
		if err := rows.Scan(
			&item.GlobalPersonID,
			&item.PersonID,
			&item.PersonName,
			&item.IdentitySource,
			&item.IdentityConfidence,
			&item.LastServiceID,
			&item.LastCameraName,
			&firstSeenRaw,
			&lastSeenRaw,
			&item.MomentCount,
			&item.SnapshotPath,
		); err != nil {
			return nil, err
		}
		item.FirstSeenAt = dbValueToTime(firstSeenRaw)
		item.LastSeenAt = dbValueToTime(lastSeenRaw)
		out = append(out, item)
	}
	return out, rows.Err()
}

type TimelineRouteNode struct {
	ServiceID  string    `json:"service_id"`
	CameraName string    `json:"camera_name,omitempty"`
	Zone       string    `json:"zone,omitempty"`
	X          float64   `json:"x"`
	Y          float64   `json:"y"`
	FirstSeen  time.Time `json:"first_seen_at"`
	LastSeen   time.Time `json:"last_seen_at"`
	Count      int       `json:"count"`
	Inferred   bool      `json:"inferred,omitempty"`
}

type TimelineJourneySegment struct {
	FromMomentID  string    `json:"from_moment_id"`
	ToMomentID    string    `json:"to_moment_id"`
	FromServiceID string    `json:"from_service_id"`
	ToServiceID   string    `json:"to_service_id"`
	FromAt        time.Time `json:"from_at"`
	ToAt          time.Time `json:"to_at"`
	GapSeconds    int64     `json:"gap_seconds"`
	DwellSeconds  int64     `json:"dwell_seconds"`
	Confidence    float64   `json:"confidence"`
	Inferred      bool      `json:"inferred"`
	Reason        string    `json:"reason"`
}

type TimelineJourneyStats struct {
	FirstSeenAt        time.Time        `json:"first_seen_at,omitempty"`
	LastSeenAt         time.Time        `json:"last_seen_at,omitempty"`
	MomentCount        int              `json:"moment_count"`
	CameraCount        int              `json:"camera_count"`
	DwellSeconds       int64            `json:"dwell_seconds"`
	AverageConfidence  float64          `json:"average_confidence"`
	GapsOver120Sec     int              `json:"gaps_over_120_sec"`
	FrequentRoutes     map[string]int   `json:"frequent_routes"`
	CameraDwellSeconds map[string]int64 `json:"camera_dwell_seconds"`
}

type TimelineNearbyActivity struct {
	AnchorMomentID string          `json:"anchor_moment_id"`
	WindowMinutes  int             `json:"window_minutes"`
	Count          int             `json:"count"`
	Moments        []*PersonMoment `json:"moments"`
}

type TimelineProximityEdge struct {
	GlobalPersonID string    `json:"global_person_id"`
	PersonName     string    `json:"person_name,omitempty"`
	ServiceID      string    `json:"service_id,omitempty"`
	CameraName     string    `json:"camera_name,omitempty"`
	OverlapCount   int       `json:"overlap_count"`
	WindowSeconds  int64     `json:"window_seconds"`
	LastSeenAt     time.Time `json:"last_seen_at,omitempty"`
}

type TimelinePredictedCamera struct {
	ServiceID   string  `json:"service_id"`
	CameraName  string  `json:"camera_name,omitempty"`
	Probability float64 `json:"probability"`
	Reason      string  `json:"reason"`
}

type TimelineAppearanceMatch struct {
	GlobalPersonID string    `json:"global_person_id"`
	PersonID       string    `json:"person_id,omitempty"`
	PersonName     string    `json:"person_name,omitempty"`
	MatchKind      string    `json:"match_kind"`
	Score          float64   `json:"score"`
	LastServiceID  string    `json:"last_service_id,omitempty"`
	LastCameraName string    `json:"last_camera_name,omitempty"`
	LastSeenAt     time.Time `json:"last_seen_at,omitempty"`
	MomentCount    int       `json:"moment_count"`
	SnapshotPath   string    `json:"snapshot_path,omitempty"`
}

type TimelineGhostStep struct {
	FromServiceID string    `json:"from_service_id"`
	ToServiceID   string    `json:"to_service_id"`
	StartAt       time.Time `json:"start_at"`
	EndAt         time.Time `json:"end_at"`
	Reason        string    `json:"reason"`
	Inferred      bool      `json:"inferred"`
}

type TimelineJourney struct {
	Identity       TimelineIdentity          `json:"identity"`
	Moments        []*PersonMoment           `json:"moments"`
	Route          []TimelineRouteNode       `json:"route"`
	Segments       []TimelineJourneySegment  `json:"segments"`
	Stats          TimelineJourneyStats      `json:"stats"`
	NearbyActivity []TimelineNearbyActivity  `json:"nearby_activity"`
	Proximity      []TimelineProximityEdge   `json:"proximity"`
	PredictedNext  []TimelinePredictedCamera `json:"predicted_next"`
	GhostTrail     []TimelineGhostStep       `json:"ghost_trail"`
}

type TimelineCase struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Owner     string    `json:"owner,omitempty"`
	Notes     string    `json:"notes,omitempty"`
	Reviewed  bool      `json:"reviewed"`
	ItemCount int       `json:"item_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type TimelineCasePatch struct {
	Title    string
	Status   string
	Owner    string
	Notes    string
	Reviewed *bool
}

type TimelineCaseItem struct {
	ID        string    `json:"id"`
	CaseID    string    `json:"case_id"`
	MomentID  string    `json:"moment_id"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type TimelineEvidenceLock struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CaseID    string    `json:"case_id,omitempty"`
	MomentIDs []string  `json:"moment_ids"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type TimelineTopologyNode struct {
	ServiceID          string    `json:"service_id"`
	Label              string    `json:"label,omitempty"`
	Zone               string    `json:"zone,omitempty"`
	X                  float64   `json:"x"`
	Y                  float64   `json:"y"`
	AdjacentServiceIDs []string  `json:"adjacent_service_ids"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type TimelineSnapshotHash struct {
	MomentID     string `json:"moment_id"`
	SnapshotPath string `json:"snapshot_path"`
	SHA256       string `json:"sha256,omitempty"`
	Error        string `json:"error,omitempty"`
}

type TimelineExportManifest struct {
	Case           TimelineCase           `json:"case"`
	GeneratedAt    time.Time              `json:"generated_at"`
	Moments        []*PersonMoment        `json:"moments"`
	CSVRows        []map[string]string    `json:"csv_rows"`
	SnapshotHashes []TimelineSnapshotHash `json:"snapshot_hashes"`
	Notes          string                 `json:"notes,omitempty"`
}

func (db *Database) QueryTimelineMoments(userID int64, filter PersonTimelineFilter) ([]*PersonMoment, error) {
	return db.QueryPersonTimeline(userID, filter)
}

func (db *Database) SearchTimelineAppearance(userID int64, globalPersonID, kind string, limit int) ([]TimelineAppearanceMatch, error) {
	globalPersonID = strings.TrimSpace(globalPersonID)
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = "any"
	}
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	if db == nil || userID <= 0 || globalPersonID == "" {
		return []TimelineAppearanceMatch{}, nil
	}

	targetBody, targetFace, targetCentroid, err := db.timelineIdentityEmbeddings(globalPersonID)
	if err != nil {
		return nil, err
	}
	targets := map[string][]float64{}
	if (kind == "any" || kind == "body") && len(targetBody) > 0 {
		targets["body"] = targetBody
	}
	if (kind == "any" || kind == "face") && len(targetFace) > 0 {
		targets["face"] = targetFace
	}
	if len(targets) == 0 && len(targetCentroid) > 0 {
		targets["centroid"] = targetCentroid
	}
	if len(targets) == 0 {
		return []TimelineAppearanceMatch{}, nil
	}

	rows, err := db.Query(`
		WITH ranked AS (
			SELECT ps.*,
			       ROW_NUMBER() OVER (PARTITION BY ps.global_person_id ORDER BY ps.created_at DESC) AS rn,
			       COUNT(*) OVER (PARTITION BY ps.global_person_id) AS moment_count
			FROM person_sightings ps
			WHERE ps.user_id = ? AND ps.global_person_id != ?
		)
		SELECT r.global_person_id, COALESCE(r.person_id,''), COALESCE(r.person_name,''),
		       COALESCE(r.service_id,''), COALESCE(r.camera_name,''), r.created_at,
		       r.moment_count, COALESCE(r.snapshot_path,''),
		       COALESCE(pgi.body_prototype,''), COALESCE(pgi.face_prototype,''), COALESCE(pgi.embedding_centroid,'')
		FROM ranked r
		LEFT JOIN person_global_identities pgi ON pgi.global_person_id = r.global_person_id
		WHERE r.rn = 1
	`, userID, globalPersonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []TimelineAppearanceMatch{}
	for rows.Next() {
		var match TimelineAppearanceMatch
		var lastSeenRaw interface{}
		var bodyRaw, faceRaw, centroidRaw string
		if err := rows.Scan(
			&match.GlobalPersonID,
			&match.PersonID,
			&match.PersonName,
			&match.LastServiceID,
			&match.LastCameraName,
			&lastSeenRaw,
			&match.MomentCount,
			&match.SnapshotPath,
			&bodyRaw,
			&faceRaw,
			&centroidRaw,
		); err != nil {
			return nil, err
		}
		candidates := map[string][]float64{
			"body":     parseEmbeddingJSON(bodyRaw),
			"face":     parseEmbeddingJSON(faceRaw),
			"centroid": parseEmbeddingJSON(centroidRaw),
		}
		bestKind := ""
		bestScore := 0.0
		for targetKind, targetEmbedding := range targets {
			candidate := candidates[targetKind]
			if len(candidate) == 0 && targetKind != "centroid" {
				candidate = candidates["centroid"]
			}
			score := cosineSimilarity(targetEmbedding, candidate)
			if score > bestScore {
				bestScore = score
				bestKind = targetKind
			}
		}
		if bestScore <= 0 {
			continue
		}
		match.MatchKind = bestKind
		match.Score = bestScore
		match.LastSeenAt = dbValueToTime(lastSeenRaw)
		out = append(out, match)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].LastSeenAt.After(out[j].LastSeenAt)
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > limit {
		return out[:limit], nil
	}
	return out, nil
}

func (db *Database) timelineIdentityEmbeddings(globalPersonID string) ([]float64, []float64, []float64, error) {
	var bodyRaw, faceRaw, centroidRaw string
	err := db.QueryRow(`
		SELECT COALESCE(body_prototype,''), COALESCE(face_prototype,''), COALESCE(embedding_centroid,'')
		FROM person_global_identities
		WHERE global_person_id = ?
	`, strings.TrimSpace(globalPersonID)).Scan(&bodyRaw, &faceRaw, &centroidRaw)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, err
	}
	return parseEmbeddingJSON(bodyRaw), parseEmbeddingJSON(faceRaw), parseEmbeddingJSON(centroidRaw), nil
}

func (db *Database) BuildTimelineJourney(userID int64, globalPersonID string, filter PersonTimelineFilter) (*TimelineJourney, error) {
	globalPersonID = strings.TrimSpace(globalPersonID)
	if db == nil || userID <= 0 || globalPersonID == "" {
		return &TimelineJourney{}, nil
	}
	filter.GlobalPersonID = globalPersonID
	if filter.Limit <= 0 {
		filter.Limit = 500
	}
	momentsDesc, err := db.QueryPersonTimeline(userID, filter)
	if err != nil {
		return nil, err
	}
	moments := reversePersonMoments(momentsDesc)
	journey := &TimelineJourney{
		Moments:        moments,
		Stats:          TimelineJourneyStats{FrequentRoutes: map[string]int{}, CameraDwellSeconds: map[string]int64{}},
		NearbyActivity: []TimelineNearbyActivity{},
		Proximity:      []TimelineProximityEdge{},
		PredictedNext:  []TimelinePredictedCamera{},
		GhostTrail:     []TimelineGhostStep{},
	}
	if len(moments) == 0 {
		journey.Identity = TimelineIdentity{GlobalPersonID: globalPersonID}
		return journey, nil
	}

	journey.Identity = timelineIdentityFromMoments(globalPersonID, moments)
	routeByService := map[string]*TimelineRouteNode{}
	cameraSet := map[string]bool{}
	confidenceSum := 0.0
	confidenceCount := 0
	for i, m := range moments {
		if m == nil {
			continue
		}
		if i == 0 {
			journey.Stats.FirstSeenAt = m.CreatedAt
		}
		journey.Stats.LastSeenAt = m.CreatedAt
		journey.Stats.MomentCount++
		if m.ServiceID != "" {
			cameraSet[m.ServiceID] = true
			if routeByService[m.ServiceID] == nil {
				routeByService[m.ServiceID] = &TimelineRouteNode{
					ServiceID:  m.ServiceID,
					CameraName: m.CameraName,
					X:          0.5,
					Y:          0.5,
					FirstSeen:  m.CreatedAt,
					LastSeen:   m.CreatedAt,
				}
			}
			node := routeByService[m.ServiceID]
			if m.CameraName != "" {
				node.CameraName = m.CameraName
			}
			node.Count++
			if m.CreatedAt.Before(node.FirstSeen) || node.FirstSeen.IsZero() {
				node.FirstSeen = m.CreatedAt
			}
			if m.CreatedAt.After(node.LastSeen) {
				node.LastSeen = m.CreatedAt
			}
		}
		if m.IdentityConfidence > 0 {
			confidenceSum += m.IdentityConfidence
			confidenceCount++
		}
	}
	journey.Stats.CameraCount = len(cameraSet)
	if confidenceCount > 0 {
		journey.Stats.AverageConfidence = confidenceSum / float64(confidenceCount)
	}

	topology, _ := db.GetTimelineTopology(userID)
	topologyByService := map[string]TimelineTopologyNode{}
	for _, node := range topology {
		topologyByService[node.ServiceID] = node
		if routeNode := routeByService[node.ServiceID]; routeNode != nil {
			routeNode.Zone = node.Zone
			if node.X >= 0 && node.X <= 1 {
				routeNode.X = node.X
			}
			if node.Y >= 0 && node.Y <= 1 {
				routeNode.Y = node.Y
			}
			if node.Label != "" && routeNode.CameraName == "" {
				routeNode.CameraName = node.Label
			}
		}
	}

	for i := 1; i < len(moments); i++ {
		prev := moments[i-1]
		next := moments[i]
		if prev == nil || next == nil {
			continue
		}
		gap := next.CreatedAt.Sub(prev.CreatedAt)
		if gap < 0 {
			gap = 0
		}
		dwell := int64(0)
		if prev.ServiceID == next.ServiceID {
			dwell = int64(gap.Seconds())
			journey.Stats.DwellSeconds += dwell
			journey.Stats.CameraDwellSeconds[prev.ServiceID] += dwell
		}
		routeKey := prev.ServiceID + " -> " + next.ServiceID
		if prev.ServiceID != "" && next.ServiceID != "" && prev.ServiceID != next.ServiceID {
			journey.Stats.FrequentRoutes[routeKey]++
		}
		inferred := gap > 120*time.Second && prev.ServiceID != "" && next.ServiceID != "" && prev.ServiceID != next.ServiceID
		if inferred {
			journey.Stats.GapsOver120Sec++
			journey.GhostTrail = append(journey.GhostTrail, TimelineGhostStep{
				FromServiceID: prev.ServiceID,
				ToServiceID:   next.ServiceID,
				StartAt:       prev.CreatedAt,
				EndAt:         next.CreatedAt,
				Reason:        "camera handoff gap exceeds 120 seconds; route is inferred from timestamps and topology",
				Inferred:      true,
			})
		}
		confidence := 0.65
		if prev.IdentityConfidence > 0 && next.IdentityConfidence > 0 {
			confidence = (prev.IdentityConfidence + next.IdentityConfidence) / 2
		}
		journey.Segments = append(journey.Segments, TimelineJourneySegment{
			FromMomentID:  prev.ID,
			ToMomentID:    next.ID,
			FromServiceID: prev.ServiceID,
			ToServiceID:   next.ServiceID,
			FromAt:        prev.CreatedAt,
			ToAt:          next.CreatedAt,
			GapSeconds:    int64(gap.Seconds()),
			DwellSeconds:  dwell,
			Confidence:    confidence,
			Inferred:      inferred,
			Reason:        timelineSegmentReason(prev, next, topologyByService),
		})
	}

	for _, node := range routeByService {
		journey.Route = append(journey.Route, *node)
	}
	sort.SliceStable(journey.Route, func(i, j int) bool {
		return journey.Route[i].FirstSeen.Before(journey.Route[j].FirstSeen)
	})

	anchor := timelineAnchorMoment(moments, filter.AnchorMomentID)
	journey.NearbyActivity = db.timelineNearbyActivity(userID, anchor)
	journey.Proximity = db.timelineProximity(userID, globalPersonID, moments)
	journey.PredictedNext = db.timelinePredictedNextCameras(userID, globalPersonID, moments, topologyByService)
	return journey, nil
}

func reversePersonMoments(in []*PersonMoment) []*PersonMoment {
	out := make([]*PersonMoment, 0, len(in))
	for i := len(in) - 1; i >= 0; i-- {
		out = append(out, in[i])
	}
	return out
}

func timelineIdentityFromMoments(globalPersonID string, moments []*PersonMoment) TimelineIdentity {
	out := TimelineIdentity{GlobalPersonID: globalPersonID}
	for i, m := range moments {
		if m == nil {
			continue
		}
		if i == 0 || out.FirstSeenAt.IsZero() || m.CreatedAt.Before(out.FirstSeenAt) {
			out.FirstSeenAt = m.CreatedAt
		}
		if m.CreatedAt.After(out.LastSeenAt) {
			out.LastSeenAt = m.CreatedAt
			out.LastServiceID = m.ServiceID
			out.LastCameraName = m.CameraName
			out.SnapshotPath = m.SnapshotPath
			out.PersonID = m.PersonID
			out.PersonName = m.PersonName
			out.IdentitySource = m.IdentitySource
			out.IdentityConfidence = m.IdentityConfidence
		}
		out.MomentCount++
	}
	return out
}

func timelineAnchorMoment(moments []*PersonMoment, anchorID string) *PersonMoment {
	anchorID = strings.TrimSpace(anchorID)
	if anchorID != "" {
		for _, m := range moments {
			if m != nil && m.ID == anchorID {
				return m
			}
		}
	}
	if len(moments) == 0 {
		return nil
	}
	return moments[len(moments)-1]
}

func timelineSegmentReason(prev, next *PersonMoment, topology map[string]TimelineTopologyNode) string {
	if prev == nil || next == nil {
		return ""
	}
	if prev.ServiceID == next.ServiceID {
		return "same-camera dwell inferred from consecutive sightings"
	}
	if node, ok := topology[prev.ServiceID]; ok {
		for _, adjacent := range node.AdjacentServiceIDs {
			if adjacent == next.ServiceID {
				return "direct topology adjacency and chronological handoff"
			}
		}
	}
	return "chronological cross-camera handoff"
}

func (db *Database) timelineNearbyActivity(userID int64, anchor *PersonMoment) []TimelineNearbyActivity {
	if db == nil || userID <= 0 || anchor == nil {
		return []TimelineNearbyActivity{}
	}
	out := make([]TimelineNearbyActivity, 0, 3)
	for _, minutes := range []int{5, 15, 30} {
		start := anchor.CreatedAt.Add(-time.Duration(minutes) * time.Minute)
		end := anchor.CreatedAt.Add(time.Duration(minutes) * time.Minute)
		where := []string{"user_id = ?", "id != ?", "created_at >= ?", "created_at <= ?"}
		args := []interface{}{userID, anchor.ID, start.UTC(), end.UTC()}
		if anchor.ServiceID != "" {
			where = append(where, "service_id = ?")
			args = append(args, anchor.ServiceID)
		}
		args = append(args, 25)
		rows, err := db.DB.Query(`
			SELECT id, global_person_id, COALESCE(person_id,''), COALESCE(person_name,''),
			       COALESCE(identity_source,''), COALESCE(identity_confidence,0),
			       COALESCE(service_id,''), COALESCE(camera_name,''), COALESCE(event_id,''),
			       COALESCE(track_id,-1), COALESCE(bbox_json,''), COALESCE(snapshot_path,''),
			       COALESCE(activity_brief,''), COALESCE(event_kind,''), COALESCE(severity,''),
			       COALESCE(metadata_json,'{}'), COALESCE(dedupe_key,''),
			       COALESCE(review_status,'unreviewed'), COALESCE(operator_note,''),
			       COALESCE(is_false_match,0), reviewed_at, created_at
			FROM person_sightings
			WHERE `+strings.Join(where, " AND ")+`
			ORDER BY created_at ASC
			LIMIT ?
		`, args...)
		if err != nil {
			out = append(out, TimelineNearbyActivity{AnchorMomentID: anchor.ID, WindowMinutes: minutes, Moments: []*PersonMoment{}})
			continue
		}
		moments, err := scanPersonMoments(rows, 25)
		_ = rows.Close()
		if err != nil {
			moments = []*PersonMoment{}
		}
		out = append(out, TimelineNearbyActivity{
			AnchorMomentID: anchor.ID,
			WindowMinutes:  minutes,
			Count:          len(moments),
			Moments:        moments,
		})
	}
	return out
}

func (db *Database) timelineProximity(userID int64, globalPersonID string, moments []*PersonMoment) []TimelineProximityEdge {
	if db == nil || userID <= 0 || globalPersonID == "" || len(moments) == 0 {
		return []TimelineProximityEdge{}
	}
	type agg struct {
		edge TimelineProximityEdge
	}
	edges := map[string]*agg{}
	for _, anchor := range moments {
		if anchor == nil || anchor.ServiceID == "" {
			continue
		}
		start := anchor.CreatedAt.Add(-90 * time.Second)
		end := anchor.CreatedAt.Add(90 * time.Second)
		rows, err := db.DB.Query(`
			SELECT global_person_id, COALESCE(person_name,''), COALESCE(service_id,''), COALESCE(camera_name,''), COUNT(*), MAX(created_at)
			FROM person_sightings
			WHERE user_id = ? AND global_person_id != ? AND service_id = ? AND created_at >= ? AND created_at <= ?
			GROUP BY global_person_id, person_name, service_id, camera_name
			ORDER BY COUNT(*) DESC
			LIMIT 20
		`, userID, globalPersonID, anchor.ServiceID, start.UTC(), end.UTC())
		if err != nil {
			continue
		}
		for rows.Next() {
			var id, name, serviceID, cameraName string
			var count int
			var lastRaw interface{}
			if err := rows.Scan(&id, &name, &serviceID, &cameraName, &count, &lastRaw); err != nil {
				continue
			}
			key := id + "|" + serviceID
			if edges[key] == nil {
				edges[key] = &agg{edge: TimelineProximityEdge{
					GlobalPersonID: id,
					PersonName:     name,
					ServiceID:      serviceID,
					CameraName:     cameraName,
					WindowSeconds:  180,
				}}
			}
			edges[key].edge.OverlapCount += count
			lastSeen := dbValueToTime(lastRaw)
			if lastSeen.After(edges[key].edge.LastSeenAt) {
				edges[key].edge.LastSeenAt = lastSeen
			}
		}
		_ = rows.Close()
	}
	out := make([]TimelineProximityEdge, 0, len(edges))
	for _, item := range edges {
		out = append(out, item.edge)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OverlapCount == out[j].OverlapCount {
			return out[i].LastSeenAt.After(out[j].LastSeenAt)
		}
		return out[i].OverlapCount > out[j].OverlapCount
	})
	if len(out) > 20 {
		return out[:20]
	}
	return out
}

func (db *Database) timelinePredictedNextCameras(userID int64, globalPersonID string, moments []*PersonMoment, topology map[string]TimelineTopologyNode) []TimelinePredictedCamera {
	if db == nil || userID <= 0 || globalPersonID == "" || len(moments) == 0 {
		return []TimelinePredictedCamera{}
	}
	last := moments[len(moments)-1]
	if last == nil || last.ServiceID == "" {
		return []TimelinePredictedCamera{}
	}
	history, err := db.QueryPersonTimeline(userID, PersonTimelineFilter{GlobalPersonID: globalPersonID, Limit: 500})
	if err != nil {
		return []TimelinePredictedCamera{}
	}
	history = reversePersonMoments(history)
	counts := map[string]int{}
	names := map[string]string{}
	total := 0
	for i := 1; i < len(history); i++ {
		prev, next := history[i-1], history[i]
		if prev == nil || next == nil || prev.ServiceID != last.ServiceID || next.ServiceID == "" || next.ServiceID == prev.ServiceID {
			continue
		}
		counts[next.ServiceID]++
		names[next.ServiceID] = next.CameraName
		total++
	}
	out := make([]TimelinePredictedCamera, 0, len(counts))
	for serviceID, count := range counts {
		probability := float64(count) / float64(total)
		out = append(out, TimelinePredictedCamera{
			ServiceID:   serviceID,
			CameraName:  names[serviceID],
			Probability: probability,
			Reason:      "historical transition from current camera",
		})
	}
	if len(out) == 0 {
		if node, ok := topology[last.ServiceID]; ok && len(node.AdjacentServiceIDs) > 0 {
			probability := 1.0 / float64(len(node.AdjacentServiceIDs))
			for _, serviceID := range node.AdjacentServiceIDs {
				out = append(out, TimelinePredictedCamera{
					ServiceID:   serviceID,
					Probability: probability,
					Reason:      "topology adjacency fallback",
				})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Probability > out[j].Probability })
	if len(out) > 5 {
		return out[:5]
	}
	return out
}

func normalizeTimelineReviewStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "reviewed", "needs_review", "false_match", "pinned", "unreviewed":
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return "reviewed"
	}
}

func (db *Database) auditTimelineAction(userID int64, action, subjectID string, payload interface{}) error {
	if db == nil || userID <= 0 || strings.TrimSpace(action) == "" {
		return nil
	}
	payloadJSON := "{}"
	if payload != nil {
		payloadJSON = mustJSON(payload, "{}")
	}
	_, err := db.execWrite(`
		INSERT INTO timeline_audit_log (id, user_id, action, subject_id, payload_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, uuid.New().String(), userID, strings.TrimSpace(action), strings.TrimSpace(subjectID), payloadJSON, time.Now().UTC())
	return err
}

func (db *Database) ReviewTimelineMoment(userID int64, momentID, status, note string, falseMatch bool) error {
	if db == nil || userID <= 0 || strings.TrimSpace(momentID) == "" {
		return fmt.Errorf("moment id required")
	}
	status = normalizeTimelineReviewStatus(status)
	if falseMatch {
		status = "false_match"
	}
	reviewedAt := time.Now().UTC()
	res, err := db.execWrite(`
		UPDATE person_sightings
		SET review_status = ?, operator_note = ?, is_false_match = ?, reviewed_at = ?
		WHERE user_id = ? AND id = ?
	`, status, strings.TrimSpace(note), boolInt(falseMatch), reviewedAt, userID, strings.TrimSpace(momentID))
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return db.auditTimelineAction(userID, "moment_review", momentID, map[string]interface{}{
		"status":         status,
		"note":           strings.TrimSpace(note),
		"is_false_match": falseMatch,
	})
}

func (db *Database) AuditTimelineReveal(userID int64, momentID string) error {
	momentID = strings.TrimSpace(momentID)
	if db == nil || userID <= 0 || momentID == "" {
		return fmt.Errorf("moment id required")
	}
	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM person_sightings WHERE user_id = ? AND id = ?`, userID, momentID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return sql.ErrNoRows
	}
	return db.auditTimelineAction(userID, "privacy_reveal", momentID, map[string]string{"moment_id": momentID})
}

func (db *Database) AssignTimelineIdentity(userID int64, globalPersonID, personID string) (string, error) {
	globalPersonID = strings.TrimSpace(globalPersonID)
	personID = strings.TrimSpace(personID)
	if db == nil || userID <= 0 || globalPersonID == "" || personID == "" {
		return "", fmt.Errorf("global person id and person id are required")
	}
	var personName string
	if err := db.QueryRow(`SELECT name FROM persons WHERE id = ? AND user_id = ?`, personID, userID).Scan(&personName); err != nil {
		return "", err
	}
	targetGlobalID := "person:" + personID
	res, err := db.execWrite(`
		UPDATE person_sightings
		SET global_person_id = ?, person_id = ?, person_name = ?, identity_source = 'operator',
		    identity_confidence = 1, review_status = 'reviewed', operator_note = 'assigned to registered person',
		    reviewed_at = ?
		WHERE user_id = ? AND global_person_id = ?
	`, targetGlobalID, personID, personName, time.Now().UTC(), userID, globalPersonID)
	if err != nil {
		return "", err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return "", sql.ErrNoRows
	}
	now := time.Now().UTC()
	if err := db.UpsertPersonGlobalIdentityRecord(PersonGlobalIdentityRecord{
		GlobalPersonID:     targetGlobalID,
		PersonID:           personID,
		PersonName:         personName,
		IdentitySource:     "operator",
		IdentityConfidence: 1,
		SeenAt:             now,
		LastObservationAt:  now,
	}); err != nil {
		return "", err
	}
	if globalPersonID != targetGlobalID {
		_ = db.UpsertPersonGlobalIdentityRecord(PersonGlobalIdentityRecord{
			GlobalPersonID:    globalPersonID,
			AliasOf:           targetGlobalID,
			SeenAt:            now,
			LastObservationAt: now,
		})
	}
	return targetGlobalID, db.auditTimelineAction(userID, "identity_assign", globalPersonID, map[string]string{
		"target_global_person_id": targetGlobalID,
		"person_id":               personID,
		"person_name":             personName,
	})
}

func (db *Database) MergeTimelineIdentities(userID int64, sourceID, targetID string) error {
	sourceID = strings.TrimSpace(sourceID)
	targetID = strings.TrimSpace(targetID)
	if db == nil || userID <= 0 || sourceID == "" || targetID == "" || sourceID == targetID {
		return fmt.Errorf("source and target identities are required")
	}
	personID, personName := db.timelineTargetPersonFields(userID, targetID)
	res, err := db.execWrite(`
		UPDATE person_sightings
		SET global_person_id = ?,
		    person_id = CASE WHEN ? != '' THEN ? ELSE person_id END,
		    person_name = CASE WHEN ? != '' THEN ? ELSE person_name END,
		    identity_source = CASE WHEN ? != '' THEN 'operator_merge' ELSE identity_source END,
		    identity_confidence = CASE WHEN ? != '' THEN MAX(COALESCE(identity_confidence, 0), 0.9) ELSE identity_confidence END,
		    review_status = 'reviewed',
		    operator_note = 'merged identity',
		    reviewed_at = ?
		WHERE user_id = ? AND global_person_id = ?
	`, targetID, personID, personID, personName, personName, personID, personID, time.Now().UTC(), userID, sourceID)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	_ = db.UpsertPersonGlobalIdentityRecord(PersonGlobalIdentityRecord{
		GlobalPersonID:    sourceID,
		AliasOf:           targetID,
		SeenAt:            time.Now().UTC(),
		LastObservationAt: time.Now().UTC(),
	})
	return db.auditTimelineAction(userID, "identity_merge", sourceID, map[string]string{"target_global_person_id": targetID})
}

func (db *Database) timelineTargetPersonFields(userID int64, targetID string) (string, string) {
	var personID, personName string
	_ = db.QueryRow(`
		SELECT COALESCE(person_id,''), COALESCE(person_name,'')
		FROM person_sightings
		WHERE user_id = ? AND global_person_id = ? AND COALESCE(person_id,'') != ''
		ORDER BY created_at DESC
		LIMIT 1
	`, userID, targetID).Scan(&personID, &personName)
	if personID == "" && strings.HasPrefix(targetID, "person:") {
		personID = strings.TrimSpace(strings.TrimPrefix(targetID, "person:"))
		_ = db.QueryRow(`SELECT name FROM persons WHERE id = ? AND user_id = ?`, personID, userID).Scan(&personName)
	}
	return personID, personName
}

func (db *Database) SplitTimelineMoments(userID int64, sourceID string, momentIDs []string) (string, error) {
	sourceID = strings.TrimSpace(sourceID)
	momentIDs = sanitizeTimelineIDs(momentIDs)
	if db == nil || userID <= 0 || len(momentIDs) == 0 {
		return "", fmt.Errorf("moment ids are required")
	}
	newID := "unknown:" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	placeholders := make([]string, 0, len(momentIDs))
	args := []interface{}{newID, time.Now().UTC(), userID}
	for _, id := range momentIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	where := "user_id = ? AND id IN (" + strings.Join(placeholders, ",") + ")"
	if sourceID != "" {
		where += " AND global_person_id = ?"
		args = append(args, sourceID)
	}
	res, err := db.execWrite(`
		UPDATE person_sightings
		SET global_person_id = ?, person_id = '', person_name = '', identity_source = 'operator_split',
		    identity_confidence = 0.5, review_status = 'needs_review', operator_note = 'split into new identity',
		    reviewed_at = ?
		WHERE `+where, args...)
	if err != nil {
		return "", err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return "", sql.ErrNoRows
	}
	now := time.Now().UTC()
	_ = db.UpsertPersonGlobalIdentityRecord(PersonGlobalIdentityRecord{
		GlobalPersonID:    newID,
		IdentitySource:    "operator_split",
		LastObservationAt: now,
		SeenAt:            now,
	})
	return newID, db.auditTimelineAction(userID, "identity_split", sourceID, map[string]interface{}{
		"new_global_person_id": newID,
		"moment_ids":           momentIDs,
	})
}

func sanitizeTimelineIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (db *Database) CreateTimelineCase(userID int64, title, owner, notes string) (*TimelineCase, error) {
	if db == nil || userID <= 0 {
		return nil, fmt.Errorf("user id required")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Untitled timeline case"
	}
	now := time.Now().UTC()
	item := &TimelineCase{
		ID:        uuid.New().String(),
		Title:     title,
		Status:    "open",
		Owner:     strings.TrimSpace(owner),
		Notes:     strings.TrimSpace(notes),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.execWrite(`
		INSERT INTO timeline_cases (id, user_id, title, status, owner, notes, reviewed, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, item.ID, userID, item.Title, item.Status, item.Owner, item.Notes, boolInt(item.Reviewed), item.CreatedAt, item.UpdatedAt); err != nil {
		return nil, err
	}
	return item, db.auditTimelineAction(userID, "case_create", item.ID, item)
}

func (db *Database) ListTimelineCases(userID int64) ([]TimelineCase, error) {
	if db == nil || userID <= 0 {
		return nil, nil
	}
	rows, err := db.DB.Query(`
		SELECT c.id, c.title, c.status, COALESCE(c.owner,''), COALESCE(c.notes,''), COALESCE(c.reviewed,0),
		       c.created_at, c.updated_at, COUNT(ci.id)
		FROM timeline_cases c
		LEFT JOIN timeline_case_items ci ON ci.case_id = c.id
		WHERE c.user_id = ?
		GROUP BY c.id, c.title, c.status, c.owner, c.notes, c.reviewed, c.created_at, c.updated_at
		ORDER BY c.updated_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TimelineCase{}
	for rows.Next() {
		var item TimelineCase
		var reviewed int
		var createdRaw, updatedRaw interface{}
		if err := rows.Scan(&item.ID, &item.Title, &item.Status, &item.Owner, &item.Notes, &reviewed, &createdRaw, &updatedRaw, &item.ItemCount); err != nil {
			return nil, err
		}
		item.Reviewed = reviewed != 0
		item.CreatedAt = dbValueToTime(createdRaw)
		item.UpdatedAt = dbValueToTime(updatedRaw)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *Database) GetTimelineCase(userID int64, caseID string) (TimelineCase, error) {
	caseID = strings.TrimSpace(caseID)
	var item TimelineCase
	if db == nil || userID <= 0 || caseID == "" {
		return item, fmt.Errorf("case id required")
	}
	var reviewed int
	var createdRaw, updatedRaw interface{}
	err := db.QueryRow(`
		SELECT c.id, c.title, c.status, COALESCE(c.owner,''), COALESCE(c.notes,''), COALESCE(c.reviewed,0),
		       c.created_at, c.updated_at, COUNT(ci.id)
		FROM timeline_cases c
		LEFT JOIN timeline_case_items ci ON ci.case_id = c.id
		WHERE c.user_id = ? AND c.id = ?
		GROUP BY c.id, c.title, c.status, c.owner, c.notes, c.reviewed, c.created_at, c.updated_at
	`, userID, caseID).Scan(&item.ID, &item.Title, &item.Status, &item.Owner, &item.Notes, &reviewed, &createdRaw, &updatedRaw, &item.ItemCount)
	if err != nil {
		return item, err
	}
	item.Reviewed = reviewed != 0
	item.CreatedAt = dbValueToTime(createdRaw)
	item.UpdatedAt = dbValueToTime(updatedRaw)
	return item, nil
}

func (db *Database) UpdateTimelineCase(userID int64, caseID string, patch TimelineCasePatch) (*TimelineCase, error) {
	existing, err := db.GetTimelineCase(userID, caseID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(patch.Title) != "" {
		existing.Title = strings.TrimSpace(patch.Title)
	}
	if strings.TrimSpace(patch.Status) != "" {
		existing.Status = strings.TrimSpace(patch.Status)
	}
	if patch.Owner != "" {
		existing.Owner = strings.TrimSpace(patch.Owner)
	}
	if patch.Notes != "" {
		existing.Notes = strings.TrimSpace(patch.Notes)
	}
	if patch.Reviewed != nil {
		existing.Reviewed = *patch.Reviewed
	}
	existing.UpdatedAt = time.Now().UTC()
	if _, err := db.execWrite(`
		UPDATE timeline_cases
		SET title = ?, status = ?, owner = ?, notes = ?, reviewed = ?, updated_at = ?
		WHERE user_id = ? AND id = ?
	`, existing.Title, existing.Status, existing.Owner, existing.Notes, boolInt(existing.Reviewed), existing.UpdatedAt, userID, caseID); err != nil {
		return nil, err
	}
	return &existing, db.auditTimelineAction(userID, "case_update", caseID, existing)
}

func (db *Database) AddTimelineCaseItems(userID int64, caseID string, momentIDs []string, note string) ([]TimelineCaseItem, error) {
	caseID = strings.TrimSpace(caseID)
	momentIDs = sanitizeTimelineIDs(momentIDs)
	if db == nil || userID <= 0 || caseID == "" || len(momentIDs) == 0 {
		return nil, fmt.Errorf("case id and moment ids are required")
	}
	if _, err := db.GetTimelineCase(userID, caseID); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := make([]TimelineCaseItem, 0, len(momentIDs))
	for _, momentID := range momentIDs {
		var exists int
		if err := db.QueryRow(`SELECT COUNT(*) FROM person_sightings WHERE user_id = ? AND id = ?`, userID, momentID).Scan(&exists); err != nil || exists == 0 {
			continue
		}
		item := TimelineCaseItem{ID: uuid.New().String(), CaseID: caseID, MomentID: momentID, Note: strings.TrimSpace(note), CreatedAt: now}
		_, err := db.execWrite(`
			INSERT INTO timeline_case_items (id, case_id, moment_id, note, created_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(case_id, moment_id) DO UPDATE SET note = excluded.note
		`, item.ID, item.CaseID, item.MomentID, item.Note, item.CreatedAt)
		if err != nil {
			return out, err
		}
		out = append(out, item)
	}
	_, _ = db.execWrite(`UPDATE timeline_cases SET updated_at = ? WHERE user_id = ? AND id = ?`, now, userID, caseID)
	return out, db.auditTimelineAction(userID, "case_items_add", caseID, map[string]interface{}{"moment_ids": momentIDs, "note": strings.TrimSpace(note)})
}

func (db *Database) CreateTimelineEvidenceLock(userID int64, title, caseID string, momentIDs []string, expiresAt time.Time) (*TimelineEvidenceLock, error) {
	if db == nil || userID <= 0 {
		return nil, fmt.Errorf("user id required")
	}
	momentIDs = sanitizeTimelineIDs(momentIDs)
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Timeline evidence lock"
	}
	now := time.Now().UTC()
	lock := &TimelineEvidenceLock{ID: uuid.New().String(), Title: title, CaseID: strings.TrimSpace(caseID), MomentIDs: momentIDs, ExpiresAt: expiresAt, CreatedAt: now}
	expiresValue := interface{}(nil)
	if !expiresAt.IsZero() {
		expiresValue = expiresAt.UTC()
	}
	if _, err := db.execWrite(`
		INSERT INTO timeline_evidence_locks (id, user_id, title, case_id, moment_ids_json, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, lock.ID, userID, lock.Title, lock.CaseID, mustJSON(momentIDs, "[]"), expiresValue, lock.CreatedAt); err != nil {
		return nil, err
	}
	return lock, db.auditTimelineAction(userID, "evidence_lock_create", lock.ID, lock)
}

func (db *Database) GetTimelineTopology(userID int64) ([]TimelineTopologyNode, error) {
	if db == nil || userID <= 0 {
		return nil, nil
	}
	rows, err := db.Query(`
		SELECT service_id, COALESCE(label,''), COALESCE(zone,''), COALESCE(x,0.5), COALESCE(y,0.5),
		       COALESCE(adjacent_service_ids,'[]'), updated_at
		FROM timeline_topology
		WHERE user_id = ?
		ORDER BY label ASC, service_id ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TimelineTopologyNode{}
	for rows.Next() {
		var item TimelineTopologyNode
		var adjacentJSON string
		var updatedRaw interface{}
		if err := rows.Scan(&item.ServiceID, &item.Label, &item.Zone, &item.X, &item.Y, &adjacentJSON, &updatedRaw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(adjacentJSON), &item.AdjacentServiceIDs)
		item.UpdatedAt = dbValueToTime(updatedRaw)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *Database) UpsertTimelineTopology(userID int64, nodes []TimelineTopologyNode) ([]TimelineTopologyNode, error) {
	if db == nil || userID <= 0 {
		return nil, fmt.Errorf("user id required")
	}
	now := time.Now().UTC()
	for _, node := range nodes {
		node.ServiceID = strings.TrimSpace(node.ServiceID)
		if node.ServiceID == "" {
			continue
		}
		if node.X < 0 {
			node.X = 0
		}
		if node.X > 1 {
			node.X = 1
		}
		if node.Y < 0 {
			node.Y = 0
		}
		if node.Y > 1 {
			node.Y = 1
		}
		_, err := db.execWrite(`
			INSERT INTO timeline_topology (service_id, user_id, label, zone, x, y, adjacent_service_ids, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id, service_id) DO UPDATE SET
				label = excluded.label,
				zone = excluded.zone,
				x = excluded.x,
				y = excluded.y,
				adjacent_service_ids = excluded.adjacent_service_ids,
				updated_at = excluded.updated_at
		`, node.ServiceID, userID, strings.TrimSpace(node.Label), strings.TrimSpace(node.Zone), node.X, node.Y, mustJSON(sanitizeTimelineIDs(node.AdjacentServiceIDs), "[]"), now)
		if err != nil {
			return nil, err
		}
	}
	_ = db.auditTimelineAction(userID, "topology_update", "", nodes)
	return db.GetTimelineTopology(userID)
}

func (db *Database) ExportTimelineCase(userID int64, caseID string) (*TimelineExportManifest, error) {
	caseItem, err := db.GetTimelineCase(userID, caseID)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
		SELECT ps.id, ps.global_person_id, COALESCE(ps.person_id,''), COALESCE(ps.person_name,''),
		       COALESCE(ps.identity_source,''), COALESCE(ps.identity_confidence,0),
		       COALESCE(ps.service_id,''), COALESCE(ps.camera_name,''), COALESCE(ps.event_id,''),
		       COALESCE(ps.track_id,-1), COALESCE(ps.bbox_json,''), COALESCE(ps.snapshot_path,''),
		       COALESCE(ps.activity_brief,''), COALESCE(ps.event_kind,''), COALESCE(ps.severity,''),
		       COALESCE(ps.metadata_json,'{}'), COALESCE(ps.dedupe_key,''),
		       COALESCE(ps.review_status,'unreviewed'), COALESCE(ps.operator_note,''),
		       COALESCE(ps.is_false_match,0), ps.reviewed_at, ps.created_at
		FROM timeline_case_items ci
		JOIN person_sightings ps ON ps.id = ci.moment_id
		WHERE ci.case_id = ? AND ps.user_id = ?
		ORDER BY ps.created_at ASC
	`, strings.TrimSpace(caseID), userID)
	if err != nil {
		return nil, err
	}
	moments, err := scanPersonMoments(rows, 500)
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	manifest := &TimelineExportManifest{
		Case:        caseItem,
		GeneratedAt: time.Now().UTC(),
		Moments:     moments,
		CSVRows:     make([]map[string]string, 0, len(moments)),
		Notes:       caseItem.Notes,
	}
	for _, m := range moments {
		if m == nil {
			continue
		}
		manifest.CSVRows = append(manifest.CSVRows, map[string]string{
			"id":                  m.ID,
			"global_person_id":    m.GlobalPersonID,
			"person_id":           m.PersonID,
			"person_name":         m.PersonName,
			"camera":              firstNonEmpty(m.CameraName, m.ServiceID),
			"service_id":          m.ServiceID,
			"event_kind":          m.EventKind,
			"severity":            m.Severity,
			"identity_confidence": fmt.Sprintf("%.3f", m.IdentityConfidence),
			"review_status":       m.ReviewStatus,
			"created_at":          m.CreatedAt.UTC().Format(time.RFC3339),
			"snapshot_path":       m.SnapshotPath,
		})
	}
	_ = db.auditTimelineAction(userID, "case_export", caseID, map[string]interface{}{"moment_count": len(moments)})
	return manifest, nil
}

func (db *Database) BackfillPersonSightingsFromEvents(limit int) (int, error) {
	return db.backfillPersonSightingsFromEvents(0, limit)
}

func (db *Database) BackfillPersonSightingsFromEventsForUser(userID int64, limit int) (int, error) {
	if userID <= 0 {
		return 0, nil
	}
	return db.backfillPersonSightingsFromEvents(userID, limit)
}

func (db *Database) backfillPersonSightingsFromEvents(userID int64, limit int) (int, error) {
	if db == nil {
		return 0, nil
	}
	if limit <= 0 {
		limit = 50000
	}
	rows, err := db.DB.Query(`
		SELECT ae.id, ae.agent_id, ae.data, ae.metadata, ae.created_at, COALESCE(a.user_id, 0)
		FROM agent_events ae
		LEFT JOIN agents a ON a.id = ae.agent_id
		WHERE (? <= 0 OR COALESCE(a.user_id, 0) = ?)
		ORDER BY ae.created_at ASC
		LIMIT ?
	`, userID, userID, limit)
	if err != nil {
		return 0, err
	}

	type backfillEvent struct {
		userID int64
		event  AgentEvent
	}
	var events []backfillEvent
	for rows.Next() {
		var ev AgentEvent
		var dataJSON string
		var metadataJSON sql.NullString
		var userID int64
		if err := rows.Scan(&ev.ID, &ev.AgentID, &dataJSON, &metadataJSON, &ev.CreatedAt, &userID); err != nil {
			continue
		}
		if userID <= 0 {
			continue
		}
		if err := json.Unmarshal([]byte(dataJSON), &ev.Data); err != nil {
			continue
		}
		if metadataJSON.Valid && strings.TrimSpace(metadataJSON.String) != "" {
			_ = json.Unmarshal([]byte(metadataJSON.String), &ev.Metadata)
		}
		events = append(events, backfillEvent{userID: userID, event: ev})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()

	inserted := 0
	for _, item := range events {
		for _, rec := range TimelineRecordsFromAgentEvent(item.userID, &item.event) {
			if err := db.StorePersonSightingRecord(rec); err != nil {
				log.Printf("[Timeline] backfill failed event=%s identity=%s: %v", item.event.ID, rec.GlobalPersonID, err)
				continue
			}
			inserted++
		}
	}
	return inserted, nil
}
