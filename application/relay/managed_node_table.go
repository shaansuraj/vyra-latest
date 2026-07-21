package relay

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ManagedNode represents a locally managed node whose lifecycle VYRA controls.
type ManagedNode struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	UserID    int64     `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// ManagedNodeTable handles CRUD for the managed_nodes table.
type ManagedNodeTable struct {
	db *sql.DB
}

// NewManagedNodeTable creates a new ManagedNodeTable.
func NewManagedNodeTable(db *sql.DB) *ManagedNodeTable {
	return &ManagedNodeTable{db: db}
}

// CreateManagedNode inserts a new managed node record.
func (t *ManagedNodeTable) CreateManagedNode(name string, userID int64) (*ManagedNode, error) {
	id := uuid.New().String()
	now := time.Now()
	_, err := t.db.Exec(
		"INSERT INTO managed_nodes (id, name, user_id, created_at) VALUES (?, ?, ?, ?)",
		id, name, userID, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert managed_node: %w", err)
	}
	return &ManagedNode{ID: id, Name: name, UserID: userID, CreatedAt: now}, nil
}

// GetManagedNodesByUser returns all managed nodes in the shared workspace.
// userID is ignored to preserve call-site compatibility.
func (t *ManagedNodeTable) GetManagedNodesByUser(userID int64) ([]*ManagedNode, error) {
	_ = userID
	rows, err := t.db.Query(
		"SELECT id, name, user_id, created_at FROM managed_nodes ORDER BY created_at ASC",
	)
	if err != nil {
		return nil, fmt.Errorf("query managed_nodes: %w", err)
	}
	defer rows.Close()
	return scanManagedNodes(rows)
}

// GetAllManagedNodes returns all managed nodes regardless of owner.
func (t *ManagedNodeTable) GetAllManagedNodes() ([]*ManagedNode, error) {
	rows, err := t.db.Query(
		"SELECT id, name, user_id, created_at FROM managed_nodes ORDER BY created_at ASC",
	)
	if err != nil {
		return nil, fmt.Errorf("query all managed_nodes: %w", err)
	}
	defer rows.Close()
	return scanManagedNodes(rows)
}

// GetManagedNode returns a single managed node by ID.
func (t *ManagedNodeTable) GetManagedNode(id string) (*ManagedNode, error) {
	var mn ManagedNode
	err := t.db.QueryRow(
		"SELECT id, name, user_id, created_at FROM managed_nodes WHERE id = ?", id,
	).Scan(&mn.ID, &mn.Name, &mn.UserID, &mn.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("managed node not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get managed_node: %w", err)
	}
	return &mn, nil
}

// UpdateManagedNodeName changes the display name.
func (t *ManagedNodeTable) UpdateManagedNodeName(id, name string) error {
	res, err := t.db.Exec("UPDATE managed_nodes SET name = ? WHERE id = ?", name, id)
	if err != nil {
		return fmt.Errorf("update managed_node: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("managed node not found: %s", id)
	}
	return nil
}

// DeleteManagedNode removes the managed node record.
func (t *ManagedNodeTable) DeleteManagedNode(id string) error {
	res, err := t.db.Exec("DELETE FROM managed_nodes WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete managed_node: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("managed node not found: %s", id)
	}
	return nil
}

// UserOwnsManagedNode checks whether a managed node exists.
// userID is ignored to preserve call-site compatibility.
func (t *ManagedNodeTable) UserOwnsManagedNode(userID int64, nodeID string) bool {
	_ = userID
	var id string
	err := t.db.QueryRow(
		"SELECT id FROM managed_nodes WHERE id = ?", nodeID,
	).Scan(&id)
	return err == nil
}

func scanManagedNodes(rows *sql.Rows) ([]*ManagedNode, error) {
	var result []*ManagedNode
	for rows.Next() {
		var mn ManagedNode
		if err := rows.Scan(&mn.ID, &mn.Name, &mn.UserID, &mn.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan managed_node: %w", err)
		}
		result = append(result, &mn)
	}
	return result, rows.Err()
}
