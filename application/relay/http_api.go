package relay

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func StartHTTPAPIServer(relay *Relay, addr string, cfg *Config) (*http.Server, error) {
	mux := http.NewServeMux()

	// Wrap with CORS middleware
	handler := corsMiddleware(mux)

	sessionManager := NewWebRTCSessionManager()
	authStore := NewAuthStore(relay.db.DB)

	mux.HandleFunc("/api/authorize", func(w http.ResponseWriter, r *http.Request) {
		handleAuthorizeAPI(w, r, cfg, relay)
	})

	// Feature flags endpoint (no auth required)
	mux.HandleFunc("/flags", func(w http.ResponseWriter, r *http.Request) {
		handleFlags(w, r, cfg)
	})

	// Browser client WebSocket connection (auth via registration message)
	mux.HandleFunc("/client/connect", relay.clientEvents.HandleWebSocket)

	// ── System / process-manager endpoints (protected) ──────────────────────
	mux.HandleFunc("/system/status", func(w http.ResponseWriter, r *http.Request) {
		_, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleSystemStatus(w, r, relay)
	})
	mux.HandleFunc("/system/workers", func(w http.ResponseWriter, r *http.Request) {
		_, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleSystemWorkers(w, r, relay)
	})
	mux.HandleFunc("/system/scale", func(w http.ResponseWriter, r *http.Request) {
		_, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleSystemScale(w, r, relay)
	})
	mux.HandleFunc("/system/autoscale", func(w http.ResponseWriter, r *http.Request) {
		_, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleSystemAutoscale(w, r, relay)
	})

	mux.HandleFunc("/system/frame-interval", func(w http.ResponseWriter, r *http.Request) {
		_, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleFrameInterval(w, r, relay)
	})

	mux.HandleFunc("/system/start/agent", func(w http.ResponseWriter, r *http.Request) {
		_, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleStartAgent(w, r, relay)
	})

	mux.HandleFunc("/system/custom/start", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleSystemCustomStart(w, r, relay, userID)
	})
	mux.HandleFunc("/system/custom/scale", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleSystemCustomScale(w, r, relay, userID)
	})
	mux.HandleFunc("/system/custom/stop", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleSystemCustomStop(w, r, relay, userID)
	})
	mux.HandleFunc("/system/custom/workers", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleSystemCustomWorkers(w, r, relay, userID)
	})
	mux.HandleFunc("/system/llm/start", func(w http.ResponseWriter, r *http.Request) {
		_, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleSystemLLMStart(w, r, relay)
	})
	mux.HandleFunc("/system/llm/stop", func(w http.ResponseWriter, r *http.Request) {
		_, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleSystemLLMStop(w, r, relay)
	})
	mux.HandleFunc("/system/llm/status", func(w http.ResponseWriter, r *http.Request) {
		_, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleSystemLLMStatus(w, r, relay)
	})
	mux.HandleFunc("/system/runtime-settings", func(w http.ResponseWriter, r *http.Request) {
		_, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleSystemRuntimeSettings(w, r, relay)
	})
	mux.HandleFunc("/system/process-policy", func(w http.ResponseWriter, r *http.Request) {
		_, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleSystemProcessPolicy(w, r, relay)
	})

	// /system/start/{name} and /system/stop/{name}
	mux.HandleFunc("/system/start/", func(w http.ResponseWriter, r *http.Request) {
		_, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/system/start/")
		if name == "" {
			http.Error(w, "Component name required", http.StatusBadRequest)
			return
		}
		handleSystemStart(w, r, relay, name)
	})

	mux.HandleFunc("/system/stop/", func(w http.ResponseWriter, r *http.Request) {
		_, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/system/stop/")
		if name == "" {
			http.Error(w, "Component name required", http.StatusBadRequest)
			return
		}
		handleSystemStop(w, r, relay, name)
	})

	// ── Managed nodes endpoints (protected) ────────────────────────────────
	mux.HandleFunc("/managed-nodes", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		switch r.Method {
		case http.MethodGet:
			handleListManagedNodes(w, r, relay, userID)
		case http.MethodPost:
			handleCreateManagedNode(w, r, relay, userID)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// /managed-nodes/{id}, /managed-nodes/{id}/start, /managed-nodes/{id}/stop,
	// /managed-nodes/{id}/name, /managed-nodes/{id}/cameras/{cameraId}
	mux.HandleFunc("/managed-nodes/", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		// Trim prefix and split: ["nodeID", "action", ...]
		rest := strings.TrimPrefix(r.URL.Path, "/managed-nodes/")
		parts := strings.SplitN(rest, "/", 3)
		if len(parts) < 1 || parts[0] == "" {
			http.Error(w, "Node ID required", http.StatusBadRequest)
			return
		}
		nodeID := parts[0]

		if len(parts) == 1 {
			// DELETE /managed-nodes/{id}
			handleDeleteManagedNode(w, r, relay, userID, nodeID)
			return
		}

		action := parts[1]
		if action == "cameras" && len(parts) == 3 {
			cameraID := parts[2]
			switch r.Method {
			case http.MethodPost:
				handleAssignCamera(w, r, relay, userID, nodeID, cameraID)
			case http.MethodDelete:
				handleUnassignCamera(w, r, relay, userID, nodeID, cameraID)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
			return
		}

		handleManagedNodeAction(w, r, relay, userID, nodeID, action)
	})

	// List all nodes (protected)
	mux.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleListNodes(w, r, relay, userID)
	})

	// Node-specific endpoints: /nodes/{nodeId}/services and /nodes/{nodeId}/offer (protected)
	mux.HandleFunc("/nodes/", func(w http.ResponseWriter, r *http.Request) {
		// Parse: /nodes/{nodeId}/{endpoint}
		path := strings.TrimPrefix(r.URL.Path, "/nodes/")
		parts := strings.SplitN(path, "/", 2)

		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			http.Error(w, "Invalid path. Expected /nodes/{nodeId}/{services|offer}", http.StatusBadRequest)
			return
		}

		nodeID := parts[0]
		endpoint := parts[1]

		// Check authentication
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}

		switch endpoint {
		case "services":
			handleNodeServices(w, r, relay, nodeID)
		case "offer":
			handleNodeOffer(w, r, relay, nodeID, sessionManager)
		case "name":
			if !relay.nodeTable.UserOwnsNode(userID, nodeID) {
				http.Error(w, "Node not found", http.StatusNotFound)
				return
			}
			handleUpdateNodeName(w, r, relay, nodeID)
		case "delete":
			if !relay.nodeTable.UserOwnsNode(userID, nodeID) {
				http.Error(w, "Node not found", http.StatusNotFound)
				return
			}
			handleDeleteNode(w, r, relay, nodeID)
		default:
			http.Error(w, "Not found", http.StatusNotFound)
		}
	})

	// Authentication endpoints
	mux.HandleFunc("/auth/register", func(w http.ResponseWriter, r *http.Request) {
		handleRegister(w, r, authStore, cfg)
	})

	mux.HandleFunc("/auth/login", func(w http.ResponseWriter, r *http.Request) {
		handleLogin(w, r, authStore, cfg)
	})

	mux.HandleFunc("/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		handleLogout(w, r)
	})

	mux.HandleFunc("/auth/me", func(w http.ResponseWriter, r *http.Request) {
		handleMe(w, r, authStore, cfg)
	})

	mux.HandleFunc("/auth/check-username", func(w http.ResponseWriter, r *http.Request) {
		handleCheckUsername(w, r, authStore)
	})

	// Agent endpoints (protected)
	mux.HandleFunc("/agents", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}

		switch r.Method {
		case http.MethodPost:
			handleCreateAgent(w, r, relay, userID)
		case http.MethodGet:
			handleListAgents(w, r, relay, userID)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/agents/custom-yolo", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleCreateCustomYOLOAgent(w, r, relay, userID)
	})
	mux.HandleFunc("/agents/predefined/sync", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleSyncPredefinedAgents(w, r, relay, userID)
	})

	// Agent-specific endpoints: /agents/{agentId} (protected)
	mux.HandleFunc("/agents/", func(w http.ResponseWriter, r *http.Request) {
		// Parse: /agents/{agentId}/{endpoint}
		path := strings.TrimPrefix(r.URL.Path, "/agents/")
		parts := strings.SplitN(path, "/", 2)

		if len(parts) < 1 || parts[0] == "" {
			http.Error(w, "Invalid path. Expected /agents/{agentId}", http.StatusBadRequest)
			return
		}

		agentID := parts[0]
		endpoint := ""
		if len(parts) > 1 {
			endpoint = parts[1]
		}

		// Check authentication
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}

		// Verify agent ownership
		if !relay.agentTable.UserOwnsAgent(userID, agentID) {
			http.Error(w, "Agent not found", http.StatusNotFound)
			return
		}

		// Route to specific handler
		if endpoint == "" {
			// GET/PATCH/DELETE /agents/{agentId}
			switch r.Method {
			case http.MethodGet:
				handleGetAgent(w, r, relay, agentID)
			case http.MethodPatch:
				handlePatchAgent(w, r, relay, agentID)
			case http.MethodDelete:
				handleDeleteAgent(w, r, relay, agentID)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		} else {
			http.Error(w, "Not found", http.StatusNotFound)
		}
	})

	// Event count (must be before /events catch-all)
	mux.HandleFunc("/events/count", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleEventCount(w, r, relay, userID)
	})

	// Daily summary
	mux.HandleFunc("/daily-summary", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleGetDailySummary(w, r, relay, userID)
	})

	// AI query (natural language → grounded answer via Ask-AI worker)
	mux.HandleFunc("/query", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleAIQuery(w, r, relay, userID)
	})

	mux.HandleFunc("/query/chats", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleQueryChats(w, r, relay, userID)
	})
	mux.HandleFunc("/query/chats/", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleQueryChat(w, r, relay, userID)
	})

	mux.HandleFunc("/timeline/identities", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleTimelineIdentities(w, r, relay, userID)
	})
	mux.HandleFunc("/timeline/backfill", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleTimelineBackfill(w, r, relay, userID)
	})
	mux.HandleFunc("/timeline/identities/", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		globalPersonID, action := timelineIdentityActionFromPath(r.URL.Path)
		if globalPersonID == "" {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		switch action {
		case "moments":
			handleTimelineIdentityMoments(w, r, relay, userID, globalPersonID)
		case "assign":
			handleTimelineIdentityAssign(w, r, relay, userID, globalPersonID)
		case "merge":
			handleTimelineIdentityMerge(w, r, relay, userID, globalPersonID)
		case "split":
			handleTimelineIdentitySplit(w, r, relay, userID, globalPersonID)
		default:
			http.Error(w, "Not found", http.StatusNotFound)
		}
	})
	mux.HandleFunc("/timeline/moments", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleTimelineMoments(w, r, relay, userID)
	})
	mux.HandleFunc("/timeline/moments/", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		momentID, action := timelineMomentActionFromPath(r.URL.Path)
		if momentID == "" {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		switch action {
		case "review":
			handleTimelineMomentReview(w, r, relay, userID, momentID)
		case "reveal":
			handleTimelineMomentReveal(w, r, relay, userID, momentID)
		default:
			http.Error(w, "Not found", http.StatusNotFound)
		}
	})
	mux.HandleFunc("/timeline/journey", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleTimelineJourney(w, r, relay, userID)
	})
	mux.HandleFunc("/timeline/appearance-search", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleTimelineAppearanceSearch(w, r, relay, userID)
	})
	mux.HandleFunc("/timeline/cases", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleTimelineCases(w, r, relay, userID)
	})
	mux.HandleFunc("/timeline/cases/", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		caseID, action := timelineCaseActionFromPath(r.URL.Path)
		if caseID == "" {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		handleTimelineCase(w, r, relay, userID, caseID, action)
	})
	mux.HandleFunc("/timeline/evidence-locks", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleTimelineEvidenceLocks(w, r, relay, userID)
	})
	mux.HandleFunc("/timeline/topology", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleTimelineTopology(w, r, relay, userID)
	})

	// Knowledge-base RAG endpoints (protected)
	mux.HandleFunc("/kb/index", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleKBIndex(w, r, relay, userID)
	})
	mux.HandleFunc("/kb/reindex-docs", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleKBReindexDocs(w, r, relay, userID)
	})
	mux.HandleFunc("/kb/search", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleKBSearch(w, r, relay, userID)
	})
	mux.HandleFunc("/kb/stats", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleKBStats(w, r, relay, userID)
	})
	mux.HandleFunc("/kb/feedback", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleKBFeedback(w, r, relay, userID)
	})

	// Events endpoints (protected)
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleListEvents(w, r, relay, userID)
	})

	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleSearchEvents(w, r, relay, userID)
	})

	// Camera grouping endpoints for Bird-eye (protected)
	mux.HandleFunc("/camera-groups", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleCameraGroups(w, r, relay, userID)
	})

	mux.HandleFunc("/camera-groups/", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/camera-groups/")
		handleCameraGroupsWithID(w, r, relay, userID, path)
	})

	// Camera management endpoints (protected)
	mux.HandleFunc("/cameras", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		switch r.Method {
		case http.MethodGet:
			handleListCameras(w, r, relay, userID)
		case http.MethodPost:
			handleCreateCamera(w, r, relay, userID)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/cameras/", func(w http.ResponseWriter, r *http.Request) {
		_, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		cameraID := strings.TrimPrefix(r.URL.Path, "/cameras/")
		if cameraID == "" {
			http.Error(w, "Camera ID required", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodPatch:
			handleUpdateCamera(w, r, relay, cameraID)
		case http.MethodDelete:
			handleDeleteCamera(w, r, relay, cameraID)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Snapshot storage endpoints
	// GET /storage/snapshots/{date}/{filename} - Serve snapshot images (protected)
	mux.HandleFunc("/storage/snapshots/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		handleGetSnapshot(w, r, relay)
	})

	// POST /cv/upload-snapshot - Upload snapshot from workers (internal use)
	mux.HandleFunc("/cv/upload-snapshot", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Note: Authentication handled by engine/adapter trust relationship
		handleUploadSnapshot(w, r, relay)
	})

	// ── Face identity service (shared face worker pool) ─────────────────────
	// Detection workers call these to request/poll face lookups instead of
	// embedding InsightFace directly.
	mux.HandleFunc("/cv/face-lookup", func(w http.ResponseWriter, r *http.Request) {
		relay.faceIdentityService.HandleFaceLookup(w, r)
	})
	mux.HandleFunc("/cv/face-identities", func(w http.ResponseWriter, r *http.Request) {
		relay.faceIdentityService.HandleFaceIdentities(w, r)
	})

	// ── Global person identity service (cross-camera IDs) ───────────────────
	// Detection workers call these lightweight internal endpoints to resolve
	// and fetch stable global person IDs (recognized + unknown).
	mux.HandleFunc("/cv/person-identity/resolve", func(w http.ResponseWriter, r *http.Request) {
		relay.personIdentityService.HandleResolve(w, r)
	})
	mux.HandleFunc("/cv/person-identities", func(w http.ResponseWriter, r *http.Request) {
		relay.personIdentityService.HandleIdentities(w, r)
	})

	// ── Persons (face recognition registry) ─────────────────────────────────
	// GET  /persons          → list persons for user
	// POST /persons          → create person { name }
	mux.HandleFunc("/persons", func(w http.ResponseWriter, r *http.Request) {
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		switch r.Method {
		case http.MethodGet:
			handleListPersons(w, r, relay, userID)
		case http.MethodPost:
			handleCreatePerson(w, r, relay, userID)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// GET /persons/registry      → face registry for face_recognizer worker (internal, no auth)
	mux.HandleFunc("/persons/registry", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handlePersonsRegistry(w, r, relay)
	})

	// /persons/{id}                 → GET (person info), PATCH (rename), DELETE
	// /persons/{id}/face            → GET (legacy/default face JPEG), POST (legacy upload)
	// /persons/{id}/faces           → GET (list profiles), POST (add profile)
	// /persons/{id}/faces/{faceId}  → GET (profile image), DELETE (remove profile)
	// /persons/{id}/sightings       → GET (recent face recognition events for this person)
	// /persons/{id}/moments         → GET (person timeline snapshots/briefs)
	mux.HandleFunc("/persons/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/persons/")
		parts := strings.Split(rest, "/")
		if len(parts) < 1 || strings.TrimSpace(parts[0]) == "" {
			http.Error(w, "Person ID required", http.StatusBadRequest)
			return
		}
		personID := parts[0]
		sub1 := ""
		sub2 := ""
		if len(parts) >= 2 {
			sub1 = parts[1]
		}
		if len(parts) >= 3 {
			sub2 = parts[2]
		}
		if len(parts) > 3 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		// Unauthenticated worker/image fetch endpoints.
		// Legacy single-face image.
		if sub1 == "face" && sub2 == "" && r.Method == http.MethodGet {
			handleGetPersonFace(w, r, relay, personID)
			return
		}
		// Multi-profile list and per-profile image.
		if sub1 == "faces" && r.Method == http.MethodGet {
			if sub2 == "" {
				handleListPersonFaces(w, r, relay, personID)
			} else {
				handleGetPersonFaceProfile(w, r, relay, personID, sub2)
			}
			return
		}

		// All remaining paths require auth.
		userID, err := requireAuth(w, r, cfg, authStore)
		if err != nil {
			return
		}
		if !relay.personTable.UserOwnsPerson(userID, personID) {
			http.Error(w, "Person not found", http.StatusNotFound)
			return
		}

		switch sub1 {
		case "":
			switch r.Method {
			case http.MethodGet:
				handleGetPerson(w, r, relay, personID)
			case http.MethodPatch:
				handleRenamePerson(w, r, relay, personID)
			case http.MethodDelete:
				handleDeletePerson(w, r, relay, personID)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		case "face":
			// POST /persons/{id}/face — upload face image
			if sub2 == "" && r.Method == http.MethodPost {
				handleUploadPersonFace(w, r, relay, personID)
			} else {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		case "faces":
			switch r.Method {
			case http.MethodGet:
				if sub2 == "" {
					handleListPersonFaces(w, r, relay, personID)
				} else {
					handleGetPersonFaceProfile(w, r, relay, personID, sub2)
				}
			case http.MethodPost:
				if sub2 == "" {
					handleUploadPersonFaceProfile(w, r, relay, personID)
				} else {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				}
			case http.MethodDelete:
				if sub2 != "" {
					handleDeletePersonFaceProfile(w, r, relay, personID, sub2)
				} else {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				}
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		case "sightings":
			// GET /persons/{id}/sightings — recent detection events for this person
			if sub2 == "" && r.Method == http.MethodGet {
				handlePersonSightings(w, r, relay, userID, personID)
			} else {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		case "moments":
			// GET /persons/{id}/moments?date=YYYY-MM-DD — person timeline feed
			if sub2 == "" && r.Method == http.MethodGet {
				handlePersonMoments(w, r, relay, userID, personID)
			} else {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		default:
			http.Error(w, "Not found", http.StatusNotFound)
		}
	})

	if frontendHandler := newFrontendHandler(cfg.FrontendDistDir); frontendHandler != nil {
		mux.Handle("/", frontendHandler)
	}

	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	log.Printf("[HTTP] Starting HTTP API on %s", addr)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[HTTP] Server error: %v", err)
		}
	}()

	return server, nil
}

func newFrontendHandler(distDir string) http.Handler {
	distDir = strings.TrimSpace(distDir)
	if distDir == "" {
		return nil
	}
	indexPath := filepath.Join(distDir, "index.html")
	info, err := os.Stat(indexPath)
	if err != nil || info.IsDir() {
		log.Printf("[HTTP] Frontend dist missing, skipping static SPA serving: %s", indexPath)
		return nil
	}
	fileServer := http.FileServer(http.Dir(distDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		cleanURLPath := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
		localPath := filepath.Join(distDir, filepath.FromSlash(strings.TrimPrefix(cleanURLPath, "/")))
		if info, err := os.Stat(localPath); err == nil && !info.IsDir() {
			if strings.HasPrefix(cleanURLPath, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		if ext := path.Ext(cleanURLPath); ext != "" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, indexPath)
	})
}

// corsMiddleware adds CORS headers to all responses
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Dev-Impersonate, X-Worker-Key")
		w.Header().Set("Access-Control-Allow-Credentials", "true") // Required for credentials mode

		// Handle preflight requests
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Call the next handler
		next.ServeHTTP(w, r)
	})
}

// handleListNodes returns a list of all nodes visible to authenticated users.
func handleListNodes(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	_ = userID
	nodes, err := relay.nodeTable.GetAllNodes()
	if err != nil {
		log.Printf("[HTTP] handleListNodes: error getting nodes: %v", err)
		http.Error(w, "Failed to fetch nodes", http.StatusInternalServerError)
		return
	}

	// Return nodes with status information
	type NodeResponse struct {
		ID              string  `json:"id"`
		Status          string  `json:"status"`
		Name            *string `json:"name,omitempty"`
		LastConnectedAt *string `json:"last_connected_at,omitempty"`
	}

	result := make([]NodeResponse, len(nodes))
	for i, node := range nodes {
		// Check if node is currently connected
		status := "offline"
		if relay.GetNode(node.ID) != nil {
			status = "online"
		}

		var lastConnected *string
		if node.LastConnectedAt != nil {
			formatted := node.LastConnectedAt.Format("2006-01-02T15:04:05Z07:00")
			lastConnected = &formatted
		}

		result[i] = NodeResponse{
			ID:              node.ID,
			Status:          status,
			Name:            node.Name,
			LastConnectedAt: lastConnected,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleNodeServices returns services filtered by node ID
func handleNodeServices(w http.ResponseWriter, r *http.Request, relay *Relay, nodeID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	services := relay.Services().ListByNode(nodeID)

	// Check if node exists (either connected or has registered services)
	node := relay.GetNode(nodeID)
	nodeExists := node != nil || len(services) > 0

	if !nodeExists {

		http.Error(w, "Node not found: "+nodeID, http.StatusNotFound)
		return
	}

	type ServiceInfo struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Type   string `json:"type"`
		NodeID string `json:"node_id"`
		Addr   string `json:"addr"`
		Port   int    `json:"port"`
		Path   string `json:"path"`
	}

	result := make([]ServiceInfo, len(services))
	for i, s := range services {
		result[i] = ServiceInfo{
			ID:     s.Service.ID,
			Name:   s.Service.Name,
			Type:   s.Service.Type,
			NodeID: s.NodeID,
			Addr:   s.Service.Addr,
			Port:   s.Service.Port,
			Path:   s.Service.Path,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleNodeOffer handles WebRTC offers for a specific node's services
func handleNodeOffer(w http.ResponseWriter, r *http.Request, relay *Relay, nodeID string, sessionManager *WebRTCSessionManager) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SDP       string `json:"sdp"`
		ServiceID string `json:"serviceId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("[HTTP] WebRTC offer for service %s (node %s) from %s", req.ServiceID, nodeID, r.RemoteAddr)

	// Find service
	regService := relay.Services().GetForNode(req.ServiceID, nodeID)
	if regService == nil {
		http.Error(w, "Service not found: "+req.ServiceID, http.StatusNotFound)
		return
	}

	service := regService.Service

	// Create WebRTC session
	sessionID, answerSDP, err := sessionManager.NewSession(req.SDP, service, relay)
	if err != nil {
		log.Printf("[HTTP] WebRTC session failed: %v", err)
		http.Error(w, "Failed to create session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("[HTTP] WebRTC session %s created for service %s", sessionID, req.ServiceID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"type": "answer",
		"sdp":  answerSDP,
	})
}

// handleUpdateNodeName handles updating a node's name
func handleUpdateNodeName(w http.ResponseWriter, r *http.Request, relay *Relay, nodeID string) {
	if r.Method != http.MethodPatch && r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate name
	if len(req.Name) > 255 {
		http.Error(w, "Name too long (max 255 characters)", http.StatusBadRequest)
		return
	}

	// Update node name
	if err := relay.nodeTable.UpdateNodeName(nodeID, req.Name); err != nil {
		log.Printf("[HTTP] Failed to update node name: %v", err)
		http.Error(w, "Failed to update node name", http.StatusInternalServerError)
		return
	}

	log.Printf("[HTTP] Updated name for node %s to '%s'", nodeID, req.Name)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"success": "true",
		"name":    req.Name,
	})
}

// handleDeleteNode handles deleting/unauthorizing a node
func handleDeleteNode(w http.ResponseWriter, r *http.Request, relay *Relay, nodeID string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Delete node from database
	if err := relay.nodeTable.DeleteNode(nodeID); err != nil {
		log.Printf("[HTTP] Failed to delete node: %v", err)
		http.Error(w, "Failed to delete node", http.StatusInternalServerError)
		return
	}

	// If node is currently connected, disconnect it
	if nc := relay.GetNode(nodeID); nc != nil {
		nc.Close()
	}

	log.Printf("[HTTP] Deleted node %s", nodeID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"success": "true",
	})
}

// Agent handler functions

// AgentResponse represents the agent data returned in API responses
type AgentResponse struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Instruction string   `json:"instruction"`
	WorkerID    string   `json:"worker_id"`
	ServiceIDs  []string `json:"service_ids"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
	Kind        string   `json:"kind"`

	CustomModelFilename       string                `json:"custom_model_filename,omitempty"`
	CustomModelSizeBytes      int64                 `json:"custom_model_size_bytes,omitempty"`
	CustomRuntimeDefaults     *AgentRuntimeDefaults `json:"custom_runtime_defaults,omitempty"`
	CustomRuntimeDesiredScale int                   `json:"custom_runtime_desired_replicas,omitempty"`
}

type AgentRuntimeDefaults struct {
	EnableVLM  bool `json:"enable_vlm"`
	EnableFace bool `json:"enable_face"`
}

func toAgentResponse(agent *Agent) AgentResponse {
	resp := AgentResponse{
		ID:          agent.ID,
		Name:        agent.Name,
		Instruction: agent.Config.Instruction,
		WorkerID:    agent.WorkerID,
		ServiceIDs:  agent.ServiceIDs,
		CreatedAt:   agent.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:   agent.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		Kind:        "standard",
	}
	if agent.Config.CustomYOLO != nil && agent.Config.CustomYOLO.Enabled {
		resp.Kind = "custom_yolo"
		resp.CustomModelFilename = agent.Config.CustomYOLO.ModelFilename
		resp.CustomModelSizeBytes = agent.Config.CustomYOLO.ModelSizeBytes
		resp.CustomRuntimeDefaults = &AgentRuntimeDefaults{
			EnableVLM:  agent.Config.CustomYOLO.RuntimeDefaultsEnableVLM,
			EnableFace: agent.Config.CustomYOLO.RuntimeDefaultsEnableFace,
		}
		resp.CustomRuntimeDesiredScale = agent.Config.CustomYOLO.RuntimeDesiredReplicas
	}
	return resp
}

// handleSyncPredefinedAgents upserts the predefined agents for a user.
// POST /agents/predefined/sync
func handleSyncPredefinedAgents(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := relay.agentTable.MigrateLegacyFaceAgents(); err != nil {
		http.Error(w, "Failed to migrate legacy agents", http.StatusInternalServerError)
		return
	}

	allAgents, err := relay.agentTable.GetAgentsByUser(userID)
	if err != nil {
		http.Error(w, "Failed to read agents", http.StatusInternalServerError)
		return
	}

	created := 0
	updated := 0
	var synced []*Agent

	for _, tpl := range predefinedAgentTemplates {
		matches := make([]*Agent, 0, 2)
		for _, a := range allAgents {
			if workerAliasMatch(a.WorkerID, tpl.Aliases) {
				matches = append(matches, a)
			}
		}
		// Fallback matching by name to avoid creating a duplicate predefined agent
		// when worker IDs were manually edited.
		if len(matches) == 0 {
			for _, a := range allAgents {
				if strings.EqualFold(strings.TrimSpace(a.Name), strings.TrimSpace(tpl.Name)) {
					matches = append(matches, a)
				}
			}
		}

		var target *Agent
		if len(matches) == 0 {
			target, err = relay.agentTable.CreateAgent(tpl.Name, tpl.Instruction, tpl.WorkerID, []string{}, userID)
			if err != nil {
				http.Error(w, "Failed to create predefined agent", http.StatusInternalServerError)
				return
			}
			created++
		} else {
			mergedServiceIDs := make([]string, 0, len(matches))
			for _, m := range matches {
				mergedServiceIDs = mergeServiceIDs(mergedServiceIDs, m.ServiceIDs)
			}

			// Pick the record with the richest binding set as canonical target.
			target = canonicalAgentTarget(matches)
			name := tpl.Name
			instr := tpl.Instruction
			if err := relay.agentTable.UpdateAgent(target.ID, &name, &instr, &mergedServiceIDs); err != nil {
				http.Error(w, "Failed to update predefined agent", http.StatusInternalServerError)
				return
			}
			// Canonicalize worker_id while preserving legacy compatibility.
			if !strings.EqualFold(target.WorkerID, tpl.WorkerID) {
				if _, err := relay.db.DB.Exec("UPDATE agents SET worker_id = ?, updated_at = datetime('now') WHERE id = ?", tpl.WorkerID, target.ID); err != nil {
					http.Error(w, "Failed to normalize predefined agent worker_id", http.StatusInternalServerError)
					return
				}
			}
			updated++

			// Keep a single record per template.
			if len(matches) > 1 {
				for _, dup := range matches[1:] {
					_ = relay.agentTable.DeleteAgent(dup.ID)
					relay.agentRegistry.RemoveAgent(dup.ID)
				}
			}

			target, err = relay.agentTable.GetAgentByID(target.ID)
			if err != nil {
				http.Error(w, "Failed to reload predefined agent", http.StatusInternalServerError)
				return
			}
		}

		relay.agentRegistry.RegisterAgent(&AgentInfo{
			ID:          target.ID,
			Name:        target.Name,
			WorkerID:    target.WorkerID,
			Instruction: target.Config.Instruction,
			ServiceIDs:  target.ServiceIDs,
			UserID:      target.UserID,
			CustomYOLO:  target.Config.CustomYOLO,
		})
		synced = append(synced, target)
	}

	resp := make([]AgentResponse, 0, len(synced))
	for _, a := range synced {
		resp = append(resp, toAgentResponse(a))
	}
	relay.syncAllFrameExtractors()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"agents":  resp,
		"created": created,
		"updated": updated,
	})
}

// handleCreateAgent handles POST /agents
func handleCreateAgent(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	var req struct {
		Name        string   `json:"name"`
		Instruction string   `json:"instruction"`
		WorkerID    string   `json:"worker_id"`
		ServiceIDs  []string `json:"service_ids"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validation
	if req.Name == "" {
		http.Error(w, "Agent name is required", http.StatusBadRequest)
		return
	}
	if req.Instruction == "" {
		http.Error(w, "Agent instruction is required", http.StatusBadRequest)
		return
	}
	if req.WorkerID == "" {
		http.Error(w, "Worker ID is required", http.StatusBadRequest)
		return
	}
	if isLegacyFaceWorkerID(req.WorkerID) {
		http.Error(w, "Legacy face-only agents were removed. Use Person Detection instead.", http.StatusBadRequest)
		return
	}
	if len(req.Name) > 255 {
		http.Error(w, "Name too long (max 255 characters)", http.StatusBadRequest)
		return
	}
	if len(req.Instruction) > 5000 {
		http.Error(w, "Instruction too long (max 5000 characters)", http.StatusBadRequest)
		return
	}

	// Create agent
	agent, err := relay.agentTable.CreateAgent(req.Name, req.Instruction, req.WorkerID, req.ServiceIDs, userID)
	if err != nil {
		log.Printf("[HTTP] Failed to create agent: %v", err)
		http.Error(w, "Failed to create agent", http.StatusInternalServerError)
		return
	}

	// Register agent in memory registry
	relay.agentRegistry.RegisterAgent(&AgentInfo{
		ID:          agent.ID,
		Name:        agent.Name,
		WorkerID:    agent.WorkerID,
		Instruction: agent.Config.Instruction,
		ServiceIDs:  agent.ServiceIDs,
		UserID:      userID,
		CustomYOLO:  agent.Config.CustomYOLO,
	})
	relay.syncFrameExtractorsForServices(agent.ServiceIDs...)

	log.Printf("[HTTP] Agent %s created by user %d", agent.ID, userID)

	// Format response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"id":      agent.ID,
		"agent":   toAgentResponse(agent),
	})
}

// handleListAgents handles GET /agents
func handleListAgents(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	agents, err := relay.agentTable.GetAgentsByUser(userID)
	if err != nil {
		log.Printf("[HTTP] Failed to list agents: %v", err)
		http.Error(w, "Failed to fetch agents", http.StatusInternalServerError)
		return
	}

	result := make([]AgentResponse, len(agents))
	for i, agent := range agents {
		result[i] = toAgentResponse(agent)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleGetAgent handles GET /agents/{agentId}
func handleGetAgent(w http.ResponseWriter, r *http.Request, relay *Relay, agentID string) {
	agent, err := relay.agentTable.GetAgentByID(agentID)
	if err != nil {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(toAgentResponse(agent))
}

// handlePatchAgent handles PATCH /agents/{agentId}
func handlePatchAgent(w http.ResponseWriter, r *http.Request, relay *Relay, agentID string) {
	var req struct {
		Name        *string   `json:"name"`
		Instruction *string   `json:"instruction"`
		ServiceIDs  *[]string `json:"service_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	existingAgent, err := relay.agentTable.GetAgentByID(agentID)
	if err != nil {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	// Validate fields if provided
	if req.Name != nil && len(*req.Name) > 255 {
		http.Error(w, "Name too long (max 255 characters)", http.StatusBadRequest)
		return
	}
	if req.Instruction != nil && len(*req.Instruction) > 5000 {
		http.Error(w, "Instruction too long (max 5000 characters)", http.StatusBadRequest)
		return
	}

	// Update agent with partial fields
	if err := relay.agentTable.UpdateAgent(agentID, req.Name, req.Instruction, req.ServiceIDs); err != nil {
		log.Printf("[HTTP] Failed to update agent: %v", err)
		http.Error(w, "Failed to update agent: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Reload agent from database and update registry
	agent, err := relay.agentTable.GetAgentByID(agentID)
	if err != nil {
		log.Printf("[HTTP] Failed to reload agent after update: %v", err)
		http.Error(w, "Failed to reload updated agent", http.StatusInternalServerError)
		return
	}
	relay.agentRegistry.RegisterAgent(&AgentInfo{
		ID:          agent.ID,
		Name:        agent.Name,
		WorkerID:    agent.WorkerID,
		Instruction: agent.Config.Instruction,
		ServiceIDs:  agent.ServiceIDs,
		UserID:      agent.UserID,
		CustomYOLO:  agent.Config.CustomYOLO,
	})
	relay.syncFrameExtractorsForServices(mergeServiceIDs(existingAgent.ServiceIDs, agent.ServiceIDs)...)

	log.Printf("[HTTP] Updated agent %s", agentID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(toAgentResponse(agent))
}

// handleDeleteAgent handles DELETE /agents/{agentId}
func handleDeleteAgent(w http.ResponseWriter, r *http.Request, relay *Relay, agentID string) {
	agent, err := relay.agentTable.GetAgentByID(agentID)
	if err != nil {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	// Best-effort runtime cleanup for custom agents.
	if agent.Config.CustomYOLO != nil && agent.Config.CustomYOLO.Enabled {
		if err := relay.processManager.StopCustomAgentRuntime(agentID); err != nil {
			log.Printf("[CustomYOLO] stop runtime before delete failed (agent=%s): %v", agentID, err)
		}

		if modelPath, ok := resolveCustomModelPath(relay.config.AppDir, agent.Config.CustomYOLO.ModelRelPath); ok {
			if err := os.Remove(modelPath); err != nil && !os.IsNotExist(err) {
				log.Printf("[CustomYOLO] remove model file failed (agent=%s path=%s): %v", agentID, modelPath, err)
			}
			agentDir := filepath.Dir(modelPath)
			if err := os.RemoveAll(agentDir); err != nil {
				log.Printf("[CustomYOLO] remove model dir failed (agent=%s dir=%s): %v", agentID, agentDir, err)
			}
		}
	}

	if err := relay.agentTable.DeleteAgent(agentID); err != nil {
		log.Printf("[HTTP] Failed to delete agent: %v", err)
		http.Error(w, "Failed to delete agent", http.StatusInternalServerError)
		return
	}

	// Remove agent from memory registry
	relay.agentRegistry.RemoveAgent(agentID)
	relay.syncFrameExtractorsForServices(agent.ServiceIDs...)

	log.Printf("[HTTP] Deleted agent %s", agentID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

// handleGetSnapshot serves snapshot images from storage
// GET /storage/snapshots/{date}/{filename}
func handleGetSnapshot(w http.ResponseWriter, r *http.Request, relay *Relay) {
	// Parse path: /storage/snapshots/{date}/{filename}
	path := strings.TrimPrefix(r.URL.Path, "/storage/snapshots/")
	parts := strings.SplitN(path, "/", 2)

	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "Invalid path format. Expected: /storage/snapshots/{date}/{filename}", http.StatusBadRequest)
		return
	}

	date := parts[0]
	filename := parts[1]

	// Validate date format (security: prevent directory traversal)
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(date) {
		http.Error(w, "Invalid date format", http.StatusBadRequest)
		return
	}

	// Validate filename (security: alphanumeric + underscores + hyphens + .jpg only)
	if !regexp.MustCompile(`^snapshot_[a-zA-Z0-9\-]+\.jpg$`).MatchString(filename) {
		http.Error(w, "Invalid filename format", http.StatusBadRequest)
		return
	}

	// Check if snapshot manager is available
	if relay.snapshotManager == nil {
		http.Error(w, "Snapshot storage not configured", http.StatusInternalServerError)
		return
	}

	// Construct full path
	relativePath := filepath.Join(date, filename)
	fullPath := relay.snapshotManager.GetSnapshotPath(relativePath)

	// Check file exists
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		http.Error(w, "Snapshot not found", http.StatusNotFound)
		return
	}

	// Serve file with caching headers
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, fullPath)

	log.Printf("[HTTP] Served snapshot: %s", relativePath)
}

// handleUploadSnapshot receives snapshot data from workers and stores it
// POST /cv/upload-snapshot
func handleUploadSnapshot(w http.ResponseWriter, r *http.Request, relay *Relay) {
	// Check if snapshot manager is available
	if relay.snapshotManager == nil {
		http.Error(w, "Snapshot storage not configured", http.StatusInternalServerError)
		return
	}

	// Read image data from request body
	data, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[HTTP] Failed to read snapshot upload body: %v", err)
		if relay.metrics != nil {
			relay.metrics.IncSnapshotFailure()
		}
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	// Validate that we received data
	if len(data) == 0 {
		if relay.metrics != nil {
			relay.metrics.IncSnapshotFailure()
		}
		http.Error(w, "Empty snapshot data", http.StatusBadRequest)
		return
	}

	// Save snapshot via snapshot manager
	relativePath, err := relay.snapshotManager.SaveSnapshot(data)
	if err != nil {
		log.Printf("[HTTP] Failed to save snapshot: %v", err)
		if relay.metrics != nil {
			relay.metrics.IncSnapshotFailure()
		}
		http.Error(w, "Failed to save snapshot", http.StatusInternalServerError)
		return
	}
	if relay.metrics != nil {
		relay.metrics.IncSnapshotSuccess()
	}

	log.Printf("[HTTP] Saved snapshot: %s (%d bytes)", relativePath, len(data))

	// Return relative path for metadata storage
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"snapshot_path": relativePath,
	})
}

// Camera management handlers

// handleListCameras returns all cameras visible in the shared workspace.
func handleListCameras(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	_ = userID
	cameras, err := relay.cameraTable.GetAllCameras()
	if err != nil {
		log.Printf("[HTTP] Failed to list cameras: %v", err)
		http.Error(w, "Failed to fetch cameras", http.StatusInternalServerError)
		return
	}

	if cameras == nil {
		cameras = []*Camera{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cameras)
}

// handleCreateCamera creates a new camera
func handleCreateCamera(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	var req struct {
		Name          string   `json:"name"`
		Type          string   `json:"type"`
		Addr          string   `json:"addr"`
		Port          int      `json:"port"`
		Path          string   `json:"path"`
		ProcessingFPS float64  `json:"processing_fps"`
		VLMFPS        float64  `json:"vlm_fps"`
		AuthUsername  string   `json:"auth_username"`
		AuthPassword  string   `json:"auth_password"`
		ManagedNodeID string   `json:"managed_node_id"`
		NodeIDs       []string `json:"node_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "Camera name is required", http.StatusBadRequest)
		return
	}
	if req.Addr == "" {
		http.Error(w, "Camera address is required", http.StatusBadRequest)
		return
	}
	if req.Port <= 0 || req.Port > 65535 {
		http.Error(w, "Invalid port number", http.StatusBadRequest)
		return
	}
	if req.Type == "" {
		req.Type = "rtsp"
	}

	nodeIDs := normalizeNodeIDList(req.NodeIDs)
	if req.ManagedNodeID != "" {
		nodeIDs = normalizeNodeIDList(append(nodeIDs, req.ManagedNodeID))
	}
	for _, nodeID := range nodeIDs {
		if !nodeExists(relay, nodeID) {
			http.Error(w, "Unknown node_id: "+nodeID, http.StatusBadRequest)
			return
		}
	}

	primaryNodeID := ""
	if len(nodeIDs) > 0 {
		primaryNodeID = nodeIDs[0]
	}

	camera, err := relay.cameraTable.CreateCameraWithNode(
		req.Name, req.Type, req.Addr, req.Port, req.Path,
		req.AuthUsername, req.AuthPassword, userID, primaryNodeID,
	)
	if err != nil {
		log.Printf("[HTTP] Failed to create camera: %v", err)
		http.Error(w, "Failed to create camera", http.StatusInternalServerError)
		return
	}

	if err := relay.cameraTable.SetCameraNodeAssignments(camera.ID, nodeIDs); err != nil {
		_ = relay.cameraTable.DeleteCamera(camera.ID)
		log.Printf("[HTTP] Failed to set camera assignments: %v", err)
		http.Error(w, "Failed to set camera assignments", http.StatusInternalServerError)
		return
	}

	if req.ProcessingFPS > 0 || req.VLMFPS > 0 {
		if req.ProcessingFPS <= 0 {
			req.ProcessingFPS = camera.ProcessingFPS
		}
		if req.VLMFPS <= 0 {
			req.VLMFPS = camera.VLMFPS
		}
		if updated, err := relay.cameraTable.UpdateCameraFPS(camera.ID, req.ProcessingFPS, req.VLMFPS); err == nil {
			camera = updated
		} else {
			_ = relay.cameraTable.DeleteCamera(camera.ID)
			http.Error(w, "Invalid camera FPS settings: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	camera, err = relay.cameraTable.GetCameraByID(camera.ID)
	if err != nil {
		log.Printf("[HTTP] Failed to reload camera %s: %v", camera.ID, err)
		http.Error(w, "Failed to reload camera", http.StatusInternalServerError)
		return
	}

	var streamNodeID string
	svc := relay.cameraToService(camera)
	for _, nodeID := range camera.NodeIDs {
		if relay.GetNode(nodeID) != nil {
			relay.services.Register(svc, nodeID)
			log.Printf("[HTTP] Registered camera %s as service under node %s", camera.ID, nodeID)
			if streamNodeID == "" {
				streamNodeID = nodeID
			}
		}
	}

	if streamNodeID != "" && relay.config.AutoRequestRealtimeStream && relay.realtimeStreamManager != nil && (camera.Type == "rtsp" || camera.Type == "mjpeg") {
		go relay.realtimeStreamManager.OnServiceAnnounced(svc, streamNodeID)
	}

	log.Printf("[HTTP] Camera created: %s (%s:%d%s)", camera.Name, camera.Addr, camera.Port, camera.Path)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"camera":  camera,
	})
}

// handleUpdateCamera handles PATCH /cameras/{cameraID}
func handleUpdateCamera(w http.ResponseWriter, r *http.Request, relay *Relay, cameraID string) {
	var req struct {
		Name          *string   `json:"name"`
		Addr          *string   `json:"addr"`
		Port          *int      `json:"port"`
		Path          *string   `json:"path"`
		ProcessingFPS *float64  `json:"processing_fps"`
		VLMFPS        *float64  `json:"vlm_fps"`
		AuthUsername  *string   `json:"auth_username"`
		AuthPassword  *string   `json:"auth_password"`
		NodeIDs       *[]string `json:"node_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	existing, err := relay.cameraTable.GetCameraByID(cameraID)
	if err != nil {
		http.Error(w, "camera not found", http.StatusNotFound)
		return
	}
	prevProcessingFPS := existing.ProcessingFPS

	name := existing.Name
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
	}
	addr := existing.Addr
	if req.Addr != nil {
		addr = strings.TrimSpace(*req.Addr)
	}
	port := existing.Port
	if req.Port != nil {
		port = *req.Port
	}
	path := existing.Path
	if req.Path != nil {
		path = *req.Path
	}
	authUsername := existing.AuthUsername
	if req.AuthUsername != nil {
		authUsername = *req.AuthUsername
	}
	authPassword := existing.AuthPassword
	if req.AuthPassword != nil {
		authPassword = *req.AuthPassword
	}

	if name == "" || addr == "" || port <= 0 {
		http.Error(w, "name, addr and port must be valid", http.StatusBadRequest)
		return
	}

	camera, err := relay.cameraTable.UpdateCamera(cameraID, name, addr, port, path, authUsername, authPassword)
	if err != nil {
		log.Printf("[HTTP] Failed to update camera %s: %v", cameraID, err)
		http.Error(w, "Failed to update camera", http.StatusInternalServerError)
		return
	}

	if req.NodeIDs != nil {
		nodeIDs := normalizeNodeIDList(*req.NodeIDs)
		for _, nodeID := range nodeIDs {
			if !nodeExists(relay, nodeID) {
				http.Error(w, "Unknown node_id: "+nodeID, http.StatusBadRequest)
				return
			}
		}
		if err := relay.cameraTable.SetCameraNodeAssignments(cameraID, nodeIDs); err != nil {
			log.Printf("[HTTP] Failed to update camera assignments %s: %v", cameraID, err)
			http.Error(w, "Failed to update camera assignments", http.StatusInternalServerError)
			return
		}
		camera, err = relay.cameraTable.GetCameraByID(cameraID)
		if err != nil {
			log.Printf("[HTTP] Failed to reload camera %s after assignment update: %v", cameraID, err)
			http.Error(w, "Failed to reload camera", http.StatusInternalServerError)
			return
		}
	}

	if req.ProcessingFPS != nil || req.VLMFPS != nil {
		pfps := camera.ProcessingFPS
		vfps := camera.VLMFPS
		if req.ProcessingFPS != nil && *req.ProcessingFPS > 0 {
			pfps = *req.ProcessingFPS
		}
		if req.VLMFPS != nil && *req.VLMFPS > 0 {
			vfps = *req.VLMFPS
		}
		if updated, err := relay.cameraTable.UpdateCameraFPS(cameraID, pfps, vfps); err == nil {
			camera = updated
		} else {
			http.Error(w, "Invalid camera FPS settings: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	// Refresh all service registry entries for this camera.
	relay.services.Remove(cameraID)
	svc := relay.cameraToService(camera)
	streamNodeID := ""
	for _, nodeID := range camera.NodeIDs {
		if relay.GetNode(nodeID) != nil {
			relay.services.Register(svc, nodeID)
			if streamNodeID == "" {
				streamNodeID = nodeID
			}
		}
	}

	if streamNodeID != "" && relay.config.AutoRequestRealtimeStream && relay.realtimeStreamManager != nil && (camera.Type == "rtsp" || camera.Type == "mjpeg") {
		go relay.realtimeStreamManager.OnServiceAnnounced(svc, streamNodeID)
	}

	// Apply FPS change to the running frame extractor.
	// If FPS is increased, restart the stream so FFmpeg can raise decode rate.
	if (req.ProcessingFPS != nil || req.VLMFPS != nil) && relay.realtimeStreamManager != nil {
		fpsIncrease := req.ProcessingFPS != nil && camera.ProcessingFPS > prevProcessingFPS+0.001
		if fpsIncrease && streamNodeID != "" && relay.config.AutoRequestRealtimeStream && (camera.Type == "rtsp" || camera.Type == "mjpeg") {
			relay.realtimeStreamManager.OnServiceRemoved(cameraID)
			go relay.realtimeStreamManager.OnServiceAnnounced(svc, streamNodeID)
			log.Printf("[HTTP] Restarted stream for camera %s to apply increased processing_fps %.2f -> %.2f", cameraID, prevProcessingFPS, camera.ProcessingFPS)
		} else if stream, err := relay.realtimeStreamManager.GetStream(cameraID); err == nil {
			if extractor := stream.GetFrameExtractor(); extractor != nil {
				newInterval := time.Duration(float64(time.Second) / camera.ProcessingFPS)
				extractor.SetInterval(newInterval)
				log.Printf("[HTTP] Applied processing_fps=%.2f to running extractor for camera %s", camera.ProcessingFPS, cameraID)
			}
		}
	}

	log.Printf("[HTTP] Updated camera %s (%s:%d%s)", camera.Name, camera.Addr, camera.Port, camera.Path)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"camera":  camera,
	})
}

func normalizeNodeIDList(nodeIDs []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		nodeID = strings.TrimSpace(nodeID)
		if nodeID == "" {
			continue
		}
		if _, exists := seen[nodeID]; exists {
			continue
		}
		seen[nodeID] = struct{}{}
		out = append(out, nodeID)
	}
	return out
}

func nodeExists(relay *Relay, nodeID string) bool {
	if nodeID == "" {
		return false
	}
	if _, err := relay.managedNodeTable.GetManagedNode(nodeID); err == nil {
		return true
	}
	if _, err := relay.nodeTable.GetNodeByID(nodeID); err == nil {
		return true
	}
	return false
}

// handleDeleteCamera deletes a camera
func handleDeleteCamera(w http.ResponseWriter, r *http.Request, relay *Relay, cameraID string) {
	// Remove from ServiceRegistry
	relay.services.Remove(cameraID)

	// Remove from database
	if err := relay.cameraTable.DeleteCamera(cameraID); err != nil {
		log.Printf("[HTTP] Failed to delete camera: %v", err)
		http.Error(w, "Failed to delete camera", http.StatusInternalServerError)
		return
	}

	log.Printf("[HTTP] Deleted camera %s", cameraID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}
