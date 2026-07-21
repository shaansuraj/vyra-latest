package relay

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSelectDatabaseDriver(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		driver string
	}{
		{name: "local file path", path: filepath.Join(t.TempDir(), "relay.db"), driver: "sqlite3"},
		{name: "sqlite memory", path: ":memory:", driver: "sqlite3"},
		{name: "remote libsql", path: "libsql://example.turso.io", driver: "turso"},
		{name: "remote https", path: "https://example.turso.io", driver: "turso"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := selectDatabaseDriver(tc.path); got != tc.driver {
				t.Fatalf("selectDatabaseDriver(%q) = %q, want %q", tc.path, got, tc.driver)
			}
		})
	}
}

func TestNewDatabaseUsesSQLiteDriverForLocalPaths(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("NewDatabase failed: %v", err)
	}
	defer db.Close()

	if got := fmt.Sprintf("%T", db.Driver()); got != "*sqlite3.SQLiteDriver" {
		t.Fatalf("expected sqlite3 driver for local database, got %s", got)
	}
}

func TestStoreAgentEventConcurrentWritesLocalSQLite(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("NewDatabase failed: %v", err)
	}
	defer db.Close()

	const writes = 64

	var wg sync.WaitGroup
	errCh := make(chan error, writes)
	for i := 0; i < writes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errCh <- db.StoreAgentEvent(
				"agent-concurrency-test",
				map[string]interface{}{"index": i},
				map[string]interface{}{"service_id": "cam-concurrency-test"},
				time.Now().UTC(),
			)
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("StoreAgentEvent failed: %v", err)
		}
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_events`).Scan(&count); err != nil {
		t.Fatalf("count agent_events failed: %v", err)
	}
	if count != writes {
		t.Fatalf("expected %d agent events, got %d", writes, count)
	}
}
