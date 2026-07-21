package relay

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type personTrackKey struct {
	MediaID string
	TrackID int
}

type PersonTrackIdentity struct {
	GlobalPersonID     string  `json:"global_person_id,omitempty"`
	PersonID           string  `json:"person_id,omitempty"`
	PersonName         string  `json:"person_name,omitempty"`
	IdentitySource     string  `json:"identity_source,omitempty"`
	IdentityConfidence float64 `json:"identity_confidence,omitempty"`
	MatchScore         float64 `json:"match_score,omitempty"`
	Pending            bool    `json:"pending,omitempty"`
	UpdatedAt          int64   `json:"updated_at"`
}

type PersonIdentityResolveRequest struct {
	MediaID         string    `json:"media_id,omitempty"`
	ServiceID       string    `json:"service_id,omitempty"`
	TrackID         int       `json:"track_id"`
	FrameID         string    `json:"frame_id,omitempty"`
	PersonID        string    `json:"person_id,omitempty"`
	PersonName      string    `json:"person_name,omitempty"`
	MatchScore      float64   `json:"match_score,omitempty"`
	Embedding       []float64 `json:"embedding,omitempty"`
	CameraName      string    `json:"camera_name,omitempty"`
	CameraTimestamp string    `json:"camera_timestamp,omitempty"`
	EmbeddingKind   string    `json:"embedding_kind,omitempty"`
	EmbeddingModel  string    `json:"embedding_model,omitempty"`
	QualityScore    float64   `json:"quality_score,omitempty"`
	ObservationTS   string    `json:"observation_ts,omitempty"`
}

type personGlobalIdentityState struct {
	GlobalPersonID     string
	PersonID           string
	PersonName         string
	IdentitySource     string
	IdentityConfidence float64
	BodyPrototype      []float64
	BodyEmbeddingCount int
	FacePrototype      []float64
	FaceEmbeddingCount int
	AliasOf            string
	LastMediaID        string
	LastCameraName     string
	LastObservationAt  time.Time
	LastEmbeddingKind  string
	LastEmbeddingModel string
	LastQualityScore   float64
	LastSeenAt         time.Time
}

type PersonIdentityService struct {
	mu sync.RWMutex

	db         *Database
	agentTable *AgentTable
	legacyMode bool

	trackIdentities  map[personTrackKey]*PersonTrackIdentity
	lastTrackSeen    map[personTrackKey]time.Time
	identities       map[string]*personGlobalIdentityState
	knownByPersonID  map[string]string
	aliasToCanonical map[string]string

	unknownMatchThreshold     float64
	faceUnknownMatchThreshold float64
	minQualityScore           float64
	crossCameraMinGap         time.Duration
	sameCameraConflictWindow  time.Duration
	staleTTL                  time.Duration

	nextUnknownIdx      int
	resolveCount        uint64
	totalResolveLatency time.Duration
}

func NewPersonIdentityService(db *Database, agentTable *AgentTable) *PersonIdentityService {
	s := &PersonIdentityService{
		db:                        db,
		agentTable:                agentTable,
		legacyMode:                legacyModeEnabled(),
		trackIdentities:           make(map[personTrackKey]*PersonTrackIdentity),
		lastTrackSeen:             make(map[personTrackKey]time.Time),
		identities:                make(map[string]*personGlobalIdentityState),
		knownByPersonID:           make(map[string]string),
		aliasToCanonical:          make(map[string]string),
		unknownMatchThreshold:     parseFloat(os.Getenv("PERSON_REID_MATCH_THRESHOLD"), 0.72),
		faceUnknownMatchThreshold: parseFloat(os.Getenv("PERSON_FACE_MATCH_THRESHOLD"), 0.62),
		minQualityScore:           parseFloat(os.Getenv("PERSON_REID_MIN_QUALITY_SCORE"), 0.20),
		crossCameraMinGap:         parseDurationSecondsEnv("IDENTITY_CROSS_CAMERA_MIN_GAP_SECONDS", 0),
		sameCameraConflictWindow:  parseDurationSecondsEnv("PERSON_REID_SAME_CAMERA_CONFLICT_SECONDS", 3*time.Second),
		staleTTL:                  parseDurationSecondsEnv("PERSON_IDENTITY_TRACK_TTL_SECONDS", 5*time.Minute),
	}
	s.bootstrapFromDB()
	return s
}

func parseDurationSecondsEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	seconds, err := strconv.ParseFloat(raw, 64)
	if err != nil || seconds < 0 {
		return fallback
	}
	return time.Duration(seconds * float64(time.Second))
}

func (s *PersonIdentityService) RunCleanup(stop <-chan struct{}) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.cleanupStale()
		}
	}
}

func (s *PersonIdentityService) cleanupStale() {
	cutoff := time.Now().Add(-s.staleTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, seenAt := range s.lastTrackSeen {
		if seenAt.Before(cutoff) {
			delete(s.lastTrackSeen, k)
			delete(s.trackIdentities, k)
		}
	}
}

func (s *PersonIdentityService) RuntimeStats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	avgLatency := 0.0
	if s.resolveCount > 0 {
		avgLatency = float64(s.totalResolveLatency.Milliseconds()) / float64(s.resolveCount)
	}
	knownCount := 0
	for _, state := range s.identities {
		if state == nil || state.AliasOf != "" {
			continue
		}
		if strings.TrimSpace(state.PersonID) != "" {
			knownCount++
		}
	}
	return map[string]interface{}{
		"resolve_count":                 s.resolveCount,
		"avg_latency_ms":                avgLatency,
		"track_cache_size":              len(s.trackIdentities),
		"identity_count":                len(s.identities),
		"legacy_mode":                   s.legacyMode,
		"known_identity_count":          knownCount,
		"alias_count":                   len(s.aliasToCanonical),
		"person_reid_match_threshold":   s.unknownMatchThreshold,
		"person_reid_min_quality_score": s.minQualityScore,
		"cross_camera_min_gap_seconds":  s.crossCameraMinGap.Seconds(),
		"face_unknown_match_threshold":  s.faceUnknownMatchThreshold,
	}
}

func (s *PersonIdentityService) SetUnknownMatchThreshold(v float64) {
	if v <= 0 {
		return
	}
	if v > 1 {
		v = 1
	}
	s.mu.Lock()
	s.unknownMatchThreshold = v
	s.mu.Unlock()
}

func (s *PersonIdentityService) UnknownMatchThreshold() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.unknownMatchThreshold
}

func (s *PersonIdentityService) HandleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req PersonIdentityResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	req.MediaID = strings.TrimSpace(req.MediaID)
	if req.MediaID == "" {
		req.MediaID = strings.TrimSpace(req.ServiceID)
	}
	if req.MediaID == "" {
		http.Error(w, "media_id is required", http.StatusBadRequest)
		return
	}

	identity := s.ResolveTrack(req)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(identity)
}

func (s *PersonIdentityService) HandleIdentities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mediaID := strings.TrimSpace(r.URL.Query().Get("media_id"))
	if mediaID == "" {
		mediaID = strings.TrimSpace(r.URL.Query().Get("service_id"))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.GetIdentities(mediaID))
}

func (s *PersonIdentityService) GetIdentities(mediaID string) map[string]*PersonTrackIdentity {
	mediaID = strings.TrimSpace(mediaID)
	out := map[string]*PersonTrackIdentity{}
	if mediaID == "" {
		return out
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	for key, identity := range s.trackIdentities {
		if key.MediaID != mediaID || identity == nil {
			continue
		}
		canonical := s.canonicalIDLocked(identity.GlobalPersonID)
		copyIdentity := *identity
		copyIdentity.GlobalPersonID = canonical
		if state := s.identities[canonical]; state != nil {
			copyIdentity.PersonID = state.PersonID
			copyIdentity.PersonName = state.PersonName
			copyIdentity.IdentitySource = state.identitySource()
			copyIdentity.IdentityConfidence = state.IdentityConfidence
			copyIdentity.MatchScore = state.IdentityConfidence
		}
		out[strconv.Itoa(key.TrackID)] = &copyIdentity
	}
	return out
}

func (s *PersonIdentityService) ResolveTrack(req PersonIdentityResolveRequest) *PersonTrackIdentity {
	startedAt := time.Now()
	if s.legacyMode {
		return s.resolveTrackLegacy(req, startedAt)
	}
	req.MediaID = strings.TrimSpace(req.MediaID)
	if req.MediaID == "" {
		req.MediaID = strings.TrimSpace(req.ServiceID)
	}
	embedding := normalizeEmbedding(req.Embedding)
	embeddingKind := normalizeEmbeddingKind(req)
	qualityScore := req.QualityScore
	if len(embedding) > 0 && qualityScore <= 0 {
		qualityScore = 1
	}
	observedAt := parseObservationTime(req)
	key := personTrackKey{MediaID: req.MediaID, TrackID: req.TrackID}

	var response *PersonTrackIdentity
	var persistRecords []PersonGlobalIdentityRecord

	s.mu.Lock()

	if lastSeen, ok := s.lastTrackSeen[key]; ok && observedAt.Sub(lastSeen) > s.staleTTL {
		delete(s.lastTrackSeen, key)
		delete(s.trackIdentities, key)
	}

	existingTrack := s.trackIdentities[key]
	if existingTrack != nil {
		existingTrack.GlobalPersonID = s.canonicalIDLocked(existingTrack.GlobalPersonID)
	}

	var currentState *personGlobalIdentityState
	if existingTrack != nil {
		currentState = s.identities[existingTrack.GlobalPersonID]
	}

	switch {
	case strings.TrimSpace(req.PersonID) != "":
		knownState := s.ensureKnownStateLocked(req.PersonID, req.PersonName, req.MatchScore, observedAt)
		if currentState != nil && currentState.GlobalPersonID != knownState.GlobalPersonID && strings.TrimSpace(currentState.PersonID) == "" {
			persistRecords = append(persistRecords, s.mergeIdentityStatesLocked(currentState.GlobalPersonID, knownState.GlobalPersonID, observedAt)...)
			knownState = s.identities[s.canonicalIDLocked(knownState.GlobalPersonID)]
		}
		s.applyObservationLocked(knownState, embeddingKind, embedding, req.EmbeddingModel, qualityScore, req.MediaID, req.CameraName, observedAt)
		response = s.bindTrackLocked(key, knownState)
		persistRecords = append(persistRecords, s.recordForStateLocked(knownState))
	case currentState != nil && strings.TrimSpace(currentState.PersonID) != "":
		s.applyObservationLocked(currentState, embeddingKind, embedding, req.EmbeddingModel, qualityScore, req.MediaID, req.CameraName, observedAt)
		response = s.bindTrackLocked(key, currentState)
		persistRecords = append(persistRecords, s.recordForStateLocked(currentState))
	case currentState != nil:
		if len(embedding) > 0 && qualityScore >= s.minQualityScore && strings.TrimSpace(currentState.PersonID) == "" {
			if matchedID, score := s.matchUnknownLocked(embedding, embeddingKind, req.MediaID, req.TrackID, observedAt, currentState.GlobalPersonID); matchedID != "" {
				mergeFromID, mergeToID := s.unknownMergeIDsLocked(currentState.GlobalPersonID, matchedID)
				persistRecords = append(persistRecords, s.mergeIdentityStatesLocked(mergeFromID, mergeToID, observedAt)...)
				currentState = s.identities[s.canonicalIDLocked(mergeToID)]
				if currentState != nil && score > currentState.IdentityConfidence {
					currentState.IdentityConfidence = score
				}
			}
		}
		s.applyObservationLocked(currentState, embeddingKind, embedding, req.EmbeddingModel, qualityScore, req.MediaID, req.CameraName, observedAt)
		response = s.bindTrackLocked(key, currentState)
		persistRecords = append(persistRecords, s.recordForStateLocked(currentState))
	default:
		var state *personGlobalIdentityState
		if len(embedding) > 0 && qualityScore >= s.minQualityScore {
			if matchedID, score := s.matchUnknownLocked(embedding, embeddingKind, req.MediaID, req.TrackID, observedAt); matchedID != "" {
				state = s.identities[matchedID]
				if state != nil && score > state.IdentityConfidence {
					state.IdentityConfidence = score
				}
			}
		}
		if state == nil {
			state = s.createUnknownStateLocked(observedAt)
		}
		s.applyObservationLocked(state, embeddingKind, embedding, req.EmbeddingModel, qualityScore, req.MediaID, req.CameraName, observedAt)
		response = s.bindTrackLocked(key, state)
		persistRecords = append(persistRecords, s.recordForStateLocked(state))
	}

	s.lastTrackSeen[key] = observedAt
	s.resolveCount++
	s.totalResolveLatency += time.Since(startedAt)
	s.mu.Unlock()

	for _, record := range dedupeIdentityRecords(persistRecords) {
		if s.db == nil {
			continue
		}
		if err := s.db.UpsertPersonGlobalIdentityRecord(record); err != nil {
			log.Printf("[PersonIdentityService] persist identity %s failed: %v", record.GlobalPersonID, err)
		}
	}

	if response == nil {
		response = &PersonTrackIdentity{UpdatedAt: time.Now().UnixMilli()}
	}
	return response
}

func (s *PersonIdentityService) resolveTrackLegacy(req PersonIdentityResolveRequest, startedAt time.Time) *PersonTrackIdentity {
	req.MediaID = strings.TrimSpace(req.MediaID)
	if req.MediaID == "" {
		req.MediaID = strings.TrimSpace(req.ServiceID)
	}
	embedding := normalizeEmbedding(req.Embedding)
	embeddingKind := normalizeEmbeddingKind(req)
	qualityScore := req.QualityScore
	if len(embedding) > 0 && qualityScore <= 0 {
		qualityScore = 1
	}
	observedAt := parseObservationTime(req)
	key := personTrackKey{MediaID: req.MediaID, TrackID: req.TrackID}

	var response *PersonTrackIdentity
	var persistRecord PersonGlobalIdentityRecord
	var shouldPersist bool

	s.mu.Lock()

	if lastSeen, ok := s.lastTrackSeen[key]; ok && observedAt.Sub(lastSeen) > s.staleTTL {
		delete(s.lastTrackSeen, key)
		delete(s.trackIdentities, key)
	}

	existingTrack := s.trackIdentities[key]
	if existingTrack != nil {
		existingTrack.GlobalPersonID = s.canonicalIDLocked(existingTrack.GlobalPersonID)
	}

	switch {
	case strings.TrimSpace(req.PersonID) != "":
		knownState := s.ensureKnownStateLocked(req.PersonID, req.PersonName, req.MatchScore, observedAt)
		s.applyObservationLocked(knownState, embeddingKind, embedding, req.EmbeddingModel, qualityScore, req.MediaID, req.CameraName, observedAt)
		response = s.bindTrackLocked(key, knownState)
		persistRecord = s.recordForStateLocked(knownState)
		shouldPersist = true
	case existingTrack != nil:
		if currentState := s.identities[existingTrack.GlobalPersonID]; currentState != nil {
			s.applyObservationLocked(currentState, embeddingKind, embedding, req.EmbeddingModel, qualityScore, req.MediaID, req.CameraName, observedAt)
			response = s.bindTrackLocked(key, currentState)
			persistRecord = s.recordForStateLocked(currentState)
			shouldPersist = true
		} else {
			copyIdentity := *existingTrack
			copyIdentity.UpdatedAt = time.Now().UnixMilli()
			response = &copyIdentity
		}
	default:
		state := s.createUnknownStateLocked(observedAt)
		s.applyObservationLocked(state, embeddingKind, embedding, req.EmbeddingModel, qualityScore, req.MediaID, req.CameraName, observedAt)
		response = s.bindTrackLocked(key, state)
		persistRecord = s.recordForStateLocked(state)
		shouldPersist = true
	}

	s.lastTrackSeen[key] = observedAt
	s.resolveCount++
	s.totalResolveLatency += time.Since(startedAt)
	s.mu.Unlock()

	if shouldPersist && s.db != nil {
		if err := s.db.UpsertPersonGlobalIdentityRecord(persistRecord); err != nil {
			log.Printf("[PersonIdentityService] persist legacy identity %s failed: %v", persistRecord.GlobalPersonID, err)
		}
	}

	if response == nil {
		response = &PersonTrackIdentity{UpdatedAt: time.Now().UnixMilli()}
	}
	return response
}

func (s *PersonIdentityService) OnAgentEvent(agentID string, metadata map[string]interface{}, data map[string]interface{}, createdAt time.Time) {
	if metadata == nil {
		return
	}
	ids := s.collectGlobalIDs(metadata)
	if len(ids) == 0 {
		return
	}

	var userID int64
	if s.agentTable != nil {
		if agent, err := s.agentTable.GetAgent(agentID); err == nil && agent != nil {
			userID = agent.UserID
		}
	}

	serviceID, _ := metadata["service_id"].(string)
	if serviceID == "" {
		serviceID, _ = metadata["media_id"].(string)
	}
	snapshotPath, _ := metadata["snapshot_path"].(string)
	activityBrief, _ := metadata["activity_brief"].(string)
	if activityBrief == "" {
		activityBrief, _ = metadata["event_brief"].(string)
	}

	s.mu.RLock()
	identityRecords := make([]PersonGlobalIdentityRecord, 0, len(ids))
	personIDs := make(map[string]string, len(ids))
	for _, id := range ids {
		canonical := s.canonicalIDLocked(id)
		if state := s.identities[canonical]; state != nil {
			identityRecords = append(identityRecords, s.recordForStateLocked(state))
			personIDs[canonical] = state.PersonID
		}
	}
	s.mu.RUnlock()

	for _, record := range dedupeIdentityRecords(identityRecords) {
		if s.db == nil {
			continue
		}
		if err := s.db.UpsertPersonGlobalIdentityRecord(record); err != nil {
			log.Printf("[PersonIdentityService] refresh identity %s failed: %v", record.GlobalPersonID, err)
		}
	}

	if s.db == nil || userID <= 0 {
		return
	}
	eventID, _ := metadata["event_id"].(string)
	if eventID == "" {
		eventID, _ = metadata["id"].(string)
	}
	ev := &AgentEvent{
		ID:        eventID,
		AgentID:   agentID,
		Data:      data,
		Metadata:  metadata,
		CreatedAt: createdAt,
	}
	timelineRecords := TimelineRecordsFromAgentEvent(userID, ev)
	if len(timelineRecords) == 0 {
		for _, id := range ids {
			canonical := s.canonicalID(id)
			timelineRecords = append(timelineRecords, PersonTimelineRecord{
				UserID:         userID,
				GlobalPersonID: canonical,
				PersonID:       personIDs[canonical],
				ServiceID:      serviceID,
				EventID:        eventID,
				TrackID:        -1,
				SnapshotPath:   snapshotPath,
				ActivityBrief:  activityBrief,
				MetadataJSON:   mustJSON(metadata, "{}"),
				CreatedAt:      createdAt,
			})
		}
	}
	for _, record := range timelineRecords {
		if canonical := s.canonicalID(record.GlobalPersonID); canonical != "" {
			record.GlobalPersonID = canonical
		}
		if record.PersonID == "" {
			record.PersonID = personIDs[record.GlobalPersonID]
		}
		if err := s.db.StorePersonSightingRecord(record); err != nil {
			log.Printf("[PersonIdentityService] store sighting for %s failed: %v", record.GlobalPersonID, err)
		}
	}
}

func (s *PersonIdentityService) canonicalID(id string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.canonicalIDLocked(id)
}

func (s *PersonIdentityService) canonicalIDLocked(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	seen := map[string]bool{}
	for id != "" {
		if seen[id] {
			return id
		}
		seen[id] = true
		next := strings.TrimSpace(s.aliasToCanonical[id])
		if next == "" {
			if state := s.identities[id]; state != nil && strings.TrimSpace(state.AliasOf) != "" {
				next = strings.TrimSpace(state.AliasOf)
			}
		}
		if next == "" || next == id {
			return id
		}
		id = next
	}
	return ""
}

func (s *PersonIdentityService) collectGlobalIDs(metadata map[string]interface{}) []string {
	if metadata == nil {
		return nil
	}
	seen := map[string]bool{}
	add := func(raw interface{}) {
		id, ok := raw.(string)
		if !ok {
			return
		}
		canonical := s.canonicalID(id)
		if !isCanonicalPublicIdentityID(canonical) || seen[canonical] {
			return
		}
		seen[canonical] = true
	}

	add(metadata["global_person_id"])
	if ids, ok := metadata["global_person_ids"].([]interface{}); ok {
		for _, id := range ids {
			add(id)
		}
	}
	if bboxes, ok := metadata["bboxes"].([]interface{}); ok {
		for _, bbox := range bboxes {
			if item, ok := bbox.(map[string]interface{}); ok {
				add(item["global_person_id"])
			}
		}
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (s *PersonIdentityService) bootstrapFromDB() {
	if s.db == nil {
		return
	}

	rows, err := s.db.Query(`
		SELECT
			global_person_id,
			person_id,
			person_name,
			identity_source,
			identity_confidence,
			embedding_centroid,
			body_prototype,
			body_embedding_count,
			face_prototype,
			face_embedding_count,
			alias_of,
			last_media_id,
			last_camera_name,
			last_observation_at,
			last_embedding_kind,
			last_embedding_model,
			last_quality_score,
			last_seen_at
		FROM person_global_identities
	`)
	if err != nil {
		log.Printf("[PersonIdentityService] bootstrap query failed: %v", err)
		return
	}
	defer rows.Close()

	s.mu.Lock()
	defer s.mu.Unlock()

	for rows.Next() {
		var (
			globalID           string
			personID           sql.NullString
			personName         sql.NullString
			identitySource     sql.NullString
			identityConfidence sql.NullFloat64
			embeddingCentroid  sql.NullString
			bodyPrototype      sql.NullString
			bodyCount          sql.NullInt64
			facePrototype      sql.NullString
			faceCount          sql.NullInt64
			aliasOf            sql.NullString
			lastMediaID        sql.NullString
			lastCameraName     sql.NullString
			lastObservationAny interface{}
			lastEmbeddingKind  sql.NullString
			lastEmbeddingModel sql.NullString
			lastQualityScore   sql.NullFloat64
			lastSeenAny        interface{}
		)
		if err := rows.Scan(
			&globalID,
			&personID,
			&personName,
			&identitySource,
			&identityConfidence,
			&embeddingCentroid,
			&bodyPrototype,
			&bodyCount,
			&facePrototype,
			&faceCount,
			&aliasOf,
			&lastMediaID,
			&lastCameraName,
			&lastObservationAny,
			&lastEmbeddingKind,
			&lastEmbeddingModel,
			&lastQualityScore,
			&lastSeenAny,
		); err != nil {
			log.Printf("[PersonIdentityService] bootstrap scan failed: %v", err)
			continue
		}

		state := &personGlobalIdentityState{
			GlobalPersonID:     globalID,
			PersonID:           strings.TrimSpace(personID.String),
			PersonName:         strings.TrimSpace(personName.String),
			IdentitySource:     strings.TrimSpace(identitySource.String),
			IdentityConfidence: identityConfidence.Float64,
			BodyPrototype:      parseEmbeddingJSON(bodyPrototype.String),
			BodyEmbeddingCount: int(bodyCount.Int64),
			FacePrototype:      parseEmbeddingJSON(facePrototype.String),
			FaceEmbeddingCount: int(faceCount.Int64),
			AliasOf:            strings.TrimSpace(aliasOf.String),
			LastMediaID:        strings.TrimSpace(lastMediaID.String),
			LastCameraName:     strings.TrimSpace(lastCameraName.String),
			LastObservationAt:  dbValueToTime(lastObservationAny),
			LastEmbeddingKind:  strings.TrimSpace(lastEmbeddingKind.String),
			LastEmbeddingModel: strings.TrimSpace(lastEmbeddingModel.String),
			LastQualityScore:   lastQualityScore.Float64,
			LastSeenAt:         dbValueToTime(lastSeenAny),
		}

		if len(state.BodyPrototype) == 0 {
			state.BodyPrototype = parseEmbeddingJSON(embeddingCentroid.String)
			if len(state.BodyPrototype) > 0 && state.BodyEmbeddingCount == 0 {
				state.BodyEmbeddingCount = 1
			}
		}
		s.identities[state.GlobalPersonID] = state
		if state.PersonID != "" {
			s.knownByPersonID[state.PersonID] = state.GlobalPersonID
		}
		if state.AliasOf != "" {
			s.aliasToCanonical[state.GlobalPersonID] = state.AliasOf
		}
		if idx := parseUnknownSequence(state.GlobalPersonID); idx > s.nextUnknownIdx {
			s.nextUnknownIdx = idx
		}
	}
}

func (s *PersonIdentityService) ensureKnownStateLocked(personID, personName string, matchScore float64, observedAt time.Time) *personGlobalIdentityState {
	personID = strings.TrimSpace(personID)
	personName = strings.TrimSpace(personName)
	globalID := s.canonicalIDLocked(s.knownByPersonID[personID])
	if globalID == "" {
		globalID = "person:" + personID
	}
	state := s.identities[globalID]
	if state == nil {
		state = &personGlobalIdentityState{
			GlobalPersonID: globalID,
			PersonID:       personID,
		}
		s.identities[globalID] = state
	}
	state.PersonID = personID
	if personName != "" {
		state.PersonName = personName
	}
	if matchScore > state.IdentityConfidence {
		state.IdentityConfidence = matchScore
	}
	if state.IdentitySource == "" || state.IdentitySource == "unknown_track" {
		state.IdentitySource = "face_registered"
	}
	if observedAt.After(state.LastSeenAt) {
		state.LastSeenAt = observedAt
	}
	s.knownByPersonID[personID] = globalID
	return state
}

func (s *PersonIdentityService) createUnknownStateLocked(observedAt time.Time) *personGlobalIdentityState {
	s.nextUnknownIdx++
	state := &personGlobalIdentityState{
		GlobalPersonID: fmt.Sprintf("unknown:%d", s.nextUnknownIdx),
		IdentitySource: "unknown_track",
		LastSeenAt:     observedAt,
	}
	s.identities[state.GlobalPersonID] = state
	return state
}

func (s *PersonIdentityService) applyObservationLocked(state *personGlobalIdentityState, kind string, embedding []float64, model string, quality float64, mediaID, cameraName string, observedAt time.Time) {
	if state == nil {
		return
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	if observedAt.After(state.LastSeenAt) {
		state.LastSeenAt = observedAt
	}
	state.LastObservationAt = observedAt
	if mediaID != "" {
		state.LastMediaID = mediaID
	}
	if cameraName != "" {
		state.LastCameraName = cameraName
	}
	if kind != "" {
		state.LastEmbeddingKind = kind
	}
	if strings.TrimSpace(model) != "" {
		state.LastEmbeddingModel = strings.TrimSpace(model)
	}
	if quality > 0 {
		state.LastQualityScore = quality
	}

	if len(embedding) == 0 || quality < s.minQualityScore {
		if state.IdentitySource == "" {
			if state.PersonID != "" {
				state.IdentitySource = "face_registered"
			} else {
				state.IdentitySource = "unknown_track"
			}
		}
		return
	}

	switch kind {
	case "face":
		state.FacePrototype, state.FaceEmbeddingCount = blendEmbeddingPrototype(state.FacePrototype, state.FaceEmbeddingCount, embedding, weightFromQuality(quality))
		if state.PersonID != "" {
			state.IdentitySource = "face_registered"
		} else {
			state.IdentitySource = "face_unknown_match"
		}
	default:
		state.BodyPrototype, state.BodyEmbeddingCount = blendEmbeddingPrototype(state.BodyPrototype, state.BodyEmbeddingCount, embedding, weightFromQuality(quality))
		if state.PersonID == "" && state.IdentitySource == "" {
			state.IdentitySource = "unknown_track"
		}
	}
}

func (s *PersonIdentityService) bindTrackLocked(key personTrackKey, state *personGlobalIdentityState) *PersonTrackIdentity {
	if state == nil {
		return &PersonTrackIdentity{UpdatedAt: time.Now().UnixMilli()}
	}
	canonical := s.canonicalIDLocked(state.GlobalPersonID)
	state = s.identities[canonical]
	if state == nil {
		return &PersonTrackIdentity{GlobalPersonID: canonical, UpdatedAt: time.Now().UnixMilli()}
	}
	identity := &PersonTrackIdentity{
		GlobalPersonID:     canonical,
		PersonID:           state.PersonID,
		PersonName:         state.PersonName,
		IdentitySource:     state.identitySource(),
		IdentityConfidence: state.IdentityConfidence,
		MatchScore:         state.IdentityConfidence,
		UpdatedAt:          time.Now().UnixMilli(),
	}
	s.trackIdentities[key] = identity
	return identity
}

func (s *PersonIdentityService) matchUnknownLocked(embedding []float64, kind, mediaID string, trackID int, observedAt time.Time, excludeIDs ...string) (string, float64) {
	threshold := s.unknownMatchThreshold
	if kind == "face" {
		threshold = s.faceUnknownMatchThreshold
	}
	exclude := make(map[string]bool, len(excludeIDs))
	for _, id := range excludeIDs {
		id = s.canonicalIDLocked(id)
		if id != "" {
			exclude[id] = true
		}
	}
	bestID := ""
	bestScore := threshold
	for id, state := range s.identities {
		if state == nil || state.AliasOf != "" || strings.TrimSpace(state.PersonID) != "" {
			continue
		}
		if exclude[s.canonicalIDLocked(id)] {
			continue
		}
		if s.identityActiveOnDifferentTrackLocked(id, mediaID, trackID, observedAt) {
			continue
		}
		prototype := state.BodyPrototype
		if kind == "face" && len(state.FacePrototype) > 0 {
			prototype = state.FacePrototype
		}
		if len(prototype) == 0 {
			continue
		}
		if s.crossCameraMinGap > 0 && mediaID != "" && state.LastMediaID != "" && state.LastMediaID != mediaID && !state.LastSeenAt.IsZero() && observedAt.Sub(state.LastSeenAt) < s.crossCameraMinGap {
			continue
		}
		score := cosineSimilarity(prototype, embedding)
		if score >= bestScore {
			bestID = id
			bestScore = score
		}
	}
	return bestID, bestScore
}

func (s *PersonIdentityService) identityActiveOnDifferentTrackLocked(globalID, mediaID string, trackID int, observedAt time.Time) bool {
	if mediaID == "" || trackID < 0 || s.sameCameraConflictWindow <= 0 {
		return false
	}
	canonical := s.canonicalIDLocked(globalID)
	if canonical == "" {
		return false
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	for key, identity := range s.trackIdentities {
		if identity == nil || key.MediaID != mediaID || key.TrackID == trackID {
			continue
		}
		if s.canonicalIDLocked(identity.GlobalPersonID) != canonical {
			continue
		}
		lastSeen := s.lastTrackSeen[key]
		if lastSeen.IsZero() {
			return true
		}
		delta := observedAt.Sub(lastSeen)
		if delta < 0 {
			delta = -delta
		}
		if delta <= s.sameCameraConflictWindow {
			return true
		}
	}
	return false
}

func (s *PersonIdentityService) unknownMergeIDsLocked(currentID, matchedID string) (string, string) {
	currentID = s.canonicalIDLocked(currentID)
	matchedID = s.canonicalIDLocked(matchedID)
	if currentID == "" || matchedID == "" || currentID == matchedID {
		return "", ""
	}

	currentSeq := parseUnknownSequence(currentID)
	matchedSeq := parseUnknownSequence(matchedID)
	switch {
	case currentSeq > 0 && matchedSeq > 0:
		if currentSeq <= matchedSeq {
			return matchedID, currentID
		}
		return currentID, matchedID
	case currentSeq > 0:
		return matchedID, currentID
	case matchedSeq > 0:
		return currentID, matchedID
	default:
		return currentID, matchedID
	}
}

func (s *PersonIdentityService) mergeIdentityStatesLocked(fromID, toID string, observedAt time.Time) []PersonGlobalIdentityRecord {
	fromID = s.canonicalIDLocked(fromID)
	toID = s.canonicalIDLocked(toID)
	if fromID == "" || toID == "" || fromID == toID {
		return nil
	}
	fromState := s.identities[fromID]
	toState := s.identities[toID]
	if fromState == nil || toState == nil {
		if fromID != "" && toID != "" {
			s.aliasToCanonical[fromID] = toID
		}
		return nil
	}

	toState.BodyPrototype, toState.BodyEmbeddingCount = mergePrototypePair(
		toState.BodyPrototype,
		toState.BodyEmbeddingCount,
		fromState.BodyPrototype,
		fromState.BodyEmbeddingCount,
	)
	toState.FacePrototype, toState.FaceEmbeddingCount = mergePrototypePair(
		toState.FacePrototype,
		toState.FaceEmbeddingCount,
		fromState.FacePrototype,
		fromState.FaceEmbeddingCount,
	)
	if toState.PersonName == "" && fromState.PersonName != "" {
		toState.PersonName = fromState.PersonName
	}
	if toState.IdentityConfidence < fromState.IdentityConfidence {
		toState.IdentityConfidence = fromState.IdentityConfidence
	}
	if toState.LastSeenAt.Before(fromState.LastSeenAt) {
		toState.LastSeenAt = fromState.LastSeenAt
		toState.LastObservationAt = fromState.LastObservationAt
		toState.LastMediaID = fromState.LastMediaID
		toState.LastCameraName = fromState.LastCameraName
		toState.LastEmbeddingKind = fromState.LastEmbeddingKind
		toState.LastEmbeddingModel = fromState.LastEmbeddingModel
		toState.LastQualityScore = fromState.LastQualityScore
	}
	if toState.IdentitySource == "" || toState.IdentitySource == "unknown_track" {
		toState.IdentitySource = toState.identitySource()
	}

	affectedTrackKeys := make([]personTrackKey, 0)
	for trackKey, identity := range s.trackIdentities {
		if identity == nil {
			continue
		}
		if s.canonicalIDLocked(identity.GlobalPersonID) == fromID {
			affectedTrackKeys = append(affectedTrackKeys, trackKey)
		}
	}

	fromState.AliasOf = toID
	s.aliasToCanonical[fromID] = toID

	for _, trackKey := range affectedTrackKeys {
		identity := s.trackIdentities[trackKey]
		if identity == nil {
			continue
		}
		identity.GlobalPersonID = toID
		identity.PersonID = toState.PersonID
		identity.PersonName = toState.PersonName
		identity.IdentitySource = toState.identitySource()
		identity.IdentityConfidence = toState.IdentityConfidence
		identity.MatchScore = toState.IdentityConfidence
		s.trackIdentities[trackKey] = identity
	}

	return []PersonGlobalIdentityRecord{
		s.recordForStateLocked(fromState),
		s.recordForStateLocked(toState),
	}
}

func (s *PersonIdentityService) recordForStateLocked(state *personGlobalIdentityState) PersonGlobalIdentityRecord {
	if state == nil {
		return PersonGlobalIdentityRecord{}
	}
	bodyPrototype := serializeEmbedding(state.BodyPrototype)
	facePrototype := serializeEmbedding(state.FacePrototype)
	embeddingCentroid := bodyPrototype
	if embeddingCentroid == "" {
		embeddingCentroid = facePrototype
	}
	return PersonGlobalIdentityRecord{
		GlobalPersonID:     state.GlobalPersonID,
		PersonID:           state.PersonID,
		PersonName:         state.PersonName,
		IdentitySource:     state.identitySource(),
		IdentityConfidence: state.IdentityConfidence,
		EmbeddingCentroid:  embeddingCentroid,
		BodyPrototype:      bodyPrototype,
		BodyEmbeddingCount: state.BodyEmbeddingCount,
		FacePrototype:      facePrototype,
		FaceEmbeddingCount: state.FaceEmbeddingCount,
		AliasOf:            state.AliasOf,
		LastMediaID:        state.LastMediaID,
		LastCameraName:     state.LastCameraName,
		LastObservationAt:  state.LastObservationAt,
		LastEmbeddingKind:  state.LastEmbeddingKind,
		LastEmbeddingModel: state.LastEmbeddingModel,
		LastQualityScore:   state.LastQualityScore,
		SeenAt:             state.LastSeenAt,
	}
}

func (s *personGlobalIdentityState) identitySource() string {
	if s == nil {
		return ""
	}
	if strings.TrimSpace(s.IdentitySource) != "" {
		return s.IdentitySource
	}
	if strings.TrimSpace(s.PersonID) != "" {
		return "face_registered"
	}
	return "unknown_track"
}

func dedupeIdentityRecords(records []PersonGlobalIdentityRecord) []PersonGlobalIdentityRecord {
	if len(records) == 0 {
		return nil
	}
	last := map[string]PersonGlobalIdentityRecord{}
	for _, record := range records {
		if strings.TrimSpace(record.GlobalPersonID) == "" {
			continue
		}
		last[record.GlobalPersonID] = record
	}
	out := make([]PersonGlobalIdentityRecord, 0, len(last))
	for _, record := range last {
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].GlobalPersonID < out[j].GlobalPersonID
	})
	return out
}

func normalizeEmbeddingKind(req PersonIdentityResolveRequest) string {
	kind := strings.ToLower(strings.TrimSpace(req.EmbeddingKind))
	switch kind {
	case "face":
		return "face"
	case "body", "body_reid", "reid":
		return "body_reid"
	}
	if strings.TrimSpace(req.PersonID) != "" {
		return "face"
	}
	return "body_reid"
}

func parseObservationTime(req PersonIdentityResolveRequest) time.Time {
	for _, raw := range []string{req.ObservationTS, req.CameraTimestamp} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		for _, layout := range []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02 15:04:05",
			"2006-01-02T15:04:05",
		} {
			if parsed, err := time.Parse(layout, raw); err == nil {
				return parsed
			}
		}
	}
	return time.Now()
}

func normalizeEmbedding(values []float64) []float64 {
	if len(values) == 0 {
		return nil
	}
	out := make([]float64, 0, len(values))
	var sumSquares float64
	for _, value := range values {
		out = append(out, value)
		sumSquares += value * value
	}
	if sumSquares == 0 {
		return nil
	}
	norm := 1.0 / sqrt(sumSquares)
	for idx := range out {
		out[idx] *= norm
	}
	return out
}

func cosineSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	sum := 0.0
	for idx := range a {
		sum += a[idx] * b[idx]
	}
	return sum
}

func blendEmbeddingPrototype(existing []float64, existingCount int, incoming []float64, weight int) ([]float64, int) {
	if len(incoming) == 0 {
		return existing, existingCount
	}
	if weight <= 0 {
		weight = 1
	}
	if len(existing) == 0 || existingCount <= 0 || len(existing) != len(incoming) {
		copyIncoming := append([]float64(nil), incoming...)
		return copyIncoming, weight
	}
	out := make([]float64, len(existing))
	total := existingCount + weight
	for idx := range existing {
		out[idx] = ((existing[idx] * float64(existingCount)) + (incoming[idx] * float64(weight))) / float64(total)
	}
	return normalizeEmbedding(out), total
}

func mergePrototypePair(a []float64, aCount int, b []float64, bCount int) ([]float64, int) {
	if len(b) == 0 || bCount <= 0 {
		return a, aCount
	}
	return blendEmbeddingPrototype(a, aCount, b, bCount)
}

func weightFromQuality(quality float64) int {
	switch {
	case quality >= 0.90:
		return 4
	case quality >= 0.75:
		return 3
	case quality >= 0.50:
		return 2
	default:
		return 1
	}
}

func serializeEmbedding(values []float64) string {
	if len(values) == 0 {
		return ""
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return ""
	}
	return string(payload)
}

func parseEmbeddingJSON(raw string) []float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var values []float64
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	return normalizeEmbedding(values)
}

func dbValueToTime(value interface{}) time.Time {
	switch typed := value.(type) {
	case time.Time:
		return typed
	case []byte:
		return parseDBTimeString(string(typed))
	case string:
		return parseDBTimeString(typed)
	default:
		return time.Time{}
	}
}

func parseDBTimeString(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func parseUnknownSequence(id string) int {
	if !strings.HasPrefix(id, "unknown:") {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(id, "unknown:"))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func isCanonicalPublicIdentityID(id string) bool {
	id = strings.TrimSpace(id)
	switch {
	case strings.HasPrefix(id, "person:") && len(strings.TrimSpace(strings.TrimPrefix(id, "person:"))) > 0:
		return true
	case parseUnknownSequence(id) > 0:
		return true
	default:
		return false
	}
}

func sqrt(value float64) float64 {
	if value <= 0 {
		return 0
	}
	z := value
	for i := 0; i < 10; i++ {
		z -= (z*z - value) / (2 * z)
	}
	return z
}
