package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/unblink/unblink/node"
)

func newSystemStopTestRelay(t *testing.T) *Relay {
	t.Helper()
	root := t.TempDir()
	pm := NewProcessManager(root, root, "9020", "8020", "5000", "secret")
	return &Relay{
		config:         &Config{},
		processManager: pm,
		nodes:          map[string]*NodeConn{},
		services:       NewServiceRegistry(),
	}
}

type stubMessageTransport struct {
	closed bool
}

func (s *stubMessageTransport) ReadMessage() (*node.Message, error) { return nil, nil }
func (s *stubMessageTransport) WriteMessage(*node.Message) error    { return nil }
func (s *stubMessageTransport) ReadRawMessage() ([]byte, error)     { return nil, nil }
func (s *stubMessageTransport) WriteRawMessage([]byte) error        { return nil }
func (s *stubMessageTransport) Close() error {
	s.closed = true
	return nil
}

func TestHandleSystemStopWorkerScalesPoolToZero(t *testing.T) {
	relay := newSystemStopTestRelay(t)
	relay.processManager.mu.Lock()
	relay.processManager.desiredScale["ppe"] = 3
	relay.processManager.mu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/system/stop/worker-ppe", nil)
	rec := httptest.NewRecorder()
	handleSystemStop(rec, req, relay, "worker-ppe")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := relay.processManager.DesiredScale("ppe"); got != 0 {
		t.Fatalf("expected ppe desired scale to be 0, got %d", got)
	}
}

func TestHandleSystemStopAdapterScalesPoolToZero(t *testing.T) {
	relay := newSystemStopTestRelay(t)
	relay.processManager.mu.Lock()
	relay.processManager.desiredScale["objdet"] = 2
	relay.processManager.mu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/system/stop/adapter-objdet", nil)
	rec := httptest.NewRecorder()
	handleSystemStop(rec, req, relay, "adapter-objdet")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := relay.processManager.DesiredScale("objdet"); got != 0 {
		t.Fatalf("expected objdet desired scale to be 0, got %d", got)
	}
}

func TestHandleSystemStopEngineClearsDesiredState(t *testing.T) {
	relay := newSystemStopTestRelay(t)
	pm := relay.processManager

	for _, name := range []string{
		"engine",
		"worker-ppe",
		"adapter-ppe",
		"worker-custom-agent1-1",
		"adapter-custom-agent1-1",
	} {
		startSleepProcess(t, pm, name)
	}

	pm.mu.Lock()
	pm.desiredScale["ppe"] = 1
	pm.desiredScale["vlm"] = 1
	pm.customRuntime["agent1"] = &customRuntimeState{
		Desired: 1,
		Options: CustomRuntimeOptions{ModelPath: "/tmp/model.pt"},
	}
	pm.llmDesired = true
	pm.mu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/system/stop/engine", nil)
	rec := httptest.NewRecorder()
	handleSystemStop(rec, req, relay, "engine")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	for _, wtype := range []string{"ppe", "objdet", "vlm", "face"} {
		if got := pm.DesiredScale(wtype); got != 0 {
			t.Fatalf("expected %s desired scale 0, got %d", wtype, got)
		}
	}
	if got := pm.CustomDesiredScale("agent1"); got != 0 {
		t.Fatalf("expected custom desired scale 0, got %d", got)
	}
	for _, name := range []string{
		"engine",
		"worker-ppe",
		"adapter-ppe",
		"worker-custom-agent1-1",
		"adapter-custom-agent1-1",
	} {
		if pm.IsRunning(name) {
			t.Fatalf("expected %s to be stopped", name)
		}
	}
}

func TestHandleStopManagedNodeRemovesConnectedNodeAndServices(t *testing.T) {
	relay := newSystemStopTestRelay(t)

	transport := &stubMessageTransport{}
	nodeConn := &NodeConn{
		conn:        transport,
		relay:       relay,
		nodeID:      "node-1",
		bridges:     make(map[string]*Bridge),
		bridgeChans: make(map[string]chan []byte),
		shutdown:    make(chan struct{}),
	}
	relay.nodes["node-1"] = nodeConn
	relay.services.Register(node.Service{ID: "cam-1", Name: "Camera 1", Type: "rtsp"}, "node-1")

	req := httptest.NewRequest(http.MethodPost, "/managed-nodes/node-1/stop", nil)
	rec := httptest.NewRecorder()
	handleStopManagedNode(rec, req, relay, "node-1")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if relay.GetNode("node-1") != nil {
		t.Fatalf("expected node-1 to be removed from relay nodes map")
	}
	if got := len(relay.services.ListByNode("node-1")); got != 0 {
		t.Fatalf("expected node-1 services to be removed, got %d", got)
	}
	if !transport.closed {
		t.Fatalf("expected node transport to be closed during stop cleanup")
	}
}
