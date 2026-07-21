package cv

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// SnapshotManager handles saving and retrieving frame snapshots
type SnapshotManager struct {
	baseDir string
}

// NewSnapshotManager creates a new snapshot manager with the given base directory
func NewSnapshotManager(baseDir string) *SnapshotManager {
	return &SnapshotManager{baseDir: baseDir}
}

// SaveSnapshot copies frame data to snapshot storage and returns relative path
// Organizes snapshots by date (YYYY-MM-DD) for easy cleanup and browsing
func (sm *SnapshotManager) SaveSnapshot(frameData []byte) (string, error) {
	// Create date-based subdirectory (YYYY-MM-DD)
	date := time.Now().Format("2006-01-02")
	dateDir := filepath.Join(sm.baseDir, date)
	if err := os.MkdirAll(dateDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create snapshot directory: %w", err)
	}

	// Generate unique filename
	snapshotID := uuid.New().String()
	filename := fmt.Sprintf("snapshot_%s.jpg", snapshotID)
	fullPath := filepath.Join(dateDir, filename)

	// Write frame data
	if err := os.WriteFile(fullPath, frameData, 0644); err != nil {
		return "", fmt.Errorf("failed to write snapshot: %w", err)
	}

	// Return relative path for storage in database metadata
	return fmt.Sprintf("%s/%s", date, filename), nil
}

// GetSnapshotPath returns full filesystem path for a relative snapshot path
func (sm *SnapshotManager) GetSnapshotPath(relativePath string) string {
	return filepath.Join(sm.baseDir, relativePath)
}

// DeleteOldSnapshots removes snapshots older than the specified number of days
// Can be called periodically to manage disk space
func (sm *SnapshotManager) DeleteOldSnapshots(daysToKeep int) error {
	cutoffDate := time.Now().AddDate(0, 0, -daysToKeep)

	entries, err := os.ReadDir(sm.baseDir)
	if err != nil {
		return fmt.Errorf("failed to read snapshot directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		// Parse directory name as date (YYYY-MM-DD)
		dirDate, err := time.Parse("2006-01-02", entry.Name())
		if err != nil {
			continue // Skip non-date directories
		}

		// Delete if older than cutoff
		if dirDate.Before(cutoffDate) {
			dirPath := filepath.Join(sm.baseDir, entry.Name())
			if err := os.RemoveAll(dirPath); err != nil {
				return fmt.Errorf("failed to delete old snapshot directory %s: %w", entry.Name(), err)
			}
		}
	}

	return nil
}
