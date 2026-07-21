package relay

import (
	"strings"
	"testing"
	"time"
)

func TestResolveQueryLocationFallback(t *testing.T) {
	if got := resolveQueryLocation(""); got == nil {
		t.Fatalf("expected local timezone fallback")
	}
	if got := resolveQueryLocation("invalid/timezone"); got == nil {
		t.Fatalf("expected fallback timezone for invalid input")
	}
	if got := resolveQueryLocation("UTC"); got == nil || got.String() != "UTC" {
		t.Fatalf("expected UTC location, got %#v", got)
	}
}

func TestNormalizeQueryDateUsesProvidedTimezone(t *testing.T) {
	loc := resolveQueryLocation("Pacific/Honolulu")
	now := time.Now().In(loc)

	if got, want := normalizeQueryDate("", loc), now.Format("2006-01-02"); got != want {
		t.Fatalf("expected today %q, got %q", want, got)
	}
	if got, want := normalizeQueryDate("today", loc), now.Format("2006-01-02"); got != want {
		t.Fatalf("expected today %q, got %q", want, got)
	}
	if got, want := normalizeQueryDate("yesterday", loc), now.AddDate(0, 0, -1).Format("2006-01-02"); got != want {
		t.Fatalf("expected yesterday %q, got %q", want, got)
	}
}

func TestFilterQueryEventsByDateUsesTimezone(t *testing.T) {
	loc := resolveQueryLocation("UTC")
	events := []*AgentEvent{
		{ID: "1", CreatedAt: time.Date(2026, 2, 23, 1, 0, 0, 0, time.UTC)},
		{ID: "2", CreatedAt: time.Date(2026, 2, 22, 23, 30, 0, 0, time.UTC)},
	}

	filtered := filterQueryEventsByDate(events, "2026-02-23", loc)
	if len(filtered) != 1 || filtered[0].ID != "1" {
		t.Fatalf("expected only event 1, got %#v", filtered)
	}
}

func TestInferDateFromQuestionParsesNaturalDate(t *testing.T) {
	loc := resolveQueryLocation("UTC")
	now := time.Date(2026, 2, 23, 10, 0, 0, 0, loc)

	got, ok := inferDateFromQuestion("What did ABC do on 21 February?", loc, now)
	if !ok {
		t.Fatalf("expected parsed date")
	}
	if got != "2026-02-21" {
		t.Fatalf("expected 2026-02-21, got %s", got)
	}
}

func TestInferDateFromQuestionParsesNumericDate(t *testing.T) {
	loc := resolveQueryLocation("UTC")
	now := time.Date(2026, 2, 23, 10, 0, 0, 0, loc)

	got, ok := inferDateFromQuestion("Show me events on 21/02", loc, now)
	if !ok {
		t.Fatalf("expected parsed date")
	}
	if got != "2026-02-21" {
		t.Fatalf("expected 2026-02-21, got %s", got)
	}
}

func TestInferTimeWindowFromQuestionParsesExplicitTime(t *testing.T) {
	window, ok := inferTimeWindowFromQuestion("What happened around 3:30 pm?")
	if !ok {
		t.Fatalf("expected parsed time window")
	}
	if window.StartMin != (14*60+30) || window.EndMin != (16*60+30) {
		t.Fatalf("unexpected time window: %#v", window)
	}
}

func TestFilterQueryEventsByTimeWindow(t *testing.T) {
	loc := resolveQueryLocation("UTC")
	events := []*AgentEvent{
		{ID: "1", CreatedAt: time.Date(2026, 2, 21, 15, 10, 0, 0, loc)},
		{ID: "2", CreatedAt: time.Date(2026, 2, 21, 18, 0, 0, 0, loc)},
	}
	w := queryTimeWindow{StartMin: 14 * 60, EndMin: 16 * 60}
	filtered := filterQueryEventsByTimeWindow(events, w, loc)
	if len(filtered) != 1 || filtered[0].ID != "1" {
		t.Fatalf("expected only event 1, got %#v", filtered)
	}
}

func TestLooksWeakAIAnswerRejectsPromptEcho(t *testing.T) {
	answer := "If the evidence does not support an answer, say you do not know. Today, Main observed Unknown #11."
	if !looksWeakAIAnswer(answer) {
		t.Fatalf("expected prompt echo answer to be rejected")
	}
}

func TestEnsureGroundedAIAnswerFallsBackForPromptEcho(t *testing.T) {
	events := []qsContextEvent{
		{
			EventID:    "e1",
			Severity:   "medium",
			Brief:      "Person detected on Main; visit observed.",
			CameraName: "Main",
			CreatedAt:  "2026-06-29T11:47:07Z",
			PersonName: "Unknown #11",
		},
	}
	answer, confidence := ensureGroundedAIAnswer(
		"is everything alright?",
		"2026-06-29",
		events,
		nil,
		nil,
		"",
		"If the evidence does not support an answer, say you do not know. Today, Main was observed by Unknown #11.",
		"high",
	)
	if strings.Contains(strings.ToLower(answer), "if the evidence does not support") {
		t.Fatalf("fallback leaked prompt instruction: %s", answer)
	}
	if confidence != "medium" {
		t.Fatalf("expected medium confidence fallback, got %q", confidence)
	}
}

func TestBuildDeterministicScaffoldDoesNotPromoteUnknownTrack(t *testing.T) {
	got := buildDeterministicScaffold(
		"is everything alright?",
		"2026-06-29",
		[]qsContextEvent{
			{
				PersonName: "Unknown #11",
				CameraName: "Main",
				Brief:      "Unknown #11 detected on Main.",
			},
		},
		nil,
		"",
	)
	if got != "" {
		t.Fatalf("expected no person scaffold for unknown track, got %q", got)
	}
}

func TestDetectTargetCamerasAndFilterEvents(t *testing.T) {
	cameras := []*Camera{
		{ID: "cam-main", Name: "Main"},
		{ID: "cam-extra", Name: "Extra"},
	}
	targets := detectTargetCameras("Who entered the main camera today?", cameras)
	if len(targets) != 1 || targets[0].Name != "Main" {
		t.Fatalf("expected Main camera target, got %#v", targets)
	}

	events := []*AgentEvent{
		{
			ID:        "main",
			Metadata:  map[string]interface{}{"service_id": "cam-main", "camera_name": "Main"},
			Data:      map[string]interface{}{"brief": "Unknown #1 detected on Main."},
			CreatedAt: time.Date(2026, 6, 29, 12, 57, 0, 0, time.UTC),
		},
		{
			ID:        "extra",
			Metadata:  map[string]interface{}{"service_id": "cam-extra", "camera_name": "Extra"},
			Data:      map[string]interface{}{"brief": "Unknown #2 detected on Extra."},
			CreatedAt: time.Date(2026, 6, 29, 12, 58, 0, 0, time.UTC),
		},
	}
	filtered := filterQueryEventsByCamera(events, targets)
	if len(filtered) != 1 || filtered[0].ID != "main" {
		t.Fatalf("expected only Main event, got %#v", filtered)
	}
}

func TestShouldRetrieveKnowledgeSkipsSurveillanceEventQuestions(t *testing.T) {
	if shouldRetrieveKnowledgeForQuery("Who entered the main camera today and when?", KnowledgeSearchFilters{}) {
		t.Fatalf("event question should not trigger knowledge retrieval")
	}
	if !shouldRetrieveKnowledgeForQuery("What does the bridge proxy protocol do?", KnowledgeSearchFilters{}) {
		t.Fatalf("documentation question should trigger knowledge retrieval")
	}
}

func TestClassifyQueryIntentRoutesTimelineDocsAndHybrid(t *testing.T) {
	mainTarget := []queryCameraTarget{{ID: "cam-main", Name: "Main"}}
	if got := classifyQueryIntent("Who entered Main today and when?", "", "", mainTarget, KnowledgeSearchFilters{}); got != "camera_activity" {
		t.Fatalf("expected camera_activity, got %s", got)
	}
	if got := classifyQueryIntent("Show Unknown #2 activity today", "", "unknown:2", nil, KnowledgeSearchFilters{}); got != "person_activity" {
		t.Fatalf("expected person_activity, got %s", got)
	}
	if got := classifyQueryIntent("What does the bridge proxy protocol do?", "", "", nil, KnowledgeSearchFilters{}); got != "docs" {
		t.Fatalf("expected docs, got %s", got)
	}
	if got := classifyQueryIntent("Compare Main activity with the bridge proxy docs", "", "", mainTarget, KnowledgeSearchFilters{}); got != "hybrid" {
		t.Fatalf("expected hybrid, got %s", got)
	}
}

func TestDeterministicEventAnswerSummarizesInsteadOfDumpingRows(t *testing.T) {
	events := []qsContextEvent{
		{
			EventID:        "e1",
			Severity:       "medium",
			Brief:          "Unknown #1 detected on Main; person visit observed.",
			CameraName:     "Main",
			PersonName:     "Unknown #1",
			GlobalPersonID: "unknown:cam-main:1",
			CreatedAt:      "2026-06-29T18:27:00+05:30",
		},
		{
			EventID:        "e2",
			Severity:       "medium",
			Brief:          "Unknown #1 detected on Main; person visit observed.",
			CameraName:     "Main",
			PersonName:     "Unknown #1",
			GlobalPersonID: "unknown:cam-main:1",
			CreatedAt:      "2026-06-29T18:31:00+05:30",
		},
	}

	answer := queryDeterministicEventAnswer(
		"Who entered the main camera today and when?",
		events,
		"2026-06-29",
		[]queryCameraTarget{{ID: "cam-main", Name: "Main"}},
		"",
	)
	if !strings.Contains(answer, "Main on Jun 29, 2026 recorded 2 matching events") {
		t.Fatalf("missing scoped summary: %s", answer)
	}
	if !strings.Contains(answer, "Unknown #1 was seen at 18:27 and 18:31") {
		t.Fatalf("missing compact person timeline: %s", answer)
	}
	if strings.Count(answer, "Unknown #1") != 1 {
		t.Fatalf("expected person to be summarized once, got: %s", answer)
	}
}

func TestDeterministicTimelineAnswerSummarizesMoments(t *testing.T) {
	timeline := []qsContextTimeline{
		{
			ID:             "m1",
			GlobalPersonID: "unknown:1",
			PersonName:     "Unknown #1",
			CameraName:     "Main",
			Severity:       "medium",
			ActivityBrief:  "Unknown #1 detected on Main.",
			CreatedAt:      "2026-06-29T18:27:00+05:30",
		},
		{
			ID:             "m2",
			GlobalPersonID: "unknown:1",
			PersonName:     "Unknown #1",
			CameraName:     "Main",
			Severity:       "medium",
			ActivityBrief:  "Unknown #1 detected on Main.",
			CreatedAt:      "2026-06-29T18:31:00+05:30",
		},
	}

	answer := queryDeterministicTimelineAnswer(
		"Who entered the main camera today and when?",
		timeline,
		"2026-06-29",
		[]queryCameraTarget{{ID: "cam-main", Name: "Main"}},
		"",
	)
	if !strings.Contains(answer, "Main on Jun 29, 2026 recorded 2 person timeline moments") {
		t.Fatalf("missing scoped timeline summary: %s", answer)
	}
	if !strings.Contains(answer, "Unknown #1 was seen at 18:27 and 18:31") {
		t.Fatalf("missing compact person timeline: %s", answer)
	}
}
