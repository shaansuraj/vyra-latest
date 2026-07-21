package relay

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type ragEvalCase struct {
	ID                string   `json:"id"`
	Category          string   `json:"category"`
	Question          string   `json:"question"`
	Date              string   `json:"date"`
	ExpectedSourceIDs []string `json:"expected_source_ids"`
	ExpectedEventIDs  []string `json:"expected_event_ids"`
	ReferenceAnswer   string   `json:"reference_answer"`
	MustSayUnknown    bool     `json:"must_say_unknown"`
}

type ragEvalMetrics struct {
	Cases                 int
	ExpectedItems         int
	FoundItems            int
	RankedQueries         int
	ReciprocalRankSum     float64
	CitationCases         int
	CitationCorrect       int
	UnknownCases          int
	UnknownCorrect        int
	HallucinationFailures int
	GroundedCases         int
	GroundedCorrect       int
	Latencies             []time.Duration
}

func TestRAGEvaluationSmokeDataset(t *testing.T) {
	datasetPath := os.Getenv("RAG_EVAL_DATASET")
	if strings.TrimSpace(datasetPath) == "" {
		datasetPath = filepath.Join("testdata", "rag_eval_smoke.json")
	}
	cases := loadRAGEvalCases(t, datasetPath)
	if len(cases) == 0 {
		t.Fatalf("empty RAG eval dataset: %s", datasetPath)
	}

	db, err := NewDatabase(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("NewDatabase failed: %v", err)
	}
	defer db.Close()
	seedRAGEvalFixture(t, db)

	var metrics ragEvalMetrics
	for _, tc := range cases {
		started := time.Now()
		sourceRanks, eventRanks, answer := runRAGEvalCase(t, db, tc)
		metrics.Latencies = append(metrics.Latencies, time.Since(started))
		metrics.Cases++

		foundSources, firstSourceRank := countExpectedRanks(tc.ExpectedSourceIDs, sourceRanks)
		foundEvents, firstEventRank := countExpectedRanks(tc.ExpectedEventIDs, eventRanks)
		expected := len(tc.ExpectedSourceIDs) + len(tc.ExpectedEventIDs)
		found := foundSources + foundEvents
		metrics.ExpectedItems += expected
		metrics.FoundItems += found
		if rank := bestPositiveRank(firstSourceRank, firstEventRank); rank > 0 {
			metrics.ReciprocalRankSum += 1 / float64(rank)
			metrics.RankedQueries++
		}

		if expected > 0 {
			metrics.CitationCases++
			if found == expected {
				metrics.CitationCorrect++
			}
			metrics.GroundedCases++
			if strings.TrimSpace(answer) != "" && found > 0 {
				metrics.GroundedCorrect++
			}
		}
		if tc.MustSayUnknown {
			metrics.UnknownCases++
			unknownAnswer := strings.Contains(strings.ToLower(answer), "did not find") ||
				strings.Contains(strings.ToLower(answer), "no matching")
			if found == 0 && unknownAnswer {
				metrics.UnknownCorrect++
			} else {
				metrics.HallucinationFailures++
			}
		}
	}

	recall := safeRatio(metrics.FoundItems, metrics.ExpectedItems)
	mrr := safeFloatRatio(metrics.ReciprocalRankSum, metrics.RankedQueries)
	citation := safeRatio(metrics.CitationCorrect, metrics.CitationCases)
	grounded := safeRatio(metrics.GroundedCorrect, metrics.GroundedCases)
	unknown := safeRatio(metrics.UnknownCorrect, metrics.UnknownCases)
	hallucinationRate := safeRatio(metrics.HallucinationFailures, metrics.UnknownCases)
	p50, p95 := durationPercentiles(metrics.Latencies)

	t.Logf(
		"RAG eval metrics dataset=%s cases=%d recall@10=%.3f mrr=%.3f citation_correctness=%.3f groundedness=%.3f unknown_correctness=%.3f hallucination_rate=%.3f latency_p50_ms=%d latency_p95_ms=%d",
		datasetPath,
		metrics.Cases,
		recall,
		mrr,
		citation,
		grounded,
		unknown,
		hallucinationRate,
		p50.Milliseconds(),
		p95.Milliseconds(),
	)

	if recall < envFloat("RAG_EVAL_MIN_RECALL_AT_10", 0.95) {
		t.Fatalf("recall@10 %.3f below threshold", recall)
	}
	if citation < envFloat("RAG_EVAL_MIN_CITATION_CORRECTNESS", 0.95) {
		t.Fatalf("citation correctness %.3f below threshold", citation)
	}
	if hallucinationRate > envFloat("RAG_EVAL_MAX_HALLUCINATION_RATE", 0) {
		t.Fatalf("hallucination rate %.3f above threshold", hallucinationRate)
	}
}

func loadRAGEvalCases(t *testing.T, path string) []ragEvalCase {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read eval dataset: %v", err)
	}
	var cases []ragEvalCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("decode eval dataset: %v", err)
	}
	return cases
}

func seedRAGEvalFixture(t *testing.T, db *Database) {
	t.Helper()
	input := KnowledgeDocumentInput{
		SourceURI:   "memory://ppe-policy",
		SourceType:  "markdown",
		Title:       "PPE Policy",
		Language:    "en",
		AccessLevel: "internal",
		Text: `# PPE Policy

Helmet policy requires hard hats in the loading bay. Supervisors should treat missing helmets as a PPE violation.`,
	}
	if _, err := db.IndexKnowledgeDocument(1, input, nil); err != nil {
		t.Fatalf("index eval knowledge: %v", err)
	}
	_, err := db.DB.Exec(`
		INSERT INTO agents (id, name, worker_id, config, user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "agent-helmet", "PPE Monitor", "worker-eval", `{}`, 1, time.Now().UTC(), time.Now().UTC())
	if err != nil {
		t.Fatalf("insert eval agent: %v", err)
	}
	created := time.Date(2026, 2, 21, 10, 15, 0, 0, time.UTC)
	_, err = db.DB.Exec(`
		INSERT INTO agent_events (id, agent_id, data, metadata, created_at)
		VALUES (?, ?, ?, ?, ?)
	`,
		"event-helmet-1",
		"agent-helmet",
		`{"brief":"Helmet violation near entry","event_brief":"Helmet violation near entry","severity":"high","service_id":"cam-main","camera_name":"Main"}`,
		`{"service_id":"cam-main","camera_name":"Main","severity":"high"}`,
		created,
	)
	if err != nil {
		t.Fatalf("insert eval event: %v", err)
	}
}

func runRAGEvalCase(t *testing.T, db *Database, tc ragEvalCase) (map[string]int, map[string]int, string) {
	t.Helper()
	category := strings.ToLower(strings.TrimSpace(tc.Category))
	sourceRanks := map[string]int{}
	eventRanks := map[string]int{}
	var chunks []qsContextChunk
	var events []qsContextEvent

	if category == "docs" || category == "hybrid" || category == "unknown" {
		results, err := db.SearchKnowledge(1, KnowledgeSearchRequest{Query: tc.Question, Limit: 10})
		if err != nil {
			t.Fatalf("%s knowledge search failed: %v", tc.ID, err)
		}
		for i, r := range results {
			sourceRanks[r.ID] = i + 1
			if strings.TrimSpace(r.SourceURI) != "" {
				sourceRanks[r.SourceURI] = i + 1
			}
		}
		chunks = toQSContextChunks(results)
	}

	if category == "event" || category == "hybrid" || category == "unknown" {
		start, end := queryDateRangeUTC(tc.Date, time.UTC, queryTimeWindow{}, false)
		filter := AgentEventAIQueryFilter{
			StartAt:  start,
			EndAt:    end,
			Keywords: extractQueryKeywords(tc.Question),
			Limit:    10,
		}
		if strings.Contains(strings.ToLower(tc.Question), "main") {
			filter.ServiceIDs = []string{"cam-main"}
		}
		results, err := db.QueryAgentEventsForAI(1, filter)
		if err != nil {
			t.Fatalf("%s event search failed: %v", tc.ID, err)
		}
		for i, ev := range results {
			eventRanks[ev.ID] = i + 1
			events = append(events, extractEventContext(ev, time.UTC))
		}
	}

	answer := queryDeterministicAnswer(tc.Question, events, nil, chunks, tc.Date, "")
	return sourceRanks, eventRanks, answer
}

func countExpectedRanks(expected []string, ranks map[string]int) (int, int) {
	found := 0
	first := 0
	for _, id := range expected {
		rank := ranks[strings.TrimSpace(id)]
		if rank <= 0 {
			continue
		}
		found++
		if first == 0 || rank < first {
			first = rank
		}
	}
	return found, first
}

func bestPositiveRank(a, b int) int {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}

func safeRatio(n, d int) float64 {
	if d <= 0 {
		return 1
	}
	return float64(n) / float64(d)
}

func safeFloatRatio(n float64, d int) float64 {
	if d <= 0 {
		return 1
	}
	return n / float64(d)
}

func durationPercentiles(values []time.Duration) (time.Duration, time.Duration) {
	if len(values) == 0 {
		return 0, 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	at := func(p float64) time.Duration {
		idx := int(math.Ceil(p*float64(len(sorted)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}
	return at(0.50), at(0.95)
}

func envFloat(key string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return value
}

func (m ragEvalMetrics) String() string {
	return fmt.Sprintf("cases=%d expected=%d found=%d", m.Cases, m.ExpectedItems, m.FoundItems)
}
