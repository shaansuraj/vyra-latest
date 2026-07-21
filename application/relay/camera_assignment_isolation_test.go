package relay

import (
	"path/filepath"
	"testing"
)

func testRelayWithDB(t *testing.T) (*Relay, *Database) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "relay-test.db")
	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase failed: %v", err)
	}

	r := &Relay{
		db:          db,
		cameraTable: NewCameraTable(db.DB),
		services:    NewServiceRegistry(),
		config:      &Config{AutoRequestRealtimeStream: false},
	}
	return r, db
}

func insertTestUser(t *testing.T, db *Database, email, username string) int64 {
	t.Helper()
	res, err := db.Exec(
		"INSERT INTO users (email, password_hash, name, username) VALUES (?, ?, ?, ?)",
		email, "hash", username, username,
	)
	if err != nil {
		t.Fatalf("insert test user failed: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId failed: %v", err)
	}
	return id
}

func TestRegisterCamerasForNodeOnlyRegistersAssignedCameras(t *testing.T) {
	relay, db := testRelayWithDB(t)

	userA := insertTestUser(t, db, "a@example.com", "user_a")
	userB := insertTestUser(t, db, "b@example.com", "user_b")

	_, err := relay.cameraTable.CreateCameraWithNode("cam-a", "rtsp", "10.0.0.10", 554, "/a", "", "", userA, "node-a")
	if err != nil {
		t.Fatalf("create cam-a failed: %v", err)
	}
	_, err = relay.cameraTable.CreateCameraWithNode("cam-b", "rtsp", "10.0.0.11", 554, "/b", "", "", userB, "node-b")
	if err != nil {
		t.Fatalf("create cam-b failed: %v", err)
	}
	_, err = relay.cameraTable.CreateCameraWithNode("cam-unassigned", "rtsp", "10.0.0.12", 554, "/u", "", "", userB, "")
	if err != nil {
		t.Fatalf("create cam-unassigned failed: %v", err)
	}

	relay.registerCamerasForNode("node-a")

	services := relay.services.ListByNode("node-a")
	if len(services) != 1 {
		t.Fatalf("expected 1 service on node-a, got %d", len(services))
	}
	if services[0].Service.Name != "cam-a" {
		t.Fatalf("expected cam-a on node-a, got %s", services[0].Service.Name)
	}
}

func TestCameraNodeAssignmentsSupportMultipleNodes(t *testing.T) {
	relay, db := testRelayWithDB(t)
	userID := insertTestUser(t, db, "multi@example.com", "user_multi")

	camera, err := relay.cameraTable.CreateCameraWithNode("cam-multi", "rtsp", "10.0.1.10", 554, "/multi", "", "", userID, "")
	if err != nil {
		t.Fatalf("create camera failed: %v", err)
	}

	if err := relay.cameraTable.SetCameraNodeAssignments(camera.ID, []string{"node-1", "node-2", "node-1"}); err != nil {
		t.Fatalf("SetCameraNodeAssignments failed: %v", err)
	}

	updated, err := relay.cameraTable.GetCameraByID(camera.ID)
	if err != nil {
		t.Fatalf("GetCameraByID failed: %v", err)
	}
	if len(updated.NodeIDs) != 2 {
		t.Fatalf("expected 2 unique node assignments, got %d (%v)", len(updated.NodeIDs), updated.NodeIDs)
	}
	if updated.ManagedNodeID != "node-1" {
		t.Fatalf("expected managed_node_id primary node-1, got %s", updated.ManagedNodeID)
	}

	if err := relay.cameraTable.UnassignCameraFromSpecificNode(camera.ID, "node-1"); err != nil {
		t.Fatalf("UnassignCameraFromSpecificNode failed: %v", err)
	}

	updated, err = relay.cameraTable.GetCameraByID(camera.ID)
	if err != nil {
		t.Fatalf("GetCameraByID after unassign failed: %v", err)
	}
	if len(updated.NodeIDs) != 1 || updated.NodeIDs[0] != "node-2" {
		t.Fatalf("expected remaining assignment [node-2], got %v", updated.NodeIDs)
	}
	if updated.ManagedNodeID != "node-2" {
		t.Fatalf("expected managed_node_id to rotate to node-2, got %s", updated.ManagedNodeID)
	}
}
