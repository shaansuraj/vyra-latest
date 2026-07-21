package relay

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// handleListPersons returns all persons for the authenticated user.
// GET /persons
func handleListPersons(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	persons, err := relay.personTable.GetPersonsByUser(userID)
	if err != nil {
		log.Printf("[Persons] ListPersons: %v", err)
		http.Error(w, "Failed to list persons", http.StatusInternalServerError)
		return
	}
	if persons == nil {
		persons = []*Person{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(persons)
}

// handleCreatePerson creates a new person record.
// POST /persons   body: { "name": "John Doe" }
func handleCreatePerson(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	p, err := relay.personTable.CreatePerson(strings.TrimSpace(req.Name), userID)
	if err != nil {
		log.Printf("[Persons] CreatePerson: %v", err)
		http.Error(w, "Failed to create person", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(p)
}

// handleGetPerson returns a single person record.
// GET /persons/{id}
func handleGetPerson(w http.ResponseWriter, r *http.Request, relay *Relay, personID string) {
	p, err := relay.personTable.GetPersonByID(personID)
	if err != nil {
		http.Error(w, "Person not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

// handleRenamePerson updates a person's display name.
// PATCH /persons/{id}   body: { "name": "Jane Doe" }
func handleRenamePerson(w http.ResponseWriter, r *http.Request, relay *Relay, personID string) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := relay.personTable.UpdatePersonName(personID, strings.TrimSpace(req.Name)); err != nil {
		http.Error(w, "Failed to rename person", http.StatusInternalServerError)
		return
	}
	p, _ := relay.personTable.GetPersonByID(personID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

// handleDeletePerson deletes a person and their face data.
// DELETE /persons/{id}
func handleDeletePerson(w http.ResponseWriter, r *http.Request, relay *Relay, personID string) {
	if err := relay.personTable.DeletePerson(personID); err != nil {
		http.Error(w, "Failed to delete person", http.StatusInternalServerError)
		return
	}

	// Best-effort cleanup of profile files.
	_ = os.Remove(filepath.Join(relay.config.AppDir, "storage", "persons", personID+".jpg"))
	_ = os.RemoveAll(filepath.Join(relay.config.AppDir, "storage", "persons", personID))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// handleUploadPersonFace stores/replaces legacy single face image for a person.
// POST /persons/{id}/face
// Accepts multipart/form-data field "image" or raw image body.
func handleUploadPersonFace(w http.ResponseWriter, r *http.Request, relay *Relay, personID string) {
	imageData, err := readUploadedImageData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(imageData) == 0 {
		http.Error(w, "Empty image data", http.StatusBadRequest)
		return
	}

	// Legacy path stays stable for backward compatibility.
	personsDir := filepath.Join(relay.config.AppDir, "storage", "persons")
	storedOnDisk := false
	if err := os.MkdirAll(personsDir, 0755); err == nil {
		imgFile := filepath.Join(personsDir, personID+".jpg")
		if err := os.WriteFile(imgFile, imageData, 0644); err == nil {
			relPath := filepath.Join("persons", personID+".jpg")
			if err := relay.personTable.SetFaceImagePath(personID, relPath); err == nil {
				storedOnDisk = true
				log.Printf("[Persons] Legacy face image saved for person %s (%d bytes)", personID, len(imageData))
			}
		}
	}

	// Fallback: store as BLOB if disk write failed.
	if !storedOnDisk {
		if err := relay.personTable.SetFaceImage(personID, imageData); err != nil {
			log.Printf("[Persons] SetFaceImage %s: %v", personID, err)
			http.Error(w, "Failed to store face image", http.StatusInternalServerError)
			return
		}
		log.Printf("[Persons] Legacy face image stored as BLOB for person %s (%d bytes)", personID, len(imageData))
	}

	p, _ := relay.personTable.GetPersonByID(personID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "person": p})
}

// handleListPersonFaces returns all face profiles for one person.
// GET /persons/{id}/faces
func handleListPersonFaces(w http.ResponseWriter, r *http.Request, relay *Relay, personID string) {
	profiles, err := relay.personTable.ListFaceProfiles(personID)
	if err != nil {
		log.Printf("[Persons] ListFaceProfiles %s: %v", personID, err)
		http.Error(w, "Failed to list face profiles", http.StatusInternalServerError)
		return
	}

	out := make([]*PersonFaceProfile, 0, len(profiles)+1)
	out = append(out, profiles...)

	// Surface legacy single-face record as a synthetic "legacy" profile.
	if relay.personTable.HasLegacyFace(personID) {
		p, _ := relay.personTable.GetPersonByID(personID)
		t := time.Time{}
		if p != nil {
			t = p.FaceUpdatedAt
		}
		out = append(out, &PersonFaceProfile{
			ID:        "legacy",
			PersonID:  personID,
			CreatedAt: t,
			UpdatedAt: t,
			IsLegacy:  true,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleUploadPersonFaceProfile stores one additional face profile image.
// POST /persons/{id}/faces
func handleUploadPersonFaceProfile(w http.ResponseWriter, r *http.Request, relay *Relay, personID string) {
	imageData, err := readUploadedImageData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(imageData) == 0 {
		http.Error(w, "Empty image data", http.StatusBadRequest)
		return
	}

	qualityEnforced := personEnvBool("FACE_ENROLLMENT_ENFORCE_QUALITY", true)
	acceptedImageData := imageData
	var qualityResult *faceEnrollmentQualityResult
	if qualityEnforced {
		qr, normalizedData, qErr := runFaceEnrollmentQualityCheck(relay, imageData)
		if qErr != nil {
			log.Printf("[Persons] Enrollment quality check failed for %s: %v", personID, qErr)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success":         false,
				"accepted":        false,
				"reason":          "Enrollment quality check failed. Please try again.",
				"quality_metrics": map[string]interface{}{},
			})
			return
		}
		qualityResult = qr
		if len(normalizedData) > 0 {
			acceptedImageData = normalizedData
		}
		if qualityResult != nil && !qualityResult.Accepted {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success":         false,
				"accepted":        false,
				"reason":          qualityResult.Reason,
				"quality_metrics": qualityResult.Metrics,
			})
			return
		}
	}

	fileID := uuid.New().String()
	relPath := filepath.Join("persons", personID, fileID+".jpg")
	fullPath := filepath.Join(relay.config.AppDir, "storage", relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		http.Error(w, "Failed to create profile storage", http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(fullPath, acceptedImageData, 0644); err != nil {
		http.Error(w, "Failed to save profile image", http.StatusInternalServerError)
		return
	}

	profile, err := relay.personTable.CreateFaceProfile(personID, relPath)
	if err != nil {
		_ = os.Remove(fullPath)
		log.Printf("[Persons] CreateFaceProfile %s: %v", personID, err)
		http.Error(w, "Failed to save profile", http.StatusInternalServerError)
		return
	}

	log.Printf("[Persons] Face profile saved: person=%s profile=%s", personID, profile.ID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":         true,
		"accepted":        true,
		"reason":          "ok",
		"profile":         profile,
		"quality_metrics": qualityMetrics(qualityResult),
	})
}

// handleGetPersonFace serves a face image.
// GET /persons/{id}/face — no auth (internal worker compatibility endpoint).
func handleGetPersonFace(w http.ResponseWriter, r *http.Request, relay *Relay, personID string) {
	// Prefer filesystem path over BLOB.
	if imgPath := relay.personTable.GetFaceImagePath(personID); imgPath != "" {
		if data, ok := readImageFromStorage(relay, imgPath); ok {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Cache-Control", "max-age=3600")
			w.Write(data)
			return
		}
	}
	// Additional fallback to legacy file path if profile file is missing.
	if legacyPath := relay.personTable.GetLegacyFaceImagePath(personID); legacyPath != "" {
		if data, ok := readImageFromStorage(relay, legacyPath); ok {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Cache-Control", "max-age=3600")
			w.Write(data)
			return
		}
	}

	// Fallback: serve legacy BLOB.
	data, err := relay.personTable.GetFaceImage(personID)
	if err != nil || len(data) == 0 {
		http.Error(w, "Face image not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

// handleGetPersonFaceProfile serves one specific profile image.
// GET /persons/{id}/faces/{profileId} — no auth (internal worker use).
func handleGetPersonFaceProfile(w http.ResponseWriter, r *http.Request, relay *Relay, personID, profileID string) {
	if profileID == "legacy" {
		legacyPath := relay.personTable.GetLegacyFaceImagePath(personID)
		if legacyPath != "" {
			if data, ok := readImageFromStorage(relay, legacyPath); ok {
				w.Header().Set("Content-Type", "image/jpeg")
				w.Header().Set("Cache-Control", "max-age=3600")
				w.Write(data)
				return
			}
		}

		data, err := relay.personTable.GetFaceImage(personID)
		if err != nil || len(data) == 0 {
			http.Error(w, "Face profile not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(data)
		return
	}

	profile, err := relay.personTable.GetFaceProfileByID(personID, profileID)
	if err == sql.ErrNoRows {
		http.Error(w, "Face profile not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("[Persons] GetFaceProfileByID %s/%s: %v", personID, profileID, err)
		http.Error(w, "Failed to load face profile", http.StatusInternalServerError)
		return
	}

	if data, ok := readImageFromStorage(relay, profile.ImagePath); ok {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Write(data)
		return
	}

	http.Error(w, "Face profile image not found", http.StatusNotFound)
}

// handleDeletePersonFaceProfile deletes one profile image.
// DELETE /persons/{id}/faces/{profileId}
func handleDeletePersonFaceProfile(w http.ResponseWriter, r *http.Request, relay *Relay, personID, profileID string) {
	if profileID == "legacy" {
		legacyPath := relay.personTable.GetLegacyFaceImagePath(personID)
		if err := relay.personTable.ClearLegacyFaceImage(personID); err != nil {
			log.Printf("[Persons] ClearLegacyFaceImage %s: %v", personID, err)
			http.Error(w, "Failed to delete legacy face image", http.StatusInternalServerError)
			return
		}
		if legacyPath != "" {
			_ = os.Remove(filepath.Join(relay.config.AppDir, "storage", legacyPath))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
		return
	}

	imagePath, err := relay.personTable.DeleteFaceProfile(personID, profileID)
	if err == sql.ErrNoRows {
		http.Error(w, "Face profile not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("[Persons] DeleteFaceProfile %s/%s: %v", personID, profileID, err)
		http.Error(w, "Failed to delete face profile", http.StatusInternalServerError)
		return
	}
	if imagePath != "" {
		_ = os.Remove(filepath.Join(relay.config.AppDir, "storage", imagePath))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// handlePersonSightings returns recent events where this person_id appears in metadata.
// GET /persons/{id}/sightings?limit=50  (authenticated)
func handlePersonSightings(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64, personID string) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	if !relay.personTable.UserOwnsPerson(userID, personID) {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	events, err := relay.db.GetPersonSightings(userID, personID, limit)
	if err != nil {
		log.Printf("[Persons] GetPersonSightings %s: %v", personID, err)
		http.Error(w, "Failed to fetch sightings", http.StatusInternalServerError)
		return
	}

	type sighting struct {
		ID        string                 `json:"id"`
		AgentID   string                 `json:"agent_id"`
		Data      map[string]interface{} `json:"data"`
		Metadata  map[string]interface{} `json:"metadata,omitempty"`
		CreatedAt string                 `json:"created_at"`
	}

	out := make([]sighting, 0, len(events))
	for _, ev := range events {
		out = append(out, sighting{
			ID:        ev.ID,
			AgentID:   ev.AgentID,
			Data:      ev.Data,
			Metadata:  ev.Metadata,
			CreatedAt: ev.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handlePersonMoments returns timeline moments for one person.
// GET /persons/{id}/moments?date=YYYY-MM-DD&limit=100
func handlePersonMoments(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64, personID string) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if date != "" {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			http.Error(w, "date must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
	}

	if !relay.personTable.UserOwnsPerson(userID, personID) {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	moments, err := relay.db.GetPersonMoments(userID, personID, date, limit)
	if err != nil {
		log.Printf("[Persons] GetPersonMoments %s: %v", personID, err)
		http.Error(w, "Failed to fetch moments", http.StatusInternalServerError)
		return
	}

	type momentOut struct {
		ID             string `json:"id"`
		GlobalPersonID string `json:"global_person_id"`
		PersonID       string `json:"person_id,omitempty"`
		ServiceID      string `json:"service_id,omitempty"`
		EventID        string `json:"event_id,omitempty"`
		SnapshotPath   string `json:"snapshot_path,omitempty"`
		ActivityBrief  string `json:"activity_brief,omitempty"`
		CreatedAt      string `json:"created_at"`
	}
	out := make([]momentOut, 0, len(moments))
	for _, m := range moments {
		out = append(out, momentOut{
			ID:             m.ID,
			GlobalPersonID: m.GlobalPersonID,
			PersonID:       m.PersonID,
			ServiceID:      m.ServiceID,
			EventID:        m.EventID,
			SnapshotPath:   m.SnapshotPath,
			ActivityBrief:  m.ActivityBrief,
			CreatedAt:      m.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handlePersonsRegistry returns a lightweight registry of all persons with face images
// for worker-side face recognition sync.
// GET /persons/registry — no auth (internal use, no PII beyond person_id/name)
func handlePersonsRegistry(w http.ResponseWriter, r *http.Request, relay *Relay) {
	rows, err := relay.db.DB.Query(
		`SELECT p.id, p.name, COALESCE(p.face_updated_at, ''), COUNT(fp.id) AS profile_count,
		        (p.face_image IS NOT NULL OR (p.image_path IS NOT NULL AND p.image_path != '')) AS has_legacy_face
		 FROM persons p
		 LEFT JOIN person_face_profiles fp ON fp.person_id = p.id
		 GROUP BY p.id, p.name, p.face_updated_at, p.face_image, p.image_path, p.created_at
		 HAVING (
		     (p.face_image IS NOT NULL OR (p.image_path IS NOT NULL AND p.image_path != ''))
		     OR COUNT(fp.id) > 0
		 )
		 ORDER BY p.created_at DESC`,
	)
	if err != nil {
		log.Printf("[Persons] Registry query: %v", err)
		http.Error(w, "Failed to query registry", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type entry struct {
		PersonID               string `json:"person_id"`
		Name                   string `json:"name"`
		FaceUpdatedAt          string `json:"face_updated_at"`
		ProfileCount           int    `json:"profile_count"`
		EnrollmentReady        bool   `json:"enrollment_ready"`
		EnrollmentStatusReason string `json:"enrollment_status_reason,omitempty"`
	}

	entries := make([]entry, 0)
	minProfiles := enrollmentMinProfiles()
	for rows.Next() {
		var hasLegacy bool
		var e entry
		if err := rows.Scan(&e.PersonID, &e.Name, &e.FaceUpdatedAt, &e.ProfileCount, &hasLegacy); err != nil {
			continue
		}
		effectiveProfiles := e.ProfileCount
		if effectiveProfiles == 0 && hasLegacy {
			effectiveProfiles = 1
		}
		if effectiveProfiles >= minProfiles {
			e.EnrollmentReady = true
			e.EnrollmentStatusReason = "Ready for recognition."
		} else {
			e.EnrollmentReady = false
			e.EnrollmentStatusReason = fmt.Sprintf(
				"Need at least %d accepted profiles (%d/%d).",
				minProfiles,
				effectiveProfiles,
				minProfiles,
			)
		}
		entries = append(entries, e)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

func readUploadedImageData(r *http.Request) ([]byte, error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/") {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			return nil, err
		}
		f, _, err := r.FormFile("image")
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, 10<<20))
	}
	return io.ReadAll(io.LimitReader(r.Body, 10<<20))
}

func readImageFromStorage(relay *Relay, relPath string) ([]byte, bool) {
	fullPath := filepath.Join(relay.config.AppDir, "storage", relPath)
	data, err := os.ReadFile(fullPath)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

type faceEnrollmentQualityResult struct {
	Accepted bool                   `json:"accepted"`
	Reason   string                 `json:"reason"`
	Metrics  map[string]interface{} `json:"metrics"`
}

func qualityMetrics(v *faceEnrollmentQualityResult) map[string]interface{} {
	if v == nil || v.Metrics == nil {
		return map[string]interface{}{}
	}
	return v.Metrics
}

func personEnvBool(name string, defaultValue bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	if raw == "" {
		return defaultValue
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return defaultValue
	}
}

func runFaceEnrollmentQualityCheck(relay *Relay, rawImage []byte) (*faceEnrollmentQualityResult, []byte, error) {
	if relay == nil || relay.config == nil {
		return nil, nil, fmt.Errorf("relay config missing")
	}
	if strings.TrimSpace(relay.config.RootDir) == "" {
		return nil, nil, fmt.Errorf("VYRA_ROOT is not configured")
	}

	enginePyDir := filepath.Join(relay.config.RootDir, "engine", "py")
	scriptPath := filepath.Join(enginePyDir, "face_enrollment_quality.py")
	if _, err := os.Stat(scriptPath); err != nil {
		return nil, nil, fmt.Errorf("quality checker script not found: %w", err)
	}

	tempDir, err := os.MkdirTemp("", "face-enroll-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	inputPath := filepath.Join(tempDir, "input.jpg")
	outputPath := filepath.Join(tempDir, "normalized.jpg")

	if err := os.WriteFile(inputPath, rawImage, 0644); err != nil {
		return nil, nil, fmt.Errorf("write temp input: %w", err)
	}

	cmd := exec.Command("uv", "run", "python", scriptPath, "--input", inputPath, "--output", outputPath)
	cmd.Dir = enginePyDir
	out, cmdErr := cmd.CombinedOutput()

	var result faceEnrollmentQualityResult
	parsed := false
	payload := strings.TrimSpace(string(out))
	if payload != "" {
		// Keep only the last line to tolerate incidental log output.
		lines := strings.Split(payload, "\n")
		last := strings.TrimSpace(lines[len(lines)-1])
		if last != "" {
			if err := json.Unmarshal([]byte(last), &result); err == nil {
				parsed = true
			}
		}
	}

	if cmdErr != nil && !parsed {
		return nil, nil, fmt.Errorf("quality checker execution failed: %w (output=%s)", cmdErr, payload)
	}
	if !parsed {
		return nil, nil, fmt.Errorf("quality checker returned invalid output: %s", payload)
	}

	normalized := rawImage
	if data, err := os.ReadFile(outputPath); err == nil && len(data) > 0 {
		normalized = data
	}

	if cmdErr != nil {
		// Script exits non-zero for internal errors but still emits a structured reason.
		return &result, normalized, nil
	}
	return &result, normalized, nil
}
