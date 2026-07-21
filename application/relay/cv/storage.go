package cv

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FrameMetadata contains metadata about a stored frame (held in memory, not on disk)
type FrameMetadata struct {
	UUID      string
	ServiceID string
	Data      []byte // JPEG bytes — held in memory until fetched or TTL expires
	Timestamp time.Time
	CreatedAt time.Time
	// ReadsRemaining allows a frame to be fetched by multiple adapters/workers
	// (e.g., PPE + Person + VLM) before eviction.
	ReadsRemaining int
}

// StorageManager manages in-memory frame storage and HTTP serving.
// Frames are never written to disk; they live in memory from extraction to worker fetch.
type StorageManager struct {
	frames         map[string]*FrameMetadata // UUID → metadata
	workerRegistry *CVWorkerRegistry         // For key validation
	frameMaxReads  int
	mu             sync.RWMutex
}

// NewStorageManager creates a new storage manager.
// baseDir is accepted for API compatibility but is no longer used for frames.
func NewStorageManager(baseDir string, workerRegistry *CVWorkerRegistry) *StorageManager {
	frameMaxReads := 6
	if raw := strings.TrimSpace(os.Getenv("FRAME_MAX_READS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			frameMaxReads = n
		}
	}

	// Face lookup is asynchronous: object detection may request recognition
	// after a cold model load or a brief queue delay. Keep frames long enough
	// for that handoff instead of expiring them before the face adapter fetches.
	ttl := 120 * time.Second
	if raw := strings.TrimSpace(os.Getenv("FRAME_TTL_SECONDS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			ttl = time.Duration(n) * time.Second
		}
	}

	sm := &StorageManager{
		frames:         make(map[string]*FrameMetadata),
		workerRegistry: workerRegistry,
		frameMaxReads:  frameMaxReads,
	}
	// Background TTL cleanup: discard frames after a short retention window.
	go sm.cleanupLoop(ttl)
	return sm
}

// RegisterFrameData stores a JPEG frame in memory.
func (s *StorageManager) RegisterFrameData(frameUUID, serviceID string, data []byte, timestamp time.Time) error {
	s.mu.Lock()
	s.frames[frameUUID] = &FrameMetadata{
		UUID:           frameUUID,
		ServiceID:      serviceID,
		Data:           data,
		Timestamp:      timestamp,
		CreatedAt:      timestamp,
		ReadsRemaining: s.frameMaxReads,
	}
	s.mu.Unlock()
	return nil
}

// RegisterFrame stores a pre-built FrameMetadata (kept for API compatibility).
func (s *StorageManager) RegisterFrame(metadata *FrameMetadata) error {
	if metadata.ReadsRemaining <= 0 {
		metadata.ReadsRemaining = s.frameMaxReads
	}
	s.mu.Lock()
	s.frames[metadata.UUID] = metadata
	s.mu.Unlock()
	return nil
}

// GetFrame retrieves frame metadata by UUID.
func (s *StorageManager) GetFrame(uuid string) (*FrameMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	frame, exists := s.frames[uuid]
	if !exists {
		return nil, fmt.Errorf("frame not found: %s", uuid)
	}
	return frame, nil
}

// DeleteFrame removes a frame from memory.
func (s *StorageManager) DeleteFrame(uuid string) {
	s.mu.Lock()
	delete(s.frames, uuid)
	s.mu.Unlock()
}

// cleanupLoop removes frames older than ttl that workers never fetched.
func (s *StorageManager) cleanupLoop(ttl time.Duration) {
	ticker := time.NewTicker(ttl / 2)
	defer ticker.Stop()

	for range ticker.C {
		cutoff := time.Now().Add(-ttl)

		s.mu.Lock()
		var stale []string
		for uuid, frame := range s.frames {
			if frame.CreatedAt.Before(cutoff) {
				stale = append(stale, uuid)
			}
		}
		for _, uuid := range stale {
			delete(s.frames, uuid)
		}
		s.mu.Unlock()

		if len(stale) > 0 {
			log.Printf("[StorageManager] TTL cleanup: removed %d stale frames from memory", len(stale))
		}
	}
}

// ValidateWorkerKey validates that a worker key is registered.
func (s *StorageManager) ValidateWorkerKey(workerKey string) (string, error) {
	connID, exists := s.workerRegistry.GetWorkerIDByKey(workerKey)
	if !exists {
		return "", fmt.Errorf("invalid worker key")
	}
	return connID, nil
}

// HandleFrameDownload handles HTTP requests for frame downloads.
// URL format: /worker/frames/{frameUUID}
// Frames support bounded multi-read so multiple adapters can process the same frame.
func (s *StorageManager) HandleFrameDownload(w http.ResponseWriter, r *http.Request) {
	// Parse path: /worker/frames/{frameUUID}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "worker" || parts[1] != "frames" {
		http.Error(w, "Invalid path format. Expected: /worker/frames/{frameUUID}", http.StatusBadRequest)
		return
	}

	frameUUID := parts[2]

	// Authenticate worker
	workerKey := r.Header.Get("X-Worker-Key")
	if workerKey == "" {
		http.Error(w, "Missing X-Worker-Key header", http.StatusUnauthorized)
		return
	}
	if _, err := s.ValidateWorkerKey(workerKey); err != nil {
		log.Printf("[StorageManager] Invalid worker key for frame %s", frameUUID)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Atomically read and decrement remaining reads.
	// This allows one frame to be fetched by multiple worker adapters.
	s.mu.Lock()
	frame, exists := s.frames[frameUUID]
	var frameData []byte
	if exists {
		frameData = frame.Data
		frame.ReadsRemaining--
		if frame.ReadsRemaining <= 0 {
			delete(s.frames, frameUUID)
		}
	}
	s.mu.Unlock()

	if !exists {
		http.Error(w, "Frame not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%s.jpg", frameUUID))
	w.Write(frameData) //nolint:errcheck
}
