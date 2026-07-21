package relay

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type timelineIdentityOut struct {
	GlobalPersonID     string  `json:"global_person_id"`
	PersonID           string  `json:"person_id,omitempty"`
	PersonName         string  `json:"person_name,omitempty"`
	IdentitySource     string  `json:"identity_source,omitempty"`
	IdentityConfidence float64 `json:"identity_confidence,omitempty"`
	LastServiceID      string  `json:"last_service_id,omitempty"`
	LastCameraName     string  `json:"last_camera_name,omitempty"`
	FirstSeenAt        string  `json:"first_seen_at"`
	LastSeenAt         string  `json:"last_seen_at"`
	MomentCount        int     `json:"moment_count"`
	SnapshotPath       string  `json:"snapshot_path,omitempty"`
}

type timelineMomentOut struct {
	ID                 string  `json:"id"`
	GlobalPersonID     string  `json:"global_person_id"`
	PersonID           string  `json:"person_id,omitempty"`
	PersonName         string  `json:"person_name,omitempty"`
	IdentitySource     string  `json:"identity_source,omitempty"`
	IdentityConfidence float64 `json:"identity_confidence,omitempty"`
	ServiceID          string  `json:"service_id,omitempty"`
	CameraName         string  `json:"camera_name,omitempty"`
	EventID            string  `json:"event_id,omitempty"`
	TrackID            int     `json:"track_id,omitempty"`
	BBoxJSON           string  `json:"bbox_json,omitempty"`
	SnapshotPath       string  `json:"snapshot_path,omitempty"`
	ActivityBrief      string  `json:"activity_brief,omitempty"`
	EventKind          string  `json:"event_kind,omitempty"`
	Severity           string  `json:"severity,omitempty"`
	MetadataJSON       string  `json:"metadata_json,omitempty"`
	ReviewStatus       string  `json:"review_status,omitempty"`
	OperatorNote       string  `json:"operator_note,omitempty"`
	IsFalseMatch       bool    `json:"is_false_match,omitempty"`
	ReviewedAt         string  `json:"reviewed_at,omitempty"`
	CreatedAt          string  `json:"created_at"`
}

func handleTimelineIdentities(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	filter, ok := timelineFilterFromRequest(w, r, 100)
	if !ok {
		return
	}
	items, err := relay.db.QueryTimelineIdentities(userID, filter)
	if err != nil {
		http.Error(w, "Failed to fetch timeline identities", http.StatusInternalServerError)
		return
	}
	out := make([]timelineIdentityOut, 0, len(items))
	for _, item := range items {
		out = append(out, timelineIdentityOut{
			GlobalPersonID:     item.GlobalPersonID,
			PersonID:           item.PersonID,
			PersonName:         item.PersonName,
			IdentitySource:     item.IdentitySource,
			IdentityConfidence: item.IdentityConfidence,
			LastServiceID:      item.LastServiceID,
			LastCameraName:     item.LastCameraName,
			FirstSeenAt:        item.FirstSeenAt.UTC().Format(time.RFC3339),
			LastSeenAt:         item.LastSeenAt.UTC().Format(time.RFC3339),
			MomentCount:        item.MomentCount,
			SnapshotPath:       item.SnapshotPath,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func handleTimelineIdentityMoments(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64, globalPersonID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	filter, ok := timelineFilterFromRequest(w, r, 100)
	if !ok {
		return
	}
	filter.GlobalPersonID = strings.TrimSpace(globalPersonID)
	moments, err := relay.db.QueryPersonTimeline(userID, filter)
	if err != nil {
		http.Error(w, "Failed to fetch timeline moments", http.StatusInternalServerError)
		return
	}
	out := timelineMomentsOut(moments)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func handleTimelineMoments(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	filter, ok := timelineFilterFromRequest(w, r, 250)
	if !ok {
		return
	}
	moments, err := relay.db.QueryTimelineMoments(userID, filter)
	if err != nil {
		http.Error(w, "Failed to fetch timeline moments", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(timelineMomentsOut(moments))
}

func handleTimelineBackfill(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 50000
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > 500000 {
			http.Error(w, "limit must be between 1 and 500000", http.StatusBadRequest)
			return
		}
		limit = n
	}
	processed, err := relay.db.BackfillPersonSightingsFromEventsForUser(userID, limit)
	if err != nil {
		http.Error(w, "Failed to import timeline events", http.StatusInternalServerError)
		return
	}
	_ = relay.db.auditTimelineAction(userID, "timeline_backfill", "", map[string]int{"processed": processed})
	writeTimelineJSON(w, map[string]int{"processed": processed})
}

func handleTimelineJourney(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	filter, ok := timelineFilterFromRequest(w, r, 500)
	if !ok {
		return
	}
	globalPersonID := strings.TrimSpace(firstNonEmpty(r.URL.Query().Get("global_person_id"), r.URL.Query().Get("identity_id")))
	if globalPersonID == "" {
		http.Error(w, "global_person_id is required", http.StatusBadRequest)
		return
	}
	journey, err := relay.db.BuildTimelineJourney(userID, globalPersonID, filter)
	if err != nil {
		http.Error(w, "Failed to build timeline journey", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(journey)
}

func handleTimelineAppearanceSearch(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	globalPersonID := strings.TrimSpace(firstNonEmpty(r.URL.Query().Get("global_person_id"), r.URL.Query().Get("identity_id")))
	if globalPersonID == "" {
		http.Error(w, "global_person_id is required", http.StatusBadRequest)
		return
	}
	limit := 25
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	matches, err := relay.db.SearchTimelineAppearance(userID, globalPersonID, r.URL.Query().Get("kind"), limit)
	if err != nil {
		http.Error(w, "Failed to search timeline appearance", http.StatusInternalServerError)
		return
	}
	writeTimelineJSON(w, matches)
}

func handleTimelineIdentityAssign(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64, globalPersonID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		PersonID string `json:"person_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}
	targetID, err := relay.db.AssignTimelineIdentity(userID, globalPersonID, req.PersonID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Identity or person not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to assign identity", http.StatusBadRequest)
		return
	}
	writeTimelineJSON(w, map[string]string{"global_person_id": targetID})
}

func handleTimelineIdentityMerge(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64, sourceID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		TargetID string `json:"target_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := relay.db.MergeTimelineIdentities(userID, sourceID, req.TargetID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Identity not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to merge identity", http.StatusBadRequest)
		return
	}
	writeTimelineJSON(w, map[string]string{"status": "merged"})
}

func handleTimelineIdentitySplit(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64, sourceID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		MomentIDs []string `json:"moment_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}
	newID, err := relay.db.SplitTimelineMoments(userID, sourceID, req.MomentIDs)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Moments not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to split moments", http.StatusBadRequest)
		return
	}
	writeTimelineJSON(w, map[string]string{"global_person_id": newID})
}

func handleTimelineMomentReview(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64, momentID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Status       string `json:"status"`
		Note         string `json:"note"`
		IsFalseMatch bool   `json:"is_false_match"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := relay.db.ReviewTimelineMoment(userID, momentID, req.Status, req.Note, req.IsFalseMatch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Moment not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to review moment", http.StatusBadRequest)
		return
	}
	writeTimelineJSON(w, map[string]string{"status": "reviewed"})
}

func handleTimelineMomentReveal(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64, momentID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := relay.db.AuditTimelineReveal(userID, momentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Moment not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to audit reveal", http.StatusBadRequest)
		return
	}
	writeTimelineJSON(w, map[string]string{"status": "revealed"})
}

func handleTimelineCases(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	switch r.Method {
	case http.MethodGet:
		items, err := relay.db.ListTimelineCases(userID)
		if err != nil {
			http.Error(w, "Failed to fetch timeline cases", http.StatusInternalServerError)
			return
		}
		writeTimelineJSON(w, items)
	case http.MethodPost:
		var req struct {
			Title string `json:"title"`
			Owner string `json:"owner"`
			Notes string `json:"notes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
		item, err := relay.db.CreateTimelineCase(userID, req.Title, req.Owner, req.Notes)
		if err != nil {
			http.Error(w, "Failed to create timeline case", http.StatusBadRequest)
			return
		}
		writeTimelineJSON(w, item)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleTimelineCase(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64, caseID, action string) {
	switch {
	case action == "" && r.Method == http.MethodPatch:
		var req struct {
			Title    string `json:"title"`
			Status   string `json:"status"`
			Owner    string `json:"owner"`
			Notes    string `json:"notes"`
			Reviewed *bool  `json:"reviewed"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
		item, err := relay.db.UpdateTimelineCase(userID, caseID, TimelineCasePatch{
			Title: req.Title, Status: req.Status, Owner: req.Owner, Notes: req.Notes, Reviewed: req.Reviewed,
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "Case not found", http.StatusNotFound)
				return
			}
			http.Error(w, "Failed to update timeline case", http.StatusBadRequest)
			return
		}
		writeTimelineJSON(w, item)
	case action == "items" && r.Method == http.MethodPost:
		var req struct {
			MomentIDs []string `json:"moment_ids"`
			Note      string   `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
		items, err := relay.db.AddTimelineCaseItems(userID, caseID, req.MomentIDs, req.Note)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "Case not found", http.StatusNotFound)
				return
			}
			http.Error(w, "Failed to add timeline case items", http.StatusBadRequest)
			return
		}
		writeTimelineJSON(w, items)
	case action == "export" && r.Method == http.MethodPost:
		manifest, err := relay.db.ExportTimelineCase(userID, caseID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "Case not found", http.StatusNotFound)
				return
			}
			http.Error(w, "Failed to export timeline case", http.StatusInternalServerError)
			return
		}
		fillTimelineSnapshotHashes(relay, manifest)
		writeTimelineJSON(w, manifest)
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

func handleTimelineEvidenceLocks(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Title     string   `json:"title"`
		CaseID    string   `json:"case_id"`
		MomentIDs []string `json:"moment_ids"`
		ExpiresAt string   `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}
	var expiresAt time.Time
	if strings.TrimSpace(req.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			http.Error(w, "expires_at must be RFC3339", http.StatusBadRequest)
			return
		}
		expiresAt = parsed
	}
	lock, err := relay.db.CreateTimelineEvidenceLock(userID, req.Title, req.CaseID, req.MomentIDs, expiresAt)
	if err != nil {
		http.Error(w, "Failed to create evidence lock", http.StatusBadRequest)
		return
	}
	writeTimelineJSON(w, lock)
}

func handleTimelineTopology(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	switch r.Method {
	case http.MethodGet:
		nodes, err := relay.db.GetTimelineTopology(userID)
		if err != nil {
			http.Error(w, "Failed to fetch timeline topology", http.StatusInternalServerError)
			return
		}
		writeTimelineJSON(w, nodes)
	case http.MethodPatch:
		var req struct {
			Nodes []TimelineTopologyNode `json:"nodes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
		nodes, err := relay.db.UpsertTimelineTopology(userID, req.Nodes)
		if err != nil {
			http.Error(w, "Failed to update timeline topology", http.StatusBadRequest)
			return
		}
		writeTimelineJSON(w, nodes)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func timelineFilterFromRequest(w http.ResponseWriter, r *http.Request, fallbackLimit int) (PersonTimelineFilter, bool) {
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if date != "" {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			http.Error(w, "date must be YYYY-MM-DD", http.StatusBadRequest)
			return PersonTimelineFilter{}, false
		}
	}
	var startAt, endAt time.Time
	if raw := strings.TrimSpace(r.URL.Query().Get("start_at")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			http.Error(w, "start_at must be RFC3339", http.StatusBadRequest)
			return PersonTimelineFilter{}, false
		}
		startAt = parsed
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("end_at")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			http.Error(w, "end_at must be RFC3339", http.StatusBadRequest)
			return PersonTimelineFilter{}, false
		}
		endAt = parsed
	}
	known := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("known")))
	switch known {
	case "", "all", "known", "unknown":
	default:
		http.Error(w, "known must be all, known, or unknown", http.StatusBadRequest)
		return PersonTimelineFilter{}, false
	}
	if known == "all" {
		known = ""
	}
	limit := fallbackLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	return PersonTimelineFilter{
		Date:           date,
		StartAt:        startAt,
		EndAt:          endAt,
		ServiceID:      strings.TrimSpace(firstNonEmpty(r.URL.Query().Get("camera_id"), r.URL.Query().Get("service_id"))),
		CameraGroupID:  strings.TrimSpace(r.URL.Query().Get("camera_group_id")),
		GlobalPersonID: strings.TrimSpace(r.URL.Query().Get("global_person_id")),
		PersonID:       strings.TrimSpace(r.URL.Query().Get("person_id")),
		AnchorMomentID: strings.TrimSpace(r.URL.Query().Get("anchor_moment_id")),
		Known:          known,
		EventKind:      strings.TrimSpace(r.URL.Query().Get("event_kind")),
		Severity:       strings.TrimSpace(r.URL.Query().Get("severity")),
		ReviewStatus:   strings.TrimSpace(r.URL.Query().Get("review_status")),
		Query:          strings.TrimSpace(firstNonEmpty(r.URL.Query().Get("q"), r.URL.Query().Get("query"))),
		Limit:          limit,
	}, true
}

func timelineMomentsOut(moments []*PersonMoment) []timelineMomentOut {
	out := make([]timelineMomentOut, 0, len(moments))
	for _, m := range moments {
		if m == nil {
			continue
		}
		out = append(out, timelineMomentOut{
			ID:                 m.ID,
			GlobalPersonID:     m.GlobalPersonID,
			PersonID:           m.PersonID,
			PersonName:         m.PersonName,
			IdentitySource:     m.IdentitySource,
			IdentityConfidence: m.IdentityConfidence,
			ServiceID:          m.ServiceID,
			CameraName:         m.CameraName,
			EventID:            m.EventID,
			TrackID:            m.TrackID,
			BBoxJSON:           m.BBoxJSON,
			SnapshotPath:       m.SnapshotPath,
			ActivityBrief:      m.ActivityBrief,
			EventKind:          m.EventKind,
			Severity:           m.Severity,
			MetadataJSON:       m.MetadataJSON,
			ReviewStatus:       m.ReviewStatus,
			OperatorNote:       m.OperatorNote,
			IsFalseMatch:       m.IsFalseMatch,
			CreatedAt:          m.CreatedAt.UTC().Format(time.RFC3339),
		})
		if !m.ReviewedAt.IsZero() {
			out[len(out)-1].ReviewedAt = m.ReviewedAt.UTC().Format(time.RFC3339)
		}
	}
	return out
}

func timelineIdentityFromPath(path string) string {
	rest := strings.TrimPrefix(path, "/timeline/identities/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return ""
	}
	parts := strings.Split(rest, "/")
	raw, err := url.PathUnescape(parts[0])
	if err != nil {
		return parts[0]
	}
	return raw
}

func timelineIdentityActionFromPath(path string) (string, string) {
	rest := strings.TrimPrefix(path, "/timeline/identities/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return "", ""
	}
	parts := strings.Split(rest, "/")
	rawID, err := url.PathUnescape(parts[0])
	if err != nil {
		rawID = parts[0]
	}
	action := ""
	if len(parts) > 1 {
		action = strings.TrimSpace(parts[1])
	}
	return rawID, action
}

func timelineMomentActionFromPath(path string) (string, string) {
	rest := strings.TrimPrefix(path, "/timeline/moments/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return "", ""
	}
	parts := strings.Split(rest, "/")
	rawID, err := url.PathUnescape(parts[0])
	if err != nil {
		rawID = parts[0]
	}
	action := ""
	if len(parts) > 1 {
		action = strings.TrimSpace(parts[1])
	}
	return rawID, action
}

func timelineCaseActionFromPath(path string) (string, string) {
	rest := strings.TrimPrefix(path, "/timeline/cases/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return "", ""
	}
	parts := strings.Split(rest, "/")
	rawID, err := url.PathUnescape(parts[0])
	if err != nil {
		rawID = parts[0]
	}
	action := ""
	if len(parts) > 1 {
		action = strings.TrimSpace(parts[1])
	}
	return rawID, action
}

func writeTimelineJSON(w http.ResponseWriter, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func fillTimelineSnapshotHashes(relay *Relay, manifest *TimelineExportManifest) {
	if relay == nil || relay.snapshotManager == nil || manifest == nil {
		return
	}
	manifest.SnapshotHashes = make([]TimelineSnapshotHash, 0, len(manifest.Moments))
	for _, moment := range manifest.Moments {
		if moment == nil || strings.TrimSpace(moment.SnapshotPath) == "" {
			continue
		}
		hash := TimelineSnapshotHash{MomentID: moment.ID, SnapshotPath: moment.SnapshotPath}
		fullPath := relay.snapshotManager.GetSnapshotPath(moment.SnapshotPath)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			hash.Error = "snapshot unavailable"
			manifest.SnapshotHashes = append(manifest.SnapshotHashes, hash)
			continue
		}
		sum := sha256.Sum256(data)
		hash.SHA256 = hex.EncodeToString(sum[:])
		manifest.SnapshotHashes = append(manifest.SnapshotHashes, hash)
	}
}
