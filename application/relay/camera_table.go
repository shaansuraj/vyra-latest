package relay

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Camera represents a user-managed camera stored in the database
type Camera struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	Addr          string    `json:"addr"`
	Port          int       `json:"port"`
	Path          string    `json:"path"`
	ProcessingFPS float64   `json:"processing_fps"`
	VLMFPS        float64   `json:"vlm_fps"`
	AuthUsername  string    `json:"-"`
	AuthPassword  string    `json:"-"`
	UserID        int64     `json:"-"`
	ManagedNodeID string    `json:"managed_node_id,omitempty"`
	NodeIDs       []string  `json:"node_ids,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// CameraTable handles camera data access
type CameraTable struct {
	db *sql.DB
}

const (
	defaultProcessingFPS = 15.0
	defaultVLMFPS        = 5.0
)

func normalizeCameraFPS(processingFPS, vlmFPS float64) (float64, float64, error) {
	if processingFPS <= 0 {
		processingFPS = defaultProcessingFPS
	} else if processingFPS < defaultProcessingFPS {
		return 0, 0, fmt.Errorf("processing_fps must be at least %.0f", defaultProcessingFPS)
	}
	if vlmFPS <= 0 {
		vlmFPS = defaultVLMFPS
	}
	return processingFPS, vlmFPS, nil
}

// NewCameraTable creates a new CameraTable
func NewCameraTable(db *sql.DB) *CameraTable {
	return &CameraTable{db: db}
}

// CreateCamera creates a new camera and returns it.
// managedNodeID may be empty (camera unassigned).
func (t *CameraTable) CreateCamera(name, cameraType, addr string, port int, path, authUsername, authPassword string, userID int64) (*Camera, error) {
	return t.CreateCameraWithNode(name, cameraType, addr, port, path, authUsername, authPassword, userID, "")
}

// CreateCameraWithNode creates a camera pre-assigned to a managed node.
func (t *CameraTable) CreateCameraWithNode(name, cameraType, addr string, port int, path, authUsername, authPassword string, userID int64, managedNodeID string) (*Camera, error) {
	id := uuid.New().String()
	now := time.Now()

	var nodeIDVal sql.NullString
	if managedNodeID != "" {
		nodeIDVal = sql.NullString{String: managedNodeID, Valid: true}
	}

	_, err := t.db.Exec(
		"INSERT INTO cameras (id, name, type, addr, port, path, auth_username, auth_password, processing_fps, vlm_fps, user_id, managed_node_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		id, name, cameraType, addr, port, path, authUsername, authPassword, defaultProcessingFPS, defaultVLMFPS, userID, nodeIDVal, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert camera: %w", err)
	}

	if managedNodeID != "" {
		if _, err := t.db.Exec(
			"INSERT INTO camera_node_assignments (camera_id, node_id, created_at) VALUES (?, ?, ?)",
			id, managedNodeID, now,
		); err != nil {
			return nil, fmt.Errorf("insert camera assignment: %w", err)
		}
	}

	return &Camera{
		ID:            id,
		Name:          name,
		Type:          cameraType,
		Addr:          addr,
		Port:          port,
		Path:          path,
		ProcessingFPS: defaultProcessingFPS,
		VLMFPS:        defaultVLMFPS,
		AuthUsername:  authUsername,
		AuthPassword:  authPassword,
		UserID:        userID,
		ManagedNodeID: managedNodeID,
		NodeIDs:       normalizeNodeIDs([]string{managedNodeID}),
		CreatedAt:     now,
	}, nil
}

// GetCamerasByUser returns all cameras in the shared workspace.
// userID is ignored to preserve call-site compatibility.
func (t *CameraTable) GetCamerasByUser(userID int64) ([]*Camera, error) {
	_ = userID
	rows, err := t.db.Query(
		"SELECT id, name, type, addr, port, path, COALESCE(processing_fps,15.0), COALESCE(vlm_fps,5.0), auth_username, auth_password, user_id, COALESCE(managed_node_id,''), created_at FROM cameras ORDER BY created_at DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("query cameras: %w", err)
	}
	defer rows.Close()
	cameras, err := scanCameras(rows)
	if err != nil {
		return nil, err
	}
	if err := t.attachNodeAssignments(cameras); err != nil {
		return nil, err
	}
	return cameras, nil
}

// GetAllCameras returns all cameras (for startup registration)
func (t *CameraTable) GetAllCameras() ([]*Camera, error) {
	rows, err := t.db.Query(
		"SELECT id, name, type, addr, port, path, COALESCE(processing_fps,15.0), COALESCE(vlm_fps,5.0), auth_username, auth_password, user_id, COALESCE(managed_node_id,''), created_at FROM cameras ORDER BY created_at DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("query all cameras: %w", err)
	}
	defer rows.Close()
	cameras, err := scanCameras(rows)
	if err != nil {
		return nil, err
	}
	if err := t.attachNodeAssignments(cameras); err != nil {
		return nil, err
	}
	return cameras, nil
}

// GetCamerasByManagedNode returns cameras assigned to a specific managed node.
func (t *CameraTable) GetCamerasByManagedNode(managedNodeID string) ([]*Camera, error) {
	rows, err := t.db.Query(
		`SELECT DISTINCT c.id, c.name, c.type, c.addr, c.port, c.path,
			COALESCE(c.processing_fps,15.0), COALESCE(c.vlm_fps,5.0),
			c.auth_username, c.auth_password, c.user_id, COALESCE(c.managed_node_id,''), c.created_at
		FROM cameras c
		LEFT JOIN camera_node_assignments a ON a.camera_id = c.id
		WHERE a.node_id = ? OR c.managed_node_id = ?
		ORDER BY c.created_at DESC`,
		managedNodeID, managedNodeID,
	)
	if err != nil {
		return nil, fmt.Errorf("query cameras by managed node: %w", err)
	}
	defer rows.Close()
	cameras, err := scanCameras(rows)
	if err != nil {
		return nil, err
	}
	if err := t.attachNodeAssignments(cameras); err != nil {
		return nil, err
	}
	return cameras, nil
}

func (t *CameraTable) GetCameraByID(cameraID string) (*Camera, error) {
	row := t.db.QueryRow(
		"SELECT id, name, type, addr, port, path, COALESCE(processing_fps,15.0), COALESCE(vlm_fps,5.0), auth_username, auth_password, user_id, COALESCE(managed_node_id,''), created_at FROM cameras WHERE id = ?",
		cameraID,
	)
	var c Camera
	if err := row.Scan(&c.ID, &c.Name, &c.Type, &c.Addr, &c.Port, &c.Path, &c.ProcessingFPS, &c.VLMFPS, &c.AuthUsername, &c.AuthPassword, &c.UserID, &c.ManagedNodeID, &c.CreatedAt); err != nil {
		return nil, fmt.Errorf("get camera by id: %w", err)
	}
	nodeIDs, err := t.GetCameraNodeIDs(cameraID)
	if err != nil {
		return nil, err
	}
	c.NodeIDs = nodeIDs
	return &c, nil
}

// AssignCameraToNode sets the managed_node_id for a camera.
func (t *CameraTable) AssignCameraToNode(cameraID, managedNodeID string) error {
	tx, err := t.db.Begin()
	if err != nil {
		return fmt.Errorf("begin assign camera: %w", err)
	}
	defer tx.Rollback()

	var existingID string
	if err := tx.QueryRow("SELECT id FROM cameras WHERE id = ?", cameraID).Scan(&existingID); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("camera not found: %s", cameraID)
		}
		return fmt.Errorf("check camera exists: %w", err)
	}

	var assignmentExists int
	if err := tx.QueryRow(
		"SELECT COUNT(1) FROM camera_node_assignments WHERE camera_id = ? AND node_id = ?",
		cameraID, managedNodeID,
	).Scan(&assignmentExists); err != nil {
		return fmt.Errorf("check assignment exists: %w", err)
	}
	if assignmentExists == 0 {
		if _, err := tx.Exec(
			"INSERT INTO camera_node_assignments (camera_id, node_id, created_at) VALUES (?, ?, ?)",
			cameraID, managedNodeID, time.Now(),
		); err != nil {
			return fmt.Errorf("insert assignment: %w", err)
		}
	}

	var currentPrimary string
	_ = tx.QueryRow("SELECT COALESCE(managed_node_id,'') FROM cameras WHERE id = ?", cameraID).Scan(&currentPrimary)
	if strings.TrimSpace(currentPrimary) == "" {
		if _, err := tx.Exec("UPDATE cameras SET managed_node_id = ? WHERE id = ?", managedNodeID, cameraID); err != nil {
			return fmt.Errorf("set primary assignment: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit assign camera: %w", err)
	}
	return nil
}

// UnassignCameraFromNode clears the managed_node_id for a camera.
func (t *CameraTable) UnassignCameraFromNode(cameraID string) error {
	tx, err := t.db.Begin()
	if err != nil {
		return fmt.Errorf("begin unassign camera: %w", err)
	}
	defer tx.Rollback()

	var existingID string
	if err := tx.QueryRow("SELECT id FROM cameras WHERE id = ?", cameraID).Scan(&existingID); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("camera not found: %s", cameraID)
		}
		return fmt.Errorf("check camera exists: %w", err)
	}

	if _, err := tx.Exec("DELETE FROM camera_node_assignments WHERE camera_id = ?", cameraID); err != nil {
		return fmt.Errorf("delete assignments: %w", err)
	}

	if _, err := tx.Exec("UPDATE cameras SET managed_node_id = NULL WHERE id = ?", cameraID); err != nil {
		return fmt.Errorf("clear primary assignment: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit unassign camera: %w", err)
	}
	return nil
}

// UnassignCameraFromSpecificNode removes one node assignment for a camera.
func (t *CameraTable) UnassignCameraFromSpecificNode(cameraID, managedNodeID string) error {
	tx, err := t.db.Begin()
	if err != nil {
		return fmt.Errorf("begin unassign camera from node: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM camera_node_assignments WHERE camera_id = ? AND node_id = ?", cameraID, managedNodeID); err != nil {
		return fmt.Errorf("delete assignment: %w", err)
	}

	var nextPrimary string
	_ = tx.QueryRow(
		"SELECT node_id FROM camera_node_assignments WHERE camera_id = ? ORDER BY created_at ASC, node_id ASC LIMIT 1",
		cameraID,
	).Scan(&nextPrimary)

	var currentPrimary string
	_ = tx.QueryRow("SELECT COALESCE(managed_node_id,'') FROM cameras WHERE id = ?", cameraID).Scan(&currentPrimary)
	if currentPrimary == managedNodeID {
		var primaryVal interface{}
		if strings.TrimSpace(nextPrimary) != "" {
			primaryVal = nextPrimary
		}
		if _, err := tx.Exec("UPDATE cameras SET managed_node_id = ? WHERE id = ?", primaryVal, cameraID); err != nil {
			return fmt.Errorf("update primary assignment: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit unassign camera from node: %w", err)
	}
	return nil
}

// GetCameraNodeIDs returns all node IDs assigned to a camera.
func (t *CameraTable) GetCameraNodeIDs(cameraID string) ([]string, error) {
	rows, err := t.db.Query(
		"SELECT node_id FROM camera_node_assignments WHERE camera_id = ? ORDER BY created_at ASC, node_id ASC",
		cameraID,
	)
	if err != nil {
		return nil, fmt.Errorf("query camera assignments: %w", err)
	}
	defer rows.Close()

	var nodeIDs []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return nil, fmt.Errorf("scan camera assignment: %w", err)
		}
		nodeID = strings.TrimSpace(nodeID)
		if nodeID != "" {
			nodeIDs = append(nodeIDs, nodeID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate camera assignments: %w", err)
	}

	if len(nodeIDs) == 0 {
		var legacy string
		if err := t.db.QueryRow("SELECT COALESCE(managed_node_id,'') FROM cameras WHERE id = ?", cameraID).Scan(&legacy); err == nil {
			legacy = strings.TrimSpace(legacy)
			if legacy != "" {
				nodeIDs = append(nodeIDs, legacy)
			}
		}
	}
	return normalizeNodeIDs(nodeIDs), nil
}

// SetCameraNodeAssignments replaces all node assignments for a camera.
func (t *CameraTable) SetCameraNodeAssignments(cameraID string, nodeIDs []string) error {
	nodeIDs = normalizeNodeIDs(nodeIDs)

	tx, err := t.db.Begin()
	if err != nil {
		return fmt.Errorf("begin set camera assignments: %w", err)
	}
	defer tx.Rollback()

	var existingID string
	if err := tx.QueryRow("SELECT id FROM cameras WHERE id = ?", cameraID).Scan(&existingID); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("camera not found: %s", cameraID)
		}
		return fmt.Errorf("check camera exists: %w", err)
	}

	if _, err := tx.Exec("DELETE FROM camera_node_assignments WHERE camera_id = ?", cameraID); err != nil {
		return fmt.Errorf("clear camera assignments: %w", err)
	}

	for _, nodeID := range nodeIDs {
		if _, err := tx.Exec(
			"INSERT INTO camera_node_assignments (camera_id, node_id, created_at) VALUES (?, ?, ?)",
			cameraID, nodeID, time.Now(),
		); err != nil {
			return fmt.Errorf("insert camera assignment: %w", err)
		}
	}

	var primaryVal interface{}
	if len(nodeIDs) > 0 {
		primaryVal = nodeIDs[0]
	}
	if _, err := tx.Exec("UPDATE cameras SET managed_node_id = ? WHERE id = ?", primaryVal, cameraID); err != nil {
		return fmt.Errorf("set primary assignment: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set camera assignments: %w", err)
	}
	return nil
}

// UpdateCamera updates mutable camera fields and returns the updated camera
func (t *CameraTable) UpdateCamera(id, name, addr string, port int, path, authUsername, authPassword string) (*Camera, error) {
	_, err := t.db.Exec(
		`UPDATE cameras SET name=?, addr=?, port=?, path=?, auth_username=?, auth_password=? WHERE id=?`,
		name, addr, port, path, authUsername, authPassword, id,
	)
	if err != nil {
		return nil, fmt.Errorf("update camera: %w", err)
	}
	var c Camera
	var managedNodeID string
	err = t.db.QueryRow(
		`SELECT id, name, type, addr, port, path, COALESCE(processing_fps,15.0), COALESCE(vlm_fps,5.0), auth_username, auth_password, user_id, COALESCE(managed_node_id,''), created_at FROM cameras WHERE id=?`, id,
	).Scan(&c.ID, &c.Name, &c.Type, &c.Addr, &c.Port, &c.Path, &c.ProcessingFPS, &c.VLMFPS, &c.AuthUsername, &c.AuthPassword, &c.UserID, &managedNodeID, &c.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("reload camera: %w", err)
	}
	c.ManagedNodeID = managedNodeID
	c.NodeIDs, err = t.GetCameraNodeIDs(id)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (t *CameraTable) UpdateCameraFPS(id string, processingFPS, vlmFPS float64) (*Camera, error) {
	var err error
	processingFPS, vlmFPS, err = normalizeCameraFPS(processingFPS, vlmFPS)
	if err != nil {
		return nil, err
	}
	if _, err := t.db.Exec(`UPDATE cameras SET processing_fps=?, vlm_fps=? WHERE id=?`, processingFPS, vlmFPS, id); err != nil {
		return nil, fmt.Errorf("update camera fps: %w", err)
	}
	var c Camera
	var managedNodeID string
	err = t.db.QueryRow(
		`SELECT id, name, type, addr, port, path, COALESCE(processing_fps,15.0), COALESCE(vlm_fps,5.0), auth_username, auth_password, user_id, COALESCE(managed_node_id,''), created_at FROM cameras WHERE id=?`, id,
	).Scan(&c.ID, &c.Name, &c.Type, &c.Addr, &c.Port, &c.Path, &c.ProcessingFPS, &c.VLMFPS, &c.AuthUsername, &c.AuthPassword, &c.UserID, &managedNodeID, &c.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("reload camera fps: %w", err)
	}
	c.ManagedNodeID = managedNodeID
	c.NodeIDs, err = t.GetCameraNodeIDs(id)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// DeleteCamera removes a camera by ID
func (t *CameraTable) DeleteCamera(cameraID string) error {
	result, err := t.db.Exec("DELETE FROM cameras WHERE id = ?", cameraID)
	if err != nil {
		return fmt.Errorf("delete camera: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("camera not found: %s", cameraID)
	}
	return nil
}

// UserOwnsCamera checks whether a camera exists in the shared workspace.
// userID is ignored to preserve call-site compatibility.
func (t *CameraTable) UserOwnsCamera(userID int64, cameraID string) bool {
	_ = userID
	var id string
	err := t.db.QueryRow("SELECT id FROM cameras WHERE id = ?", cameraID).Scan(&id)
	return err == nil
}

func scanCameras(rows *sql.Rows) ([]*Camera, error) {
	var cameras []*Camera
	for rows.Next() {
		var c Camera
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.Addr, &c.Port, &c.Path, &c.ProcessingFPS, &c.VLMFPS, &c.AuthUsername, &c.AuthPassword, &c.UserID, &c.ManagedNodeID, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan camera: %w", err)
		}
		cameras = append(cameras, &c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cameras: %w", err)
	}
	return cameras, nil
}

func (t *CameraTable) attachNodeAssignments(cameras []*Camera) error {
	for _, camera := range cameras {
		nodeIDs, err := t.GetCameraNodeIDs(camera.ID)
		if err != nil {
			return err
		}
		camera.NodeIDs = nodeIDs
	}
	return nil
}

func normalizeNodeIDs(nodeIDs []string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}
