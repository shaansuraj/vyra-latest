package relay

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// CameraGroup represents a named grouping of camera service IDs for a user.
type CameraGroup struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	ServiceIDs []string  `json:"service_ids"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func parseDBTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	// Prefer RFC3339Nano (what we write)
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	// Common sqlite CURRENT_TIMESTAMP format
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t.UTC()
	}
	return time.Now().UTC()
}

// ListCameraGroups returns all camera groups in the shared workspace with their member service IDs.
// userID is ignored to preserve call-site compatibility.
func (db *Database) ListCameraGroups(userID int64) ([]*CameraGroup, error) {
	_ = userID
	rows, err := db.DB.Query(`
		SELECT id, name, created_at, updated_at
		FROM camera_groups
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := []*CameraGroup{}
	for rows.Next() {
		var createdAtStr, updatedAtStr string
		g := &CameraGroup{}
		if err := rows.Scan(&g.ID, &g.Name, &createdAtStr, &updatedAtStr); err != nil {
			return nil, err
		}
		g.CreatedAt = parseDBTime(createdAtStr)
		g.UpdatedAt = parseDBTime(updatedAtStr)

		// Members
		mrows, err := db.DB.Query(`
			SELECT service_id
			FROM camera_group_members
			WHERE group_id = ?
			ORDER BY service_id ASC
		`, g.ID)
		if err != nil {
			return nil, err
		}
		for mrows.Next() {
			var sid string
			if err := mrows.Scan(&sid); err != nil {
				mrows.Close()
				return nil, err
			}
			g.ServiceIDs = append(g.ServiceIDs, sid)
		}
		mrows.Close()

		groups = append(groups, g)
	}

	return groups, nil
}

// CreateCameraGroup creates a workspace camera group.
// userID is retained as creator metadata.
func (db *Database) CreateCameraGroup(userID int64, name string) (*CameraGroup, error) {
	now := time.Now().UTC()
	id := uuid.New().String()

	_, err := db.DB.Exec(`
		INSERT INTO camera_groups (id, name, user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, id, name, userID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}

	return &CameraGroup{ID: id, Name: name, ServiceIDs: []string{}, CreatedAt: now, UpdatedAt: now}, nil
}

// RenameCameraGroup updates a workspace camera group name.
// userID is ignored to preserve call-site compatibility.
func (db *Database) RenameCameraGroup(userID int64, groupID string, name string) error {
	_ = userID
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := db.DB.Exec(`
		UPDATE camera_groups
		SET name = ?, updated_at = ?
		WHERE id = ?
	`, name, now, groupID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteCameraGroup removes a workspace camera group.
// userID is ignored to preserve call-site compatibility.
func (db *Database) DeleteCameraGroup(userID int64, groupID string) error {
	_ = userID
	res, err := db.DB.Exec(`
		DELETE FROM camera_groups
		WHERE id = ?
	`, groupID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// AddCameraToGroup adds a service to a workspace camera group.
// userID is ignored to preserve call-site compatibility.
func (db *Database) AddCameraToGroup(userID int64, groupID string, serviceID string) error {
	_ = userID
	// Ensure group exists.
	var exists int
	err := db.DB.QueryRow(`SELECT 1 FROM camera_groups WHERE id = ?`, groupID).Scan(&exists)
	if err != nil {
		return err
	}

	_, err = db.DB.Exec(`
		INSERT OR IGNORE INTO camera_group_members (group_id, service_id)
		VALUES (?, ?)
	`, groupID, serviceID)
	if err != nil {
		return err
	}

	// Touch group
	_, _ = db.DB.Exec(`UPDATE camera_groups SET updated_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339Nano), groupID)
	return nil
}

// RemoveCameraFromGroup removes a service from a workspace camera group.
// userID is ignored to preserve call-site compatibility.
func (db *Database) RemoveCameraFromGroup(userID int64, groupID string, serviceID string) error {
	_ = userID
	// Ensure group exists.
	var exists int
	err := db.DB.QueryRow(`SELECT 1 FROM camera_groups WHERE id = ?`, groupID).Scan(&exists)
	if err != nil {
		return err
	}

	_, err = db.DB.Exec(`
		DELETE FROM camera_group_members
		WHERE group_id = ? AND service_id = ?
	`, groupID, serviceID)
	if err != nil {
		return err
	}

	// Touch group
	_, _ = db.DB.Exec(`UPDATE camera_groups SET updated_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339Nano), groupID)
	return nil
}
