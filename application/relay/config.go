package relay

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type ICEServerConfig struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// Config holds all relay configuration loaded from environment
type Config struct {
	// Base directory
	AppDir          string
	StorageDir      string // Computed: AppDir + "/storage"
	DatabasePath    string // Computed: AppDir + "/database/relay.db"
	FrontendDistDir string // Built app dist directory served by the relay in local production mode

	// CV Configuration
	FrameInterval time.Duration
	BatchSize     int // Number of frames per batch

	// Ports
	RelayPort string // Port for WebSocket connections (nodes + workers) (e.g., "9020")
	APIPort   string // Port for HTTP API for browsers (e.g., "8020")

	// Dashboard
	DashboardURL string

	// Realtime Streams
	AutoRequestRealtimeStream bool // Auto-create streams for RTSP/MJPEG
	WebRTCIceServers          []ICEServerConfig

	// Security
	JWTSecret string // Secret for signing JWT tokens

	// Dev Mode
	DevImpersonate string // "", "true", or specific email like "dev@local"
	SignupEnabled  bool   // SIGNUP_ENABLED (default: false)

	// Snapshot retention
	SnapshotRetentionDays int // Days to keep snapshots before auto-delete (default 7)

	// Process manager (optional — disabled when RootDir is empty)
	RootDir      string // VYRA_ROOT: absolute path to the VYRA project root
	EnginePort   string // ENGINE_PORT: port Bun engine listens on (default "5000")
	WorkerSecret string // WORKER_SECRET: shared secret for engine workers

	// Process lifecycle policy
	ProcessSelfHealEnabled       bool          // PROCESS_SELF_HEAL_ENABLED (default: false)
	ProcessAutoStartDependencies bool          // PROCESS_AUTO_START_DEPENDENCIES (default: false)
	ProcessAutoStartEngine       bool          // PROCESS_AUTO_START_ENGINE (default: true)
	ProcessCleanupStaleWorkers   bool          // PROCESS_CLEANUP_STALE_WORKERS_ON_START (default: true)
	ProcessAutoScaleEnabled      bool          // PROCESS_AUTO_SCALE_ENABLED (default: false)
	ProcessAutoScaleMax          int           // PROCESS_AUTO_SCALE_MAX (default: 6)
	ProcessAutoScaleInterval     time.Duration // PROCESS_AUTO_SCALE_INTERVAL_SECONDS (default: 3s)
}

// LoadConfig loads and validates all configuration from environment
func LoadConfig() (*Config, error) {
	var missingVars []string
	var errors []string

	// ALL variables are required - no defaults
	appDir := os.Getenv("APP_DIR")
	if appDir == "" {
		missingVars = append(missingVars, "APP_DIR")
	}

	frameIntervalStr := os.Getenv("FRAME_INTERVAL_SECONDS")
	if frameIntervalStr == "" {
		missingVars = append(missingVars, "FRAME_INTERVAL_SECONDS")
	}

	relayPort := os.Getenv("RELAY_PORT")
	if relayPort == "" {
		missingVars = append(missingVars, "RELAY_PORT")
	}

	apiPort := os.Getenv("API_PORT")
	if apiPort == "" {
		missingVars = append(missingVars, "API_PORT")
	}

	dashboardURL := os.Getenv("DASHBOARD_URL")
	if dashboardURL == "" {
		missingVars = append(missingVars, "DASHBOARD_URL")
	}

	// Optional: Auto-request realtime streams (default: true)
	autoRequestRealtimeStream := true
	if val := os.Getenv("AUTO_REQUEST_REALTIME_STREAM"); val != "" {
		autoRequestRealtimeStream = val == "true" || val == "1"
	}

	webRTCIceServers := []ICEServerConfig{
		{URLs: []string{"stun:stun.l.google.com:19302"}},
	}
	if val := os.Getenv("WEBRTC_ICE_SERVERS_JSON"); val != "" {
		var parsed []ICEServerConfig
		if err := json.Unmarshal([]byte(val), &parsed); err != nil {
			errors = append(errors, fmt.Sprintf("WEBRTC_ICE_SERVERS_JSON must be valid JSON, got error: %v", err))
		} else {
			validated := make([]ICEServerConfig, 0, len(parsed))
			for i, srv := range parsed {
				urls := make([]string, 0, len(srv.URLs))
				for _, url := range srv.URLs {
					if url != "" {
						urls = append(urls, url)
					}
				}
				if len(urls) == 0 {
					errors = append(errors, fmt.Sprintf("WEBRTC_ICE_SERVERS_JSON[%d] must include at least one url", i))
					continue
				}
				validated = append(validated, ICEServerConfig{
					URLs:       urls,
					Username:   srv.Username,
					Credential: srv.Credential,
				})
			}
			if len(validated) > 0 {
				webRTCIceServers = validated
			}
		}
	}

	// Optional: Batch size (default: 10)
	batchSize := 10
	if val := os.Getenv("CV_FRAME_BATCH_SIZE"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			batchSize = parsed
		} else {
			errors = append(errors, fmt.Sprintf("CV_FRAME_BATCH_SIZE must be a positive number, got: %s", val))
		}
	}

	// Optional: snapshot retention (default 7 days)
	snapshotRetentionDays := 7
	if val := os.Getenv("SNAPSHOT_RETENTION_DAYS"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			snapshotRetentionDays = parsed
		}
	}

	// JWT secret for token signing
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = "change-me-in-production" // fallback
		log.Printf("[Config] WARNING: Using default JWT_SECRET. Set JWT_SECRET in production!")
	}

	// Dev impersonation mode (optional, default: disabled)
	devImpersonate := os.Getenv("DEV_IMPERSONATE")

	// Public signups are disabled by default; explicitly enable with SIGNUP_ENABLED=true.
	signupEnabled := parseBoolEnv(os.Getenv("SIGNUP_ENABLED"), false)

	// Stop if any required variables are missing
	if len(missingVars) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %v\nPlease set them in .env file or environment", missingVars)
	}

	// Parse and validate durations (supports fractional seconds like 0.2 for 5 FPS)
	frameIntervalSeconds, err := strconv.ParseFloat(frameIntervalStr, 64)
	if err != nil || frameIntervalSeconds <= 0 {
		errors = append(errors, fmt.Sprintf("FRAME_INTERVAL_SECONDS must be a positive number, got: %s", frameIntervalStr))
	}
	frameInterval := time.Duration(frameIntervalSeconds * float64(time.Second))

	// Validate port number
	if _, err := strconv.Atoi(relayPort); err != nil {
		errors = append(errors, fmt.Sprintf("RELAY_PORT must be a number, got: %s", relayPort))
	}

	if _, err := strconv.Atoi(apiPort); err != nil {
		errors = append(errors, fmt.Sprintf("API_PORT must be a number, got: %s", apiPort))
	}

	if len(errors) > 0 {
		return nil, fmt.Errorf("configuration validation errors:\n%v", errors)
	}

	// Compute paths from APP_DIR
	storageDir := filepath.Join(appDir, "storage")
	databasePath := filepath.Join(appDir, "database", "relay.db")

	// Optional process manager settings
	rootDir := os.Getenv("VYRA_ROOT")
	enginePort := os.Getenv("ENGINE_PORT")
	if enginePort == "" {
		enginePort = "5000"
	}
	workerSecret := os.Getenv("WORKER_SECRET")
	processSelfHealEnabled := parseBoolEnv(os.Getenv("PROCESS_SELF_HEAL_ENABLED"), false)
	processAutoStartDependencies := parseBoolEnv(os.Getenv("PROCESS_AUTO_START_DEPENDENCIES"), false)
	processAutoStartEngine := parseBoolEnv(os.Getenv("PROCESS_AUTO_START_ENGINE"), true)
	processCleanupStaleWorkers := parseBoolEnv(os.Getenv("PROCESS_CLEANUP_STALE_WORKERS_ON_START"), true)
	processAutoScaleEnabled := parseBoolEnv(os.Getenv("PROCESS_AUTO_SCALE_ENABLED"), false)
	processAutoScaleMax := 6
	if raw := strings.TrimSpace(os.Getenv("PROCESS_AUTO_SCALE_MAX")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			processAutoScaleMax = parsed
		} else {
			errors = append(errors, fmt.Sprintf("PROCESS_AUTO_SCALE_MAX must be a positive number, got: %s", raw))
		}
	}
	autoScaleIntervalSeconds := 3.0
	if raw := strings.TrimSpace(os.Getenv("PROCESS_AUTO_SCALE_INTERVAL_SECONDS")); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && parsed > 0 {
			autoScaleIntervalSeconds = parsed
		} else {
			errors = append(errors, fmt.Sprintf("PROCESS_AUTO_SCALE_INTERVAL_SECONDS must be a positive number, got: %s", raw))
		}
	}
	autoScaleInterval := time.Duration(autoScaleIntervalSeconds * float64(time.Second))

	frontendDistDir := strings.TrimSpace(os.Getenv("FRONTEND_DIST_DIR"))
	if frontendDistDir == "" && strings.TrimSpace(rootDir) != "" {
		candidate := filepath.Join(rootDir, "application", "app", "dist")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			frontendDistDir = candidate
		}
	}

	config := &Config{
		AppDir:                       appDir,
		StorageDir:                   storageDir,
		DatabasePath:                 databasePath,
		FrontendDistDir:              frontendDistDir,
		FrameInterval:                frameInterval,
		BatchSize:                    batchSize,
		RelayPort:                    relayPort,
		APIPort:                      apiPort,
		DashboardURL:                 dashboardURL,
		AutoRequestRealtimeStream:    autoRequestRealtimeStream,
		WebRTCIceServers:             webRTCIceServers,
		JWTSecret:                    jwtSecret,
		DevImpersonate:               devImpersonate,
		SignupEnabled:                signupEnabled,
		SnapshotRetentionDays:        snapshotRetentionDays,
		RootDir:                      rootDir,
		EnginePort:                   enginePort,
		WorkerSecret:                 workerSecret,
		ProcessSelfHealEnabled:       processSelfHealEnabled,
		ProcessAutoStartDependencies: processAutoStartDependencies,
		ProcessAutoStartEngine:       processAutoStartEngine,
		ProcessCleanupStaleWorkers:   processCleanupStaleWorkers,
		ProcessAutoScaleEnabled:      processAutoScaleEnabled,
		ProcessAutoScaleMax:          processAutoScaleMax,
		ProcessAutoScaleInterval:     autoScaleInterval,
	}

	// Log loaded configuration
	log.Printf("[Config] Loaded configuration:")
	log.Printf("[Config]   APP_DIR: %s", config.AppDir)
	log.Printf("[Config]   STORAGE_DIR: %s", config.StorageDir)
	log.Printf("[Config]   DATABASE_PATH: %s", config.DatabasePath)
	log.Printf("[Config]   FRAME_INTERVAL: %v", config.FrameInterval)
	log.Printf("[Config]   RELAY_PORT: %s (nodes + workers)", config.RelayPort)
	log.Printf("[Config]   API_PORT: %s", config.APIPort)
	log.Printf("[Config]   CV_FRAME_BATCH_SIZE: %d", config.BatchSize)
	log.Printf("[Config]   DASHBOARD_URL: %s", config.DashboardURL)
	log.Printf("[Config]   SIGNUP_ENABLED: %v", config.SignupEnabled)
	log.Printf("[Config]   AUTO_REQUEST_REALTIME_STREAM: %v", config.AutoRequestRealtimeStream)
	log.Printf("[Config]   WEBRTC_ICE_SERVERS: %d configured", len(config.WebRTCIceServers))
	if config.FrontendDistDir != "" {
		log.Printf("[Config]   FRONTEND_DIST_DIR: %s", config.FrontendDistDir)
	}
	log.Printf("[Config]   PROCESS_SELF_HEAL_ENABLED: %v", config.ProcessSelfHealEnabled)
	log.Printf("[Config]   PROCESS_AUTO_START_DEPENDENCIES: %v", config.ProcessAutoStartDependencies)
	log.Printf("[Config]   PROCESS_AUTO_START_ENGINE: %v", config.ProcessAutoStartEngine)
	log.Printf("[Config]   PROCESS_CLEANUP_STALE_WORKERS_ON_START: %v", config.ProcessCleanupStaleWorkers)
	log.Printf("[Config]   PROCESS_AUTO_SCALE_ENABLED: %v", config.ProcessAutoScaleEnabled)
	log.Printf("[Config]   PROCESS_AUTO_SCALE_MAX: %d", config.ProcessAutoScaleMax)
	log.Printf("[Config]   PROCESS_AUTO_SCALE_INTERVAL: %v", config.ProcessAutoScaleInterval)
	if config.DevImpersonate != "" {
		log.Printf("[Config]   DEV_IMPERSONATE: %s", config.DevImpersonate)
	}

	return config, nil
}

func parseBoolEnv(raw string, fallback bool) bool {
	switch raw {
	case "1", "true", "TRUE", "yes", "YES", "on", "ON":
		return true
	case "0", "false", "FALSE", "no", "NO", "off", "OFF":
		return false
	default:
		return fallback
	}
}
