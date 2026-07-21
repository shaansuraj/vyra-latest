# VYRA Architecture Reference

> AI-powered real-time video surveillance platform for workplace safety monitoring.

---

## Table of Contents

1. [System Overview](#1-system-overview)
2. [Component Architecture](#2-component-architecture)
3. [AI Models & Algorithms](#3-ai-models--algorithms)
4. [Data Flow Pipeline](#4-data-flow-pipeline)
5. [WebRTC Video Streaming](#5-webrtc-video-streaming)
6. [Database Schema](#6-database-schema)
7. [REST API Reference](#7-rest-api-reference)
8. [WebSocket Protocol Reference](#8-websocket-protocol-reference)
9. [Frontend Architecture](#9-frontend-architecture)
10. [Configuration Reference](#10-configuration-reference)
11. [Key Files Reference](#11-key-files-reference)

---

## 1. System Overview

VYRA is a multi-camera AI surveillance platform designed for workplace safety. It processes live video feeds from IP cameras, runs real-time AI inference for PPE compliance, person detection, face recognition, and situational awareness, then delivers results to a browser-based dashboard with live bounding box overlays, event history, analytics, and alerts.

### 1.1 Architecture Tiers

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                              BROWSER                                         │
│  Solid.js SPA + Tailwind CSS v4                                              │
│  WebRTC video  │  JSON WebSocket events  │  REST API                         │
└──────────┬─────────────┬──────────────────┬──────────────────────────────────┘
           │             │                  │
           │ WebRTC      │ WS (JSON)        │ HTTP
           │ SDP/ICE     │ events/overlays  │ CRUD/auth
           ▼             ▼                  ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                          RELAY (Go)                                          │
│  Port 8020: HTTP API (auth, events, cameras, persons, system)                │
│  Port 9020: WebSocket hub (nodes, workers, frame serving)                    │
│  SQLite (WAL mode) │ go2rtc │ ProcessManager │ ServiceRegistry               │
└───────────┬────────────────────┬─────────────────────────────────────────────┘
            │ WS (JSON)          │ HTTP (frame download)
            │ frame_batch/event  │ /worker/frames/{uuid}
            ▼                    ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                        ENGINE (Bun/TypeScript)                               │
│  Port 5000: CBOR-X WebSocket distributor                                     │
│  Job batching │ Worker routing │ Timeout recovery                            │
│                                                                              │
│  ADAPTERS (4 processes, same Bun codebase):                                  │
│    adapter-ppe │ adapter-objdet │ adapter-vlm │ adapter-face                 │
│    Queue management │ Backpressure │ In-flight tracking │ Snapshot upload    │
└───────────┬──────────────────────────────────────────────────────────────────┘
            │ WS (CBOR binary)
            │ worker_request / worker_output
            ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                     PYTHON AI WORKERS                                        │
│  worker_ppe.py          │ YOLOv8m PPE (14 classes) + ByteTrack + VLM         │
│  worker_object_detection│ YOLOv8m COCO (80 classes) + ByteTrack + Face       │
│  worker_vlm.py          │ SmolVLM2-2.2B-Instruct situational analysis        │
│  worker_face_recognition│ InsightFace buffalo_sc/buffalo_l                   │
│  worker_query_llm.py    │ Gemma-3 natural language queries                   │
└──────────────────────────────────────────────────────────────────────────────┘
```

### 1.2 Communication Protocols

|       Connection       |  Protocol |    Encoding   |            Purpose            |
|------------------------|-----------|---------------|-------------------------------|
| Browser ↔ Relay API    | HTTP/1.1  | JSON          | Auth, CRUD, events REST       |
| Browser ↔ Relay Events | WebSocket | JSON          | Live events, bbox overlays    |
| Browser ↔ Relay Video  | WebRTC    | H264 RTP      | Live camera streams           |
| Node ↔ Relay           | WebSocket | JSON + binary | Camera tunnel, bridge control |
| Relay ↔ Adapter        | WebSocket | JSON          | Frame batches, worker events  |
| Adapter ↔ Engine       | WebSocket | CBOR-X binary | Job requests, responses       |
| Engine ↔ Worker        | WebSocket | CBOR2 binary  | Job input/output batches      |
| Adapter → Relay        | HTTP POST | multipart     | Snapshot upload               |
| Adapter → Relay        | HTTP GET  | binary JPEG   | Frame download                |

### 1.3 Technology Stack

|          Layer         |         Technology        |      Version     |
|------------------------|---------------------------|------------------|
| Frontend               | Solid.js                  | 1.9.9            |
| Frontend build         | Vite                      | 7.1.4            |
| Frontend CSS           | Tailwind CSS              | 4.1.13           |     
| Frontend charts        | Chart.js                  | 4.5.1            |
| Frontend UI kit        | Ark UI (Solid)            | 5.26.2           |
| Frontend icons         | solid-icons (Feather)     | 1.1.0            |
| Relay server           | Go                        | 1.24.0           |
| Relay database         | SQLite (Turso driver)     | WAL mode         |
| Relay WebRTC           | go2rtc                    | 1.9.13           |
| Relay WebRTC           | Pion WebRTC               | 4.2.1            |
| Relay WebSocket        | gorilla/websocket         | 1.5.3            |
| Engine runtime         | Bun                       | 1.2.22           |
| Engine serialization   | cbor-x                    | 1.6.0            |
| Engine logging         | pino                      | 10.1.0           |
| Worker runtime         | Python                    | 3.12             |
| Worker package manager | uv                        | latest           |
| Object detection       | ultralytics (YOLOv8)      | 8.1+             |
| Person tracking        | supervision (ByteTrack)   | 0.25+            |
| VLM inference          | transformers (local fork) | HEAD (SAM3)      |
| VLM model              | SmolVLM2-2.2B-Instruct    | —                |
| Face recognition       | InsightFace               | latest           |
| ONNX runtime           | onnxruntime-gpu           | 1.20+            |
| Deep learning          | PyTorch                   | 2.7+ (CUDA 12.8) |
| LLM (optional)         | Gemma-3-1b-it             | —                |
| Production frontend    | Nginx                     | 1.27 (Alpine)    |

---

## 2. Component Architecture

### 2.1 Relay (Go)

The relay is the central orchestration hub. It manages everything: node connections, camera service registry, video stream lifecycle, frame extraction for AI, worker pool routing, event persistence, WebRTC session management, process spawning, and browser client WebSocket delivery.

**Entry point:** `application/relay/cmd/relay/main.go`

**Core struct: `Relay`**

```
Relay
├── nodes map[nodeID]*NodeConn          // Connected edge nodes (cameras)
├── services *ServiceRegistry           // In-memory camera registry
├── db *Database                        // SQLite (WAL, 4 conns, 32MB cache)
│
├── Tables
│   ├── nodeTable *NodeTable
│   ├── agentTable *AgentTable
│   ├── cameraTable *CameraTable
│   ├── personTable *PersonTable
│   ├── managedNodeTable *ManagedNodeTable
│   └── dailySummaryTable *DailySummaryTable
│
├── CV Subsystem
│   ├── cvEventBus *CVEventBus              // Pub/sub for frames + worker events
│   ├── cvWorkerRegistry *CVWorkerRegistry  // Worker pool + round-robin routing
│   ├── storageManager *StorageManager      // In-memory JPEG frame store
│   ├── snapshotManager *SnapshotManager    // Disk-based snapshot persistence
│   └── realtimeStreamManager               // go2rtc WebRTC stream lifecycle
│
├── clientEvents *ClientEventRegistry   // Browser WebSocket connections
├── processManager *ProcessManager      // OS child process spawner
├── agentRegistry *AgentRegistry        // In-memory agent cache
│
├── faceIdentityService                 // Server-side face identity cache
├── personIdentityService               // Cross-camera person identity
└── metrics *RuntimeMetrics
```

**Key subsystems:**

- **Service Registry**: In-memory map of `serviceID → {name, type, addr, port, path, nodeID}`. Populated from DB cameras on node connect. Used for frame routing and WebRTC session creation.

- **CV Event Bus**: Pub/sub with 3 event channels — `FrameEvent` (single frame), `FrameBatchEvent` (batch for workers), `WorkerEvent` (results from Python). All listeners invoked in goroutines. Worker events stored in a 1000-entry ring buffer.

- **Worker Registry**: Manages Python worker WebSocket connections. Workers connect to port 9020 at `/worker/connect`, register with a `worker_id` (e.g., `"vyra/ppe_detection"`). Multiple workers with the same ID form a pool with lock-free round-robin routing (`atomic.Uint64` counter). Each worker has a 512-capacity buffered send channel with adaptive drop logging.

- **Storage Manager**: In-memory JPEG frame store (no disk I/O). Frames registered with UUID, served via HTTP `/worker/frames/{uuid}` with `X-Worker-Key` authentication. `ReadsRemaining` counter (default 6) decremented on each fetch; frame evicted at zero. TTL cleanup every 10s removes frames older than 20s.

- **Process Manager**: Spawns engine, adapters, workers, and nodes as OS child processes. Controlled by `VYRA_ROOT` env var (empty = disabled, used in Docker mode). Supports per-type scaling (`ScaleWorkers(type, replicas)`) with auto-start of dependencies.

**Background goroutines:**
- `autoScaleLoop()` — every 12s: self-heals crashed workers, optional autoscale based on queue pressure (≥80%) and drop count changes
- `summaryLoop()` — periodic daily summary aggregation
- Stream lifecycle goroutines — one per active video stream, manages CVFrameExtractor lifetime

### 2.2 Frontend (Solid.js)

Single-page application with 6 navigation tabs, a collapsible sidebar, and real-time WebSocket event delivery.

**Entry:** `application/app/src/App.tsx` — path-based router (login/register/authorize/main)

**Shared state** (`shared.tsx`):
- `authState` — JWT token management, login/register/logout
- `configState` — relay feature flags (DEV_IMPERSONATE)
- `nodes()` — connected node list (auto-polled)
- `agents()` — agent configurations
- `systemStatus()` — process states (auto-polled every 4s)
- `managedNodes()` — managed node records
- `relay(path)` — URL builder using `VITE_RELAY_API_URL`
- `relayFetch(path, options)` — authenticated fetch wrapper (JWT in Authorization header)
- `subscribeToAgentEvents(callback)` — WebSocket subscription for live events
- `subscribeToAgentOverlayEvents(callback)` — WebSocket subscription for bbox-only overlays

**Navigation (SideBar.tsx):**

| Tab Type | Label | Icon | Purpose |
|---|---|---|---|
| `command` | Command Center | FiActivity | Live multi-camera grid (BirdEye) |
| `investigate` | Investigate | FiSearch | Event history (Moments) + Search |
| `performance` | Performance | FiBarChart2 | Analytics dashboards |
| `control` | Control | FiCpu | Infrastructure (Home) + AI agents |
| `identity` | Identity | FiUsers | Person registry + face enrollment |
| `system` | System | FiSettings | Platform settings |

**Design system:**
- CSS custom properties in `styles/tokens.css`
- Component classes in `styles/components.css`: `.btn`, `.btn-primary` (violet), `.input`, `.select`, `.modal`, `.badge`, `.badge-violation`, `.data-table`, `.chart-card`, `.kpi-card-premium`, `.severity-*`, etc.
- Animations in `styles/motion.css`: `.fade-in`, `.ambient-float`, scroll-reveal, CountUpNumber
- Ark UI component styles in `ark/ark.css`

### 2.3 Engine (Bun/TypeScript)

WebSocket job distributor on port 5000. Routes jobs from adapters to Python workers using CBOR-X binary serialization.

**Entry:** `engine/index.ts`

**Job routing algorithm (`sendJob`):**
1. Find first connected worker with matching `worker_type`
2. Push job to worker's `gathered` array (capped at 8)
3. If `gathered.length >= max_batch_size`: flush immediately
4. Otherwise: set timeout for `max_latency_ms` then flush
5. Flush = CBOR-encode `{inputs: [...jobs]}` and send

**Job tracking:**
- `job_map: Map<jobID, {cont, timeout}>` — pending jobs with 5-minute timeout
- Continuation callback invoked when worker responds or on timeout

**HTTP endpoints:**
- `POST /api/autocomplete` — routes to `llm_fast` worker for search query refinement
- `POST /api/worker/fast_embedding` — routes to `fast_embedding` worker
- `GET /version` — returns `{version: "1.0.1"}`

### 2.4 Adapters (Bun/TypeScript)

One adapter process per worker type. Bridges relay JSON WebSocket to engine CBOR WebSocket. Handles all job lifecycle management.

**Entry:** `engine/src/relay_worker_adapter.ts`

**4 adapter types:**

| Adapter | WORKER_ID | ENGINE_WORKER_TYPE | MAX_IN_FLIGHT | MAX_QUEUE_SIZE |
|---|---|---|---|---|
| adapter-ppe | vyra/ppe_detection | ppe_detection | 4-8 | 64 |
| adapter-objdet | vyra/person_detection | object_detection | 4-8 | 64 |
| adapter-vlm | vyra/vlm | vlm | 4 | 48 |
| adapter-face | vyra/face_recognition | face_recognition | 4 | 32 |

**Key mechanisms:**

- **Adaptive per-camera throttle**: `perCameraIntervalMs` derived from camera's `processing_fps` / `vlm_fps`. Adds `adaptivePenalty = min(250, queued*8 + inFlight*12)` based on queue pressure.

- **Backpressure**: Bbox workers skip frames when `inFlight >= MAX_IN_FLIGHT`. VLM workers skip only when both in-flight AND queue are full.

- **In-flight timeout watchdog**: Runs every 1s. Jobs exceeding `IN_FLIGHT_TIMEOUT_MS` are recovered — retried up to `MAX_JOB_RETRIES` times, then dropped.

- **Worker unavailable backoff**: If engine reports "No worker available", adapter backs off for `WORKER_UNAVAILABLE_BACKOFF_MS` (2s) before submitting new jobs.

- **Frame fetch**: HTTP GET `/worker/frames/{uuid}` from relay with `X-Worker-Key` header. Frames are raw JPEG bytes.

- **Snapshot upload**: HTTP POST `/cv/upload-snapshot` with JPEG binary body. Returns `{snapshot_path: "YYYY-MM-DD/snapshot_uuid.jpg"}`.

- **Result handling (bbox workers)**: `toBBoxes()` parses detection arrays, normalizes coordinates, extracts extras. `parseObjectDetectionBatch()` handles 3 output shapes. Builds event JSON with metadata (bboxes, severity, violation counts, person names, queue pressure).

- **Result handling (VLM)**: `parseVlmOutput()` extracts summary/brief/severity from worker response. Handles severity-based snapshot creation using cached frame bytes from in-flight payload.

### 2.5 Python AI Workers

All workers share the same WebSocket transport layer (`ws_client_handler.py`):
- Connect to engine at `BACKEND_WS_URL` via WebSocket
- Register with `{type: "i_am_worker", worker_config: {worker_type, max_latency_ms, max_batch_size}}`
- Receive CBOR-encoded job batches `{inputs: [...]}`
- Send CBOR-encoded results `{type: "worker_output", output: [...]}`
- AI workload runs in `ThreadPoolExecutor` thread (keeps event loop free)
- Reconnect with exponential backoff (1s → 60s) + jitter

See [Section 3](#3-ai-models--algorithms) for detailed per-worker documentation.

### 2.6 Node (Go)

Edge process that connects cameras to the relay. Runs on-site, tunnels camera streams via WebSocket.

**Entry:** `application/node/cmd/unblink/main.go`

**Config:** HuJSON format at `~/.unblink/config.hujson` — contains `relay_address`, `node_id`, `token`, `services[]`, `reconnect` settings.

**Camera tunnel flow:**
1. Node connects to relay WebSocket with auth token
2. Announces services (cameras) to relay
3. Relay registers services in ServiceRegistry
4. When browser requests a stream: relay sends `OPEN_BRIDGE` to node
5. Node opens TCP connection to camera (RTSP/MJPEG)
6. Node tunnels camera data through WebSocket binary frames
7. Relay creates `BridgeTCPProxy` — local TCP port proxying to the tunneled data
8. go2rtc RTSP client connects to the local proxy port

**Managed node flow (relay-spawned):**
- Relay calls `ProcessManager.StartNode(id, cameras, token)`
- Relay generates config JSON at `{APP_DIR}/nodes/{nodeID}/config.json` with pre-authorized token
- Node binary spawned with this config — connects immediately, no browser auth needed

---

## 3. AI Models & Algorithms

### 3.1 PPE Detection (`engine/py/worker_ppe.py`)

**Model:** Hexmon/vyra-yolo-ppe-detection — a YOLOv8m model fine-tuned on 14 PPE-related classes.

**14 Classes:**

| ID | Class | Type |
|---|---|---|
| 0 | Fall-Detected | Violation |
| 1 | Gloves | Compliant PPE |
| 2 | Goggles | Compliant PPE |
| 3 | Hardhat | Compliant PPE |
| 4 | Ladder | Context |
| 5 | Mask | Compliant PPE |
| 6 | NO-Gloves | Violation |
| 7 | NO-Goggles | Violation |
| 8 | NO-Hardhat | Violation |
| 9 | NO-Mask | Violation |
| 10 | NO-Safety Vest | Violation |
| 11 | Person | Person |
| 12 | Safety Cone | Context |
| 13 | Safety Vest | Compliant PPE |

**Inference parameters:** `conf=0.35`, `iou=0.45`, `max_det=300` (all ENV-overridable via `PPE_CONF`, `PPE_IOU`, `PPE_MAX_DET`).

**Full pipeline (per frame batch):**

```
Stage 1: YOLO Inference
  ├── Parse inputs (flat or nested job structure)
  ├── Open frames with PIL, record dimensions
  └── model.predict(batch_paths, verbose=False) → raw detections

Stage 2: Post-Processing
  ├── Split into person_dets (class 11) vs other_dets
  ├── ByteTrack on persons only → persistent track_id per person
  ├── Identity resolution (face rec cache or relay HTTP cache)
  ├── First-seen detection → async face lookup + person identity resolve
  ├── PPE-to-person association algorithm
  ├── Violation aggregation per person
  ├── Severity determination
  ├── Event brief generation (narrative string)
  └── Snapshot drawing with bboxes

Stage 3: VLM Integration
  ├── Poll background VLM cache for results
  ├── First-detection + violations → immediate snapshot + VLM queue
  ├── VLM gating (should_alert per track_id)
  ├── Snapshot saving (always on gated violations)
  └── VLM prompt construction → non-blocking queue
```

**PPE-to-person association algorithm:**
1. Expand each person bounding box by 25% on all sides
2. For each non-person detection: test if center point falls within any expanded person box
3. If multiple persons qualify: assign to nearest by L1 distance between centers
4. Returns `{ppe_index → person_index}` mapping
5. PPE items inherit `track_id` from associated person
6. Unassociated items get `track_id = -1`

**Severity determination:**
- `fall_detected` → `"high"`
- PPE violations exist → `"high"` if any person has ≥3 violations, else `"medium"`
- No violations → `"low"`
- VLM can upgrade severity when cached result contains `SEVERITY:HIGH/MEDIUM` markers

**Event brief generation (examples):**
- First detection + violations: `"Abhishek seen on Camera-1 at 14:30 without Hardhat, Safety Vest"`
- Fall: `"Unknown #5 fall detected on Camera-2 at 14:31"`
- Ongoing: `"PPE violation on Camera-1 at 14:32: NO-Hardhat"`

**Background VLM (`_BgVLM`):**
- Daemon thread consuming from bounded `Queue(maxsize=8)`
- `request(key, path, prompt)` — non-blocking put, drops silently if full
- `get_result(key)` — pops from cache dict (thread-safe with lock)
- Key format: `"{media_id}:{track_id}:{sorted_violations}"`
- YOLO runs at full speed, VLM results arrive asynchronously

**Snapshot drawing:**
- Red rectangles `(255, 60, 60)` around violation detections and violation person boxes
- Label format: `"#{track_id} {label} {conf}%"` above box
- Camera name drawn at bottom with dark background strip
- Falls back to raw file copy on draw error

### 3.2 Object Detection (`engine/py/worker_object_detection.py`)

**Model:** YOLOv8m with COCO 80-class pretrained weights.

**Inference parameters:** `conf=0.30`, `iou=0.45`, `max_det=1000`.

**Pipeline:**
```
YOLO predict → ByteTrack (all detections) → Person filtering
  → GlobalPersonIdentityManager (cross-camera identity)
  → FaceTrackEvidenceManager (per-track face state machine)
  → Full-frame face detection + face-person association
  → Async dual face recognition (person crop + associated face box)
  → Visit detection → VLM queue → Snapshot
```

**GlobalPersonIdentityManager:**
- In-process cross-camera identity memory
- `_track_bindings: {(media_id, track_id) → {global_person_id, person_id, person_name, ...}}`
- `_unknown_gallery: {global_id → [embeddings]}` — cosine similarity matching (threshold 0.58)
- Track bindings stale after 180s, global IDs after 1800s
- `mark_camera_visit()` returns True if ≥5s gap since last visit

**FaceTrackEvidenceManager:**
- Per-track face matching state machine with retry logic
- Bootstrap phase: first 3 attempts at 0.25s intervals
- Retry phase: every 0.6s, max 8 attempts total
- Evidence window: size 5, promotes weak → confirmed after 3 same-candidate hits
- Strong match → immediately finalized
- Unknown → finalized after max retries

**Face-to-person association:**
- Full-frame face detection via InsightFace
- Greedy assignment: face center inside person box OR ≥35% area overlap
- Scoring: `0.52×coverage + 0.24×face_conf + 0.14×vertical_score + 0.10×center_score`
- Vertical score rewards faces near upper 22% of person height (head position prior)

**Custom YOLO mode:**
- Triggered by `custom_yolo_enabled=true` or `custom_model_path` in job data
- Loads custom `.pt` models via LRU cache (max 2 entries)
- Same predict pipeline, no person-only filtering
- Optional VLM + face rec via job flags

### 3.3 VLM / Situational Awareness (`engine/py/worker_vlm.py`)

**Model:** HuggingFaceTB/SmolVLM2-2.2B-Instruct

**Loading:**
```python
processor = AutoProcessor.from_pretrained(model_id)
model = AutoModelForImageTextToText.from_pretrained(model_id, torch_dtype=float16)
processor.image_processor.size = {"longest_edge": 600}  # Performance optimization
```

**Inference pipeline:**
1. Load PIL images from file paths in input messages
2. `processor.apply_chat_template(conversations, add_generation_prompt=True)`
3. `processor(text=texts, images=images, return_tensors="pt", padding=True)`
4. `model.generate(**inputs, max_new_tokens=MAX_NEW_TOKENS)` under `torch.inference_mode()`
5. Decode only new tokens (strip input prompt)
6. `processor.batch_decode(new_tokens, skip_special_tokens=True)`

**Severity classification:**
1. Primary: look for explicit `SEVERITY:HIGH/MEDIUM/LOW` markers (case-insensitive)
2. Fallback keyword matching:
   - **High** (30+ keywords): danger, emergency, critical, injury, accident, fire, smoke, explosion, bleeding, unconscious, fight, weapon, attack, violence, intruder, toxic, electrocution, collapsed, etc.
   - **Medium** (10+ keywords): suspicious, unauthorized, restricted area, blocked exit, wet floor, hazard, overloaded, unstable, horseplay, etc.
3. Severity markers stripped from response text for clean display

**VLM is used in 3 contexts:**
1. Standalone VLM worker — processes frames from adapter-vlm (situational awareness)
2. Background VLM in PPE worker — enriches violation events asynchronously
3. Background VLM in object detection worker — enriches person visit events
4. Relay-triggered VLM — PPE/person events with snapshots trigger `maybeVLMTrigger()` (30s cooldown per service+context)

### 3.4 Face Recognition (`engine/py/face_recognizer.py`)

**Models:**
- Primary: InsightFace `buffalo_sc` (16MB, fast) — default `FACE_PRIMARY_MODEL`
- Secondary: InsightFace `buffalo_l` (326MB, accurate) — default `FACE_SECONDARY_MODEL`
- Both use `det_size=(640, 640)`, CUDAExecutionProvider if available

**Multi-profile enrollment:**
For each registered person's profile image, generate 8 geometric views:
1. Original
2. Horizontally flipped
3. +8° rotation
4. -8° rotation
5. +15° rotation
6. -15° rotation
7. Brightness ×1.08
8. Brightness ×0.92

Each view processed by InsightFace to extract `normed_embedding` (512-dim float vector). Quality weight computed: `det_score × (min_side/120) × (blur_variance/120)`, clipped to [0.2, 2.5].

**Registry refresh:**
- Every 60s: `GET /persons/registry` → for each person → `GET /persons/{id}/faces` → download profiles → extract embeddings
- Only includes persons with `enrollment_ready=true` (≥3 profiles) in V2 mode
- Builds `primary_gallery`, `secondary_gallery`, and per-person **prototypes** (weighted average of all embeddings, L2-normalized)

**Composite scoring:**
```
score = 0.65 × max_similarity + 0.25 × top_k_mean + 0.10 × prototype_similarity
```
Where `max_similarity` = highest cosine similarity to any embedding, `top_k_mean` = mean of top-3 similarities, `prototype_similarity` = cosine to person's prototype vector.

**Confidence tiers:**
- **Strong**: composite ≥ 0.52 AND margin from 2nd place ≥ 0.035
- **Weak**: composite ≥ 0.42
- **Unknown**: below weak threshold

**V2 adaptive secondary stage:**
When primary result is NOT strong:
1. Extract embedding using secondary model (buffalo_l)
2. Score against secondary gallery
3. If secondary is strong → use secondary result
4. If both agree on same candidate → average scores, promote if avg ≥ weak_threshold + 0.015

**Two recognition paths:**
1. **Embedded**: `face_recognizer.py` singleton, async thread pool (`ThreadPoolExecutor(max_workers=2)`). Used inside PPE and object detection workers when `ENABLE_FACE_RECOGNITION=1`.
2. **Relay-delegated**: fire-and-forget POST to relay `/cv/face-lookup`, results fetched per-frame via TTL-cached GET `/cv/face-identities`. Used when embedded is disabled.

**Appearance embedding for unknown re-ID:**
- Crop person bounding box, resize to 64×128
- 16-bin RGB histogram per channel (3 channels × 16 bins = 48-float vector)
- Used by relay-side PersonIdentityService for cross-camera unknown matching

### 3.5 Standalone Face Worker (`engine/py/worker_face_recognition.py`)

Simpler standalone worker (separate from embedded face rec):
- Loads `buffalo_sc` inline (not via singleton)
- Full-frame face detection → per-face embedding extraction → gallery matching
- 4 geometric views per profile (original, flip, ±8° rotation)
- Simple max cosine similarity scoring (threshold 0.45)
- ByteTrack per stream with identity stickiness
- Snapshot: green boxes for matched, yellow for unknown
- Severity: `"medium"` if matched or first_detection

### 3.6 ByteTrack Person Tracking (`engine/py/tracker.py`)

**Library:** `supervision.ByteTrack`

**Parameters:**
```python
ByteTrack(
    track_activation_threshold=0.25,
    lost_track_buffer=30,          # frames before track is lost (ENV: TRACKER_LOST_BUFFER)
    minimum_matching_threshold=0.8,
    frame_rate=frame_rate,         # ENV: TRACKER_FRAME_RATE, default 10
)
```

**StreamTracker** wraps ByteTrack with additional state:

- **`update(detections)`**: Builds NumPy arrays (xyxy, confidence, class_id), creates `sv.Detections`, calls `tracker.update_with_detections()`. Matches tracked results back to originals by minimum L1 box distance (10px tolerance). Unmatched get `track_id=-1`.

- **VLM gating (`should_alert(track_id, status, cooldown_seconds)`)**:
  - First time seen → always alert
  - State change (different status string) → alert
  - Cooldown elapsed (default 15s) → alert
  - Otherwise → skip (duplicate)

- **First-seen tracking**: `is_first_seen(tid)` / `mark_seen(tid)` — simple set membership

- **Identity cache**: `{track_id → Optional[Dict]}` — `None` = not attempted, `{}` = no match, `{person_id, person_name, match_score}` = matched

**TrackerManager**: Class-level registry of `{stream_id → StreamTracker}`, max 50 instances. Creates on first access, evicts oldest by `last_update` at capacity.

### 3.7 LLM Query Worker (`engine/py/worker_query_llm.py`)

**Model:** Gemma-3-1b-it (configurable via `LLM_MODEL_ID`)

**Purpose:** Natural language question answering about events and camera activity.

**API:** FastAPI server on port 5003:
- `POST /query` — `{query, context}` → model generates response
- `GET /health` — health check

**Not used for real-time detection** — only for interactive "Ask AI" feature in the frontend.

---

## 4. Data Flow Pipeline

### 4.1 Frame Extraction

```
Camera (RTSP/MJPEG/H265)
    │
    │ TCP tunnel via NodeClient WebSocket
    ▼
Node (Go binary)
    │
    │ BridgeTCPProxy (local TCP port)
    ▼
RealtimeStreamManager
    │
    │ go2rtc RTSP client / MJPEG source
    ▼
CVFrameExtractor
    │
    │ FFmpeg subprocess: -f image2pipe -vf fps=1/{interval} -q:v 3
    │ H264: RTP → FFmpeg stdin via WriteRTP
    │ H265: RTP → FFmpeg stdin via WriteRTP (-f hevc input)
    │ Codec auto-detected from producer.GetMedias()
    ▼
StorageManager.RegisterFrameData(uuid, serviceID, jpegBytes, timestamp)
    │
    │ In-memory map[uuid]*FrameMetadata
    │ ReadsRemaining=6, TTL=20s cleanup every 10s
    ▼
CVEventBus.EmitFrameBatchEvent({serviceID, frames[], metadata})
    │
    │ metadata includes: service_name, processing_fps, vlm_fps
    │ Batch size: CV_FRAME_BATCH_SIZE (default 1)
    ▼
CVWorkerRegistry.BroadcastFrameBatchEvent()
```

### 4.2 Worker Routing

```
CVWorkerRegistry receives FrameBatchEvent
    │
    │ getTargetWorkers(serviceID):
    │   1. agentRegistry.GetAgentsForService(serviceID) → agent list
    │   2. groupAgentsByWorker(agents) → map[workerID][]agents
    │   3. For each workerID:
    │      a. Look up pool (with compat aliases: objdet↔person, face→objdet)
    │      b. Round-robin: pool.next.Add(1) % len(workers)
    │   4. Return one target per distinct workerID
    │
    │ For each target:
    │   Copy batch event, set Agents = target's agents
    │   Marshal JSON, trySend(worker.sendChan)
    │   Adaptive drop: log every 5s when ≥80% full, count every 50 drops
    ▼
Worker WebSocket → JSON frame_batch message → Adapter
```

### 4.3 Adapter Job Processing

```
Adapter receives frame_batch from relay
    │
    │ Checks:
    │   1. workerUnavailableUntil backoff
    │   2. Per-camera adaptive throttle (interval + penalty)
    │   3. Backpressure (inFlight vs MAX_IN_FLIGHT)
    │
    │ fetchFrameBytes() — HTTP GET /worker/frames/{uuid}
    │   Returns JPEG Uint8Array
    │
    │ For each agent in batch:
    │   Build worker_request CBOR payload:
    │     Bbox: {media_id, media_name, inputs:[{id, filepath:{__type:"resource-ref"}}]}
    │     VLM:  {inputs:[{id, messages:[{role,content:[{type:"image",image:ref},{type:"text",text:prompt}]}]}]}
    │   resources: [{id, type:"image", data: Uint8Array}]
    │
    │ enqueueJob() → jobQueue
    │ pumpQueue() → send CBOR via engineWs when inFlight < MAX_IN_FLIGHT
    ▼
Engine receives CBOR worker_request
    │
    │ sendJob() → worker.gathered[]
    │ Batch by max_batch_size or max_latency_ms timeout
    │ workerFlush() → CBOR {inputs: [...]} to Python worker
    ▼
Python Worker processes batch
    │
    │ Returns CBOR {type:"worker_output", output:[...]}
    ▼
Engine → worker_response CBOR → Adapter
```

### 4.4 Result Processing

```
Adapter receives worker_response
    │
    ├── Bbox workers:
    │     parseObjectDetectionBatch(output)
    │     For each frame:
    │       toBBoxes() → {data:{summary, detections}, bboxes, extras}
    │       Build metadata: service_id, bboxes, agent_type, severity, counts
    │       If snapshot_path: uploadSnapshot() → POST /cv/upload-snapshot
    │       Collect person_names from bboxes
    │       Send JSON event to relay WS
    │
    └── VLM workers:
          parseVlmOutput(output)
          Extract summary, brief, severity
          If medium/high severity:
            Use cached frame bytes → uploadSnapshot()
          Send JSON event to relay WS
    │
    ▼
Relay CVWorkerRegistry.receiveLoop()
    │
    │ Enrich metadata: worker_id, agent_type (inferred), service_id
    │ shouldPersistEvent(metadata)?
    │   Yes → async dbWriteChan → dbWriter() → db.StoreAgentEvent()
    │
    │ CVEventBus.PublishWorkerEvent()
    ▼
Relay.OnWorkerEvent handler
    │
    ├── shouldBroadcastEvent(metadata)?
    │     Yes → clientEvents.BroadcastAgentEvent() → browser WS (full event)
    │
    ├── shouldBroadcastOverlayEvent(metadata)?
    │     Yes → BroadcastOverlayEvent() → browser WS (bbox-only, high freq)
    │
    ├── faceIdentityService.OnFaceWorkerEvent() — cache face results
    │
    ├── personIdentityService.OnAgentEvent() — cross-camera identity
    │
    └── PPE/person event with snapshot?
          maybeVLMTrigger(agentID, serviceID, snapshotPath, context)
            Rate-limit: 30s per service+context
            Load snapshot bytes → register as in-memory frame
            SendToWorkerPool("vyra/vlm", vlm_trigger message)
```

### 4.5 Event Filtering Rules

Both `shouldPersistEvent()` and `shouldBroadcastEvent()` use the same logic:

**Always persist/broadcast:**
- Non-PPE/person/face agent types (VLM events, wellness)

**For PPE/person/face, persist/broadcast only if ANY of:**
- `snapshot_path` present
- `vlm_summary` present
- `has_violations = true`
- `event_kind` in `{"ppe_violation", "person_visit"}`
- `violation_count > 0`
- `fall_detected = true`
- `first_detection = true`
- `matched_count > 0` (face recognition match)
- `severity` in `{"medium", "high"}`

**Bbox-only overlay broadcast:**
- `metadata["bboxes"]` is a non-empty array (regardless of other fields)
- These are high-frequency frames (~10 FPS) with only bounding box data

---

## 5. WebRTC Video Streaming

### 5.1 Session Creation

```
Browser                          Relay                              Camera
  │                                │                                   │
  │ POST /nodes/{nodeId}/offer     │                                   │
  │ {sdp: offer, serviceId}        │                                   │
  │───────────────────────────────>│                                   │
  │                                │ OPEN_BRIDGE via node WS           │
  │                                │──────────────────────────────────>│
  │                                │<──────────────────────────────────│
  │                                │ BridgeTCPProxy (local TCP port)   │
  │                                │                                   │
  │                                │ sources.New() → RTSP/MJPEG        │
  │                                │ Check codec: H265 only?           │
  │                                │   No → direct go2rtc pipeline     │
  │                                │   Yes → FFmpegTranscodeSource     │
  │                                │                                   │
  │                                │ api.NewPeerConnection(config)     │
  │                                │ wconn.SetOffer(offer)             │
  │                                │ Add tracks (source → WebRTC)      │
  │                                │ wconn.GetAnswer()                 │
  │                                │                                   │
  │ {sdp: answer}                  │                                   │
  │<───────────────────────────────│                                   │
  │                                │                                   │
  │ ICE candidates exchanged       │                                   │
  │ RTP H264 video stream          │                                   │
  │<═══════════════════════════════│                                   │
```

### 5.2 H265 Transcoding

When camera sends H265 (common for HD streams, subtype=0) and browser doesn't support H265:

```
Camera (H265 RTSP)
    │
    │ TCP bridge
    ▼
FFmpegTranscodeSource:
    ffmpeg -loglevel error
           -rtsp_transport tcp
           -i rtsp://[auth@]proxyAddr/path
           -c:v libx264 -preset ultrafast -tune zerolatency
           -vf scale=-2:720
           -g 30 -b:v 2M -maxrate 2M -bufsize 4M
           -an -f mpegts -
    │
    │ MPEG-TS on stdout pipe
    ▼
magic.Open(bufio.NewReader(stdout))
    │
    │ go2rtc native producer
    ▼
producer.GetTrack(H264Media, H264Codec)
    │
    │ go2rtc receiver
    ▼
wconn.AddTrack() → WebRTC PeerConnection → Browser
```

### 5.3 ICE / STUN Configuration

Currently hardcoded `stun:stun.l.google.com:19302` in 3 files:
- `application/relay/webrtc.go:132` (Go server side)
- `application/app/src/components/VideoTile.tsx:182` (browser side)
- `application/app/src/components/WebRTCStream.tsx:76` (browser side)

For airgapped/LAN deployments, these should be made configurable via `STUN_URL` env var (relay) and `VITE_STUN_URL` build arg (frontend). On a flat LAN, WebRTC host candidates typically suffice without STUN.

### 5.4 Session Lifecycle

- `pc.OnICEConnectionStateChange`:
  - `failed` → close session
  - `closed` → close session
  - `disconnected` → left to self-recover
- `session.Close()` (sync.Once): close bridge proxy + source + WebRTC conn + remove from session map
- Frontend reconnect: `scheduleReconnect(3000ms)` on ICE `failed`, `scheduleReconnect(1500ms)` on ICE `closed` (server-initiated)

---

## 6. Database Schema

SQLite with WAL mode. Connection pool: 4 max open, 4 max idle. Busy timeout: 5s. Page cache: 32MB.

### 6.1 Tables

#### `users`

| Column          | Type     | Constraints               |
|-----------------|----------|---------------------------|
| id              | INTEGER  | PRIMARY KEY AUTOINCREMENT |
| email           | TEXT     | NOT NULL UNIQUE           |
| password_hash   | TEXT     | NOT NULL                  |
| name            | TEXT     | NOT NULL                  |
| created_at      | DATETIME |                           |

#### `nodes`

| Column             | Type     | Constraints                      |
|--------------------|----------|----------------------------------|
| id                 | TEXT     | PRIMARY KEY                      |
| token              | TEXT     | UNIQUE NOT NULL                  |
| owner_id           | INTEGER  | FK → users(id) ON DELETE SET NULL|
| name               | TEXT     |                                  |
| created_at         | DATETIME |                                  |
| authorized_at      | DATETIME |                                  |
| last_connected_at  | DATETIME |                                  |

#### `nodes_users` (many-to-many)

| Column   | Type    | Constraints    |
|----------|---------|----------------|
| node_id  | TEXT    | PK (composite) |
| user_id  | INTEGER | PK (composite) |
| role     | TEXT    | DEFAULT 'owner'|

#### `agents`

| Column      | Type    | Constraints                          |
|-------------|---------|--------------------------------------|
| id          | TEXT    | PRIMARY KEY                          |
| name        | TEXT    | NOT NULL                             |
| worker_id   | TEXT    | NOT NULL (e.g., "vyra/ppe_detection")|
| config      | TEXT    | NOT NULL (JSON)                      |
| service_ids | TEXT    | JSON array of camera IDs             |
| user_id     | INTEGER | FK → users(id) ON DELETE CASCADE     |

#### `agent_events`

| Column     | Type     | Constraints                                 |
|------------|----------|---------------------------------------------|
| id         | TEXT     | PRIMARY KEY                                 |
| agent_id   | TEXT     | NOT NULL, FK → agents(id) ON DELETE CASCADE |
| data       | TEXT     | NOT NULL (JSON)                             |
| metadata   | TEXT     | JSON, nullable                              |
| created_at | DATETIME |                                             |

Indexes: `agent_id`, `created_at`

#### `cameras`

| Column          | Type     | Default           |
|-----------------|----------|-------------------|
| id              | TEXT     | PRIMARY KEY (UUID)|
| name            | TEXT     | NOT NULL          |
| type            | TEXT     | 'rtsp'            |
| addr            | TEXT     | NOT NULL          |
| port            | INTEGER  | NOT NULL          |
| path            | TEXT     | ''                |
| auth_username   | TEXT     | ''                |
| auth_password   | TEXT     | ''                |
| processing_fps  | REAL     | 10.0              |
| vlm_fps         | REAL     | 5.0               |
| user_id         | INTEGER  | FK → users(id)    |
| managed_node_id | TEXT     | NULL              |
| created_at      | DATETIME |                   |

#### `managed_nodes`

| Column     | Type     | Constraints        |
|------------|----------|--------------------|
| id         | TEXT     | PRIMARY KEY        |
| name       | TEXT     | NOT NULL           |
| user_id    | INTEGER  | FK → users(id)     |
| created_at | DATETIME |                    |

#### `persons`

| Column          | Type     | Constraints              |
|-----------------|----------|--------------------------|
| id              | TEXT     | PRIMARY KEY              |
| name            | TEXT     | NOT NULL                 |
| user_id         | INTEGER  | FK → users(id)           |
| face_image      | BLOB     | Legacy face JPEG         |
| face_updated_at | DATETIME |                          |
| image_path      | TEXT     | Filesystem path          |
| created_at      | DATETIME | NOT NULL                 |

#### `person_face_profiles`

| Column     | Type     | Constraints                           |
|------------|----------|---------------------------------------|
| id         | TEXT     | PRIMARY KEY                           |
| person_id  | TEXT     | FK → persons(id) ON DELETE CASCADE    |
| image_path | TEXT     | NOT NULL                              |
| created_at | DATETIME | NOT NULL                              |
| updated_at | DATETIME | NOT NULL                              |

#### `person_global_identities`

| Column              | Type     | Constraints                         |
|---------------------|----------|-------------------------------------|
| global_person_id    | TEXT     | PRIMARY KEY                         |
| person_id           | TEXT     | Linked to persons.id if recognized  |
| person_name         | TEXT     |                                     |
| identity_source     | TEXT     | DEFAULT ''(e.g.,"face_recognition") |
| identity_confidence | REAL     | DEFAULT 0                           |
| embedding_centroid  | TEXT     | JSON centroid vector                |
| alias_of            | TEXT     | If merged into another identity     |
| created_at          | DATETIME |                                     |
| updated_at          | DATETIME |                                     |
| last_seen_at        | DATETIME | NOT NULL                            |

#### `person_sightings`

| Column           | Type     | Constraints              |
|------------------|----------|--------------------------|
| id               | TEXT     | PRIMARY KEY              |
| user_id          | INTEGER  | FK → users(id)           |
| global_person_id | TEXT     | NOT NULL                 |
| person_id        | TEXT     | Optional                 |
| service_id       | TEXT     | Camera ID                |
| event_id         | TEXT     | Linked agent_events.id   |
| snapshot_path    | TEXT     |                          |
| activity_brief   | TEXT     |                          |
| created_at       | DATETIME | NOT NULL                 |

#### `camera_groups`

| Column     | Type    | Constraints        |
|------------|---------|--------------------|
| id         | TEXT    | PRIMARY KEY        |
| name       | TEXT    | NOT NULL           |
| user_id    | INTEGER | FK → users(id)     |
| created_at | TEXT    |                    |
| updated_at | TEXT    |                    |

#### `camera_group_members`

| Column     | Type | Constraints                                              |
|------------|------|----------------------------------------------------------|
| group_id   | TEXT | PK (composite), FK → camera_groups(id) ON DELETE CASCADE |
| service_id | TEXT | PK (composite)                                           |

#### `daily_summaries`

| Column              | Type     | Constraints                    |
|---------------------|----------|--------------------------------|
| id                  | TEXT     | PRIMARY KEY                    |
| date                | TEXT     | NOT NULL                       |
| user_id             | INTEGER  | NOT NULL                       |
| event_count         | INTEGER  | DEFAULT 0                      |
| person_sightings    | TEXT     | DEFAULT '[]' (JSON)            |
| incidents           | TEXT     | DEFAULT '[]' (JSON)            |
| violation_breakdown | TEXT     | DEFAULT '{}' (JSON)            |
| updated_at          | DATETIME | NOT NULL                       |
|                     |          | UNIQUE(date, user_id)          |

---

## 7. REST API Reference

All authenticated endpoints require `Authorization: Bearer <JWT>` header.

### 7.1 Authentication

| Method | Path              | Auth | Purpose                                    |
|--------|-------------------|------|--------------------------------------------|
| POST   | `/auth/register`  | none | Create user `{email, password, name}`      |
| POST   | `/auth/login`     | none | Login `{email, password}` → `{token, user}`|
| GET    | `/auth/me`        | user | Get current user info                      |
| GET    | `/flags`          | none | Feature flags (DEV_IMPERSONATE)            |

### 7.2 Events

| Method | Path              | Auth | Purpose                                              |
|--------|-------------------|------|------------------------------------------------------|
| GET    | `/events`         | user | List events. `?limit=N&offset=N&date=YYYY-MM-DD`     |
| GET    | `/events/search`  | user | Search events. `?query=TEXT&limit=N`                 |
| GET    | `/events/count`   | user | `{total, today}` counts                              |

Limits: max 1000 per request (events), max 2000 for filtered.

### 7.3 Persons

| Method | Path                              | Auth | Purpose                                          |
|--------|-----------------------------------|------|--------------------------------------------------|
| GET    | `/persons`                        | user | List all persons                                 |
| POST   | `/persons`                        | user | Create `{name}`                                  |
| GET    | `/persons/{id}`                   | user | Get person                                       |
| PATCH  | `/persons/{id}`                   | user | Rename `{name}`                                  |
| DELETE | `/persons/{id}`                   | user | Delete person + files                            |
| POST   | `/persons/{id}/face`              | user | Upload legacy face                               |
| GET    | `/persons/{id}/face`              | none | Serve face image                                 |
| GET    | `/persons/{id}/faces`             | user | List face profiles                               |
| POST   | `/persons/{id}/faces`             | user | Upload face profile (quality checked)            |
| GET    | `/persons/{id}/faces/{profileId}` | none | Serve profile image                              |
| DELETE | `/persons/{id}/faces/{profileId}` | user | Delete profile                                   |
| GET    | `/persons/{id}/sightings`         | user | Face recognition events                          |
| GET    | `/persons/{id}/moments`           | user | Cross-camera timeline `?date=YYYY-MM-DD`         |
| GET    | `/persons/registry`               | none | Worker sync endpoint (all persons with profiles) |

**Face enrollment quality check:** On profile upload, runs `engine/py/face_enrollment_quality.py` via subprocess. Returns `{accepted, reason, metrics}`. Rejections include: no face detected, face too small, image too blurry.

### 7.4 Cameras

| Method | Path                  | Auth | Purpose                                                                 |
|--------|-----------------------|------|-------------------------------------------------------------------------|
| GET    | `/cameras`            | user | List all cameras                                                        |
| POST   | `/cameras`            | user | Create camera `{name, type, addr, port, path, auth_*, managed_node_id}` |
| GET    | `/cameras/{id}`       | user | Get camera                                                              |
| PATCH  | `/cameras/{id}`       | user | Update camera fields                                                    |
| DELETE | `/cameras/{id}`       | user | Delete camera                                                           |
| PATCH  | `/cameras/{id}/fps`   | user | Update `{processing_fps, vlm_fps}`                                      |

### 7.5 Agents

| Method | Path           | Auth | Purpose                                               |
|--------|----------------|------|-------------------------------------------------------|
| GET    | `/agents`      | user | List agents                                           |
| POST   | `/agents`      | user | Create agent `{name, worker_id, config, service_ids}` |
| GET    | `/agents/{id}` | user | Get agent                                             |
| PATCH  | `/agents/{id}` | user | Update agent                                          |
| DELETE | `/agents/{id}` | user | Delete agent                                          |

### 7.6 System Management

| Method | Path                         | Auth | Purpose                                              |
|--------|------------------------------|------|------------------------------------------------------|
| GET    | `/system/status`             | user | All process statuses                                 |
| POST   | `/system/start/{name}`       | user | Start component (engine/llm/worker-ppe/etc.)         |
| POST   | `/system/stop/{name}`        | user | Stop component                                       |
| GET    | `/system/workers`            | user | Worker pool stats (replicas, queue, drops, autoscale)|
| POST   | `/system/scale`              | user | Scale workers `{type, replicas}`                     |
| POST   | `/system/autoscale`          | user | Configure autoscaling `{enabled, max_replicas}`      |
| POST   | `/system/start/agent`        | user | Start engine+worker+adapter for `{worker_id}`        |
| GET    | `/system/frame-interval`     | user | Get current frame interval                           |
| PATCH  | `/system/frame-interval`     | user | Update frame interval (live, no restart)             |

### 7.7 Managed Nodes

| Method | Path                                   | Auth | Purpose                              |
|--------|----------------------------------------|------|--------------------------------------|
| GET    | `/managed-nodes`                       | user | List managed nodes                   |
| POST   | `/managed-nodes`                       | user | Create `{name}`                      |
| PATCH  | `/managed-nodes/{id}/name`             | user | Rename `{name}`                      |
| POST   | `/managed-nodes/{id}/start`            | user | Generate config + spawn node process |
| POST   | `/managed-nodes/{id}/stop`             | user | Kill node process                    |
| DELETE | `/managed-nodes/{id}`                  | user | Delete + kill                        |
| POST   | `/managed-nodes/{id}/cameras/{camId}`  | user | Assign camera to node             mm |
| DELETE | `/managed-nodes/{id}/cameras/{camId}`  | user | Unassign camera                      |

### 7.8 Runtime Settings

| Method | Path                | Auth | Purpose                                                                       |
|--------|---------------------|------|-------------------------------------------------------------------------------|
| GET    | `/runtime/settings` | user | Get runtime tuning params                                                     |
| POST   | `/runtime/settings` | user | Update params (PPE association, snapshot cooldown, ReID threshold, LLM model) |

### 7.9 Storage & Snapshots

| Method | Path                        | Auth       | Purpose                               |
|--------|-----------------------------|------------|---------------------------------------|
| GET    | `/storage/snapshots/{path}` | token (query) | Serve snapshot JPEG. `?token=JWT`  |
| POST   | `/cv/upload-snapshot`       | worker key | Upload snapshot from adapter          |
| GET    | `/worker/frames/{uuid}`     | worker key | Download frame JPEG (port 9020 only)  |

### 7.10 Camera Groups

| Method | Path                          | Auth | Purpose                     |
|--------|-------------------------------|------|-----------------------------|
| GET    | `/camera-groups`              | user | List groups                 |
| POST   | `/camera-groups`              | user | Create `{name}`             |
| PATCH  | `/camera-groups/{id}`         | user | Rename                      |
| DELETE | `/camera-groups/{id}`         | user | Delete                      |
| PUT    | `/camera-groups/{id}/members` | user | Set members `{service_ids}` |

### 7.11 WebRTC

| Method | Path                   | Auth | Purpose                                                     |
|--------|------------------------|------|-------------------------------------------------------------|
| POST   | `/nodes/{nodeId}/offer` | user | Create WebRTC session `{sdp, serviceId}` → `{sdp: answer}` |

### 7.12 CV Internal APIs (worker-to-relay)

| Method | Path                           | Auth     | Purpose                                          |
|--------|--------------------------------|----------|--------------------------------------------------|
| POST   | `/cv/face-lookup`              | internal | Fire-and-forget face recognition request         |
| GET    | `/cv/face-identities`          | internal | Get cached face results `?media_id=X`            |
| POST   | `/cv/person-identity/resolve`  | internal | Report person identity for cross-camera merging  |
| GET    | `/cv/person-identities`        | internal | Get cross-camera identities `?media_id=X`        |

---

## 8. WebSocket Protocol Reference

### 8.1 Browser ↔ Relay (JSON)

**Connection:** `ws://{relay}:8020/ws/events?token=JWT`

**Server → Client messages:**

| Type                 | Payload                                                               | Purpose                           |
|----------------------|-----------------------------------------------------------------------|-----------------------------------|
| `agent_event`        | `{id, agent_id, agent_name, service_ids, data, metadata, created_at}` | Full detection event (filtered)   |
| `agent_overlay_event`| `{id, agent_id, service_ids, metadata: {bboxes}}`                     | Bbox-only overlay (high frequency)|
| `node_auth_request`  | `{node_id, node_name}`                                                | Node authorization prompt         |

### 8.2 Relay ↔ Worker/Adapter (JSON, port 9020)

**Connection:** `ws://{relay}:9020/worker/connect`

**Client → Server:**

| Type        | Payload                                                      | Purpose                          |
|-------------|--------------------------------------------------------------|----------------------------------|
| `register`  | `{worker_id}`                                                | Register adapter with relay      |
| `heartbeat` | `{}`                                                         | Keep-alive (10s interval)        |
| `event`     | `{agent_id, service_id, frame_id, data, metadata}`           | Detection result from adapter    |

**Server → Client:**

| Type          | Payload                                                         | Purpose                                             |
|---------------|-----------------------------------------------------------------|-----------------------------------------------------|
| `registered`  | `{worker_id, key}`                                              | Registration confirmed, auth key for frame download |
| `frame_batch` | `{service_id, frames[], metadata, agents[]}`                    | Frame batch for processing                          |
| `vlm_trigger` | `{agent_id, service_id, frame_id, context}`                     | Relay-initiated VLM analysis                        |
| `face_lookup` | `{media_id, track_id, frame_id, person_bbox}`                   | Relay-initiated face recognition                    |

### 8.3 Adapter ↔ Engine (CBOR-X binary)

**Connection:** `ws://{engine}:5000/ws`

**Client → Server:**

| Type             | Payload                                                                 | Purpose                        |
|------------------|-------------------------------------------------------------------------|--------------------------------|
| `i_am_server`    | `{version, token?}`                                                     | Adapter registration           |
| `worker_request` | `{jobs: [{job_id, worker_type, input}], resources: [{id, type, data}]}` | Job submission with frame data |

**Server → Client:**

| Type              | Payload        | Purpose                   |
|-------------------|----------------|---------------------------|
| `server_welcome`  | `{}`           | Registration confirmed    |
| `worker_response` | `{id, output}` | Job result from worker    |
| `error`           | `{message}`    | Error notification        |

### 8.4 Engine ↔ Python Worker (CBOR2 binary)

**Connection:** `ws://{engine}:5000/ws`

**Client → Server:**

| Type          | Payload                                                                         | Purpose             |
|---------------|---------------------------------------------------------------------------------|---------------------|
| `i_am_worker` | `{worker_config: {worker_type, max_latency_ms, max_batch_size}, worker_secret}` | Worker registration |

**Server → Client:**

Batch of jobs as CBOR dict:
```
{inputs: [{id, filepath/messages, media_id, media_name, ...}]}
```

Resources (images) saved as temporary files by engine, `filepath` points to temp path.

**Client → Server:**

```
{type: "worker_output", output: [{id, detections/response, ...}]}
```

---

## 9. Frontend Architecture

### 9.1 Tab Components

#### TabBirdEye (Command Center)

Multi-camera grid with live bounding boxes, agent assignment, camera groups, fullscreen overlay, and live context sidebar.

**Key features:**
- Camera grid with VideoTile components per camera
- Group management (create/rename/delete groups, assign cameras)
- Bulk agent assignment (select agent → Apply to all visible cameras)
- Double-click → fullscreen overlay with VideoTile
- Auto-play toggle for all tiles
- Live Context Sidebar (≥2xl screens):
  - Active Identities: persons seen in last 30 minutes, deduped, with camera+event counts
  - Incidents: violations/high/medium severity events in last 5 minutes

#### TabMoments (Investigate)

Event history feed with rich filtering, snapshot viewer, and live updates.

**Filters:**
- Camera (service_id), Agent (agent_id), Agent Type (ppe/person/situational/face)
- Severity pills (all/high/medium/low)
- Text search (in-memory across summary, brief, labels, camera name)
- Time range (HH:MM from–to)
- Date picker with Today/Yesterday shortcuts

**Event card rendering:**
- Severity color bar (top strip)
- Snapshot thumbnail (lazy loaded, click → ImageViewer with zoom/drag)
- Primary text: brief or VLM summary
- Badges: camera, person count, violation count, violation labels (red), person names (blue)

**ImageViewer:** Full-screen modal, scroll wheel zoom (0.5×–5×), drag at scale > 1.

#### TabAnalytics (Performance)

7 Chart.js charts + 4 KPI cards + high-severity alert table.

**Charts:**
1. Events – Last 24h (line, hourly buckets)
2. PPE Compliance (doughnut, violations vs compliant)
3. Severity Distribution (doughnut)
4. Top Violation Types (horizontal bar, top 8)
5. Events per Camera (vertical bar, top 8)
6. Person Movement Cross-Camera (horizontal bar, top 8 by unique camera count)
7. Violation Trend – Last 24h (line, hourly)

**KPIs:** Total Events, High Severity Count, Violation Rate %, Active Cameras

**Data:** REST fetch (500 events) + live WebSocket prepending, charts update via `chart.data` mutation + `chart.update('none')`.

#### TabHome (Control)

Three sub-views via ArkTabs:

**Managed Nodes:** Table with Start/Stop/Rename/Delete. Status indicators: green (running+online), yellow (running, connecting), gray (stopped).

**Cameras:** Table with Add/Edit/Delete. Fields: name, type (rtsp/mjpeg), address, port, path, auth, managed node assignment.

**Services:** Flat table of all services from connected nodes. Click → navigate to camera view.

#### TabAgents (Control)

**3 predefined agents:**
- PPE Detection (worker: ppe, deps: vlm, face)
- Person Detection (worker: objdet, deps: vlm, face)
- Situational Awareness (worker: vlm, deps: none)

**4 worker pools:** ppe, objdet, vlm, face — each showing replicas, queue pressure, dropped frames.

**Custom agents:** Upload `.pt` model file → create custom YOLO agent. Start with configurable replicas + optional VLM/face.

**Ask AI Worker (LLM):** Start/stop Gemma-3 for natural language queries.

#### TabPersons (Identity)

Person registry with multi-profile face enrollment, sightings, and cross-camera moments.

**Person card:** Name (editable), enrollment status (Ready/Not Ready), face profile thumbnails (horizontal scroll), upload new profile, sightings panel (lazy loaded), moments panel (date-filtered cross-camera timeline).

#### TabSettings (System)

4 sub-tabs: Alerts (webhook URL), About (model info), Frame Rates (global + per-camera), Runtime Tuning (PPE association, snapshot cooldown, ReID threshold, LLM model).

### 9.2 VideoTile Component

Core streaming component used in BirdEye grid and fullscreen overlay.

**Props:**
- `nodeId`, `serviceId` — which camera to stream
- `autoStart`, `showControls`, `startOnClick` — lifecycle control
- `showBBoxes` — render bounding box overlays
- `showDetectionCounts` — render label count badges
- `onDblClick` — double-click handler (fullscreen)
- `displayMode` — 'tile' or 'fullscreen' (affects border-radius)

**Bounding box pipeline:**
1. Subscribe to `agentEvents` and `agentOverlayEvents` via WebSocket
2. Filter by `metadata.service_id` match
3. Per-agent segregation: `agentBoxMap = Map<agentKey, {boxes, ts}>`
4. Throttle to 33ms (~30fps)
5. Stale box pruning every 500ms (remove entries > 2000ms old)
6. Letterbox math: `scale = Math.min(containerWidth/videoWidth, containerHeight/videoHeight)`, offsets from center
7. CSS transitions: `left/top/width/height` at 60ms linear for smooth movement

**Label priority:** `person_name` > `"#track_id label"` > `"label"`

**Colors:** Violations (NO-*, without, Fall) = red border + red badge. Compliant = green border + green badge.

---

## 10. Configuration Reference

### 10.1 Relay Environment Variables

| Variable                           | Default                      | Purpose                                        |
|------------------------------------|------------------------------|------------------------------------------------|
| `APP_DIR`                          | *required*                   | Data directory (SQLite, storage, snapshots)    |
| `FRAME_INTERVAL_SECONDS`           | *required*                   | Frame extraction interval(e.g., `0.1` = 10 FPS)|
| `CV_FRAME_BATCH_SIZE`              | 10                           | Frames per batch to workers                    |
| `RELAY_PORT`                       | *required*                   | WebSocket port (typically 9020)                |
| `API_PORT`                         | *required*                   | HTTP API port (typically 8020)                 |
| `HTTP_PORT`                        | same as RELAY_PORT           | Alias for RELAY_PORT                           |
| `ENGINE_PORT`                      | 5000                         | Engine WebSocket port                          |
| `DASHBOARD_URL`                    | *required*                   | Frontend URL                                   |
| `AUTO_REQUEST_REALTIME_STREAM`     | true                         | Auto-start streams on node connect             |
| `SNAPSHOT_RETENTION_DAYS`          | 7                            | Snapshot cleanup (not currently active)        |
| `JWT_SECRET`                       | "change-me-in-production"    | JWT signing key                                |
| `DEV_IMPERSONATE`                  | ""                           | Dev mode impersonation                         |
| `VYRA_ROOT`                        | ""                           | Project root (empty = disable ProcessManager)  |
| `WORKER_SECRET`                    | ""                           | Shared secret for worker auth                  |
| `STUN_URL`                         | ""                           | STUN server URL (e.g., "stun:192.168.1.5:3478")|
| `FRAME_MAX_READS`                  | 6                            | Max downloads per frame before eviction        |
| `FRAME_TTL_SECONDS`                | 20                           | Frame TTL in memory                            |
| `FACE_ENROLLMENT_ENFORCE_QUALITY`  | true                         | Enforce face enrollment quality                |
| `FACE_ENROLLMENT_MIN_PROFILES`     | 3                            | Minimum profiles for enrollment ready          |

### 10.2 Frontend Build Arguments

| Variable              | Default    | Purpose                                    |
|-----------------------|------------|--------------------------------------------|
| `VITE_RELAY_API_URL`  | *required* | Relay HTTP API URL (baked at build time)   |
| `VITE_STUN_URL`       | ""         | STUN server URL for WebRTC                 |
| `VITE_POSTHOG_KEY`    | ""         | PostHog analytics key                      |

### 10.3 Engine Environment Variables

| Variable        | Default | Purpose                               |
|-----------------|---------|---------------------------------------|
| `WORKER_SECRET` | ""      | Shared secret for worker auth         |
| `HF_TOKEN`      | ""      | HuggingFace token for model downloads |

### 10.4 Adapter Environment Variables

| Variable                    | Default                             | Purpose                                         |
|-----------------------------|-------------------------------------|-------------------------------------------------|
| `RELAY_WS_URL`              | `ws://localhost:9020/worker/connect`| Relay WebSocket URL                             |
| `RELAY_HTTP_BASE`           | `http://localhost:9020`             | Relay HTTP base(frame download,snapshot upload) |
| `ENGINE_WS_URL`             | `ws://localhost:5000/ws`            | Engine WebSocket URL                            |
| `WORKER_ID`                 | "vyra/vlm"                          | Adapter identity                                |
| `ENGINE_WORKER_TYPE`        | auto-derived                        | Python worker type                              |
| `MAX_IN_FLIGHT`             | 4                                   | Max concurrent jobs                             |
| `MAX_QUEUE_SIZE`            | 128 (bbox) / 48 (VLM)               | Max queued jobs                                 |
| `IN_FLIGHT_TIMEOUT_MS`      | 15000 (bbox) / 60000 (VLM)          | Job timeout                                     |
| `MAX_JOB_RETRIES`           | 1                                   | Max retries on timeout                          |
| `MIN_SUBMIT_INTERVAL_MS`    | 0                                   | Min ms between submissions per camera           |
| `MAX_FRAMES_PER_JOB`        | 1                                   | Frames per job                                  |
| `WORKER_UNAVAILABLE_BACKOFF_MS` | 2000                            | Backoff after no-worker error                   |

### 10.5 Python Worker Environment Variables

**Common:**

| Variable               | Default                       | Purpose                               |
|------------------------|-------------------------------|---------------------------------------|
| `BACKEND_WS_URL`       | `ws://localhost:5000/ws`      | Engine WebSocket URL                  |
| `RELAY_HTTP_BASE`      | `http://localhost:9020`       | Relay HTTP base                       |
| `WORKER_TYPE`          | *required*                    | Worker type identifier                |
| `WORKER_SECRET`        | ""                            | Auth secret                           |
| `MAX_LATENCY_MS`       | varies                        | Max wait before flushing batch        |
| `MAX_BATCH_SIZE`       | varies                        | Max items per batch                   |
| `SNAPSHOT_DIR`         | `/tmp/vyra_snapshots`         | Local snapshot directory              |
| `TRACKER_FRAME_RATE`   | 10                            | ByteTrack frame rate                  |
| `TRACKER_LOST_BUFFER`  | 30                            | Frames before track is lost           |
| `VLM_COOLDOWN_SECONDS` | 15                            | Min Secs between VLM calls per track  |

**PPE Worker:**

| Variable                            | Default                                       | Purpose                                         |
|-------------------------------------|-----------------------------------------------|-------------------------------------------------|
| `PPE_MODEL_ID`                      | `hf://Hexmon/vyra-yolo-ppe-detection/best.pt` | Model path or HF URI                            |
| `PPE_CONF`                          | 0.35                                          | Confidence threshold                            |
| `PPE_IOU`                           | 0.45                                          | IOU threshold                                   |
| `PPE_MAX_DET`                       | 300                                           | Max detections per frame                        |
| `PPE_ENABLE_VLM`                    | 0                                             | Enable background VLM                           |
| `PPE_ENABLE_FACE_RECOGNITION`       | 0                                             | Enable embedded face rec                        |
| `PPE_REQUIRE_PERSON_ASSOCIATION`    | 1                                             | Require person bbox for NO-* violations         |
| `PPE_SNAPSHOT_ALWAYS_ON_VIOLATION`  | 1                                             | Snapshot on every gated violation               |
| `SNAPSHOT_COOLDOWN_SECONDS`         | 30                                            | Min seconds between snapshots per context       |

**Object Detection Worker:**

| Variable                          | Default    | Purpose                       |
|-----------------------------------|------------|-------------------------------|
| `MODEL_ID`                        | `yolov8m.pt` | YOLO model path             |
| `CONF_THRESHOLD`                  | 0.30       | Confidence threshold          |
| `OBJECT_DET_ENABLE_VLM`           | 0          | Enable background VLM         |
| `OBJECT_DET_ENABLE_FACE_RECOGNITION` | 0       | Enable embedded face rec      |

**VLM Worker:**

| Variable        | Default                                | Purpose              |
|-----------------|----------------------------------------|----------------------|
| `VLM_MODEL_ID`  | `HuggingFaceTB/SmolVLM2-2.2B-Instruct` | VLM model            |
| `MAX_NEW_TOKENS`| 256                                    | Max generation tokens|

**Face Recognition:**

| Variable                      | Default         | Purpose                          |
|-------------------------------|-----------------|----------------------------------|
| `FACE_RECOGNITION_V2_ENABLED` | true            | Enable V2 composite scoring      |
| `FACE_PRIMARY_MODEL`          | buffalo_sc      | Primary InsightFace model        |
| `FACE_SECONDARY_MODEL`        | buffalo_l       | Secondary InsightFace model      |
| `FACE_MATCH_STRONG_THRESHOLD` | 0.52            | Strong match threshold           |
| `FACE_MATCH_WEAK_THRESHOLD`   | 0.42            | Weak match threshold             |
| `FACE_REGISTRY_REFRESH_S`     | 60              | Registry refresh interval        |
| `INSIGHTFACE_HOME`            | `~/.insightface`| InsightFace model cache          |

---

## 11. Key Files Reference

### Relay (Go)

| File                                                  | Purpose                                                                     |
|-------------------------------------------------------|-----------------------------------------------------------------------------|
| `application/relay/cmd/relay/main.go`                 | Entry point                                                                 |
| `application/relay/relay.go`                          | Core Relay struct, node management, CV init, auto-scale, VLM trigger        |
| `application/relay/database.go`                       | SQLite schema (14 tables), all queries                                      |
| `application/relay/config.go`                         | Environment variable loading + validation                                   |
| `application/relay/webrtc.go`                         | WebRTC session management, H265 detection, transcoding                      |
| `application/relay/cv/events.go`                      | CVEventBus pub/sub, event types                                             |
| `application/relay/cv/worker_registry.go`             | Worker pool, round-robin routing, event persistence                         |
| `application/relay/cv/storage.go`                     | In-memory frame store, HTTP serving                                         |
| `application/relay/cv/frame_extractor.go`             | FFmpeg frame extraction (H264+H265 RTP)                                     |
| `application/relay/cv/snapshot_manager.go`            | Disk snapshot persistence                                                   |
| `application/relay/sources/ffmpeg_transcoder.go`      | H265→H264 FFmpeg transcoding                                                |
| `application/relay/events_http.go`                    | Events REST API                                                             |
| `application/relay/persons_http.go`                   | Person CRUD + face enrollment REST                                          |
| `application/relay/camera_table.go`                   | Camera CRUD                                                                 |
| `application/relay/person_table.go`                   | Person DB operations                                                        |
| `application/relay/managed_node_table.go`             | Managed node DB operations                                                  |
| `application/relay/system_api.go`                     | System management REST API                                                  |
| `application/relay/process_manager.go`                | OS child process spawner                                                    |
| `application/relay/Dockerfile`                        | Multi-stage Go build → debian-slim                                          |

### Frontend (Solid.js)

| File                                                         | Purpose                                                    |
|--------------------------------------------------------------|------------------------------------------------------------|
| `application/app/src/App.tsx`                                | Root component, path router, auth guard                    |
| `application/app/src/shared.tsx`                             | Global state, signals, API helpers, WebSocket subscriptions|
| `application/app/src/components/VideoTile.tsx`               | WebRTC stream + bbox overlay + detection counts            |
| `application/app/src/components/WebRTCStream.tsx`            | Standalone WebRTC component                                |
| `application/app/src/components/SideBar.tsx`                 | Navigation sidebar                                         |
| `application/app/src/components/tabs/TabBirdEye.tsx`         | Multi-camera grid + fullscreen + live context              |
| `application/app/src/components/tabs/TabMoments.tsx`         | Event feed + filters + image viewer                        |
| `application/app/src/components/tabs/TabAnalytics.tsx`       | Charts + KPIs                                              |
| `application/app/src/components/tabs/TabHome.tsx`            | Nodes + Cameras + Services management                      |
| `application/app/src/components/tabs/TabAgents.tsx`          | Agent + worker pool management                             |
| `application/app/src/components/tabs/TabPersons.tsx`         | Person registry + face enrollment                          |
| `application/app/src/components/tabs/TabSettings.tsx`        | Platform settings                                          |
| `application/app/src/index.css`                              | CSS entry (imports design system)                          |
| `application/app/Dockerfile`                                 | Bun build + Nginx                                          |

### Engine (Bun/TypeScript)

| File                                     | Purpose                                          |
|------------------------------------------|--------------------------------------------------|
| `engine/index.ts`                        | Engine distributor entry point                   |
| `engine/src/relay_worker_adapter.ts`     | Adapter: queue, backpressure, result handling    |
| `engine/Dockerfile`                      | Bun runtime                                      |

### Python Workers

| File                                     | Purpose                                                        |
|------------------------------------------|----------------------------------------------------------------|
| `engine/py/worker_ppe.py`                | PPE detection (YOLOv8m + ByteTrack + VLM + face)               |
| `engine/py/worker_object_detection.py`   | Object detection (YOLOv8m + ByteTrack + face + VLM)            |
| `engine/py/worker_vlm.py`                | VLM situational analysis (SmolVLM2)                            |
| `engine/py/worker_face_recognition.py`   | Standalone face recognition (InsightFace)                      |
| `engine/py/worker_query_llm.py`          | LLM query worker (Gemma-3, FastAPI)                            |
| `engine/py/tracker.py`                   | ByteTrack wrapper, per-stream tracking, VLM gating             |
| `engine/py/vlm_helper.py`                | SmolVLM2 singleton for background VLM                          |
| `engine/py/face_recognizer.py`           | InsightFace singleton, multi-profile, composite scoring        |
| `engine/py/ws_client_handler.py`         | WebSocket transport layer for all workers                      |
| `engine/py/relay_client.py`              | Relay REST client for face/identity lookups                    |
| `engine/py/pyproject.toml`               | Python dependencies (PyTorch cu128, ultralytics, etc.)         |
| `engine/py/Dockerfile.worker`            | Python worker image (uv + CUDA)                                |
| `engine/py/Dockerfile.llm`               | LLM worker image                                               |

### Node (Go)

| File                                       | Purpose                             |
|--------------------------------------------|-------------------------------------|
| `application/node/cmd/unblink/main.go`     | Node entry point, CLI commands      |
| `application/node/go.mod`                  | Go module (CBOR, gorilla/ws, hujson)|

### Deployment

| File                              | Purpose                                                  |
|-----------------------------------|----------------------------------------------------------|
| `docker-compose.yml`              | Docker Compose with profiles (core, cv, gpu, llm)        |
| `tmux.vyra.sh`                    | Local dev launcher (relay + app only)                    |
| `deploy/k8s-placeholder/`         | Kubernetes manifests (placeholder)                       |
| `scripts/e2e_smoke.sh`            | API smoke test script                                    |
| `application/demo.sh`             | RTSP demo stream launcher                                |

---

