package relay

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ProcessManager spawns and tracks OS-level child processes for engine, workers,
// adapters, and node instances.  All state is in-memory; processes do not
// survive a relay restart.
type ProcessManager struct {
	mu            sync.Mutex
	procs         map[string]*managedProc
	desiredScale  map[string]int                 // worker type -> desired replicas
	customRuntime map[string]*customRuntimeState // agentID -> custom runtime state
	procFailCount map[string]int                 // process name -> consecutive unexpected exits
	procNextRetry map[string]time.Time           // process name -> next allowed start timestamp
	procStopReq   map[string]bool                // process name -> stop requested by manager
	autoScale     bool
	autoMaxScale  int
	rootDir       string // VYRA project root (VYRA_ROOT)
	appDir        string // relay APP_DIR (node configs stored here)
	relayPort     string
	apiPort       string // HTTP API port for browsers (snapshot uploads etc.)
	enginePort    string
	workerSecret  string
	selfHeal      bool
	autoStartDeps bool
	autoStartEng  bool
	llmModelID    string
	llmDesired    bool
	llmFailCount  int
	llmNextRetry  time.Time
}

type managedProc struct {
	cmd       *exec.Cmd
	name      string
	startedAt time.Time
}

type customRuntimeState struct {
	Desired int
	Options CustomRuntimeOptions
}

type CustomRuntimeOptions struct {
	ModelPath  string
	EnableVLM  bool
	EnableFace bool
}

// ProcStatus is the JSON-serialisable status of a single process.
type ProcStatus struct {
	Name      string    `json:"name"`
	Running   bool      `json:"running"`
	PID       int       `json:"pid,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
}

var engineReadyTimeout = 12 * time.Second

const (
	processFastEmbedding = "worker-fast-embedding"
	processRAGReranker   = "worker-rag-reranker"
)

// NewProcessManager creates a ProcessManager.  rootDir="" disables spawning.
func NewProcessManager(rootDir, appDir, relayPort, apiPort, enginePort, workerSecret string) *ProcessManager {
	defaultLLMModel := strings.TrimSpace(os.Getenv("LLM_MODEL_ID"))
	if defaultLLMModel == "" {
		defaultLLMModel = "google/gemma-3-1b-it"
	}
	return &ProcessManager{
		procs:         make(map[string]*managedProc),
		desiredScale:  map[string]int{"ppe": 0, "vlm": 0, "objdet": 0, "face": 0},
		customRuntime: make(map[string]*customRuntimeState),
		procFailCount: make(map[string]int),
		procNextRetry: make(map[string]time.Time),
		procStopReq:   make(map[string]bool),
		autoScale:     false,
		autoMaxScale:  6,
		rootDir:       rootDir,
		appDir:        appDir,
		relayPort:     relayPort,
		apiPort:       apiPort,
		enginePort:    enginePort,
		workerSecret:  workerSecret,
		selfHeal:      false,
		autoStartDeps: false,
		autoStartEng:  true,
		llmModelID:    defaultLLMModel,
		llmDesired:    false,
		llmFailCount:  0,
	}
}

// IsAvailable returns false when VYRA_ROOT is not configured.
func (pm *ProcessManager) IsAvailable() bool {
	return pm.rootDir != ""
}

// ── generic process control ─────────────────────────────────────────────────

// Start spawns a process and tracks it under the given name.
// If a process with that name is already running, it returns an error.
func (pm *ProcessManager) Start(name, program string, args []string, workDir string, extraEnv map[string]string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if p, exists := pm.procs[name]; exists && p.cmd.ProcessState == nil {
		return fmt.Errorf("process %q is already running (pid %d)", name, p.cmd.Process.Pid)
	}
	if next, ok := pm.procNextRetry[name]; ok && !next.IsZero() {
		if remain := time.Until(next); remain > 0 {
			return fmt.Errorf("process %q restart backoff active (%s remaining)", name, remain.Truncate(time.Second))
		}
	}

	cmd := exec.Command(program, args...)
	cmd.Dir = workDir
	// Put every managed child in its own process group so Stop can terminate
	// wrapper commands (for example `uv run ...` or `go run ...`) together with
	// the actual child process they spawn.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Inherit parent env and overlay extras
	env := os.Environ()
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	// Pipe stdout/stderr to relay's logger with a prefix
	cmd.Stdout = newPrefixWriter("[" + name + "] ")
	cmd.Stderr = newPrefixWriter("[" + name + "|ERR] ")

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %q: %w", name, err)
	}

	mp := &managedProc{cmd: cmd, name: name, startedAt: time.Now()}
	pm.procs[name] = mp
	pm.procStopReq[name] = false

	// Reap in background so ProcessState is populated when the child exits
	go func() {
		waitErr := cmd.Wait()
		pm.onProcessExit(name, waitErr)
		if waitErr != nil {
			log.Printf("[ProcessManager] %q exited: %v", name, waitErr)
			return
		}
		log.Printf("[ProcessManager] %q exited cleanly", name)
	}()

	log.Printf("[ProcessManager] Started %q (pid %d)", name, cmd.Process.Pid)
	return nil
}

func (pm *ProcessManager) onProcessExit(name string, waitErr error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.procStopReq[name] {
		delete(pm.procStopReq, name)
		pm.procFailCount[name] = 0
		pm.procNextRetry[name] = time.Time{}
		return
	}

	startedAt := time.Now()
	if p, ok := pm.procs[name]; ok {
		startedAt = p.startedAt
	}
	uptime := time.Since(startedAt)

	// Long-running processes that exit once should not be treated as a tight crash loop.
	if uptime > 2*time.Minute {
		pm.procFailCount[name] = 1
	} else {
		pm.procFailCount[name]++
	}
	backoff := time.Duration(1<<min(pm.procFailCount[name], 6)) * time.Second
	if backoff > 90*time.Second {
		backoff = 90 * time.Second
	}
	pm.procNextRetry[name] = time.Now().Add(backoff)
}

// Stop sends SIGTERM, waits up to 5 s, then force-kills.
func (pm *ProcessManager) Stop(name string) error {
	pm.mu.Lock()
	p, exists := pm.procs[name]
	if !exists {
		pm.mu.Unlock()
		return fmt.Errorf("process %q not found", name)
	}
	if p.cmd.ProcessState != nil {
		pm.mu.Unlock()
		return nil // already exited
	}
	pm.procStopReq[name] = true
	pm.mu.Unlock()

	pid := 0
	if p.cmd.Process != nil {
		pid = p.cmd.Process.Pid
	}
	pgid := 0
	useProcessGroup := false
	if pid > 0 {
		if resolvedPGID, err := syscall.Getpgid(pid); err == nil && resolvedPGID == pid {
			pgid = resolvedPGID
			useProcessGroup = true
		}
	}

	if err := terminateManagedProcess(pid, pgid, useProcessGroup, 5*time.Second); err != nil {
		return err
	}

	pm.mu.Lock()
	delete(pm.procs, name)
	delete(pm.procStopReq, name)
	pm.procFailCount[name] = 0
	pm.procNextRetry[name] = time.Time{}
	pm.mu.Unlock()

	log.Printf("[ProcessManager] Stopped %q", name)
	return nil
}

func terminateManagedProcess(pid, pgid int, useProcessGroup bool, timeout time.Duration) error {
	sendSignal := func(sig syscall.Signal) error {
		if useProcessGroup && pgid > 0 {
			if err := syscall.Kill(-pgid, sig); err != nil && err != syscall.ESRCH {
				return err
			}
			return nil
		}
		if pid > 0 {
			if err := syscall.Kill(pid, sig); err != nil && err != syscall.ESRCH {
				return err
			}
		}
		return nil
	}

	stillRunning := func() bool {
		if useProcessGroup && pgid > 0 {
			return processGroupExists(pgid)
		}
		return processExists(pid)
	}

	if err := sendSignal(syscall.SIGTERM); err != nil {
		if err := sendSignal(syscall.SIGKILL); err != nil {
			return err
		}
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !stillRunning() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	if err := sendSignal(syscall.SIGKILL); err != nil {
		return err
	}

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if !stillRunning() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	if stillRunning() {
		if useProcessGroup && pgid > 0 {
			return fmt.Errorf("process group %d still running after SIGKILL", pgid)
		}
		return fmt.Errorf("pid %d still running after SIGKILL", pid)
	}
	return nil
}

func processGroupExists(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	err := syscall.Kill(-pgid, 0)
	return err == nil || err == syscall.EPERM
}

// IsRunning returns true when the named process exists and hasn't exited.
func (pm *ProcessManager) IsRunning(name string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	p, ok := pm.procs[name]
	return ok && p.cmd.ProcessState == nil
}

// StatusAll returns the status of every tracked process.
func (pm *ProcessManager) StatusAll() map[string]ProcStatus {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	result := make(map[string]ProcStatus, len(pm.procs))
	for name, p := range pm.procs {
		running := p.cmd.ProcessState == nil
		pid := 0
		if running && p.cmd.Process != nil {
			pid = p.cmd.Process.Pid
		}
		result[name] = ProcStatus{
			Name:      name,
			Running:   running,
			PID:       pid,
			StartedAt: p.startedAt,
		}
	}
	return result
}

// ── named-component helpers ─────────────────────────────────────────────────

// StartEngine starts the Bun/TypeScript engine distributor.
func (pm *ProcessManager) StartEngine() error {
	if !pm.IsAvailable() {
		return fmt.Errorf("VYRA_ROOT is not configured")
	}
	engineDir := filepath.Join(pm.rootDir, "engine")
	if err := pm.Start("engine", "bun", []string{"run", "index.ts"}, engineDir, nil); err != nil {
		return err
	}
	if strings.TrimSpace(pm.enginePort) == "" {
		return nil
	}
	if err := waitForPortListening(pm.enginePort, engineReadyTimeout); err != nil {
		_ = pm.Stop("engine")
		return fmt.Errorf("engine failed to bind port %s: %w", pm.enginePort, err)
	}
	return nil
}

// ensureEngineReady guarantees the engine is available before starting worker pipelines.
// This prevents worker/adapter boots from entering a reconnect loop after relay restarts.
func (pm *ProcessManager) ensureEngineReady() error {
	if !pm.IsAvailable() {
		return fmt.Errorf("VYRA_ROOT is not configured")
	}
	if pm.enginePort != "" && isPortListening(pm.enginePort) {
		return nil
	}
	pm.mu.Lock()
	autoStartEngine := pm.autoStartEng
	pm.mu.Unlock()
	if pm.IsRunning("engine") {
		if strings.TrimSpace(pm.enginePort) == "" {
			return nil
		}
		if err := waitForPortListening(pm.enginePort, engineReadyTimeout); err != nil {
			return fmt.Errorf("engine process is running but port %s is not ready: %w", pm.enginePort, err)
		}
		return nil
	}
	if pm.enginePort != "" && isPortListening(pm.enginePort) {
		log.Printf("[ProcessManager] Engine already listening on port %s (external process), skipping start", pm.enginePort)
		return nil
	}
	if !autoStartEngine {
		return fmt.Errorf("engine is not running and PROCESS_AUTO_START_ENGINE is disabled")
	}
	if err := pm.StartEngine(); err != nil {
		return fmt.Errorf("start engine: %w", err)
	}
	return nil
}

// StartQueryService starts the Python FastAPI query_service on port 5002.
func (pm *ProcessManager) StartQueryService() error {
	if !pm.IsAvailable() {
		return fmt.Errorf("VYRA_ROOT is not configured")
	}
	pyDir := filepath.Join(pm.rootDir, "engine", "py")
	if err := pm.startPythonScript("query_service", pyDir, "query_service.py", map[string]string{
		"QUERY_SERVICE_PORT": "5002",
	}); err != nil {
		return err
	}
	if err := waitForHTTPReady("http://127.0.0.1:5002/health", 6*time.Second); err != nil {
		_ = pm.Stop("query_service")
		return fmt.Errorf("query_service failed health check: %w", err)
	}
	return nil
}

// StartLLMWorker starts the local Ask-AI worker service on port 5003.
func (pm *ProcessManager) StartLLMWorker(modelID string) error {
	if !pm.IsAvailable() {
		return fmt.Errorf("VYRA_ROOT is not configured")
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		modelID = pm.llmModelID
	}
	pyDir := filepath.Join(pm.rootDir, "engine", "py")
	env := map[string]string{
		"LLM_WORKER_PORT":             "5003",
		"LLM_MODEL_ID":                modelID,
		"MAX_ANSWER_TOKENS":           "120000",
		"LLM_MAX_NEW_TOKENS_SOFT":     "512",
		"LLM_GENERATION_MAX_TIME_SEC": "10",
	}
	if err := pm.startPythonScript("llm_query", pyDir, "worker_query_llm.py", env); err != nil {
		pm.markLLMStartFailure()
		return err
	}
	if err := waitForHTTPReady("http://127.0.0.1:5003/health", 8*time.Second); err != nil {
		_ = pm.Stop("llm_query")
		pm.markLLMStartFailure()
		return fmt.Errorf("llm worker failed health check: %w", err)
	}
	if err := triggerHTTPWarmup("http://127.0.0.1:5003/warmup", 2*time.Second); err != nil {
		log.Printf("[ProcessManager] llm warmup trigger failed: %v", err)
	}
	if err := pm.EnsureRAGWorkers(); err != nil {
		log.Printf("[ProcessManager] RAG worker startup degraded: %v", err)
	}
	pm.mu.Lock()
	pm.llmModelID = modelID
	pm.llmDesired = true
	pm.llmFailCount = 0
	pm.llmNextRetry = time.Time{}
	pm.mu.Unlock()
	return nil
}

func (pm *ProcessManager) markLLMStartFailure() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.llmDesired = true
	pm.llmFailCount++
	backoff := time.Duration(1<<min(pm.llmFailCount, 5)) * time.Second
	if backoff > 60*time.Second {
		backoff = 60 * time.Second
	}
	pm.llmNextRetry = time.Now().Add(backoff)
}

func waitForHTTPReady(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 1200 * time.Millisecond}
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(250 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timeout")
	}
	return lastErr
}

func triggerHTTPWarmup(url string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0111 != 0
}

func localVenvPython(workDir string) string {
	venvPy := filepath.Join(workDir, ".venv", "bin", "python")
	if isExecutable(venvPy) {
		return venvPy
	}
	return ""
}

func preferredPythonBinary(workDir string) string {
	if configured := strings.TrimSpace(os.Getenv("VYRA_PYTHON_BIN")); configured != "" && isExecutable(configured) {
		return configured
	}
	if venvPy := localVenvPython(workDir); venvPy != "" {
		return venvPy
	}
	if py3, err := exec.LookPath("python3"); err == nil {
		return py3
	}
	if py, err := exec.LookPath("python"); err == nil {
		return py
	}
	return ""
}

func (pm *ProcessManager) startPythonScript(name, workDir, script string, extraEnv map[string]string) error {
	if _, err := os.Stat(filepath.Join(workDir, script)); err != nil {
		return fmt.Errorf("python script %q missing: %w", script, err)
	}
	if py := preferredPythonBinary(workDir); py != "" {
		return pm.Start(name, py, []string{script}, workDir, extraEnv)
	}
	if _, err := exec.LookPath("uv"); err == nil {
		// Last-resort fallback. `--no-sync` avoids dependency resolution/network lookups.
		return pm.Start(name, "uv", []string{"run", "--no-sync", "python", script}, workDir, extraEnv)
	}
	return fmt.Errorf("no python runtime found (tried project .venv, VYRA_PYTHON_BIN, python3, python, uv)")
}

func (pm *ProcessManager) startPythonModule(name, workDir, module string, extraEnv map[string]string) error {
	module = strings.TrimSpace(module)
	if module == "" {
		return fmt.Errorf("python module is required")
	}
	if py := preferredPythonBinary(workDir); py != "" {
		return pm.Start(name, py, []string{"-m", module}, workDir, extraEnv)
	}
	if _, err := exec.LookPath("uv"); err == nil {
		// Last-resort fallback. `--no-sync` avoids dependency resolution/network lookups.
		return pm.Start(name, "uv", []string{"run", "--no-sync", "python", "-m", module}, workDir, extraEnv)
	}
	return fmt.Errorf("no python runtime found (tried project .venv, VYRA_PYTHON_BIN, python3, python, uv)")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (pm *ProcessManager) resetProcessFailure(name string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.procFailCount[name] = 0
	pm.procNextRetry[name] = time.Time{}
	delete(pm.procStopReq, name)
}

func (pm *ProcessManager) StopLLMWorker() error {
	pm.mu.Lock()
	pm.llmDesired = false
	pm.llmFailCount = 0
	pm.llmNextRetry = time.Time{}
	pm.mu.Unlock()
	var errs []string
	for _, name := range []string{"llm_query", processRAGReranker, processFastEmbedding} {
		if err := pm.Stop(name); err != nil && !strings.Contains(err.Error(), "not found") {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (pm *ProcessManager) LLMModelID() string {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.llmModelID
}

func (pm *ProcessManager) SetLLMModelID(modelID string) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return
	}
	pm.mu.Lock()
	pm.llmModelID = modelID
	pm.mu.Unlock()
}

func (pm *ProcessManager) LLMDesired() bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.llmDesired
}

func (pm *ProcessManager) EnsureLLMWorker() error {
	pm.mu.Lock()
	if !pm.llmDesired {
		pm.mu.Unlock()
		return nil
	}
	now := time.Now()
	if !pm.llmNextRetry.IsZero() && now.Before(pm.llmNextRetry) {
		pm.mu.Unlock()
		return nil
	}
	modelID := pm.llmModelID
	pm.mu.Unlock()

	if pm.IsRunning("llm_query") {
		if err := pm.EnsureRAGWorkers(); err != nil {
			log.Printf("[ProcessManager] RAG worker reconcile degraded: %v", err)
		}
		return nil
	}
	if health := readLLMWorkerHealth(); health != nil {
		// External worker mode (e.g. docker-compose llm profile) can satisfy Ask-AI.
		return nil
	}
	return pm.StartLLMWorker(modelID)
}

func (pm *ProcessManager) EnsureRAGWorkers() error {
	if !pm.IsAvailable() {
		return fmt.Errorf("VYRA_ROOT is not configured")
	}
	if err := pm.ensureEngineReady(); err != nil {
		return err
	}

	var errs []string
	if parseBoolEnv(os.Getenv("RAG_ENABLE_EMBEDDINGS"), true) {
		if err := pm.startEngineWorker(processFastEmbedding, "worker_embedding", map[string]string{
			"WORKER_TYPE":              "fast_embedding",
			"MAX_LATENCY_MS":           "250",
			"MAX_BATCH_SIZE":           "32",
			"RAG_TEXT_EMBEDDING_MODEL": firstNonEmpty(os.Getenv("RAG_TEXT_EMBEDDING_MODEL"), "BAAI/bge-small-en-v1.5"),
		}); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if parseBoolEnv(os.Getenv("RAG_ENABLE_RERANKER"), true) {
		if err := pm.startEngineWorker(processRAGReranker, "worker_rag_reranker", map[string]string{
			"WORKER_TYPE":        "rag_reranker",
			"MAX_LATENCY_MS":     "250",
			"MAX_BATCH_SIZE":     "8",
			"RAG_RERANKER_MODEL": firstNonEmpty(os.Getenv("RAG_RERANKER_MODEL"), "cross-encoder/ms-marco-MiniLM-L-6-v2"),
		}); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (pm *ProcessManager) startEngineWorker(name, module string, overrides map[string]string) error {
	if pm.IsRunning(name) {
		return nil
	}
	pyDir := filepath.Join(pm.rootDir, "engine", "py")
	env := pm.loadDotEnv(filepath.Join(pm.rootDir, "engine", ".env"))
	env["WORKER_SECRET"] = pm.workerSecret
	env["BACKEND_WS_URL"] = fmt.Sprintf("ws://localhost:%s/ws", pm.enginePort)
	for key, value := range overrides {
		env[key] = value
	}
	return pm.startPythonModule(name, pyDir, module, env)
}

// workerType maps a canonical type string to the python module name.
var workerModule = map[string]string{
	"ppe":    "worker_ppe",
	"vlm":    "worker_vlm",
	"objdet": "worker_object_detection",
	"face":   "worker_face_recognition",
}

var workerEnvOverrides = map[string]map[string]string{
	"ppe": {
		"WORKER_TYPE":    "ppe_detection",
		"MAX_LATENCY_MS": "60",
		"MAX_BATCH_SIZE": "4",
		// VLM and face recognition are handled by shared worker pools.
		// Detection workers stay lightweight (YOLO + tracking only).
		"PPE_ENABLE_VLM":              "0",
		"PPE_ENABLE_FACE_RECOGNITION": "0",
	},
	"vlm": {"WORKER_TYPE": "vlm", "MAX_LATENCY_MS": "5000", "MAX_BATCH_SIZE": "32"},
	"objdet": {
		"WORKER_TYPE":    "object_detection",
		"MAX_LATENCY_MS": "60",
		"MAX_BATCH_SIZE": "4",
		// Bias the realtime person detector toward recall. The worker still
		// drops stale frames upstream, so higher resolution is preferable to
		// missing people in the live overlay.
		"OBJECT_DET_CONF":        "0.18",
		"OBJECT_DET_IOU":         "0.55",
		"OBJECT_DET_IMG_SIZE":    "960",
		"OBJECT_DET_MAX_DET":     "300",
		"OBJECT_DET_PERSON_ONLY": "1",
		"OBJECT_DET_HALF":        "1",
		// Event/snapshot gate: live overlays can be tentative, but timeline
		// incidents must be stable enough to avoid chair/curtain false positives.
		"OBJECT_DET_EVENT_CONF":             "0.35",
		"OBJECT_DET_EVENT_MIN_HITS":         "3",
		"OBJECT_DET_EVENT_MIN_HEIGHT_RATIO": "0.08",
		"OBJECT_DET_EVENT_MIN_AREA_RATIO":   "0.002",
		// VLM and face recognition are handled by shared worker pools.
		"OBJECT_DET_ENABLE_VLM":              "0",
		"OBJECT_DET_ENABLE_FACE_RECOGNITION": "0",
		// Global person IDs still come from the relay identity service, but
		// ReID runs off the frame loop so live boxes never wait on identity.
		"PERSON_IDENTITY_SYNC_RESOLVE":      "0",
		"PERSON_IDENTITY_EMBEDDING_ENABLED": "1",
	},
	"face": {
		"WORKER_TYPE":    "face_recognition",
		"MAX_LATENCY_MS": "50",
		"MAX_BATCH_SIZE": "1",
	},
}

// agentDependencies defines which shared worker pools each agent type requires.
// When scaling up a detection agent, its dependency pools are auto-started.
var agentDependencies = map[string][]string{
	"ppe":    {"vlm", "face"},
	"objdet": {"vlm", "face"},
	"vlm":    {},
	"face":   {},
}

func agentDependencyTypes(wtype string) []string {
	deps := agentDependencies[strings.TrimSpace(strings.ToLower(wtype))]
	if len(deps) == 0 {
		return nil
	}
	includeVLM := parseBoolEnv(os.Getenv("PROCESS_AUTO_START_VLM_DEPENDENCIES"), false)
	out := make([]string, 0, len(deps))
	for _, dep := range deps {
		if dep == "vlm" && !includeVLM {
			continue
		}
		out = append(out, dep)
	}
	return out
}

var adapterEnvOverrides = map[string]map[string]string{
	"ppe": {
		"WORKER_ID":              "vyra/ppe_detection",
		"ENGINE_WORKER_TYPE":     "ppe_detection",
		"MIN_SUBMIT_INTERVAL_MS": "0",
		"MAX_IN_FLIGHT":          "2",
		"MAX_QUEUE_SIZE":         "4",
		"IN_FLIGHT_TIMEOUT_MS":   "30000",
		"MAX_JOB_RETRIES":        "1",
	},
	"vlm": {
		"WORKER_ID":              "vyra/vlm",
		"ENGINE_WORKER_TYPE":     "vlm",
		"MIN_SUBMIT_INTERVAL_MS": "200",
		"MAX_IN_FLIGHT":          "4",
		"MAX_QUEUE_SIZE":         "48",
		"IN_FLIGHT_TIMEOUT_MS":   "60000",
		"MAX_JOB_RETRIES":        "1",
	},
	"objdet": {
		"WORKER_ID":              "vyra/person_detection",
		"ENGINE_WORKER_TYPE":     "object_detection",
		"MIN_SUBMIT_INTERVAL_MS": "0",
		"MAX_IN_FLIGHT":          "2",
		"MAX_QUEUE_SIZE":         "4",
		"IN_FLIGHT_TIMEOUT_MS":   "30000",
		"MAX_JOB_RETRIES":        "1",
	},
	"face": {
		"WORKER_ID":              "vyra/face_recognition",
		"ENGINE_WORKER_TYPE":     "face_recognition",
		"MIN_SUBMIT_INTERVAL_MS": "0",
		"MAX_IN_FLIGHT":          "2",
		"MAX_QUEUE_SIZE":         "16",
		"IN_FLIGHT_TIMEOUT_MS":   "60000",
		"MAX_JOB_RETRIES":        "1",
	},
}

// StartWorker starts a Python detection worker for the given type ("ppe", "vlm", "objdet").
func (pm *ProcessManager) StartWorker(wtype string) error {
	if !pm.IsAvailable() {
		return fmt.Errorf("VYRA_ROOT is not configured")
	}
	if err := pm.ensureEngineReady(); err != nil {
		return err
	}
	module, ok := workerModule[wtype]
	if !ok {
		return fmt.Errorf("unknown worker type %q", wtype)
	}
	name := "worker-" + wtype
	if p, exists := pm.procs[name]; exists && p.cmd.ProcessState == nil {
		// Backward compatible behavior: if legacy replica-1 name is busy, start next replica.
		name = pm.nextReplicaName("worker", wtype)
	}
	pyDir := filepath.Join(pm.rootDir, "engine", "py")
	envDotFile := filepath.Join(pm.rootDir, "engine", ".env")

	env := pm.loadDotEnv(envDotFile)
	env["WORKER_SECRET"] = pm.workerSecret
	// Workers call /persons/* endpoints for face registry/profile fetches.
	// Those routes are served by the HTTP API port (apiPort), not relayPort.
	env["RELAY_HTTP_BASE"] = fmt.Sprintf("http://localhost:%s", pm.apiPort)
	for k, v := range workerEnvOverrides[wtype] {
		env[k] = v
	}

	return pm.startPythonModule(name, pyDir, module, env)
}

// StartAdapter starts the Bun adapter for the given type.
func (pm *ProcessManager) StartAdapter(wtype string) error {
	if !pm.IsAvailable() {
		return fmt.Errorf("VYRA_ROOT is not configured")
	}
	if err := pm.ensureEngineReady(); err != nil {
		return err
	}
	overrides, ok := adapterEnvOverrides[wtype]
	if !ok {
		return fmt.Errorf("unknown adapter type %q", wtype)
	}
	name := "adapter-" + wtype
	if p, exists := pm.procs[name]; exists && p.cmd.ProcessState == nil {
		// Backward compatible behavior: if legacy replica-1 name is busy, start next replica.
		name = pm.nextReplicaName("adapter", wtype)
	}
	engineDir := filepath.Join(pm.rootDir, "engine")

	env := make(map[string]string)
	for k, v := range overrides {
		env[k] = v
	}
	env["RELAY_WS_URL"] = fmt.Sprintf("ws://localhost:%s/worker/connect", pm.relayPort)
	// Frames (/worker/frames/) are served by the relay WebSocket server on relayPort.
	// Snapshot uploads (/cv/upload-snapshot) are also on relayPort.
	// Do NOT use apiPort (8020) here — /worker/frames/ is not exposed there.
	env["RELAY_HTTP_BASE"] = fmt.Sprintf("http://localhost:%s", pm.relayPort)
	env["ENGINE_WS_URL"] = fmt.Sprintf("ws://localhost:%s/ws", pm.enginePort)

	return pm.Start(name, "bun", []string{"run", "adapter"}, engineDir, env)
}

// WorkerReplicaInfo describes one worker/adapter process replica.
type WorkerReplicaInfo struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	PID     int    `json:"pid,omitempty"`
}

// WorkerPoolInfo is returned by worker inventory APIs.
type WorkerPoolInfo struct {
	Desired  int                 `json:"desired"`
	Workers  []WorkerReplicaInfo `json:"workers"`
	Adapters []WorkerReplicaInfo `json:"adapters"`
	Reason   string              `json:"reason,omitempty"`
	// Runtime telemetry from CVWorkerRegistry (populated by handleSystemWorkers)
	ConnectedWorkers int    `json:"connected_workers"`
	TotalDropped     uint64 `json:"total_dropped"`
	QueueFillPct     int    `json:"queue_fill_pct"` // 0-100
}

// CustomWorkerPoolInfo describes one dedicated custom runtime worker pool.
type CustomWorkerPoolInfo struct {
	AgentID    string              `json:"agent_id"`
	WorkerID   string              `json:"worker_id"`
	Desired    int                 `json:"desired"`
	Workers    []WorkerReplicaInfo `json:"workers"`
	Adapters   []WorkerReplicaInfo `json:"adapters"`
	EnableVLM  bool                `json:"enable_vlm"`
	EnableFace bool                `json:"enable_face"`
	ModelPath  string              `json:"model_path,omitempty"`
}

func canonicalReplicaName(kind, wtype string, replica int) string {
	if replica <= 1 {
		return kind + "-" + wtype
	}
	return fmt.Sprintf("%s-%s-%d", kind, wtype, replica)
}

func customReplicaName(kind, agentID string, replica int) string {
	return fmt.Sprintf("%s-custom-%s-%d", kind, agentID, replica)
}

func parseReplicaIndex(kind, wtype, name string) (int, bool) {
	base := kind + "-" + wtype
	if name == base {
		return 1, true
	}
	prefix := base + "-"
	if !strings.HasPrefix(name, prefix) {
		return 0, false
	}
	var idx int
	if _, err := fmt.Sscanf(strings.TrimPrefix(name, prefix), "%d", &idx); err != nil || idx <= 1 {
		return 0, false
	}
	return idx, true
}

func parseCustomReplica(kind, name string) (agentID string, replica int, ok bool) {
	prefix := kind + "-custom-"
	if !strings.HasPrefix(name, prefix) {
		return "", 0, false
	}
	rest := strings.TrimPrefix(name, prefix)
	cut := strings.LastIndex(rest, "-")
	if cut <= 0 || cut >= len(rest)-1 {
		return "", 0, false
	}
	agentID = rest[:cut]
	replica, err := strconv.Atoi(rest[cut+1:])
	if err != nil || replica <= 0 {
		return "", 0, false
	}
	return agentID, replica, true
}

func (pm *ProcessManager) nextReplicaName(kind, wtype string) string {
	used := make(map[int]bool)
	prefix := kind + "-" + wtype
	for name := range pm.procs {
		if name == prefix || strings.HasPrefix(name, prefix+"-") {
			if idx, ok := parseReplicaIndex(kind, wtype, name); ok {
				used[idx] = true
			}
		}
	}
	for i := 1; i <= 128; i++ {
		if !used[i] {
			return canonicalReplicaName(kind, wtype, i)
		}
	}
	return canonicalReplicaName(kind, wtype, len(used)+1)
}

func (pm *ProcessManager) startWorkerReplica(wtype string, replica int) error {
	module, ok := workerModule[wtype]
	if !ok {
		return fmt.Errorf("unknown worker type %q", wtype)
	}
	name := canonicalReplicaName("worker", wtype, replica)
	pyDir := filepath.Join(pm.rootDir, "engine", "py")
	envDotFile := filepath.Join(pm.rootDir, "engine", ".env")
	env := pm.loadDotEnv(envDotFile)
	env["WORKER_SECRET"] = pm.workerSecret
	// Workers call /persons/* endpoints for face registry/profile fetches.
	// Those routes are served by the HTTP API port (apiPort), not relayPort.
	env["RELAY_HTTP_BASE"] = fmt.Sprintf("http://localhost:%s", pm.apiPort)
	for k, v := range workerEnvOverrides[wtype] {
		env[k] = v
	}
	return pm.startPythonModule(name, pyDir, module, env)
}

func (pm *ProcessManager) startAdapterReplica(wtype string, replica int) error {
	overrides, ok := adapterEnvOverrides[wtype]
	if !ok {
		return fmt.Errorf("unknown adapter type %q", wtype)
	}
	name := canonicalReplicaName("adapter", wtype, replica)
	engineDir := filepath.Join(pm.rootDir, "engine")
	env := make(map[string]string)
	for k, v := range overrides {
		env[k] = v
	}
	env["RELAY_WS_URL"] = fmt.Sprintf("ws://localhost:%s/worker/connect", pm.relayPort)
	env["RELAY_HTTP_BASE"] = fmt.Sprintf("http://localhost:%s", pm.relayPort)
	env["ENGINE_WS_URL"] = fmt.Sprintf("ws://localhost:%s/ws", pm.enginePort)
	return pm.Start(name, "bun", []string{"run", "adapter"}, engineDir, env)
}

func (pm *ProcessManager) startCustomWorkerReplica(agentID string, replica int, opts CustomRuntimeOptions) error {
	name := customReplicaName("worker", agentID, replica)
	pyDir := filepath.Join(pm.rootDir, "engine", "py")
	envDotFile := filepath.Join(pm.rootDir, "engine", ".env")
	env := pm.loadDotEnv(envDotFile)
	env["WORKER_SECRET"] = pm.workerSecret
	// Workers call /persons/* endpoints for face registry/profile fetches.
	env["RELAY_HTTP_BASE"] = fmt.Sprintf("http://localhost:%s", pm.apiPort)
	for k, v := range workerEnvOverrides["objdet"] {
		env[k] = v
	}
	if opts.EnableVLM {
		env["OBJECT_DET_ENABLE_VLM"] = "1"
	} else {
		env["OBJECT_DET_ENABLE_VLM"] = "0"
	}
	if opts.EnableFace {
		env["OBJECT_DET_ENABLE_FACE_RECOGNITION"] = "1"
	} else {
		env["OBJECT_DET_ENABLE_FACE_RECOGNITION"] = "0"
	}
	env["CUSTOM_AGENT_ID"] = agentID
	env["CUSTOM_YOLO_ENABLED"] = "1"
	if strings.TrimSpace(opts.ModelPath) != "" {
		env["CUSTOM_YOLO_MODEL_PATH"] = opts.ModelPath
	}

	return pm.startPythonModule(name, pyDir, workerModule["objdet"], env)
}

func (pm *ProcessManager) startCustomAdapterReplica(agentID string, replica int, opts CustomRuntimeOptions) error {
	name := customReplicaName("adapter", agentID, replica)
	engineDir := filepath.Join(pm.rootDir, "engine")

	env := map[string]string{
		"WORKER_ID":              "vyra/custom_detection/" + agentID,
		"ENGINE_WORKER_TYPE":     "object_detection",
		"MIN_SUBMIT_INTERVAL_MS": "0",
		"MAX_IN_FLIGHT":          "2",
		"MAX_QUEUE_SIZE":         "4",
		"IN_FLIGHT_TIMEOUT_MS":   "30000",
		"MAX_JOB_RETRIES":        "1",
	}
	env["RELAY_WS_URL"] = fmt.Sprintf("ws://localhost:%s/worker/connect", pm.relayPort)
	env["RELAY_HTTP_BASE"] = fmt.Sprintf("http://localhost:%s", pm.relayPort)
	env["ENGINE_WS_URL"] = fmt.Sprintf("ws://localhost:%s/ws", pm.enginePort)
	env["CUSTOM_AGENT_ID"] = agentID
	env["CUSTOM_YOLO_ENABLED"] = "1"
	if strings.TrimSpace(opts.ModelPath) != "" {
		env["CUSTOM_MODEL_PATH"] = opts.ModelPath
	}
	if opts.EnableVLM {
		env["CUSTOM_ENABLE_VLM"] = "1"
	} else {
		env["CUSTOM_ENABLE_VLM"] = "0"
	}
	if opts.EnableFace {
		env["CUSTOM_ENABLE_FACE"] = "1"
	} else {
		env["CUSTOM_ENABLE_FACE"] = "0"
	}

	return pm.Start(name, "bun", []string{"run", "adapter"}, engineDir, env)
}

// ScaleCustomAgentRuntime reconciles dedicated worker+adapter replicas for one custom agent.
// When opts is nil, previously stored runtime options are reused.
func (pm *ProcessManager) ScaleCustomAgentRuntime(agentID string, replicas int, opts *CustomRuntimeOptions) error {
	if !pm.IsAvailable() {
		return fmt.Errorf("VYRA_ROOT is not configured")
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return fmt.Errorf("agent_id is required")
	}
	if replicas < 0 || replicas > 16 {
		return fmt.Errorf("replicas out of range: %d (allowed 0..16)", replicas)
	}
	if replicas > 0 {
		if err := pm.ensureEngineReady(); err != nil {
			return err
		}
	}

	pm.mu.Lock()
	state, exists := pm.customRuntime[agentID]
	if !exists {
		state = &customRuntimeState{
			Desired: 0,
			Options: CustomRuntimeOptions{},
		}
		pm.customRuntime[agentID] = state
	}
	if opts != nil {
		state.Options = *opts
	}
	currentOpts := state.Options
	if strings.TrimSpace(currentOpts.ModelPath) == "" && replicas > 0 {
		pm.mu.Unlock()
		return fmt.Errorf("custom runtime model path is required")
	}
	pm.mu.Unlock()

	for i := 16; i >= replicas+1; i-- {
		aName := customReplicaName("adapter", agentID, i)
		wName := customReplicaName("worker", agentID, i)
		_ = pm.Stop(aName)
		_ = pm.Stop(wName)
		pm.resetProcessFailure(aName)
		pm.resetProcessFailure(wName)
	}
	if replicas == 0 {
		aName := customReplicaName("adapter", agentID, 1)
		wName := customReplicaName("worker", agentID, 1)
		_ = pm.Stop(aName)
		_ = pm.Stop(wName)
		pm.resetProcessFailure(aName)
		pm.resetProcessFailure(wName)
		pm.mu.Lock()
		state := pm.customRuntime[agentID]
		if state != nil {
			state.Desired = 0
		}
		pm.mu.Unlock()
		return nil
	}

	for i := 1; i <= replicas; i++ {
		wName := customReplicaName("worker", agentID, i)
		if !pm.IsRunning(wName) {
			if err := pm.startCustomWorkerReplica(agentID, i, currentOpts); err != nil {
				return err
			}
		}
		aName := customReplicaName("adapter", agentID, i)
		if !pm.IsRunning(aName) {
			if err := pm.startCustomAdapterReplica(agentID, i, currentOpts); err != nil {
				return err
			}
		}
	}

	pm.mu.Lock()
	state = pm.customRuntime[agentID]
	if state == nil {
		state = &customRuntimeState{}
		pm.customRuntime[agentID] = state
	}
	state.Desired = replicas
	state.Options = currentOpts
	pm.mu.Unlock()
	return nil
}

func (pm *ProcessManager) ScaleCustomAgentReplicas(agentID string, replicas int) error {
	return pm.ScaleCustomAgentRuntime(agentID, replicas, nil)
}

func (pm *ProcessManager) StopCustomAgentRuntime(agentID string) error {
	return pm.ScaleCustomAgentRuntime(agentID, 0, nil)
}

func (pm *ProcessManager) CustomDesiredScale(agentID string) int {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if st, ok := pm.customRuntime[agentID]; ok {
		return st.Desired
	}
	return 0
}

func (pm *ProcessManager) CustomRuntimeOptions(agentID string) (CustomRuntimeOptions, bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	st, ok := pm.customRuntime[agentID]
	if !ok {
		return CustomRuntimeOptions{}, false
	}
	return st.Options, true
}

// ScaleWorkers reconciles both worker and adapter replicas for a type.
func (pm *ProcessManager) ScaleWorkers(wtype string, replicas int) error {
	if !pm.IsAvailable() {
		return fmt.Errorf("VYRA_ROOT is not configured")
	}
	if _, ok := workerModule[wtype]; !ok {
		return fmt.Errorf("unknown worker type %q", wtype)
	}
	if replicas < 0 || replicas > 16 {
		return fmt.Errorf("replicas out of range: %d (allowed 0..16)", replicas)
	}
	if replicas > 0 {
		if err := pm.ensureEngineReady(); err != nil {
			return err
		}
	}

	// Stop extra replicas first (highest index down), then start missing ones.
	for i := 16; i >= replicas+1; i-- {
		aName := canonicalReplicaName("adapter", wtype, i)
		wName := canonicalReplicaName("worker", wtype, i)
		_ = pm.Stop(aName)
		_ = pm.Stop(wName)
		pm.resetProcessFailure(aName)
		pm.resetProcessFailure(wName)
	}
	if replicas == 0 {
		aName := canonicalReplicaName("adapter", wtype, 1)
		wName := canonicalReplicaName("worker", wtype, 1)
		_ = pm.Stop(aName)
		_ = pm.Stop(wName)
		pm.resetProcessFailure(aName)
		pm.resetProcessFailure(wName)
		pm.mu.Lock()
		pm.desiredScale[wtype] = replicas
		pm.mu.Unlock()
		return nil
	}

	for i := 1; i <= replicas; i++ {
		wName := canonicalReplicaName("worker", wtype, i)
		if !pm.IsRunning(wName) {
			if err := pm.startWorkerReplica(wtype, i); err != nil {
				return err
			}
		}
		aName := canonicalReplicaName("adapter", wtype, i)
		if !pm.IsRunning(aName) {
			if err := pm.startAdapterReplica(wtype, i); err != nil {
				return err
			}
		}
	}

	pm.mu.Lock()
	pm.desiredScale[wtype] = replicas
	pm.mu.Unlock()
	return nil
}

// WorkerInventory returns current worker/adapter replicas by type.
func (pm *ProcessManager) WorkerInventory() map[string]WorkerPoolInfo {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	out := map[string]WorkerPoolInfo{
		"ppe":    {Desired: pm.desiredScale["ppe"]},
		"vlm":    {Desired: pm.desiredScale["vlm"]},
		"objdet": {Desired: pm.desiredScale["objdet"]},
		"face":   {Desired: pm.desiredScale["face"]},
	}

	for name, p := range pm.procs {
		running := p.cmd.ProcessState == nil
		pid := 0
		if running && p.cmd.Process != nil {
			pid = p.cmd.Process.Pid
		}
		info := WorkerReplicaInfo{Name: name, Running: running, PID: pid}
		for _, wtype := range []string{"ppe", "vlm", "objdet", "face"} {
			if _, ok := parseReplicaIndex("worker", wtype, name); ok {
				p := out[wtype]
				p.Workers = append(p.Workers, info)
				out[wtype] = p
				break
			}
			if _, ok := parseReplicaIndex("adapter", wtype, name); ok {
				p := out[wtype]
				p.Adapters = append(p.Adapters, info)
				out[wtype] = p
				break
			}
		}
	}

	for k, v := range out {
		sort.Slice(v.Workers, func(i, j int) bool { return v.Workers[i].Name < v.Workers[j].Name })
		sort.Slice(v.Adapters, func(i, j int) bool { return v.Adapters[i].Name < v.Adapters[j].Name })
		out[k] = v
	}
	return out
}

// CustomWorkerInventory returns dedicated runtime inventory keyed by custom agent ID.
func (pm *ProcessManager) CustomWorkerInventory() map[string]CustomWorkerPoolInfo {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	out := make(map[string]CustomWorkerPoolInfo, len(pm.customRuntime))
	for agentID, st := range pm.customRuntime {
		out[agentID] = CustomWorkerPoolInfo{
			AgentID:    agentID,
			WorkerID:   "vyra/custom_detection/" + agentID,
			Desired:    st.Desired,
			EnableVLM:  st.Options.EnableVLM,
			EnableFace: st.Options.EnableFace,
			ModelPath:  st.Options.ModelPath,
		}
	}

	for name, p := range pm.procs {
		running := p.cmd.ProcessState == nil
		pid := 0
		if running && p.cmd.Process != nil {
			pid = p.cmd.Process.Pid
		}
		info := WorkerReplicaInfo{Name: name, Running: running, PID: pid}

		if agentID, _, ok := parseCustomReplica("worker", name); ok {
			entry := out[agentID]
			entry.AgentID = agentID
			entry.WorkerID = "vyra/custom_detection/" + agentID
			entry.Workers = append(entry.Workers, info)
			out[agentID] = entry
			continue
		}
		if agentID, _, ok := parseCustomReplica("adapter", name); ok {
			entry := out[agentID]
			entry.AgentID = agentID
			entry.WorkerID = "vyra/custom_detection/" + agentID
			entry.Adapters = append(entry.Adapters, info)
			out[agentID] = entry
			continue
		}
	}

	for k, v := range out {
		sort.Slice(v.Workers, func(i, j int) bool { return v.Workers[i].Name < v.Workers[j].Name })
		sort.Slice(v.Adapters, func(i, j int) bool { return v.Adapters[i].Name < v.Adapters[j].Name })
		out[k] = v
	}
	return out
}

func (pm *ProcessManager) SetAutoScale(enabled bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.autoScale = enabled
}

func (pm *ProcessManager) AutoScaleEnabled() bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.autoScale
}

func (pm *ProcessManager) SetAutoScaleMax(max int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if max < 1 {
		max = 1
	}
	if max > 16 {
		max = 16
	}
	pm.autoMaxScale = max
}

func (pm *ProcessManager) AutoScaleMax() int {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.autoMaxScale
}

func (pm *ProcessManager) DesiredScale(wtype string) int {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.desiredScale[wtype]
}

type ProcessPolicy struct {
	SelfHealEnabled       bool `json:"self_heal_enabled"`
	AutoStartDependencies bool `json:"auto_start_dependencies"`
	AutoStartEngine       bool `json:"auto_start_engine"`
}

func (pm *ProcessManager) SetProcessPolicy(selfHealEnabled, autoStartDependencies, autoStartEngine bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.selfHeal = selfHealEnabled
	pm.autoStartDeps = autoStartDependencies
	pm.autoStartEng = autoStartEngine
}

func (pm *ProcessManager) ProcessPolicy() ProcessPolicy {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return ProcessPolicy{
		SelfHealEnabled:       pm.selfHeal,
		AutoStartDependencies: pm.autoStartDeps,
		AutoStartEngine:       pm.autoStartEng,
	}
}

func (pm *ProcessManager) AutoStartDependenciesEnabled() bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.autoStartDeps
}

func (pm *ProcessManager) clearDesiredRuntimeIntentsLocked() {
	for _, wtype := range []string{"ppe", "vlm", "objdet", "face"} {
		pm.desiredScale[wtype] = 0
	}
	for _, st := range pm.customRuntime {
		if st != nil {
			st.Desired = 0
		}
	}
	pm.llmDesired = false
	pm.llmFailCount = 0
	pm.llmNextRetry = time.Time{}
}

func (pm *ProcessManager) ClearDesiredRuntimeIntents() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.clearDesiredRuntimeIntentsLocked()
}

// StopAllManaged stops all tracked child processes and clears desired runtime intents.
func (pm *ProcessManager) StopAllManaged() error {
	pm.mu.Lock()
	pm.clearDesiredRuntimeIntentsLocked()
	names := make([]string, 0, len(pm.procs))
	for name := range pm.procs {
		names = append(names, name)
	}
	pm.mu.Unlock()

	sort.Slice(names, func(i, j int) bool {
		return stopPriority(names[i]) < stopPriority(names[j])
	})

	var errs []string
	for _, name := range names {
		if err := pm.Stop(name); err != nil && !strings.Contains(err.Error(), "not found") {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("stop all managed: %s", strings.Join(errs, "; "))
	}
	return nil
}

type processTableEntry struct {
	PID     int
	PPID    int
	PGID    int
	Cmdline string
	Cwd     string
}

type staleProcessTarget struct {
	PID             int
	PGID            int
	UseProcessGroup bool
}

// CleanupStaleManagedProcesses removes workers/adapters/engine/node processes
// that were left behind by a previous relay that was killed before it could run
// normal shutdown hooks. This intentionally only targets processes launched from
// this VYRA checkout.
func (pm *ProcessManager) CleanupStaleManagedProcesses() (int, error) {
	if !pm.IsAvailable() {
		return 0, nil
	}
	if runtime.GOOS != "linux" {
		return 0, nil
	}

	entries, err := listProcessTableEntries()
	if err != nil {
		return 0, err
	}
	targets := staleManagedProcessTargets(entries, pm.rootDir, pm.appDir, os.Getpid())
	if len(targets) == 0 {
		return 0, nil
	}

	killed := 0
	seenGroups := map[int]bool{}
	seenPIDs := map[int]bool{}
	for _, target := range targets {
		if target.UseProcessGroup && target.PGID > 0 {
			if seenGroups[target.PGID] {
				continue
			}
			seenGroups[target.PGID] = true
			if err := terminateManagedProcess(target.PID, target.PGID, true, 5*time.Second); err != nil {
				return killed, fmt.Errorf("terminate stale process group %d: %w", target.PGID, err)
			}
			killed++
			continue
		}

		if seenPIDs[target.PID] {
			continue
		}
		seenPIDs[target.PID] = true
		if err := terminatePID(target.PID, 5*time.Second); err != nil {
			return killed, fmt.Errorf("terminate stale pid %d: %w", target.PID, err)
		}
		killed++
	}

	return killed, nil
}

func staleManagedProcessTargets(entries []processTableEntry, rootDir, appDir string, currentPID int) []staleProcessTarget {
	descendants := descendantPIDSet(entries, currentPID)
	targets := make([]staleProcessTarget, 0, 8)
	seenGroup := map[int]bool{}
	seenPID := map[int]bool{}

	for _, entry := range entries {
		if entry.PID <= 0 || entry.PID == currentPID || descendants[entry.PID] {
			continue
		}
		if !isVYRAManagedProcess(entry, rootDir, appDir) {
			continue
		}

		useGroup := entry.PGID > 0 && entry.PGID != currentPID && !descendants[entry.PGID]
		if useGroup {
			if seenGroup[entry.PGID] {
				continue
			}
			seenGroup[entry.PGID] = true
		} else {
			if seenPID[entry.PID] {
				continue
			}
			seenPID[entry.PID] = true
		}

		targets = append(targets, staleProcessTarget{
			PID:             entry.PID,
			PGID:            entry.PGID,
			UseProcessGroup: useGroup,
		})
	}

	sort.Slice(targets, func(i, j int) bool {
		if targets[i].PGID == targets[j].PGID {
			return targets[i].PID < targets[j].PID
		}
		return targets[i].PGID < targets[j].PGID
	})
	return targets
}

func descendantPIDSet(entries []processTableEntry, pid int) map[int]bool {
	out := map[int]bool{}
	if pid <= 0 {
		return out
	}
	out[pid] = true
	changed := true
	for changed {
		changed = false
		for _, entry := range entries {
			if entry.PID <= 0 || out[entry.PID] {
				continue
			}
			if out[entry.PPID] {
				out[entry.PID] = true
				changed = true
			}
		}
	}
	return out
}

func isVYRAManagedProcess(entry processTableEntry, rootDir, appDir string) bool {
	cmdline := strings.TrimSpace(entry.Cmdline)
	if cmdline == "" {
		return false
	}

	root := cleanAbsPath(rootDir)
	if root == "" {
		return false
	}
	app := cleanAbsPath(appDir)
	cwd := cleanAbsPath(entry.Cwd)
	engineDir := filepath.Join(root, "engine")
	pyDir := filepath.Join(engineDir, "py")
	nodeDir := filepath.Join(root, "application", "node")

	lowerCmd := strings.ToLower(cmdline)
	switch {
	case isVYRAPythonWorkerCmd(lowerCmd):
		return pathWithin(cwd, pyDir) || strings.Contains(cmdline, pyDir)
	case isVYRAPythonManagedScriptCmd(lowerCmd):
		return pathWithin(cwd, pyDir)
	case isVYRAEngineOrAdapterCmd(lowerCmd):
		return pathWithin(cwd, engineDir)
	case isVYRANodeCmd(lowerCmd):
		if pathWithin(cwd, nodeDir) {
			return true
		}
		if app != "" && strings.Contains(cmdline, filepath.Join(app, "nodes")) {
			return true
		}
	}
	return false
}

func isVYRAPythonWorkerCmd(lowerCmd string) bool {
	return strings.Contains(lowerCmd, " -m worker_vlm") ||
		strings.Contains(lowerCmd, " -m worker_ppe") ||
		strings.Contains(lowerCmd, " -m worker_face_recognition") ||
		strings.Contains(lowerCmd, " -m worker_object_detection") ||
		strings.Contains(lowerCmd, " -m worker_embedding") ||
		strings.Contains(lowerCmd, " -m worker_rag_reranker")
}

func isVYRAPythonManagedScriptCmd(lowerCmd string) bool {
	return strings.Contains(lowerCmd, "worker_query_llm.py") ||
		strings.Contains(lowerCmd, "query_service.py")
}

func isVYRAEngineOrAdapterCmd(lowerCmd string) bool {
	return strings.Contains(lowerCmd, "bun run adapter") ||
		strings.Contains(lowerCmd, "relay_worker_adapter.ts") ||
		strings.Contains(lowerCmd, "bun run index.ts")
}

func isVYRANodeCmd(lowerCmd string) bool {
	return (strings.Contains(lowerCmd, "unblink") || strings.Contains(lowerCmd, "cmd/unblink")) &&
		strings.Contains(lowerCmd, "--config")
}

func listProcessTableEntries() ([]processTableEntry, error) {
	procEntries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}

	out := make([]processTableEntry, 0, len(procEntries))
	for _, procEntry := range procEntries {
		if !procEntry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(procEntry.Name())
		if err != nil {
			continue
		}
		cmdline, err := readCmdlineByPID(pid)
		if err != nil || strings.TrimSpace(cmdline) == "" {
			continue
		}
		ppid := readPPIDByPID(pid)
		pgid, err := syscall.Getpgid(pid)
		if err != nil {
			pgid = 0
		}
		cwd, _ := os.Readlink(filepath.Join("/proc", procEntry.Name(), "cwd"))
		out = append(out, processTableEntry{
			PID:     pid,
			PPID:    ppid,
			PGID:    pgid,
			Cmdline: cmdline,
			Cwd:     cwd,
		})
	}
	return out, nil
}

func readPPIDByPID(pid int) int {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return 0
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "PPid:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0
		}
		return ppid
	}
	return 0
}

func cleanAbsPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func pathWithin(path, base string) bool {
	path = cleanAbsPath(path)
	base = cleanAbsPath(base)
	if path == "" || base == "" {
		return false
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func stopPriority(name string) int {
	switch {
	case strings.HasPrefix(name, "worker-"), strings.HasPrefix(name, "adapter-"):
		return 1
	case strings.HasPrefix(name, "node-"):
		return 2
	case name == "llm_query", name == "query_service":
		return 3
	case name == "engine":
		return 4
	default:
		return 5
	}
}

// ── node management ─────────────────────────────────────────────────────────

// nodeConfigDir returns the directory where a node's config is stored.
func (pm *ProcessManager) nodeConfigDir(nodeID string) string {
	return filepath.Join(pm.appDir, "nodes", nodeID)
}

// nodeConfigPath returns the JSON config file path for the given node.
func (pm *ProcessManager) nodeConfigPath(nodeID string) string {
	return filepath.Join(pm.nodeConfigDir(nodeID), "config.json")
}

// nodeConfigData is a minimal representation of the node config JSON.
type nodeConfigData struct {
	RelayAddress string                   `json:"relay_address"`
	Token        string                   `json:"token,omitempty"`
	NodeID       string                   `json:"node_id,omitempty"`
	Reconnect    map[string]interface{}   `json:"reconnect"`
	Services     []map[string]interface{} `json:"services"`
}

// GenerateNodeConfig builds a JSON config for a node from assigned cameras.
// token is used when no existing config is present (first run).
// If an existing config is present its token and node_id fields are preserved.
func (pm *ProcessManager) GenerateNodeConfig(nodeID string, cameras []*Camera, token string) ([]byte, error) {
	cfgPath := pm.nodeConfigPath(nodeID)

	var existingToken, existingNodeID string
	if data, err := os.ReadFile(cfgPath); err == nil {
		var existing nodeConfigData
		if json.Unmarshal(data, &existing) == nil {
			existingToken = existing.Token
			existingNodeID = existing.NodeID
		}
	}

	// Use caller-supplied token when no persisted token is available yet
	if existingToken == "" && token != "" {
		existingToken = token
	}

	// Use the provided nodeID if not already stored
	if existingNodeID == "" {
		existingNodeID = nodeID
	}

	services := make([]map[string]interface{}, 0, len(cameras))
	for _, cam := range cameras {
		svc := map[string]interface{}{
			"name": cam.Name,
			"type": cam.Type,
			"addr": cam.Addr,
			"port": cam.Port,
			"path": cam.Path,
			"id":   cam.ID,
		}
		if cam.AuthUsername != "" {
			svc["auth"] = map[string]interface{}{
				"type":     "username_and_password",
				"username": cam.AuthUsername,
				"password": cam.AuthPassword,
			}
		}
		services = append(services, svc)
	}

	// Node requires at least one service; add a placeholder so it connects
	// even if no cameras are assigned yet.
	if len(services) == 0 {
		services = append(services, map[string]interface{}{
			"name": "placeholder",
			"type": "rtsp",
			"addr": "127.0.0.1",
			"port": 554,
			"path": "/",
			"id":   "placeholder-" + nodeID,
		})
	}

	cfg := nodeConfigData{
		RelayAddress: fmt.Sprintf("ws://localhost:%s/node/connect", pm.relayPort),
		Token:        existingToken,
		NodeID:       existingNodeID,
		Reconnect:    map[string]interface{}{"max_num_attempts": 50},
		Services:     services,
	}

	return json.MarshalIndent(cfg, "", "\t")
}

// StartNode generates a config for the node and spawns the node binary.
// token is the relay auth token for this node (pre-issued by the relay before calling StartNode).
func (pm *ProcessManager) StartNode(nodeID string, cameras []*Camera, token string) error {
	if !pm.IsAvailable() {
		return fmt.Errorf("VYRA_ROOT is not configured")
	}

	// Ensure config directory exists
	if err := os.MkdirAll(pm.nodeConfigDir(nodeID), 0755); err != nil {
		return fmt.Errorf("create node config dir: %w", err)
	}

	cfgData, err := pm.GenerateNodeConfig(nodeID, cameras, token)
	if err != nil {
		return fmt.Errorf("generate node config: %w", err)
	}
	cfgPath := pm.nodeConfigPath(nodeID)
	if err := os.WriteFile(cfgPath, cfgData, 0644); err != nil {
		return fmt.Errorf("write node config: %w", err)
	}
	log.Printf("[ProcessManager] Node config written: %s", cfgPath)

	nodeDir := filepath.Join(pm.rootDir, "application", "node")
	procName := "node-" + nodeID
	return pm.Start(procName, "go", []string{"run", "./cmd/unblink/", "--config", cfgPath}, nodeDir, nil)
}

// StopNode kills the running node process.
func (pm *ProcessManager) StopNode(nodeID string) error {
	name := "node-" + nodeID
	if err := pm.Stop(name); err != nil {
		// Process manager state is in-memory and can lose track across relay restarts.
		// Fall back to finding/killing untracked node processes by their config path.
		if !strings.Contains(err.Error(), "not found") {
			return err
		}
		killedAny, fallbackErr := pm.stopUntrackedNodeByConfig(nodeID)
		if fallbackErr != nil {
			return fallbackErr
		}
		if killedAny {
			log.Printf("[ProcessManager] Stopped untracked %q via config-path fallback", name)
		}
		// Idempotent stop: if nothing is running, treat as success.
		return nil
	}
	return nil
}

func (pm *ProcessManager) stopUntrackedNodeByConfig(nodeID string) (bool, error) {
	cfgPath := pm.nodeConfigPath(nodeID)
	pids, err := findPIDsByCmdlineSubstring(cfgPath)
	if err != nil {
		return false, fmt.Errorf("discover untracked node process for %q: %w", nodeID, err)
	}

	killedAny := false
	for _, pid := range pids {
		// Never target the relay process itself.
		if pid <= 0 || pid == os.Getpid() {
			continue
		}

		cmdline, err := readCmdlineByPID(pid)
		if err != nil || cmdline == "" {
			continue
		}

		if !isUnblinkNodeProcessForConfig(cmdline, cfgPath) {
			continue
		}

		if err := terminatePID(pid, 5*time.Second); err != nil {
			return killedAny, fmt.Errorf("stop untracked node pid %d: %w", pid, err)
		}
		killedAny = true
	}

	return killedAny, nil
}

func isUnblinkNodeProcessForConfig(cmdline, cfgPath string) bool {
	lower := strings.ToLower(cmdline)
	return strings.Contains(lower, "unblink") &&
		strings.Contains(cmdline, "--config") &&
		strings.Contains(cmdline, cfgPath)
}

func findPIDsByCmdlineSubstring(substr string) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}

	ids := make([]int, 0, 8)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		cmdline, err := readCmdlineByPID(pid)
		if err != nil || cmdline == "" {
			continue
		}
		if strings.Contains(cmdline, substr) {
			ids = append(ids, pid)
		}
	}

	sort.Ints(ids)
	return ids, nil
}

func readCmdlineByPID(pid int) (string, error) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", nil
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " ")), nil
}

func terminatePID(pid int, timeout time.Duration) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	if processExists(pid) {
		return fmt.Errorf("pid %d still running after SIGKILL", pid)
	}
	return nil
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// ── utility ──────────────────────────────────────────────────────────────────

// loadDotEnv parses a .env file into a map.  Lines starting with # are skipped.
func (pm *ProcessManager) loadDotEnv(path string) map[string]string {
	result := make(map[string]string)
	f, err := os.Open(path)
	if err != nil {
		return result
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return result
}

const processLogSummaryInterval = 5 * time.Second

var processDebugLogs = func() bool {
	for _, key := range []string{"RELAY_PROCESS_DEBUG", "PROCESS_DEBUG_LOGS", "VYRA_DEBUG_LOGS"} {
		v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
		if v == "1" || v == "true" || v == "yes" || v == "on" {
			return true
		}
	}
	return false
}()

// prefixWriter routes child-process logs through relay logging with compact
// suppression of repeated hot-path lines.
type prefixWriter struct {
	prefix           string
	mu               sync.Mutex
	lastSummaryAt    time.Time
	debugEnabled     bool
	lastEmitByLine   map[string]time.Time
	suppressedByLine map[string]int
}

func newPrefixWriter(prefix string) *prefixWriter {
	return &prefixWriter{
		prefix:           prefix,
		debugEnabled:     processDebugLogs,
		lastEmitByLine:   make(map[string]time.Time),
		suppressedByLine: make(map[string]int),
	}
}

func (pw *prefixWriter) isErrorLine(line string) bool {
	if strings.Contains(strings.ToLower(pw.prefix), "|err]") {
		return true
	}
	l := strings.ToLower(line)
	return strings.Contains(l, "error") ||
		strings.Contains(l, "panic") ||
		strings.Contains(l, "traceback") ||
		strings.Contains(l, "fatal")
}

func (pw *prefixWriter) shouldBypassThrottle(line string) bool {
	if !pw.debugEnabled {
		return false
	}
	return pw.isErrorLine(line)
}

func (pw *prefixWriter) shouldEmit(line string) bool {
	if pw.debugEnabled {
		return true
	}
	return pw.isErrorLine(line)
}

func (pw *prefixWriter) flushSuppressedLocked(now time.Time, force bool) {
	if len(pw.suppressedByLine) == 0 {
		return
	}
	if !force && !pw.lastSummaryAt.IsZero() && now.Sub(pw.lastSummaryAt) < processLogSummaryInterval {
		return
	}
	totalSuppressed := 0
	topLine := ""
	topCount := 0
	for line, count := range pw.suppressedByLine {
		totalSuppressed += count
		if count > topCount {
			topLine = line
			topCount = count
		}
	}
	sample := topLine
	if len(sample) > 180 {
		sample = sample[:177] + "..."
	}
	if sample == "" {
		log.Printf("%s[log-throttle] suppressed %d repeated line(s)", pw.prefix, totalSuppressed)
	} else {
		log.Printf(
			"%s[log-throttle] suppressed %d repeated line(s) across %d unique lines (top: %q x%d)",
			pw.prefix,
			totalSuppressed,
			len(pw.suppressedByLine),
			sample,
			topCount,
		)
	}
	pw.suppressedByLine = make(map[string]int)
	pw.lastSummaryAt = now
}

func (pw *prefixWriter) Write(p []byte) (int, error) {
	lines := strings.Split(strings.TrimRight(string(p), "\n"), "\n")
	for _, l := range lines {
		if l == "" {
			continue
		}
		if !pw.shouldEmit(l) {
			continue
		}
		now := time.Now()
		pw.mu.Lock()
		if pw.shouldBypassThrottle(l) {
			pw.flushSuppressedLocked(now, true)
			log.Print(pw.prefix + l)
			pw.lastEmitByLine[l] = now
			pw.mu.Unlock()
			continue
		}

		if lastEmitAt, ok := pw.lastEmitByLine[l]; ok && now.Sub(lastEmitAt) < processLogSummaryInterval {
			pw.suppressedByLine[l]++
			pw.flushSuppressedLocked(now, false)
			pw.mu.Unlock()
			continue
		}

		pw.flushSuppressedLocked(now, false)
		log.Print(pw.prefix + l)
		pw.lastEmitByLine[l] = now
		pw.mu.Unlock()
	}
	return len(p), nil
}
