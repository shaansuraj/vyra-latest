package relay

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Person represents a registered identity for face recognition.
type Person struct {
	ID                     string    `json:"id"`
	Name                   string    `json:"name"`
	UserID                 int64     `json:"-"`
	HasFace                bool      `json:"has_face"`
	ProfileCount           int       `json:"profile_count"`
	EnrollmentReady        bool      `json:"enrollment_ready"`
	EnrollmentStatusReason string    `json:"enrollment_status_reason,omitempty"`
	ImagePath              string    `json:"image_path,omitempty"` // legacy relative path under {AppDir}/storage/
	FaceUpdatedAt          time.Time `json:"face_updated_at,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
}

// PersonFaceProfile stores one uploaded face profile image for a person.
type PersonFaceProfile struct {
	ID        string    `json:"id"`
	PersonID  string    `json:"person_id"`
	ImagePath string    `json:"image_path,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	IsLegacy  bool      `json:"is_legacy,omitempty"`
}

// PersonTable handles person data access.
type PersonTable struct {
	db                *sql.DB
	faceSchemaOnce    sync.Once
	faceSchemaInitErr error
}

func NewPersonTable(db *sql.DB) *PersonTable {
	return &PersonTable{db: db}
}

func (t *PersonTable) ensureFaceProfilesSchema() error {
	t.faceSchemaOnce.Do(func() {
		const schema = `
		CREATE TABLE IF NOT EXISTS person_face_profiles (
			id TEXT PRIMARY KEY,
			person_id TEXT NOT NULL,
			image_path TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY (person_id) REFERENCES persons(id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_person_face_profiles_person ON person_face_profiles(person_id);
		`
		_, err := t.db.Exec(schema)
		t.faceSchemaInitErr = err
	})
	return t.faceSchemaInitErr
}

func isMissingFaceProfilesTableErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "no such table: person_face_profiles")
}

const personSelectColumns = `
	p.id, p.name, p.user_id,
	(
		(p.face_image IS NOT NULL OR (p.image_path IS NOT NULL AND p.image_path != ''))
		OR COUNT(fp.id) > 0
	) AS has_face,
	COALESCE(p.face_updated_at, ''), COALESCE(p.image_path, ''), p.created_at,
	COUNT(fp.id) AS profile_count
`

// CreatePerson creates a new person record.
func (t *PersonTable) CreatePerson(name string, userID int64) (*Person, error) {
	id := uuid.New().String()
	now := time.Now()
	_, err := t.db.Exec(
		`INSERT INTO persons (id, name, user_id, created_at) VALUES (?, ?, ?, ?)`,
		id, name, userID, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert person: %w", err)
	}
	p := &Person{ID: id, Name: name, UserID: userID, CreatedAt: now, ProfileCount: 0}
	applyEnrollmentStatus(p)
	return p, nil
}

// GetPersonsByUser returns all persons in the shared workspace (no face image bytes).
// userID is ignored to preserve call-site compatibility.
func (t *PersonTable) GetPersonsByUser(userID int64) ([]*Person, error) {
	_ = userID
	if err := t.ensureFaceProfilesSchema(); err != nil {
		return nil, fmt.Errorf("ensure face profile schema: %w", err)
	}

	rows, err := t.db.Query(
		`SELECT ` + personSelectColumns + `
		 FROM persons p
		 LEFT JOIN person_face_profiles fp ON fp.person_id = p.id
		 GROUP BY p.id, p.name, p.user_id, p.face_image, p.image_path, p.face_updated_at, p.created_at
		 ORDER BY p.created_at DESC`,
	)
	if err != nil {
		if isMissingFaceProfilesTableErr(err) {
			rowsLegacy, legacyErr := t.db.Query(
				`SELECT id, name, user_id,
				        (face_image IS NOT NULL OR (image_path IS NOT NULL AND image_path != '')) AS has_face,
				        COALESCE(face_updated_at,''), COALESCE(image_path,''), created_at,
				        0 AS profile_count
				 FROM persons ORDER BY created_at DESC`,
			)
			if legacyErr != nil {
				return nil, fmt.Errorf("query persons (legacy fallback): %w", legacyErr)
			}
			defer rowsLegacy.Close()
			return scanPersons(rowsLegacy)
		}
		return nil, fmt.Errorf("query persons: %w", err)
	}
	defer rows.Close()
	return scanPersons(rows)
}

// GetPersonByID returns a single person record (no face image bytes).
func (t *PersonTable) GetPersonByID(id string) (*Person, error) {
	if err := t.ensureFaceProfilesSchema(); err != nil {
		return nil, fmt.Errorf("ensure face profile schema: %w", err)
	}

	row := t.db.QueryRow(
		`SELECT `+personSelectColumns+`
		 FROM persons p
		 LEFT JOIN person_face_profiles fp ON fp.person_id = p.id
		 WHERE p.id = ?
		 GROUP BY p.id, p.name, p.user_id, p.face_image, p.image_path, p.face_updated_at, p.created_at`,
		id,
	)
	p, err := scanPerson(row)
	if err != nil {
		if isMissingFaceProfilesTableErr(err) {
			rowLegacy := t.db.QueryRow(
				`SELECT id, name, user_id,
				        (face_image IS NOT NULL OR (image_path IS NOT NULL AND image_path != '')) AS has_face,
				        COALESCE(face_updated_at,''), COALESCE(image_path,''), created_at,
				        0 AS profile_count
				 FROM persons WHERE id = ?`,
				id,
			)
			pLegacy, legacyErr := scanPerson(rowLegacy)
			if legacyErr != nil {
				return nil, fmt.Errorf("get person by id (legacy fallback): %w", legacyErr)
			}
			return pLegacy, nil
		}
		return nil, fmt.Errorf("get person by id: %w", err)
	}
	return p, nil
}

// GetFaceImage returns the legacy JPEG bytes stored in persons.face_image (nil if none stored).
func (t *PersonTable) GetFaceImage(personID string) ([]byte, error) {
	var data []byte
	err := t.db.QueryRow(`SELECT face_image FROM persons WHERE id = ?`, personID).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get face image: %w", err)
	}
	return data, nil
}

// SetFaceImage stores (or replaces) the legacy face JPEG as a BLOB.
func (t *PersonTable) SetFaceImage(personID string, data []byte) error {
	now := time.Now()
	_, err := t.db.Exec(
		`UPDATE persons SET face_image = ?, face_updated_at = ? WHERE id = ?`,
		data, now, personID,
	)
	if err != nil {
		return fmt.Errorf("set face image: %w", err)
	}
	return nil
}

// SetFaceImagePath stores the legacy filesystem path for a person's face image and clears any BLOB.
func (t *PersonTable) SetFaceImagePath(personID, imagePath string) error {
	now := time.Now()
	_, err := t.db.Exec(
		`UPDATE persons SET image_path = ?, face_updated_at = ?, face_image = NULL WHERE id = ?`,
		imagePath, now, personID,
	)
	if err != nil {
		return fmt.Errorf("set face image path: %w", err)
	}
	return nil
}

// ClearLegacyFaceImage removes legacy face storage fields from persons.
func (t *PersonTable) ClearLegacyFaceImage(personID string) error {
	now := time.Now()
	_, err := t.db.Exec(
		`UPDATE persons SET face_image = NULL, image_path = NULL, face_updated_at = ? WHERE id = ?`,
		now, personID,
	)
	if err != nil {
		return fmt.Errorf("clear legacy face image: %w", err)
	}
	return nil
}

// GetLegacyFaceImagePath returns persons.image_path without profile fallback.
func (t *PersonTable) GetLegacyFaceImagePath(personID string) string {
	var path sql.NullString
	_ = t.db.QueryRow(`SELECT image_path FROM persons WHERE id = ?`, personID).Scan(&path)
	return path.String
}

// HasLegacyFace returns true when the person has legacy face data in persons table.
func (t *PersonTable) HasLegacyFace(personID string) bool {
	var hasLegacy bool
	_ = t.db.QueryRow(
		`SELECT (face_image IS NOT NULL OR (image_path IS NOT NULL AND image_path != '')) FROM persons WHERE id = ?`,
		personID,
	).Scan(&hasLegacy)
	return hasLegacy
}

// GetFaceImagePath returns a usable filesystem image path for face retrieval.
// It prefers the newest multi-profile path, then falls back to legacy persons.image_path.
func (t *PersonTable) GetFaceImagePath(personID string) string {
	_ = t.ensureFaceProfilesSchema()

	var path sql.NullString
	err := t.db.QueryRow(
		`SELECT image_path
		 FROM person_face_profiles
		 WHERE person_id = ?
		 ORDER BY updated_at DESC, created_at DESC
		 LIMIT 1`,
		personID,
	).Scan(&path)
	if err == nil && path.Valid && path.String != "" {
		return path.String
	}
	return t.GetLegacyFaceImagePath(personID)
}

// CreateFaceProfile inserts a new face profile path for one person.
func (t *PersonTable) CreateFaceProfile(personID, imagePath string) (*PersonFaceProfile, error) {
	if err := t.ensureFaceProfilesSchema(); err != nil {
		return nil, fmt.Errorf("ensure face profile schema: %w", err)
	}

	id := uuid.New().String()
	now := time.Now()
	_, err := t.db.Exec(
		`INSERT INTO person_face_profiles (id, person_id, image_path, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, personID, imagePath, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert person face profile: %w", err)
	}
	_, _ = t.db.Exec(`UPDATE persons SET face_updated_at = ? WHERE id = ?`, now, personID)
	return &PersonFaceProfile{
		ID:        id,
		PersonID:  personID,
		ImagePath: imagePath,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// ListFaceProfiles returns all stored (non-legacy) face profiles for one person.
func (t *PersonTable) ListFaceProfiles(personID string) ([]*PersonFaceProfile, error) {
	if err := t.ensureFaceProfilesSchema(); err != nil {
		return nil, fmt.Errorf("ensure face profile schema: %w", err)
	}

	rows, err := t.db.Query(
		`SELECT id, person_id, image_path, COALESCE(created_at, ''), COALESCE(updated_at, '')
		 FROM person_face_profiles
		 WHERE person_id = ?
		 ORDER BY updated_at DESC, created_at DESC`,
		personID,
	)
	if err != nil {
		return nil, fmt.Errorf("list face profiles: %w", err)
	}
	defer rows.Close()

	out := make([]*PersonFaceProfile, 0)
	for rows.Next() {
		var p PersonFaceProfile
		var createdAtRaw, updatedAtRaw string
		if err := rows.Scan(&p.ID, &p.PersonID, &p.ImagePath, &createdAtRaw, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan face profile: %w", err)
		}
		p.CreatedAt = parseStoredTime(createdAtRaw)
		p.UpdatedAt = parseStoredTime(updatedAtRaw)
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate face profiles: %w", err)
	}
	return out, nil
}

// GetFaceProfileByID returns one non-legacy face profile.
func (t *PersonTable) GetFaceProfileByID(personID, profileID string) (*PersonFaceProfile, error) {
	if err := t.ensureFaceProfilesSchema(); err != nil {
		return nil, fmt.Errorf("ensure face profile schema: %w", err)
	}

	row := t.db.QueryRow(
		`SELECT id, person_id, image_path, COALESCE(created_at, ''), COALESCE(updated_at, '')
		 FROM person_face_profiles
		 WHERE person_id = ? AND id = ?`,
		personID, profileID,
	)
	var p PersonFaceProfile
	var createdAtRaw, updatedAtRaw string
	if err := row.Scan(&p.ID, &p.PersonID, &p.ImagePath, &createdAtRaw, &updatedAtRaw); err != nil {
		return nil, err
	}
	p.CreatedAt = parseStoredTime(createdAtRaw)
	p.UpdatedAt = parseStoredTime(updatedAtRaw)
	return &p, nil
}

// DeleteFaceProfile deletes one non-legacy face profile and returns its image path.
func (t *PersonTable) DeleteFaceProfile(personID, profileID string) (string, error) {
	if err := t.ensureFaceProfilesSchema(); err != nil {
		return "", fmt.Errorf("ensure face profile schema: %w", err)
	}

	var imagePath string
	err := t.db.QueryRow(
		`SELECT image_path FROM person_face_profiles WHERE person_id = ? AND id = ?`,
		personID, profileID,
	).Scan(&imagePath)
	if err != nil {
		return "", err
	}

	_, err = t.db.Exec(
		`DELETE FROM person_face_profiles WHERE person_id = ? AND id = ?`,
		personID, profileID,
	)
	if err != nil {
		return "", fmt.Errorf("delete face profile: %w", err)
	}

	now := time.Now()
	_, _ = t.db.Exec(
		`UPDATE persons
		 SET face_updated_at = ?,
		     image_path = CASE WHEN image_path = ? THEN NULL ELSE image_path END
		 WHERE id = ?`,
		now, imagePath, personID,
	)

	return imagePath, nil
}

// GetAllFaceRegistry returns all persons with any usable face profile in the shared workspace.
// userID is ignored to preserve call-site compatibility.
func (t *PersonTable) GetAllFaceRegistry(userID int64) ([]*Person, error) {
	_ = userID
	if err := t.ensureFaceProfilesSchema(); err != nil {
		return nil, fmt.Errorf("ensure face profile schema: %w", err)
	}

	rows, err := t.db.Query(
		`SELECT ` + personSelectColumns + `
		 FROM persons p
		 LEFT JOIN person_face_profiles fp ON fp.person_id = p.id
		 GROUP BY p.id, p.name, p.user_id, p.face_image, p.image_path, p.face_updated_at, p.created_at
		 HAVING (
		     (p.face_image IS NOT NULL OR (p.image_path IS NOT NULL AND p.image_path != ''))
		     OR COUNT(fp.id) > 0
		 )
		 ORDER BY p.created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query face registry: %w", err)
	}
	defer rows.Close()
	return scanPersons(rows)
}

// UpdatePersonName updates a person's display name.
func (t *PersonTable) UpdatePersonName(id, name string) error {
	_, err := t.db.Exec(`UPDATE persons SET name = ? WHERE id = ?`, name, id)
	return err
}

// DeletePerson deletes a person and their face data.
func (t *PersonTable) DeletePerson(id string) error {
	_, _ = t.db.Exec(`DELETE FROM person_face_profiles WHERE person_id = ?`, id)
	_, err := t.db.Exec(`DELETE FROM persons WHERE id = ?`, id)
	return err
}

// UserOwnsPerson returns true if the person record exists in the shared workspace.
// userID is ignored to preserve call-site compatibility.
func (t *PersonTable) UserOwnsPerson(userID int64, personID string) bool {
	_ = userID
	var id string
	err := t.db.QueryRow(`SELECT id FROM persons WHERE id = ?`, personID).Scan(&id)
	return err == nil
}

// ── Helpers ─────────────────────────────────────────────────────────────────

type personScanner interface {
	Scan(dest ...any) error
}

func scanPerson(row personScanner) (*Person, error) {
	var p Person
	var hasFace bool
	var faceUpdatedAtRaw, imagePath string
	if err := row.Scan(
		&p.ID,
		&p.Name,
		&p.UserID,
		&hasFace,
		&faceUpdatedAtRaw,
		&imagePath,
		&p.CreatedAt,
		&p.ProfileCount,
	); err != nil {
		return nil, err
	}
	p.HasFace = hasFace
	p.ImagePath = imagePath
	p.FaceUpdatedAt = parseStoredTime(faceUpdatedAtRaw)
	applyEnrollmentStatus(&p)
	return &p, nil
}

func scanPersons(rows *sql.Rows) ([]*Person, error) {
	var out []*Person
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, fmt.Errorf("scan person: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate persons: %w", err)
	}
	return out, nil
}

func parseStoredTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02T15:04:05Z07:00", raw); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", raw); err == nil {
		return t
	}
	return time.Time{}
}

func enrollmentMinProfiles() int {
	raw := strings.TrimSpace(os.Getenv("FACE_ENROLLMENT_MIN_PROFILES"))
	if raw == "" {
		return 1
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return 1
	}
	return v
}

func applyEnrollmentStatus(p *Person) {
	if p == nil {
		return
	}
	minProfiles := enrollmentMinProfiles()
	effectiveProfiles := p.ProfileCount
	if effectiveProfiles == 0 && p.HasFace {
		// Legacy single-face storage counts as one profile for readiness messaging.
		effectiveProfiles = 1
	}
	if effectiveProfiles >= minProfiles {
		p.EnrollmentReady = true
		p.EnrollmentStatusReason = "Ready for recognition."
		return
	}

	p.EnrollmentReady = false
	if !p.HasFace {
		p.EnrollmentStatusReason = "No profiles uploaded."
		return
	}
	p.EnrollmentStatusReason = fmt.Sprintf(
		"Need at least %d accepted profiles (%d/%d).",
		minProfiles,
		effectiveProfiles,
		minProfiles,
	)
}
