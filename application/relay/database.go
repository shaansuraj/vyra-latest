package relay

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/tursodatabase/turso-go"
)

// Database wraps sql.DB with serialized write helpers.
type Database struct {
	*sql.DB
	writeMu sync.Mutex
}

const writeRetryAttempts = 12

func selectDatabaseDriver(dbPath string) string {
	lower := strings.ToLower(strings.TrimSpace(dbPath))
	switch {
	case strings.HasPrefix(lower, "libsql://"),
		strings.HasPrefix(lower, "turso://"),
		strings.HasPrefix(lower, "http://"),
		strings.HasPrefix(lower, "https://"),
		strings.HasPrefix(lower, "ws://"),
		strings.HasPrefix(lower, "wss://"):
		return "turso"
	default:
		return "sqlite3"
	}
}

func shouldEnsureLocalDatabaseDir(dbPath string) bool {
	trimmed := strings.TrimSpace(dbPath)
	if trimmed == "" {
		return false
	}
	if selectDatabaseDriver(trimmed) == "turso" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if lower == ":memory:" || strings.HasPrefix(lower, "file::memory:") {
		return false
	}
	if strings.HasPrefix(lower, "file:") {
		return false
	}
	return true
}

func envInt(key string, fallback, minValue, maxValue int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	if value < minValue {
		return minValue
	}
	if maxValue > 0 && value > maxValue {
		return maxValue
	}
	return value
}

func isSQLiteWriteLockError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked") ||
		strings.Contains(msg, "database schema is locked") ||
		strings.Contains(msg, "database is busy") ||
		strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "sqlite_locked")
}

// NewDatabase creates a new database connection and initializes the schema
func NewDatabase(dbPath string) (*Database, error) {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		return nil, fmt.Errorf("database path is empty")
	}

	// Ensure directory exists for normal on-disk SQLite paths.
	if shouldEnsureLocalDatabaseDir(dbPath) {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
	}

	driverName := selectDatabaseDriver(dbPath)
	db, err := sql.Open(driverName, dbPath)
	if err != nil {
		return nil, err
	}

	// Prefer a conservative pool by default to avoid SQLITE_BUSY lock storms.
	// This can be tuned via RELAY_DB_MAX_OPEN_CONNS when needed.
	maxOpenConns := envInt("RELAY_DB_MAX_OPEN_CONNS", 1, 1, 32)
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxOpenConns)
	db.SetConnMaxLifetime(0) // keep connections alive indefinitely

	// Test connection after pool settings are applied.
	if err := db.Ping(); err != nil {
		return nil, err
	}

	// Enable WAL mode — journal_mode pragma returns a row, so use QueryRow.
	// Non-fatal: some drivers may not support all pragma variants.
	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&journalMode); err != nil {
		log.Printf("[Database] Warning: could not enable WAL mode: %v", err)
	} else {
		log.Printf("[Database] journal_mode=%s", journalMode)
	}
	// NORMAL reduces fsync cost in WAL mode while still being crash-safe.
	if _, err := db.Exec("PRAGMA synchronous=NORMAL"); err != nil {
		log.Printf("[Database] Warning: could not set synchronous=NORMAL: %v", err)
	}
	// Wait before failing locked operations (default 15s).
	busyTimeoutMS := envInt("RELAY_DB_BUSY_TIMEOUT_MS", 15000, 1000, 120000)
	if _, err := db.Exec(fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeoutMS)); err != nil {
		log.Printf("[Database] Warning: could not set busy timeout: %v", err)
	}
	// 32 MB page cache to reduce disk I/O for repeated queries.
	if _, err := db.Exec("PRAGMA cache_size=-32000"); err != nil {
		log.Printf("[Database] Warning: could not set cache size: %v", err)
	}
	log.Printf("[Database] driver=%s max_open_conns=%d busy_timeout_ms=%d", driverName, maxOpenConns, busyTimeoutMS)

	// Initialize schema
	if err := initSchema(db); err != nil {
		return nil, err
	}

	return &Database{DB: db}, nil
}

// initSchema creates the necessary tables if they don't exist
func initSchema(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		name TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);

	CREATE TABLE IF NOT EXISTS nodes (
		id TEXT PRIMARY KEY,
		token TEXT UNIQUE NOT NULL,
		owner_id INTEGER,
		name TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		authorized_at DATETIME,
		last_connected_at DATETIME,
		FOREIGN KEY (owner_id) REFERENCES users(id) ON DELETE SET NULL
	);
	CREATE INDEX IF NOT EXISTS idx_nodes_token ON nodes(token);
	CREATE INDEX IF NOT EXISTS idx_nodes_owner ON nodes(owner_id);

	CREATE TABLE IF NOT EXISTS nodes_users (
		node_id TEXT NOT NULL,
		user_id INTEGER NOT NULL,
		role TEXT NOT NULL DEFAULT 'owner',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (node_id, user_id),
		FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_nodes_users_user ON nodes_users(user_id);

	CREATE TABLE IF NOT EXISTS agents (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		worker_id TEXT NOT NULL,
		config TEXT NOT NULL,
		service_ids TEXT,
		user_id INTEGER NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_agents_user ON agents(user_id);
	CREATE INDEX IF NOT EXISTS idx_agents_worker ON agents(worker_id);

	CREATE TABLE IF NOT EXISTS agent_events (
		id TEXT PRIMARY KEY,
		agent_id TEXT NOT NULL,
		data TEXT NOT NULL,
		metadata TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_agent_events_agent_id ON agent_events(agent_id);

        -- Camera groups (Bird-eye grouping)
        CREATE TABLE IF NOT EXISTS camera_groups (
            id TEXT PRIMARY KEY,
            name TEXT NOT NULL,
            user_id INTEGER NOT NULL,
            created_at TEXT NOT NULL,
            updated_at TEXT NOT NULL,
            FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
        );

        CREATE TABLE IF NOT EXISTS camera_group_members (
            group_id TEXT NOT NULL,
            service_id TEXT NOT NULL,
            PRIMARY KEY (group_id, service_id),
            FOREIGN KEY (group_id) REFERENCES camera_groups(id) ON DELETE CASCADE
        );

        CREATE INDEX IF NOT EXISTS idx_camera_groups_user_id ON camera_groups(user_id);
        CREATE INDEX IF NOT EXISTS idx_camera_group_members_service_id ON camera_group_members(service_id);
	CREATE INDEX IF NOT EXISTS idx_agent_events_created_at ON agent_events(created_at);

	CREATE TABLE IF NOT EXISTS cameras (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		type TEXT NOT NULL DEFAULT 'rtsp',
		addr TEXT NOT NULL,
		port INTEGER NOT NULL,
		path TEXT DEFAULT '',
		auth_username TEXT DEFAULT '',
		auth_password TEXT DEFAULT '',
		processing_fps REAL DEFAULT 15.0,
		vlm_fps REAL DEFAULT 5.0,
		user_id INTEGER NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_cameras_user ON cameras(user_id);

	CREATE TABLE IF NOT EXISTS camera_node_assignments (
		camera_id TEXT NOT NULL,
		node_id TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (camera_id, node_id),
		FOREIGN KEY (camera_id) REFERENCES cameras(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_camera_node_assignments_node ON camera_node_assignments(node_id);
	CREATE INDEX IF NOT EXISTS idx_camera_node_assignments_camera ON camera_node_assignments(camera_id);

	CREATE TABLE IF NOT EXISTS managed_nodes (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		user_id INTEGER NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_managed_nodes_user ON managed_nodes(user_id);

	-- Persons (face recognition subject registry)
	CREATE TABLE IF NOT EXISTS persons (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		user_id INTEGER NOT NULL,
		face_image BLOB,
		face_updated_at DATETIME,
		created_at DATETIME NOT NULL,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_persons_user ON persons(user_id);

	-- Additional face profiles per person (multi-profile one-shot learning)
	CREATE TABLE IF NOT EXISTS person_face_profiles (
		id TEXT PRIMARY KEY,
		person_id TEXT NOT NULL,
		image_path TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		FOREIGN KEY (person_id) REFERENCES persons(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_person_face_profiles_person ON person_face_profiles(person_id);

	-- Cross-camera global person identities (recognized + unknown).
	CREATE TABLE IF NOT EXISTS person_global_identities (
		global_person_id TEXT PRIMARY KEY,
		person_id TEXT,
		person_name TEXT,
		identity_source TEXT DEFAULT '',
		identity_confidence REAL DEFAULT 0,
		embedding_centroid TEXT,
		body_prototype TEXT,
		body_embedding_count INTEGER DEFAULT 0,
		face_prototype TEXT,
		face_embedding_count INTEGER DEFAULT 0,
		alias_of TEXT,
		last_media_id TEXT,
		last_camera_name TEXT,
		last_observation_at DATETIME,
		last_embedding_kind TEXT DEFAULT '',
		last_embedding_model TEXT DEFAULT '',
		last_quality_score REAL DEFAULT 0,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		last_seen_at DATETIME NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_person_global_identities_person ON person_global_identities(person_id);
	CREATE INDEX IF NOT EXISTS idx_person_global_identities_last_seen ON person_global_identities(last_seen_at DESC);

	-- Person sightings timeline used by Ask-AI and person moments pages.
	CREATE TABLE IF NOT EXISTS person_sightings (
		id TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL,
		global_person_id TEXT NOT NULL,
		person_id TEXT,
		person_name TEXT,
		identity_source TEXT,
		identity_confidence REAL DEFAULT 0,
		service_id TEXT,
		camera_name TEXT,
		event_id TEXT,
		track_id INTEGER,
		bbox_json TEXT DEFAULT '',
		snapshot_path TEXT,
		activity_brief TEXT,
		event_kind TEXT DEFAULT '',
		severity TEXT DEFAULT '',
		metadata_json TEXT DEFAULT '{}',
		dedupe_key TEXT,
		review_status TEXT DEFAULT 'unreviewed',
		operator_note TEXT DEFAULT '',
		is_false_match INTEGER DEFAULT 0,
		reviewed_at DATETIME,
		created_at DATETIME NOT NULL,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);

	-- Timeline investigation workspace, audit, evidence locks, and camera topology.
	CREATE TABLE IF NOT EXISTS timeline_audit_log (
		id TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL,
		action TEXT NOT NULL,
		subject_id TEXT DEFAULT '',
		payload_json TEXT DEFAULT '{}',
		created_at DATETIME NOT NULL,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_timeline_audit_user_created ON timeline_audit_log(user_id, created_at DESC);

	CREATE TABLE IF NOT EXISTS timeline_cases (
		id TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL,
		title TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'open',
		owner TEXT DEFAULT '',
		notes TEXT DEFAULT '',
		reviewed INTEGER DEFAULT 0,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_timeline_cases_user_updated ON timeline_cases(user_id, updated_at DESC);
	CREATE INDEX IF NOT EXISTS idx_timeline_cases_status ON timeline_cases(user_id, status);

	CREATE TABLE IF NOT EXISTS timeline_case_items (
		id TEXT PRIMARY KEY,
		case_id TEXT NOT NULL,
		moment_id TEXT NOT NULL,
		note TEXT DEFAULT '',
		created_at DATETIME NOT NULL,
		UNIQUE(case_id, moment_id),
		FOREIGN KEY (case_id) REFERENCES timeline_cases(id) ON DELETE CASCADE,
		FOREIGN KEY (moment_id) REFERENCES person_sightings(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_timeline_case_items_case ON timeline_case_items(case_id, created_at ASC);
	CREATE INDEX IF NOT EXISTS idx_timeline_case_items_moment ON timeline_case_items(moment_id);

	CREATE TABLE IF NOT EXISTS timeline_evidence_locks (
		id TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL,
		title TEXT NOT NULL,
		case_id TEXT DEFAULT '',
		moment_ids_json TEXT DEFAULT '[]',
		expires_at DATETIME,
		created_at DATETIME NOT NULL,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_timeline_locks_user_created ON timeline_evidence_locks(user_id, created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_timeline_locks_case ON timeline_evidence_locks(case_id);

	CREATE TABLE IF NOT EXISTS timeline_topology (
		service_id TEXT NOT NULL,
		user_id INTEGER NOT NULL,
		label TEXT DEFAULT '',
		zone TEXT DEFAULT '',
		x REAL DEFAULT 0.5,
		y REAL DEFAULT 0.5,
		adjacent_service_ids TEXT DEFAULT '[]',
		updated_at DATETIME NOT NULL,
		PRIMARY KEY (user_id, service_id),
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_timeline_topology_user ON timeline_topology(user_id);

	-- Daily summaries (aggregated per user per calendar day)
	CREATE TABLE IF NOT EXISTS daily_summaries (
		id TEXT PRIMARY KEY,
		date TEXT NOT NULL,
		user_id INTEGER NOT NULL,
		event_count INTEGER DEFAULT 0,
		person_sightings TEXT DEFAULT '[]',
		incidents TEXT DEFAULT '[]',
		violation_breakdown TEXT DEFAULT '{}',
		updated_at DATETIME NOT NULL,
		UNIQUE(date, user_id)
	);
	CREATE INDEX IF NOT EXISTS idx_daily_summaries_user_date ON daily_summaries(user_id, date DESC);

	-- Knowledge base documents and chunks used by Ask-AI RAG.
	CREATE TABLE IF NOT EXISTS kb_documents (
		id TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL,
		source_uri TEXT NOT NULL,
		source_type TEXT NOT NULL DEFAULT 'text',
		title TEXT NOT NULL,
		version TEXT DEFAULT '',
		environment TEXT DEFAULT '',
		language TEXT DEFAULT 'en',
		access_level TEXT DEFAULT 'internal',
		checksum TEXT NOT NULL,
		metadata TEXT DEFAULT '{}',
		indexed_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
		UNIQUE(user_id, source_uri)
	);
	CREATE INDEX IF NOT EXISTS idx_kb_documents_user_source ON kb_documents(user_id, source_uri);
	CREATE INDEX IF NOT EXISTS idx_kb_documents_user_updated ON kb_documents(user_id, updated_at DESC);

	CREATE TABLE IF NOT EXISTS kb_chunks (
		id TEXT PRIMARY KEY,
		document_id TEXT NOT NULL,
		user_id INTEGER NOT NULL,
		chunk_index INTEGER NOT NULL,
		text TEXT NOT NULL,
		token_count INTEGER DEFAULT 0,
		title TEXT DEFAULT '',
		heading TEXT DEFAULT '',
		section_path TEXT DEFAULT '',
		source_uri TEXT DEFAULT '',
		version TEXT DEFAULT '',
		environment TEXT DEFAULT '',
		language TEXT DEFAULT 'en',
		access_level TEXT DEFAULT 'internal',
		metadata TEXT DEFAULT '{}',
		embedding TEXT DEFAULT '',
		embedding_model TEXT DEFAULT '',
		embedding_dim INTEGER DEFAULT 0,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		FOREIGN KEY (document_id) REFERENCES kb_documents(id) ON DELETE CASCADE
	);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_kb_chunks_doc_index ON kb_chunks(document_id, chunk_index);
	CREATE INDEX IF NOT EXISTS idx_kb_chunks_user_source ON kb_chunks(user_id, source_uri);
	CREATE INDEX IF NOT EXISTS idx_kb_chunks_user_version ON kb_chunks(user_id, version);
	CREATE INDEX IF NOT EXISTS idx_kb_chunks_user_environment ON kb_chunks(user_id, environment);
	CREATE INDEX IF NOT EXISTS idx_kb_chunks_user_language ON kb_chunks(user_id, language);
	CREATE INDEX IF NOT EXISTS idx_kb_chunks_user_access ON kb_chunks(user_id, access_level);

	CREATE TABLE IF NOT EXISTS kb_query_traces (
		id TEXT PRIMARY KEY,
		chat_id TEXT DEFAULT '',
		user_id INTEGER NOT NULL,
		intent TEXT DEFAULT '',
		question TEXT NOT NULL,
		rewritten_query TEXT DEFAULT '',
		filters_json TEXT DEFAULT '{}',
		retrieved_timeline_json TEXT DEFAULT '[]',
		retrieved_chunks_json TEXT DEFAULT '[]',
		retrieved_events_json TEXT DEFAULT '[]',
		prompt_digest TEXT DEFAULT '',
		answer TEXT DEFAULT '',
		confidence TEXT DEFAULT '',
		latency_ms INTEGER DEFAULT 0,
		created_at DATETIME NOT NULL,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_kb_query_traces_user_created ON kb_query_traces(user_id, created_at DESC);

	CREATE TABLE IF NOT EXISTS query_chats (
		id TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL,
		title TEXT DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_query_chats_user_updated ON query_chats(user_id, updated_at DESC);

	CREATE TABLE IF NOT EXISTS query_chat_messages (
		id TEXT PRIMARY KEY,
		chat_id TEXT NOT NULL,
		user_id INTEGER NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		trace_id TEXT DEFAULT '',
		created_at DATETIME NOT NULL,
		FOREIGN KEY (chat_id) REFERENCES query_chats(id) ON DELETE CASCADE,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_query_chat_messages_chat_created ON query_chat_messages(chat_id, created_at ASC);

	CREATE TABLE IF NOT EXISTS kb_feedback (
		id TEXT PRIMARY KEY,
		trace_id TEXT NOT NULL,
		user_id INTEGER NOT NULL,
		rating TEXT NOT NULL,
		comment TEXT DEFAULT '',
		created_at DATETIME NOT NULL,
		FOREIGN KEY (trace_id) REFERENCES kb_query_traces(id) ON DELETE CASCADE,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_kb_feedback_trace ON kb_feedback(trace_id);
	`
	_, err := db.Exec(schema)
	if err != nil {
		return err
	}
	ensureKnowledgeFTS(db)
	// Safe migration: add managed_node_id column to cameras if not present.
	// SQLite returns an error if the column already exists; we ignore it.
	_, _ = db.Exec(`ALTER TABLE cameras ADD COLUMN managed_node_id TEXT`)
	_, _ = db.Exec(`ALTER TABLE cameras ADD COLUMN processing_fps REAL DEFAULT 15.0`)
	_, _ = db.Exec(`ALTER TABLE cameras ADD COLUMN vlm_fps REAL DEFAULT 5.0`)
	_, _ = db.Exec(`ALTER TABLE person_global_identities ADD COLUMN body_prototype TEXT`)
	_, _ = db.Exec(`ALTER TABLE person_global_identities ADD COLUMN body_embedding_count INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE person_global_identities ADD COLUMN face_prototype TEXT`)
	_, _ = db.Exec(`ALTER TABLE person_global_identities ADD COLUMN face_embedding_count INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE person_global_identities ADD COLUMN last_media_id TEXT`)
	_, _ = db.Exec(`ALTER TABLE person_global_identities ADD COLUMN last_camera_name TEXT`)
	_, _ = db.Exec(`ALTER TABLE person_global_identities ADD COLUMN last_observation_at DATETIME`)
	_, _ = db.Exec(`ALTER TABLE person_global_identities ADD COLUMN last_embedding_kind TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE person_global_identities ADD COLUMN last_embedding_model TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE person_global_identities ADD COLUMN last_quality_score REAL DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE person_sightings ADD COLUMN person_name TEXT`)
	_, _ = db.Exec(`ALTER TABLE person_sightings ADD COLUMN identity_source TEXT`)
	_, _ = db.Exec(`ALTER TABLE person_sightings ADD COLUMN identity_confidence REAL DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE person_sightings ADD COLUMN camera_name TEXT`)
	_, _ = db.Exec(`ALTER TABLE person_sightings ADD COLUMN track_id INTEGER`)
	_, _ = db.Exec(`ALTER TABLE person_sightings ADD COLUMN bbox_json TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE person_sightings ADD COLUMN event_kind TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE person_sightings ADD COLUMN severity TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE person_sightings ADD COLUMN metadata_json TEXT DEFAULT '{}'`)
	_, _ = db.Exec(`ALTER TABLE person_sightings ADD COLUMN dedupe_key TEXT`)
	_, _ = db.Exec(`ALTER TABLE person_sightings ADD COLUMN review_status TEXT DEFAULT 'unreviewed'`)
	_, _ = db.Exec(`ALTER TABLE person_sightings ADD COLUMN operator_note TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE person_sightings ADD COLUMN is_false_match INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE person_sightings ADD COLUMN reviewed_at DATETIME`)
	_, _ = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_person_sightings_dedupe ON person_sightings(dedupe_key)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_person_sightings_global_created ON person_sightings(global_person_id, created_at DESC)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_person_sightings_service_created ON person_sightings(service_id, created_at DESC)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_person_sightings_person_created ON person_sightings(person_id, created_at DESC)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_person_sightings_event_created ON person_sightings(event_kind, created_at DESC)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_person_sightings_review ON person_sightings(review_status, created_at DESC)`)
	_, _ = db.Exec(`ALTER TABLE kb_query_traces ADD COLUMN chat_id TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE kb_query_traces ADD COLUMN intent TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE kb_query_traces ADD COLUMN retrieved_timeline_json TEXT DEFAULT '[]'`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_kb_query_traces_chat ON kb_query_traces(chat_id, created_at ASC)`)
	_, _ = db.Exec(`
		INSERT INTO camera_node_assignments (camera_id, node_id)
		SELECT c.id, c.managed_node_id
		FROM cameras c
		WHERE c.managed_node_id IS NOT NULL
		  AND c.managed_node_id <> ''
		  AND NOT EXISTS (
		    SELECT 1 FROM camera_node_assignments a
		    WHERE a.camera_id = c.id AND a.node_id = c.managed_node_id
		  )
	`)
	// Persons: store face image on filesystem; image_path holds the relative path.
	_, _ = db.Exec(`ALTER TABLE persons ADD COLUMN image_path TEXT`)
	// Migration: add username column to users table for existing databases.
	// Add column without UNIQUE constraint first (ALTER TABLE ADD COLUMN with
	// UNIQUE can fail on some SQLite/libSQL builds when rows already exist).
	_, _ = db.Exec(`ALTER TABLE users ADD COLUMN username TEXT`)
	_, _ = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users(username)`)
	return nil
}

// execWrite serializes DB mutations across high-throughput goroutines and
// retries short-lived SQLite lock conflicts.
func (db *Database) execWrite(query string, args ...interface{}) (sql.Result, error) {
	db.writeMu.Lock()
	defer db.writeMu.Unlock()

	backoff := 50 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= writeRetryAttempts; attempt++ {
		res, err := db.DB.Exec(query, args...)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !isSQLiteWriteLockError(err) || attempt == writeRetryAttempts {
			return nil, err
		}
		time.Sleep(backoff)
		if backoff < time.Second {
			backoff *= 2
		}
	}
	return nil, lastErr
}

// AgentEvent represents an event emitted by an agent
type AgentEvent struct {
	ID        string                 `json:"id"`
	AgentID   string                 `json:"agent_id"`
	Data      map[string]interface{} `json:"data"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
}

type PersonMoment struct {
	ID                 string    `json:"id"`
	GlobalPersonID     string    `json:"global_person_id"`
	PersonID           string    `json:"person_id,omitempty"`
	PersonName         string    `json:"person_name,omitempty"`
	IdentitySource     string    `json:"identity_source,omitempty"`
	IdentityConfidence float64   `json:"identity_confidence,omitempty"`
	ServiceID          string    `json:"service_id,omitempty"`
	CameraName         string    `json:"camera_name,omitempty"`
	EventID            string    `json:"event_id,omitempty"`
	TrackID            int       `json:"track_id,omitempty"`
	BBoxJSON           string    `json:"bbox_json,omitempty"`
	SnapshotPath       string    `json:"snapshot_path,omitempty"`
	ActivityBrief      string    `json:"activity_brief,omitempty"`
	EventKind          string    `json:"event_kind,omitempty"`
	Severity           string    `json:"severity,omitempty"`
	MetadataJSON       string    `json:"metadata_json,omitempty"`
	DedupeKey          string    `json:"dedupe_key,omitempty"`
	ReviewStatus       string    `json:"review_status,omitempty"`
	OperatorNote       string    `json:"operator_note,omitempty"`
	IsFalseMatch       bool      `json:"is_false_match,omitempty"`
	ReviewedAt         time.Time `json:"reviewed_at,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}

// StoreAgentEvent stores an agent event in the database
func (db *Database) StoreAgentEvent(agentID string, data map[string]interface{}, metadata map[string]interface{}, createdAt time.Time) error {
	eventID := uuid.New().String()
	if metadata != nil {
		if id, ok := metadata["event_id"].(string); ok && strings.TrimSpace(id) != "" {
			eventID = strings.TrimSpace(id)
		} else {
			metadata["event_id"] = eventID
		}
	}

	dataJSON, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal agent event data: %w", err)
	}

	var metadataJSON sql.NullString
	if metadata != nil {
		metadataBytes, err := json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal agent event metadata: %w", err)
		}
		metadataJSON = sql.NullString{String: string(metadataBytes), Valid: true}
	}

	_, err = db.execWrite(
		"INSERT INTO agent_events (id, agent_id, data, metadata, created_at) VALUES (?, ?, ?, ?, ?)",
		eventID, agentID, string(dataJSON), metadataJSON, createdAt,
	)
	if err != nil {
		return fmt.Errorf("failed to store agent event: %w", err)
	}

	return nil
}

// GetAgentEvents retrieves events for a specific agent
func (db *Database) GetAgentEvents(agentID string, limit int) ([]*AgentEvent, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT id, agent_id, data, metadata, created_at
		FROM agent_events
		WHERE agent_id = ?
		ORDER BY created_at DESC
		LIMIT ?
	`

	rows, err := db.Query(query, agentID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query agent events: %w", err)
	}
	defer rows.Close()

	var events []*AgentEvent
	for rows.Next() {
		var event AgentEvent
		var dataJSON, metadataJSON sql.NullString

		if err := rows.Scan(&event.ID, &event.AgentID, &dataJSON, &metadataJSON, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan agent event: %w", err)
		}

		if err := json.Unmarshal([]byte(dataJSON.String), &event.Data); err != nil {
			return nil, fmt.Errorf("failed to unmarshal agent event data: %w", err)
		}

		if metadataJSON.Valid {
			if err := json.Unmarshal([]byte(metadataJSON.String), &event.Metadata); err != nil {
				return nil, fmt.Errorf("failed to unmarshal agent event metadata: %w", err)
			}
		}

		events = append(events, &event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating agent events: %w", err)
	}

	return events, nil
}

// GetAgentEventsByService retrieves agent events for agents monitoring a specific service
// across the shared workspace (not scoped per user).
func (db *Database) GetAgentEventsByService(serviceID string, userID int64, limit int) ([]*AgentEvent, error) {
	_ = userID
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	// Use LIKE to search for service_id in the JSON array
	// service_ids is stored as JSON like: ["id1","id2"]
	// We search for the pattern "service_id" (with quotes) to avoid partial matches
	servicePattern := fmt.Sprintf("%%%q%%", serviceID) // Creates %"service_id"% pattern

	query := `
		SELECT DISTINCT ae.id, ae.agent_id, ae.data, ae.metadata, ae.created_at
		FROM agent_events ae
		JOIN agents a ON ae.agent_id = a.id
		WHERE a.service_ids LIKE ?
		ORDER BY ae.created_at DESC
		LIMIT ?
	`

	rows, err := db.Query(query, servicePattern, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query agent events by service: %w", err)
	}
	defer rows.Close()

	var events []*AgentEvent
	for rows.Next() {
		var event AgentEvent
		var dataJSON, metadataJSON sql.NullString

		if err := rows.Scan(&event.ID, &event.AgentID, &dataJSON, &metadataJSON, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan agent event: %w", err)
		}

		if err := json.Unmarshal([]byte(dataJSON.String), &event.Data); err != nil {
			return nil, fmt.Errorf("failed to unmarshal agent event data: %w", err)
		}

		if metadataJSON.Valid {
			if err := json.Unmarshal([]byte(metadataJSON.String), &event.Metadata); err != nil {
				return nil, fmt.Errorf("failed to unmarshal agent event metadata: %w", err)
			}
		}

		events = append(events, &event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating agent events: %w", err)
	}

	return events, nil
}

// GetRecentAgentEvents retrieves recent agent events across all agents
func (db *Database) GetRecentAgentEvents(limit int) ([]*AgentEvent, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT id, agent_id, data, metadata, created_at
		FROM agent_events
		ORDER BY created_at DESC
		LIMIT ?
	`

	rows, err := db.Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent agent events: %w", err)
	}
	defer rows.Close()

	var events []*AgentEvent
	for rows.Next() {
		var event AgentEvent
		var dataJSON, metadataJSON sql.NullString

		if err := rows.Scan(&event.ID, &event.AgentID, &dataJSON, &metadataJSON, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan agent event: %w", err)
		}

		if err := json.Unmarshal([]byte(dataJSON.String), &event.Data); err != nil {
			return nil, fmt.Errorf("failed to unmarshal agent event data: %w", err)
		}

		if metadataJSON.Valid {
			if err := json.Unmarshal([]byte(metadataJSON.String), &event.Metadata); err != nil {
				return nil, fmt.Errorf("failed to unmarshal agent event metadata: %w", err)
			}
		}

		events = append(events, &event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating agent events: %w", err)
	}

	return events, nil
}

// GetRecentAgentEventsForUser retrieves recent agent events across the shared workspace.
// userID is ignored to preserve call-site compatibility.
func (db *Database) GetRecentAgentEventsForUser(userID int64, limit int, offset int) ([]*AgentEvent, error) {
	_ = userID
	if limit <= 0 {
		limit = 100
	}
	if limit > 2000 {
		limit = 2000
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := db.DB.Query(`
		SELECT ae.id, ae.agent_id, ae.data, ae.metadata, ae.created_at
		FROM agent_events ae
		ORDER BY ae.created_at DESC
		LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*AgentEvent
	for rows.Next() {
		var event AgentEvent
		var dataJSON string
		var metadataJSON sql.NullString

		err := rows.Scan(&event.ID, &event.AgentID, &dataJSON, &metadataJSON, &event.CreatedAt)
		if err != nil {
			log.Printf("[Database] GetRecentAgentEventsForUser scan skip: %v", err)
			continue
		}

		// Parse data JSON
		if err := json.Unmarshal([]byte(dataJSON), &event.Data); err != nil {
			log.Printf("[Database] GetRecentAgentEventsForUser data decode skip event %s: %v", event.ID, err)
			continue
		}

		// Parse metadata JSON
		if metadataJSON.Valid && metadataJSON.String != "" {
			if err := json.Unmarshal([]byte(metadataJSON.String), &event.Metadata); err != nil {
				// ignore parse errors for metadata
				event.Metadata = nil
			}
		}

		events = append(events, &event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return events, nil
}

// SearchAgentEventsForUser searches agent events in the shared workspace.
// userID is ignored to preserve call-site compatibility.
func (db *Database) SearchAgentEventsForUser(userID int64, query string, limit int, offset int) ([]*AgentEvent, error) {
	_ = userID
	if limit <= 0 {
		limit = 100
	}
	if limit > 2000 {
		limit = 2000
	}
	if offset < 0 {
		offset = 0
	}
	like := "%" + query + "%"
	rows, err := db.DB.Query(`
		SELECT ae.id, ae.agent_id, ae.data, ae.metadata, ae.created_at
		FROM agent_events ae
		LEFT JOIN agents a ON ae.agent_id = a.id
		WHERE (
			COALESCE(a.name, '') LIKE ? OR
			ae.data LIKE ? OR
			COALESCE(ae.metadata, '') LIKE ?
		)
		ORDER BY ae.created_at DESC
		LIMIT ? OFFSET ?
	`, like, like, like, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*AgentEvent
	for rows.Next() {
		var event AgentEvent
		var dataJSON string
		var metadataJSON sql.NullString

		err := rows.Scan(&event.ID, &event.AgentID, &dataJSON, &metadataJSON, &event.CreatedAt)
		if err != nil {
			log.Printf("[Database] SearchAgentEventsForUser scan skip: %v", err)
			continue
		}

		if err := json.Unmarshal([]byte(dataJSON), &event.Data); err != nil {
			log.Printf("[Database] SearchAgentEventsForUser data decode skip event %s: %v", event.ID, err)
			continue
		}

		if metadataJSON.Valid && metadataJSON.String != "" {
			if err := json.Unmarshal([]byte(metadataJSON.String), &event.Metadata); err != nil {
				event.Metadata = nil
			}
		}

		events = append(events, &event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return events, nil
}

type AgentEventAIQueryFilter struct {
	StartAt    time.Time
	EndAt      time.Time
	ServiceIDs []string
	Keywords   []string
	Limit      int
}

// QueryAgentEventsForAI retrieves Ask-AI event candidates with SQL-first scope filters.
// userID is ignored to preserve the current shared-workspace event model.
func (db *Database) QueryAgentEventsForAI(userID int64, filter AgentEventAIQueryFilter) ([]*AgentEvent, error) {
	_ = userID
	limit := filter.Limit
	if limit <= 0 {
		limit = 500
	}
	if limit > 2000 {
		limit = 2000
	}
	where := []string{"1=1"}
	args := []interface{}{}
	if !filter.StartAt.IsZero() {
		where = append(where, "ae.created_at >= ?")
		args = append(args, filter.StartAt.UTC())
	}
	if !filter.EndAt.IsZero() {
		where = append(where, "ae.created_at < ?")
		args = append(args, filter.EndAt.UTC())
	}
	serviceIDs := cleanEventFilterValues(filter.ServiceIDs, 16, false)
	if len(serviceIDs) > 0 {
		placeholders := make([]string, 0, len(serviceIDs))
		for _, sid := range serviceIDs {
			placeholders = append(placeholders, "?")
			args = append(args, sid)
		}
		inClause := strings.Join(placeholders, ",")
		where = append(where, fmt.Sprintf(`(
			json_extract(COALESCE(ae.metadata, '{}'), '$.service_id') IN (%s) OR
			json_extract(COALESCE(ae.data, '{}'), '$.service_id') IN (%s)
		)`, inClause, inClause))
		args = append(args, serviceIDsToInterfaces(serviceIDs)...)
	}
	keywords := cleanEventFilterValues(filter.Keywords, 12, true)
	if len(keywords) > 0 {
		keywordClauses := []string{}
		for _, kw := range keywords {
			keywordClauses = append(keywordClauses, `(
				lower(COALESCE(a.name, '')) LIKE ? OR
				lower(ae.data) LIKE ? OR
				lower(COALESCE(ae.metadata, '')) LIKE ?
			)`)
			like := "%" + strings.ToLower(kw) + "%"
			args = append(args, like, like, like)
		}
		where = append(where, "("+strings.Join(keywordClauses, " OR ")+")")
	}
	args = append(args, limit)
	rows, err := db.DB.Query(`
		SELECT ae.id, ae.agent_id, ae.data, ae.metadata, ae.created_at
		FROM agent_events ae
		LEFT JOIN agents a ON ae.agent_id = a.id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY ae.created_at DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgentEventRows(rows, "QueryAgentEventsForAI")
}

func serviceIDsToInterfaces(values []string) []interface{} {
	out := make([]interface{}, 0, len(values))
	for _, v := range values {
		out = append(out, v)
	}
	return out
}

func cleanEventFilterValues(values []string, maxValues int, lower bool) []string {
	out := make([]string, 0, min(len(values), maxValues))
	seen := map[string]bool{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if lower {
			v = strings.ToLower(v)
		}
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
		if len(out) >= maxValues {
			break
		}
	}
	return out
}

func scanAgentEventRows(rows *sql.Rows, logScope string) ([]*AgentEvent, error) {
	var events []*AgentEvent
	for rows.Next() {
		var event AgentEvent
		var dataJSON string
		var metadataJSON sql.NullString
		if err := rows.Scan(&event.ID, &event.AgentID, &dataJSON, &metadataJSON, &event.CreatedAt); err != nil {
			log.Printf("[Database] %s scan skip: %v", logScope, err)
			continue
		}
		if err := json.Unmarshal([]byte(dataJSON), &event.Data); err != nil {
			log.Printf("[Database] %s data decode skip event %s: %v", logScope, event.ID, err)
			continue
		}
		if metadataJSON.Valid && metadataJSON.String != "" {
			if err := json.Unmarshal([]byte(metadataJSON.String), &event.Metadata); err != nil {
				event.Metadata = nil
			}
		}
		events = append(events, &event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// GetEventCountForUser returns total and today's event counts for the shared workspace.
// userID is ignored to preserve call-site compatibility.
func (db *Database) GetEventCountForUser(userID int64) (total int64, today int64, err error) {
	_ = userID
	err = db.DB.QueryRow(`
		SELECT
		  COUNT(*) AS total,
		  COALESCE(SUM(CASE WHEN DATE(ae.created_at, 'localtime') = DATE('now','localtime') THEN 1 ELSE 0 END), 0) AS today
		FROM agent_events ae
	`).Scan(&total, &today)
	return
}

// GetAgentEventsForUserFiltered returns shared-workspace agent events filtered by date (YYYY-MM-DD).
// userID is ignored to preserve call-site compatibility.
func (db *Database) GetAgentEventsForUserFiltered(userID int64, date string, limit, offset int) ([]*AgentEvent, error) {
	_ = userID
	if limit <= 0 {
		limit = 500
	}
	if limit > 2000 {
		limit = 2000
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := db.DB.Query(`
		SELECT ae.id, ae.agent_id, ae.data, ae.metadata, ae.created_at
		FROM agent_events ae
		WHERE DATE(ae.created_at, 'localtime') = ?
		ORDER BY ae.created_at DESC
		LIMIT ? OFFSET ?
	`, date, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*AgentEvent
	for rows.Next() {
		var event AgentEvent
		var dataJSON string
		var metadataJSON sql.NullString
		if err := rows.Scan(&event.ID, &event.AgentID, &dataJSON, &metadataJSON, &event.CreatedAt); err != nil {
			log.Printf("[Database] GetAgentEventsForUserFiltered scan skip: %v", err)
			continue
		}
		if err := json.Unmarshal([]byte(dataJSON), &event.Data); err != nil {
			log.Printf("[Database] GetAgentEventsForUserFiltered data decode skip event %s: %v", event.ID, err)
			continue
		}
		if metadataJSON.Valid && metadataJSON.String != "" {
			if err := json.Unmarshal([]byte(metadataJSON.String), &event.Metadata); err != nil {
				event.Metadata = nil
			}
		}
		events = append(events, &event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// GetPersonSightings returns shared-workspace agent events where metadata contains person_id.
// userID is ignored to preserve call-site compatibility.
func (db *Database) GetPersonSightings(userID int64, personID string, limit int) ([]*AgentEvent, error) {
	_ = userID
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// Use json_extract to match person_id stored in metadata JSON
	rows, err := db.DB.Query(`
		SELECT ae.id, ae.agent_id, ae.data, ae.metadata, ae.created_at
		FROM agent_events ae
		WHERE json_extract(ae.metadata, '$.person_id') = ?
		ORDER BY ae.created_at DESC
		LIMIT ?
	`, personID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*AgentEvent
	for rows.Next() {
		var event AgentEvent
		var dataJSON string
		var metadataJSON sql.NullString
		if err := rows.Scan(&event.ID, &event.AgentID, &dataJSON, &metadataJSON, &event.CreatedAt); err != nil {
			log.Printf("[Database] GetPersonSightings scan skip: %v", err)
			continue
		}
		if err := json.Unmarshal([]byte(dataJSON), &event.Data); err != nil {
			log.Printf("[Database] GetPersonSightings data decode skip event %s: %v", event.ID, err)
			continue
		}
		if metadataJSON.Valid && metadataJSON.String != "" {
			if err := json.Unmarshal([]byte(metadataJSON.String), &event.Metadata); err != nil {
				event.Metadata = nil
			}
		}
		events = append(events, &event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (db *Database) UpsertPersonGlobalIdentity(
	globalPersonID string,
	personID string,
	personName string,
	identitySource string,
	identityConfidence float64,
	embeddingCentroid string,
	aliasOf string,
	seenAt time.Time,
) error {
	if globalPersonID == "" {
		return nil
	}
	if seenAt.IsZero() {
		seenAt = time.Now()
	}
	return db.UpsertPersonGlobalIdentityRecord(PersonGlobalIdentityRecord{
		GlobalPersonID:     globalPersonID,
		PersonID:           personID,
		PersonName:         personName,
		IdentitySource:     identitySource,
		IdentityConfidence: identityConfidence,
		EmbeddingCentroid:  embeddingCentroid,
		AliasOf:            aliasOf,
		LastObservationAt:  seenAt,
		SeenAt:             seenAt,
	})
}

type PersonGlobalIdentityRecord struct {
	GlobalPersonID     string
	PersonID           string
	PersonName         string
	IdentitySource     string
	IdentityConfidence float64
	EmbeddingCentroid  string
	BodyPrototype      string
	BodyEmbeddingCount int
	FacePrototype      string
	FaceEmbeddingCount int
	AliasOf            string
	LastMediaID        string
	LastCameraName     string
	LastObservationAt  time.Time
	LastEmbeddingKind  string
	LastEmbeddingModel string
	LastQualityScore   float64
	SeenAt             time.Time
}

func (db *Database) UpsertPersonGlobalIdentityRecord(record PersonGlobalIdentityRecord) error {
	if record.GlobalPersonID == "" {
		return nil
	}
	if record.SeenAt.IsZero() {
		record.SeenAt = time.Now()
	}
	if record.LastObservationAt.IsZero() {
		record.LastObservationAt = record.SeenAt
	}
	_, err := db.execWrite(`
		INSERT INTO person_global_identities
			(global_person_id, person_id, person_name, identity_source, identity_confidence, embedding_centroid, body_prototype, body_embedding_count, face_prototype, face_embedding_count, alias_of, last_media_id, last_camera_name, last_observation_at, last_embedding_kind, last_embedding_model, last_quality_score, created_at, updated_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(global_person_id) DO UPDATE SET
			person_id = CASE
				WHEN excluded.person_id IS NOT NULL AND excluded.person_id != '' THEN excluded.person_id
				ELSE person_global_identities.person_id
			END,
			person_name = CASE
				WHEN excluded.person_name IS NOT NULL AND excluded.person_name != '' THEN excluded.person_name
				ELSE person_global_identities.person_name
			END,
			identity_source = CASE
				WHEN excluded.identity_source IS NOT NULL AND excluded.identity_source != '' THEN excluded.identity_source
				ELSE person_global_identities.identity_source
			END,
			identity_confidence = CASE
				WHEN excluded.identity_confidence > person_global_identities.identity_confidence THEN excluded.identity_confidence
				ELSE person_global_identities.identity_confidence
			END,
			embedding_centroid = CASE
				WHEN excluded.embedding_centroid IS NOT NULL AND excluded.embedding_centroid != '' THEN excluded.embedding_centroid
				ELSE person_global_identities.embedding_centroid
			END,
			body_prototype = CASE
				WHEN excluded.body_prototype IS NOT NULL AND excluded.body_prototype != '' THEN excluded.body_prototype
				ELSE person_global_identities.body_prototype
			END,
			body_embedding_count = CASE
				WHEN excluded.body_embedding_count > 0 THEN excluded.body_embedding_count
				ELSE person_global_identities.body_embedding_count
			END,
			face_prototype = CASE
				WHEN excluded.face_prototype IS NOT NULL AND excluded.face_prototype != '' THEN excluded.face_prototype
				ELSE person_global_identities.face_prototype
			END,
			face_embedding_count = CASE
				WHEN excluded.face_embedding_count > 0 THEN excluded.face_embedding_count
				ELSE person_global_identities.face_embedding_count
			END,
			alias_of = CASE
				WHEN excluded.alias_of IS NOT NULL AND excluded.alias_of != '' THEN excluded.alias_of
				ELSE person_global_identities.alias_of
			END,
			last_media_id = CASE
				WHEN excluded.last_media_id IS NOT NULL AND excluded.last_media_id != '' THEN excluded.last_media_id
				ELSE person_global_identities.last_media_id
			END,
			last_camera_name = CASE
				WHEN excluded.last_camera_name IS NOT NULL AND excluded.last_camera_name != '' THEN excluded.last_camera_name
				ELSE person_global_identities.last_camera_name
			END,
			last_observation_at = CASE
				WHEN excluded.last_observation_at IS NOT NULL THEN excluded.last_observation_at
				ELSE person_global_identities.last_observation_at
			END,
			last_embedding_kind = CASE
				WHEN excluded.last_embedding_kind IS NOT NULL AND excluded.last_embedding_kind != '' THEN excluded.last_embedding_kind
				ELSE person_global_identities.last_embedding_kind
			END,
			last_embedding_model = CASE
				WHEN excluded.last_embedding_model IS NOT NULL AND excluded.last_embedding_model != '' THEN excluded.last_embedding_model
				ELSE person_global_identities.last_embedding_model
			END,
			last_quality_score = CASE
				WHEN excluded.last_quality_score > 0 THEN excluded.last_quality_score
				ELSE person_global_identities.last_quality_score
			END,
			updated_at = excluded.updated_at,
			last_seen_at = excluded.last_seen_at
	`, record.GlobalPersonID, record.PersonID, record.PersonName, record.IdentitySource, record.IdentityConfidence, record.EmbeddingCentroid, record.BodyPrototype, record.BodyEmbeddingCount, record.FacePrototype, record.FaceEmbeddingCount, record.AliasOf, record.LastMediaID, record.LastCameraName, record.LastObservationAt, record.LastEmbeddingKind, record.LastEmbeddingModel, record.LastQualityScore, record.SeenAt, record.SeenAt, record.SeenAt)
	if err != nil {
		return fmt.Errorf("upsert person_global_identities: %w", err)
	}
	return nil
}

func (db *Database) GetPersonMoments(userID int64, personID string, date string, limit int) ([]*PersonMoment, error) {
	if personID == "" {
		return []*PersonMoment{}, nil
	}
	return db.QueryPersonTimeline(userID, PersonTimelineFilter{
		PersonID: strings.TrimSpace(personID),
		Date:     strings.TrimSpace(date),
		Limit:    limit,
	})
}
