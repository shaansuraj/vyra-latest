package main

import (
	"os"
	"syscall"
	"testing"
)

func TestShutdownSignalsIncludesSIGQUIT(t *testing.T) {
	got := shutdownSignals()

	want := []os.Signal{
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT,
	}

	if len(got) != len(want) {
		t.Fatalf("shutdownSignals length = %d, want %d", len(got), len(want))
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("shutdownSignals[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}
