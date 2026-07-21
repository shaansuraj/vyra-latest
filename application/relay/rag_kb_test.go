package relay

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestChunkKnowledgeTextPreservesHeadings(t *testing.T) {
	input := KnowledgeDocumentInput{
		SourceURI: "application/docs/BRIDGE_PROXY.md",
		Title:     "Bridge Proxy",
		Text: `# Bridge Proxy

Intro paragraph for bridge proxy.

## WebSocket Relay

The bridge proxy relays browser WebSocket traffic to managed nodes.
It preserves node identity and version metadata.
`,
	}

	chunks := ChunkKnowledgeText(input)
	if len(chunks) == 0 {
		t.Fatalf("expected chunks")
	}
	found := false
	for _, chunk := range chunks {
		if chunk.Heading == "WebSocket Relay" || chunk.SectionPath == "Bridge Proxy > WebSocket Relay" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected websocket heading in chunks: %#v", chunks)
	}
}

func TestKnowledgeIndexAndSearch(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("new db: %v", err)
	}
	defer db.Close()

	input := KnowledgeDocumentInput{
		SourceURI:   "application/docs/FEDERATED_WORKERS.md",
		SourceType:  "markdown",
		Title:       "Federated Workers",
		Version:     "v1",
		Environment: "prod",
		Language:    "en",
		AccessLevel: "internal",
		Text: `# Federated Workers

## Worker Registration

Workers register with the relay using a worker type, maximum batch size, and latency budget.

## Scheduling

The relay batches jobs per worker type before dispatching them to connected workers.
`,
	}
	if _, err := db.IndexKnowledgeDocument(1, input, nil); err != nil {
		t.Fatalf("index: %v", err)
	}

	results, err := db.SearchKnowledge(1, KnowledgeSearchRequest{
		Query: "worker registration batch latency",
		Limit: 3,
		Filters: KnowledgeSearchFilters{
			Version:      "v1",
			Environment:  "prod",
			AccessLevels: []string{"internal"},
		},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected results")
	}
	if results[0].Title != "Federated Workers" {
		t.Fatalf("unexpected top result: %#v", results[0])
	}
	if results[0].Score <= 0 {
		t.Fatalf("expected positive score, got %f", results[0].Score)
	}
}

func TestKnowledgeHybridSearchFindsVectorOnlyMatch(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("new db: %v", err)
	}
	defer db.Close()

	input := KnowledgeDocumentInput{
		SourceURI:   "memory://semantic-only",
		SourceType:  "text",
		Title:       "Semantic Only",
		Language:    "en",
		AccessLevel: "internal",
		Text:        "placeholder",
	}
	chunks := []KnowledgeChunk{
		{
			Text:           "Alpha beta gamma operational note.",
			Embedding:      []float64{1, 0, 0},
			EmbeddingModel: "test",
		},
	}
	if _, err := db.IndexKnowledgeDocument(1, input, chunks); err != nil {
		t.Fatalf("index: %v", err)
	}

	results, err := db.SearchKnowledge(1, KnowledgeSearchRequest{
		Query:          "completely different words",
		Limit:          3,
		QueryEmbedding: []float64{1, 0, 0},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected vector-only result")
	}
	if !strings.Contains(results[0].ScoreReason, "vector") {
		t.Fatalf("expected vector score reason, got %q", results[0].ScoreReason)
	}
}

func TestKnowledgeTraceFeedback(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("new db: %v", err)
	}
	defer db.Close()

	traceID, err := db.StoreKnowledgeTrace(KnowledgeTraceInput{
		UserID:              1,
		Question:            "How do workers register?",
		RetrievedChunksJSON: []byte(`[]`),
		RetrievedEventsJSON: []byte(`[]`),
		Answer:              "Workers register with the relay.",
		Confidence:          "high",
		LatencyMS:           12,
	})
	if err != nil {
		t.Fatalf("store trace: %v", err)
	}
	if traceID == "" {
		t.Fatalf("expected trace id")
	}
	if err := db.StoreKnowledgeFeedback(1, traceID, "up", "good answer"); err != nil {
		t.Fatalf("store feedback: %v", err)
	}
}
