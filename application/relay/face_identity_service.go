package relay

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/unblink/unblink/relay/cv"
)

// FaceIdentityService maintains an in-memory cache of track→person identities
// populated by the shared face recognition worker pool.  Detection workers
// POST /cv/face-lookup to request async identification and GET
// /cv/face-identities to read cached results.
type FaceIdentityService struct {
	mu         sync.RWMutex
	identities map[trackKey]*TrackIdentity

	workerRegistry *cv.CVWorkerRegistry
	storageManager *cv.StorageManager
	staleTTL       time.Duration
}

type trackKey struct {
	MediaID string
	TrackID int
}

// TrackIdentity holds the face recognition result for one tracked person.
type TrackIdentity struct {
	PersonID            string  `json:"person_id,omitempty"`
	PersonName          string  `json:"person_name,omitempty"`
	MatchScore          float64 `json:"match_score,omitempty"`
	CandidatePersonID   string  `json:"candidate_person_id,omitempty"`
	CandidatePersonName string  `json:"candidate_person_name,omitempty"`
	CandidateScore      float64 `json:"candidate_score,omitempty"`
	ConfidenceTier      string  `json:"confidence_tier,omitempty"`
	EvidenceHits        int     `json:"evidence_hits,omitempty"`
	Pending             bool    `json:"pending"`
	UpdatedAt           int64   `json:"updated_at"` // unix ms
}

// NewFaceIdentityService creates the service.  Call RunCleanup() in a goroutine.
func NewFaceIdentityService(wr *cv.CVWorkerRegistry, sm *cv.StorageManager) *FaceIdentityService {
	return &FaceIdentityService{
		identities:     make(map[trackKey]*TrackIdentity),
		workerRegistry: wr,
		storageManager: sm,
		staleTTL:       3 * time.Minute,
	}
}

// RunCleanup periodically removes stale entries.
func (s *FaceIdentityService) RunCleanup(stop <-chan struct{}) {
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

func (s *FaceIdentityService) cleanupStale() {
	cutoff := time.Now().Add(-s.staleTTL).UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.identities {
		if v.UpdatedAt < cutoff {
			delete(s.identities, k)
		}
	}
}

// OnFaceWorkerEvent is called when a face recognition worker event arrives.
// It extracts per-detection identities and updates the cache.
func (s *FaceIdentityService) OnFaceWorkerEvent(eventData map[string]interface{}) {
	metadata, _ := eventData["metadata"].(map[string]interface{})
	data, _ := eventData["data"].(map[string]interface{})
	if metadata == nil {
		metadata = data
	}
	if metadata == nil {
		return
	}

	agentType, _ := metadata["agent_type"].(string)
	if agentType != "face" {
		return
	}

	mediaID, _ := metadata["service_id"].(string)
	if mediaID == "" {
		mediaID, _ = metadata["media_id"].(string)
	}
	if mediaID == "" {
		return
	}

	// Extract detections from data
	detections, _ := data["detections"].([]interface{})
	if detections == nil {
		// Try top-level output array
		if output, ok := data["output"].([]interface{}); ok {
			for _, item := range output {
				if m, ok := item.(map[string]interface{}); ok {
					if dets, ok := m["detections"].([]interface{}); ok {
						detections = append(detections, dets...)
					}
				}
			}
		}
	}

	now := time.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, d := range detections {
		det, ok := d.(map[string]interface{})
		if !ok {
			continue
		}

		tidFloat, ok := det["track_id"].(float64)
		if !ok {
			continue
		}
		tid := int(tidFloat)
		if tid < 0 {
			continue
		}

		key := trackKey{MediaID: mediaID, TrackID: tid}
		personID := stringField(det, "person_id")
		personName := stringField(det, "person_name")
		matchScore := floatField(det, "match_score")
		candidateID := stringField(det, "candidate_person_id")
		candidateName := stringField(det, "candidate_person_name")
		candidateScore := floatField(det, "candidate_score")
		confidenceTier := stringField(det, "confidence_tier")

		if personID != "" {
			if personName == "" {
				personName = personID
			}
			s.identities[key] = &TrackIdentity{
				PersonID:            personID,
				PersonName:          personName,
				MatchScore:          matchScore,
				CandidatePersonID:   candidateID,
				CandidatePersonName: candidateName,
				CandidateScore:      candidateScore,
				ConfidenceTier:      "strong",
				Pending:             false,
				UpdatedAt:           now,
			}
			continue
		}

		existing := s.identities[key]
		if existing != nil && existing.PersonID != "" && candidateID == "" {
			existing.Pending = false
			existing.UpdatedAt = now
			continue
		}

		if candidateID != "" && confidenceTier == "weak" {
			hits := 1
			if existing != nil && existing.CandidatePersonID == candidateID {
				hits = existing.EvidenceHits + 1
				if existing.CandidateScore > candidateScore {
					candidateScore = existing.CandidateScore
				}
			}
			if candidateName == "" {
				candidateName = candidateID
			}
			next := &TrackIdentity{
				CandidatePersonID:   candidateID,
				CandidatePersonName: candidateName,
				CandidateScore:      candidateScore,
				ConfidenceTier:      "weak",
				EvidenceHits:        hits,
				Pending:             false,
				UpdatedAt:           now,
			}
			if hits >= faceWeakPromoteHits() {
				next.PersonID = candidateID
				next.PersonName = candidateName
				next.MatchScore = candidateScore
				next.ConfidenceTier = "weak_promoted"
			}
			s.identities[key] = next
			continue
		}

		s.identities[key] = &TrackIdentity{
			CandidatePersonID:   candidateID,
			CandidatePersonName: candidateName,
			CandidateScore:      candidateScore,
			ConfidenceTier:      confidenceTier,
			Pending:             false,
			UpdatedAt:           now,
		}
	}

	// Also check metadata-level person_names for matched persons
	if names, ok := metadata["person_names"].([]interface{}); ok && len(names) > 0 {
		log.Printf("[FaceIdentity] Updated %d detections from face worker for media=%s", len(detections), mediaID)
	}
}

func faceWeakPromoteHits() int {
	raw := os.Getenv("FACE_WEAK_PROMOTE_HITS")
	if raw == "" {
		raw = os.Getenv("FACE_TRACK_WEAK_PROMOTE_HITS")
	}
	if raw == "" {
		return 2
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 2
	}
	return n
}

func stringField(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}

func floatField(m map[string]interface{}, key string) float64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}

// SubmitFaceLookup requests face identification for a tracked person.
// The face worker pool processes it asynchronously; results arrive via OnFaceWorkerEvent.
func (s *FaceIdentityService) SubmitFaceLookup(mediaID string, trackID int, frameID string, personBBox map[string]float64) error {
	key := trackKey{MediaID: mediaID, TrackID: trackID}
	now := time.Now().UnixMilli()

	s.mu.Lock()
	existing := s.identities[key]
	if existing != nil && !existing.Pending && existing.PersonName != "" {
		// Already have a non-pending identity — skip redundant lookup
		s.mu.Unlock()
		return nil
	}
	if existing != nil && existing.Pending && (now-existing.UpdatedAt) < 3000 {
		// Lookup already in progress and recent — skip
		s.mu.Unlock()
		return nil
	}
	s.identities[key] = &TrackIdentity{Pending: true, UpdatedAt: now}
	s.mu.Unlock()

	// Build a face_lookup message for the face worker pool.
	// The face adapter handles this like a vlm_trigger: downloads frame, submits to worker.
	msg := map[string]interface{}{
		"type": "face_lookup",
		"data": map[string]interface{}{
			"media_id":    mediaID,
			"track_id":    trackID,
			"frame_id":    frameID,
			"person_bbox": personBBox,
		},
	}

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal face_lookup: %w", err)
	}

	if !s.workerRegistry.SendToWorkerPool("vyra/face_recognition", msgBytes) {
		// No face worker connected — clear pending
		s.mu.Lock()
		if cur := s.identities[key]; cur != nil && cur.Pending {
			delete(s.identities, key)
		}
		s.mu.Unlock()
		return fmt.Errorf("no face worker connected")
	}

	return nil
}

// GetIdentities returns all cached identities for a media (camera).
func (s *FaceIdentityService) GetIdentities(mediaID string) map[int]*TrackIdentity {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[int]*TrackIdentity)
	for k, v := range s.identities {
		if k.MediaID == mediaID {
			cp := *v
			out[k.TrackID] = &cp
		}
	}
	return out
}

// ── HTTP handlers ────────────────────────────────────────────────────────────

// HandleFaceLookup handles POST /cv/face-lookup
// Body: {"media_id": "...", "track_id": 42, "frame_id": "...", "person_bbox": {...}}
func (s *FaceIdentityService) HandleFaceLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		MediaID    string             `json:"media_id"`
		TrackID    int                `json:"track_id"`
		FrameID    string             `json:"frame_id"`
		PersonBBox map[string]float64 `json:"person_bbox"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.MediaID == "" || req.FrameID == "" {
		http.Error(w, "media_id and frame_id required", http.StatusBadRequest)
		return
	}

	err := s.SubmitFaceLookup(req.MediaID, req.TrackID, req.FrameID, req.PersonBBox)
	if err != nil {
		// Non-fatal: just means no face worker is available
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "no_worker",
			"message": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "queued"})
}

// HandleFaceIdentities handles GET /cv/face-identities?media_id=...
func (s *FaceIdentityService) HandleFaceIdentities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}

	mediaID := r.URL.Query().Get("media_id")
	if mediaID == "" {
		http.Error(w, "media_id query param required", http.StatusBadRequest)
		return
	}

	identities := s.GetIdentities(mediaID)

	// Convert to string-keyed map for JSON
	out := make(map[string]*TrackIdentity, len(identities))
	for tid, v := range identities {
		out[strconv.Itoa(tid)] = v
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
