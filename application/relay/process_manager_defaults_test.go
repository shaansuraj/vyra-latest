package relay

import "testing"

func TestRealtimeWorkerDefaultsFavorFreshSmallBatches(t *testing.T) {
	cases := []struct {
		wtype       string
		maxLatency  string
		maxBatch    string
		maxInFlight string
		maxQueue    string
		timeoutMS   string
	}{
		{wtype: "ppe", maxLatency: "60", maxBatch: "4", maxInFlight: "2", maxQueue: "4", timeoutMS: "30000"},
		{wtype: "objdet", maxLatency: "60", maxBatch: "4", maxInFlight: "2", maxQueue: "4", timeoutMS: "30000"},
		{wtype: "face", maxLatency: "50", maxBatch: "1", maxInFlight: "2", maxQueue: "16", timeoutMS: "60000"},
	}

	for _, tc := range cases {
		t.Run(tc.wtype, func(t *testing.T) {
			worker := workerEnvOverrides[tc.wtype]
			if got := worker["MAX_LATENCY_MS"]; got != tc.maxLatency {
				t.Fatalf("MAX_LATENCY_MS=%q, want %q", got, tc.maxLatency)
			}
			if got := worker["MAX_BATCH_SIZE"]; got != tc.maxBatch {
				t.Fatalf("MAX_BATCH_SIZE=%q, want %q", got, tc.maxBatch)
			}

			adapter := adapterEnvOverrides[tc.wtype]
			if got := adapter["MAX_IN_FLIGHT"]; got != tc.maxInFlight {
				t.Fatalf("MAX_IN_FLIGHT=%q, want %q", got, tc.maxInFlight)
			}
			if got := adapter["MAX_QUEUE_SIZE"]; got != tc.maxQueue {
				t.Fatalf("MAX_QUEUE_SIZE=%q, want %q", got, tc.maxQueue)
			}
			if got := adapter["IN_FLIGHT_TIMEOUT_MS"]; got != tc.timeoutMS {
				t.Fatalf("IN_FLIGHT_TIMEOUT_MS=%q, want %q", got, tc.timeoutMS)
			}
		})
	}
}
