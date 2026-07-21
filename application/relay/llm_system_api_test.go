package relay

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestLLMWorkerBaseURLTrimsTrailingSlash(t *testing.T) {
	t.Setenv("LLM_WORKER_URL", "http://127.0.0.1:5003/")
	if got, want := llmWorkerBaseURL(), "http://127.0.0.1:5003"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestTriggerLLMWarmupPostsWarmupEndpoint(t *testing.T) {
	var warmups int32
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local listener not available in this environment: %v", err)
	}
	defer ln.Close()

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/warmup" {
			atomic.AddInt32(&warmups, 1)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	t.Setenv("LLM_WORKER_URL", srv.URL)
	triggerLLMWarmup()

	if got := atomic.LoadInt32(&warmups); got != 1 {
		t.Fatalf("expected one warmup POST, got %d", got)
	}
}
