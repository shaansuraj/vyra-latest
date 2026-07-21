package relay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── Public request / response types ──────────────────────────────────────────

type AIQueryRequest struct {
	Question string `json:"question"`
	// YYYY-MM-DD, "today", "yesterday", or "" (defaults to today).
	Date string `json:"date"`
	// Optional persisted chat id. If omitted, the relay creates one.
	ChatID string `json:"chat_id,omitempty"`
	// Optional IANA timezone from browser, e.g. "America/New_York".
	Timezone string `json:"timezone,omitempty"`
	// Optional recent chat messages for follow-up queries.
	ChatHistory []AIChatMessage `json:"chat_history,omitempty"`
	// Optional knowledge-base metadata filters.
	KnowledgeFilters KnowledgeSearchFilters `json:"knowledge_filters,omitempty"`
	// Optional timeline evidence toggle. Defaults to true.
	IncludeTimeline *bool `json:"include_timeline,omitempty"`
}

type AIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type AIQueryEvent struct {
	ID             string `json:"id"`
	Brief          string `json:"brief"`
	Severity       string `json:"severity"`
	SnapshotPath   string `json:"snapshot_path,omitempty"`
	ServiceID      string `json:"service_id,omitempty"`
	PersonName     string `json:"person_name,omitempty"`
	GlobalPersonID string `json:"global_person_id,omitempty"`
	CreatedAt      string `json:"created_at"`
}

type AIQueryResponse struct {
	Answer           string                    `json:"answer"`
	Confidence       string                    `json:"confidence"`
	ChatID           string                    `json:"chat_id,omitempty"`
	Intent           string                    `json:"intent,omitempty"`
	RelevantEvents   []AIQueryEvent            `json:"relevant_events"`
	TimelineEvidence []AIQueryTimelineEvidence `json:"timeline_evidence,omitempty"`
	Sources          []AIQuerySource           `json:"sources,omitempty"`
	TraceID          string                    `json:"trace_id,omitempty"`
}

type AIQueryTimelineEvidence struct {
	ID                 string  `json:"id"`
	GlobalPersonID     string  `json:"global_person_id"`
	PersonID           string  `json:"person_id,omitempty"`
	PersonName         string  `json:"person_name,omitempty"`
	IdentitySource     string  `json:"identity_source,omitempty"`
	IdentityConfidence float64 `json:"identity_confidence,omitempty"`
	ServiceID          string  `json:"service_id,omitempty"`
	CameraName         string  `json:"camera_name,omitempty"`
	EventID            string  `json:"event_id,omitempty"`
	TrackID            int     `json:"track_id,omitempty"`
	SnapshotPath       string  `json:"snapshot_path,omitempty"`
	ActivityBrief      string  `json:"activity_brief,omitempty"`
	EventKind          string  `json:"event_kind,omitempty"`
	Severity           string  `json:"severity,omitempty"`
	CreatedAt          string  `json:"created_at"`
}

type AIQuerySource struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	SourceURI   string  `json:"source_uri"`
	Heading     string  `json:"heading,omitempty"`
	SectionPath string  `json:"section_path,omitempty"`
	Version     string  `json:"version,omitempty"`
	Environment string  `json:"environment,omitempty"`
	Language    string  `json:"language,omitempty"`
	Score       float64 `json:"score"`
	Snippet     string  `json:"snippet"`
}

// ── Internal types sent to the Ask-AI worker ─────────────────────────────────

type qsContextEvent struct {
	EventID         string `json:"event_id"`
	Severity        string `json:"severity"`
	Brief           string `json:"brief"`
	SnapshotPath    string `json:"snapshot_path,omitempty"`
	ServiceID       string `json:"service_id,omitempty"`
	PersonName      string `json:"person_name,omitempty"`
	GlobalPersonID  string `json:"global_person_id,omitempty"`
	CameraName      string `json:"camera_name,omitempty"`
	CameraTimestamp string `json:"camera_timestamp,omitempty"`
	CreatedAt       string `json:"created_at"`
}

type qsContextChunk struct {
	ChunkID     string  `json:"chunk_id"`
	Title       string  `json:"title"`
	SourceURI   string  `json:"source_uri"`
	Heading     string  `json:"heading,omitempty"`
	SectionPath string  `json:"section_path,omitempty"`
	Version     string  `json:"version,omitempty"`
	Environment string  `json:"environment,omitempty"`
	Language    string  `json:"language,omitempty"`
	Text        string  `json:"text"`
	Score       float64 `json:"score"`
	ScoreReason string  `json:"score_reason,omitempty"`
}

type qsContextTimeline struct {
	ID                 string  `json:"id"`
	GlobalPersonID     string  `json:"global_person_id"`
	PersonID           string  `json:"person_id,omitempty"`
	PersonName         string  `json:"person_name,omitempty"`
	IdentitySource     string  `json:"identity_source,omitempty"`
	IdentityConfidence float64 `json:"identity_confidence,omitempty"`
	ServiceID          string  `json:"service_id,omitempty"`
	CameraName         string  `json:"camera_name,omitempty"`
	EventID            string  `json:"event_id,omitempty"`
	TrackID            int     `json:"track_id,omitempty"`
	SnapshotPath       string  `json:"snapshot_path,omitempty"`
	ActivityBrief      string  `json:"activity_brief,omitempty"`
	EventKind          string  `json:"event_kind,omitempty"`
	Severity           string  `json:"severity,omitempty"`
	CreatedAt          string  `json:"created_at"`
}

type qsRequest struct {
	Question              string              `json:"question"`
	Date                  string              `json:"date"`
	ContextEvents         []qsContextEvent    `json:"context_events"`
	ContextTimeline       []qsContextTimeline `json:"context_timeline,omitempty"`
	ContextChunks         []qsContextChunk    `json:"context_chunks,omitempty"`
	KnownPersons          []string            `json:"known_persons"`
	TopSnapshotPath       string              `json:"top_snapshot_path"`
	DeterministicScaffold string              `json:"deterministic_scaffold,omitempty"`
	ChatHistory           []AIChatMessage     `json:"chat_history,omitempty"`
}

type qsResponse struct {
	Answer             string   `json:"answer"`
	Confidence         string   `json:"confidence"`
	ReferencedEventIDs []string `json:"referenced_event_ids"`
	ReferencedChunkIDs []string `json:"referenced_chunk_ids,omitempty"`
}

const queryServiceTimeout = 15 * time.Second

type queryTimeWindow struct {
	StartMin int
	EndMin   int
	Label    string
}

type queryCameraTarget struct {
	ID   string
	Name string
}

var (
	queryMonthNameToNumber = map[string]time.Month{
		"jan": 1, "january": 1,
		"feb": 2, "february": 2,
		"mar": 3, "march": 3,
		"apr": 4, "april": 4,
		"may": 5,
		"jun": 6, "june": 6,
		"jul": 7, "july": 7,
		"aug": 8, "august": 8,
		"sep": 9, "sept": 9, "september": 9,
		"oct": 10, "october": 10,
		"nov": 11, "november": 11,
		"dec": 12, "december": 12,
	}
	reQueryDateNumeric = regexp.MustCompile(`\b(\d{1,2})[/-](\d{1,2})(?:[/-](\d{2,4}))?\b`)
	reQueryDateDayMon  = regexp.MustCompile(`(?i)\b(\d{1,2})(?:st|nd|rd|th)?\s+(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|jun(?:e)?|jul(?:y)?|aug(?:ust)?|sep(?:t|tember)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)(?:\s*,?\s*(\d{2,4}))?\b`)
	reQueryDateMonDay  = regexp.MustCompile(`(?i)\b(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|jun(?:e)?|jul(?:y)?|aug(?:ust)?|sep(?:t|tember)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\s+(\d{1,2})(?:st|nd|rd|th)?(?:\s*,?\s*(\d{2,4}))?\b`)
	reQueryTimeAny     = regexp.MustCompile(`(?i)\b(?:(\d{1,2})(?::([0-5]\d))?\s*(am|pm)|([01]?\d|2[0-3]):([0-5]\d))\b`)
	reUnknownPerson    = regexp.MustCompile(`(?i)\bunknown\s*#\s*(\d+)\b`)
)

func queryServiceURL() string {
	base := strings.TrimSpace(os.Getenv("LLM_WORKER_URL"))
	if base == "" {
		base = "http://127.0.0.1:5003"
	}
	base = strings.TrimRight(base, "/")
	return base + "/query"
}

// handleAIQuery handles POST /query
func handleAIQuery(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	startedAt := time.Now()
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AIQueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	question := strings.TrimSpace(req.Question)
	if question == "" {
		http.Error(w, "question is required", http.StatusBadRequest)
		return
	}

	// Normalize date using browser timezone when provided.
	queryTZ := strings.TrimSpace(req.Timezone)
	if queryTZ == "" {
		queryTZ = strings.TrimSpace(r.Header.Get("X-Timezone"))
	}
	queryLoc := resolveQueryLocation(queryTZ)
	dateStr := normalizeQueryDate(req.Date, queryLoc)
	if inferredDate, ok := inferDateFromQuestion(question, queryLoc, time.Now().In(queryLoc)); ok {
		dateStr = inferredDate
	}
	timeWindow, hasTimeWindow := inferTimeWindowFromQuestion(question)
	rangeStart, rangeEnd := queryDateRangeUTC(dateStr, queryLoc, timeWindow, hasTimeWindow)
	chatHistory := sanitizeChatHistory(req.ChatHistory)
	chatID, _ := relay.db.EnsureQueryChat(userID, req.ChatID, question)
	if len(chatHistory) == 0 && strings.TrimSpace(req.ChatID) != "" {
		if persistedHistory, err := relay.db.RecentQueryChatMessages(userID, req.ChatID, 12); err == nil {
			chatHistory = sanitizeChatHistory(persistedHistory)
		}
	}
	includeTimeline := true
	if req.IncludeTimeline != nil {
		includeTimeline = *req.IncludeTimeline
	}

	// 1. Collect known persons for context
	persons, _ := relay.personTable.GetPersonsByUser(userID)
	knownPersons := make([]string, 0, len(persons))
	knownPersonIDs := map[string]string{}
	for _, p := range persons {
		knownPersons = append(knownPersons, p.Name)
		knownPersonIDs[strings.ToLower(strings.TrimSpace(p.Name))] = p.ID
	}
	cameras, _ := relay.cameraTable.GetAllCameras()
	targetCameras := detectTargetCameras(question, cameras)
	targetPerson := detectTargetPerson(question, knownPersons)
	targetGlobalPersonID := detectTargetGlobalPersonID(question)
	intent := classifyQueryIntent(question, targetPerson, targetGlobalPersonID, targetCameras, req.KnowledgeFilters)
	knowledgeQuery := intent == "docs" || intent == "hybrid"

	var timelineMoments []*PersonMoment
	if includeTimeline && queryIntentUsesTimeline(intent) {
		filter := PersonTimelineFilter{
			Date:           dateStr,
			StartAt:        rangeStart,
			EndAt:          rangeEnd,
			GlobalPersonID: targetGlobalPersonID,
			PersonID:       knownPersonIDs[strings.ToLower(strings.TrimSpace(targetPerson))],
			Limit:          30,
		}
		if len(targetCameras) == 1 {
			filter.ServiceID = targetCameras[0].ID
		}
		if len(targetCameras) > 1 {
			for _, targetCamera := range targetCameras {
				filter.ServiceID = targetCamera.ID
				moments, err := relay.db.QueryPersonTimeline(userID, filter)
				if err == nil {
					timelineMoments = append(timelineMoments, moments...)
				}
			}
			timelineMoments = dedupeTimelineMoments(timelineMoments)
		} else {
			moments, err := relay.db.QueryPersonTimeline(userID, filter)
			if err == nil {
				timelineMoments = moments
			}
		}
		if len(timelineMoments) > 30 {
			timelineMoments = timelineMoments[:30]
		}
	}

	// 2. Extract keywords and search events
	keywords := extractQueryKeywords(question)

	var allEvents []*AgentEvent
	serviceIDs := make([]string, 0, len(targetCameras))
	for _, targetCamera := range targetCameras {
		if strings.TrimSpace(targetCamera.ID) != "" {
			serviceIDs = append(serviceIDs, targetCamera.ID)
		}
	}
	allEvents, err := relay.db.QueryAgentEventsForAI(userID, AgentEventAIQueryFilter{
		StartAt:    rangeStart,
		EndAt:      rangeEnd,
		ServiceIDs: serviceIDs,
		Keywords:   keywords,
		Limit:      500,
	})
	if err == nil {
		// Already SQL-filtered; keep existing downstream ranking and person filters.
	} else {
		log.Printf("[QueryHTTP] SQL-first event retrieval failed: %v", err)
		allEvents = nil
	}

	// 3. Filter to the target date, deduplicate, and rank
	allEvents = deduplicateQueryEvents(allEvents)
	if len(targetCameras) > 0 {
		allEvents = filterQueryEventsByCamera(allEvents, targetCameras)
	}
	if targetPerson != "" {
		personLower := strings.ToLower(targetPerson)
		filtered := make([]*AgentEvent, 0, len(allEvents))
		for _, ev := range allEvents {
			meta := ev.Metadata
			data := ev.Data
			name := ""
			if meta != nil {
				if v, ok := meta["person_name"].(string); ok {
					name = v
				}
			}
			if name == "" && data != nil {
				if v, ok := data["person_name"].(string); ok {
					name = v
				}
			}
			if strings.EqualFold(strings.TrimSpace(name), targetPerson) {
				filtered = append(filtered, ev)
				continue
			}
			brief := ""
			if data != nil {
				if v, ok := data["event_brief"].(string); ok {
					brief = v
				}
			}
			if brief == "" && meta != nil {
				if v, ok := meta["brief"].(string); ok {
					brief = v
				}
			}
			if strings.Contains(strings.ToLower(brief), personLower) {
				filtered = append(filtered, ev)
			}
		}
		allEvents = filtered
		sort.SliceStable(allEvents, func(i, j int) bool {
			return allEvents[i].CreatedAt.Before(allEvents[j].CreatedAt)
		})
	} else {
		sort.SliceStable(allEvents, func(i, j int) bool {
			si := querySeverityScore(allEvents[i])
			sj := querySeverityScore(allEvents[j])
			if si != sj {
				return si > sj
			}
			return allEvents[i].CreatedAt.After(allEvents[j].CreatedAt)
		})
	}
	if len(allEvents) > 10 {
		allEvents = allEvents[:10]
	}

	// 4. Build context events
	ctxEvents := make([]qsContextEvent, 0, len(allEvents))
	topSnapshotPath := ""
	for _, ev := range allEvents {
		ce := extractEventContext(ev, queryLoc)
		ctxEvents = append(ctxEvents, ce)
		if topSnapshotPath == "" && ce.SnapshotPath != "" {
			topSnapshotPath = ce.SnapshotPath
		}
	}
	ctxTimeline := toQSContextTimeline(timelineMoments, queryLoc)

	// 5. Retrieve knowledge-base chunks only for document/config questions.
	var kbResults []KnowledgeSearchResult
	if knowledgeQuery {
		var queryEmbedding []float64
		if shouldUseRAGEmbeddings() {
			if emb, err := embedKnowledgeText(relay, question, "query"); err == nil {
				queryEmbedding = emb
			}
		}
		var err error
		kbResults, err = relay.db.SearchKnowledge(userID, KnowledgeSearchRequest{
			Query:          question,
			Limit:          ragRerankTopN(),
			Filters:        req.KnowledgeFilters,
			QueryEmbedding: queryEmbedding,
		})
		if err != nil {
			log.Printf("[QueryHTTP] knowledge retrieval failed: %v", err)
			kbResults = nil
		} else if reranked, err := rerankKnowledgeResults(relay, question, kbResults, ragFinalTopK()); err == nil {
			kbResults = reranked
		} else {
			log.Printf("[QueryHTTP] knowledge rerank unavailable: %v", err)
			kbResults = limitKnowledgeResults(kbResults, ragFinalTopK())
		}
	}
	ctxChunks := toQSContextChunks(kbResults)

	// 6. Answer event questions deterministically; use the LLM only for knowledge questions.
	scaffold := buildDeterministicScaffold(question, dateStr, ctxEvents, knownPersons, timeWindowLabel(hasTimeWindow, timeWindow))
	answer := ""
	confidence := "high"
	if !knowledgeQuery {
		if len(ctxTimeline) > 0 {
			answer = queryDeterministicTimelineAnswer(question, ctxTimeline, dateStr, targetCameras, timeWindowLabel(hasTimeWindow, timeWindow))
		} else {
			answer = queryDeterministicEventAnswer(question, ctxEvents, dateStr, targetCameras, timeWindowLabel(hasTimeWindow, timeWindow))
		}
		if len(ctxTimeline) == 0 && len(ctxEvents) == 0 {
			confidence = "low"
		}
	} else {
		llmStatus := buildLLMStatus(relay)
		if llmStatus.Running {
			answer, confidence = callQueryService(question, dateStr, ctxEvents, ctxTimeline, ctxChunks, knownPersons, topSnapshotPath, scaffold, chatHistory)
			answer, confidence = ensureGroundedAIAnswer(question, dateStr, ctxEvents, ctxTimeline, ctxChunks, scaffold, answer, confidence)
		} else {
			answer = queryDeterministicAnswer(question, ctxEvents, ctxTimeline, ctxChunks, dateStr, scaffold)
			confidence = "medium"
			if len(ctxTimeline) == 0 && len(ctxEvents) == 0 && len(ctxChunks) == 0 {
				confidence = "low"
			}
		}
	}

	// 7. Build response and trace
	relevantEvents := make([]AIQueryEvent, 0, len(allEvents))
	for i, ev := range allEvents {
		ce := ctxEvents[i]
		relevantEvents = append(relevantEvents, AIQueryEvent{
			ID:             ev.ID,
			Brief:          ce.Brief,
			Severity:       ce.Severity,
			SnapshotPath:   ce.SnapshotPath,
			ServiceID:      ce.ServiceID,
			PersonName:     ce.PersonName,
			GlobalPersonID: ce.GlobalPersonID,
			CreatedAt:      ce.CreatedAt,
		})
	}
	sources := toAIQuerySources(kbResults)
	timelineEvidence := toAIQueryTimelineEvidence(ctxTimeline)

	traceID := ""
	if chunksJSON, err := json.Marshal(sources); err == nil {
		eventsJSON, _ := json.Marshal(relevantEvents)
		timelineJSON, _ := json.Marshal(timelineEvidence)
		traceID, _ = relay.db.StoreKnowledgeTrace(KnowledgeTraceInput{
			UserID:                userID,
			ChatID:                chatID,
			Intent:                string(intent),
			Question:              question,
			RewrittenQuery:        strings.Join(extractQueryKeywords(question), " "),
			Filters:               req.KnowledgeFilters,
			RetrievedTimelineJSON: timelineJSON,
			RetrievedChunksJSON:   chunksJSON,
			RetrievedEventsJSON:   eventsJSON,
			PromptDigest:          promptDigest(question, ctxEvents, ctxChunks),
			Answer:                answer,
			Confidence:            confidence,
			LatencyMS:             time.Since(startedAt).Milliseconds(),
		})
	}
	if chatID != "" {
		now := time.Now().UTC()
		_ = relay.db.StoreQueryChatMessage(userID, chatID, "user", question, "", now)
		_ = relay.db.StoreQueryChatMessage(userID, chatID, "assistant", answer, traceID, now.Add(time.Millisecond))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AIQueryResponse{
		Answer:           answer,
		Confidence:       confidence,
		ChatID:           chatID,
		Intent:           string(intent),
		RelevantEvents:   relevantEvents,
		TimelineEvidence: timelineEvidence,
		Sources:          sources,
		TraceID:          traceID,
	})
}

func resolveQueryLocation(tz string) *time.Location {
	tz = strings.TrimSpace(tz)
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	return time.Local
}

// normalizeQueryDate converts "today"/"yesterday"/empty to YYYY-MM-DD.
func normalizeQueryDate(d string, loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	d = strings.TrimSpace(strings.ToLower(d))
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	switch d {
	case "", "today":
		return today
	case "yesterday":
		return now.AddDate(0, 0, -1).Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", d); err == nil {
		return d
	}
	return today
}

func queryDateRangeUTC(date string, loc *time.Location, window queryTimeWindow, hasWindow bool) (time.Time, time.Time) {
	if loc == nil {
		loc = time.Local
	}
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(date), loc)
	if err != nil {
		now := time.Now().In(loc)
		day = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	}
	start := day
	end := day.AddDate(0, 0, 1)
	if hasWindow {
		start = day.Add(time.Duration(clampMinute(window.StartMin)) * time.Minute)
		end = day.Add(time.Duration(clampMinute(window.EndMin)+1) * time.Minute)
		if !end.After(start) {
			end = end.AddDate(0, 0, 1)
		}
	}
	return start.UTC(), end.UTC()
}

func sanitizeChatHistory(messages []AIChatMessage) []AIChatMessage {
	if len(messages) == 0 {
		return nil
	}
	out := make([]AIChatMessage, 0, len(messages))
	for _, m := range messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if len(content) > 1200 {
			content = content[:1200]
		}
		out = append(out, AIChatMessage{Role: role, Content: content})
	}
	if len(out) > 12 {
		out = out[len(out)-12:]
	}
	return out
}

func inferDateFromQuestion(question string, loc *time.Location, now time.Time) (string, bool) {
	if loc == nil {
		loc = time.Local
	}
	q := strings.ToLower(strings.TrimSpace(question))
	if q == "" {
		return "", false
	}
	switch {
	case strings.Contains(q, "today"):
		return now.In(loc).Format("2006-01-02"), true
	case strings.Contains(q, "yesterday"):
		return now.In(loc).AddDate(0, 0, -1).Format("2006-01-02"), true
	case strings.Contains(q, "tomorrow"):
		return now.In(loc).AddDate(0, 0, 1).Format("2006-01-02"), true
	}

	if m := reQueryDateDayMon.FindStringSubmatch(q); len(m) >= 3 {
		day, _ := strconv.Atoi(m[1])
		month, ok := parseQueryMonth(m[2])
		if ok {
			year, yOK := inferQueryYear(m[3], int(month), day, now.In(loc))
			if yOK {
				if dt, ok := buildSafeDate(loc, year, month, day); ok {
					return dt.Format("2006-01-02"), true
				}
			}
		}
	}

	if m := reQueryDateMonDay.FindStringSubmatch(q); len(m) >= 3 {
		month, ok := parseQueryMonth(m[1])
		if ok {
			day, _ := strconv.Atoi(m[2])
			year, yOK := inferQueryYear(m[3], int(month), day, now.In(loc))
			if yOK {
				if dt, ok := buildSafeDate(loc, year, month, day); ok {
					return dt.Format("2006-01-02"), true
				}
			}
		}
	}

	if m := reQueryDateNumeric.FindStringSubmatch(q); len(m) >= 3 {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		yearRaw := ""
		if len(m) >= 4 {
			yearRaw = m[3]
		}

		day := a
		month := b
		// Try to infer intended numeric order.
		if a <= 12 && b > 12 {
			day = b
			month = a
		}

		year, yOK := inferQueryYear(yearRaw, month, day, now.In(loc))
		if yOK {
			if dt, ok := buildSafeDate(loc, year, time.Month(month), day); ok {
				return dt.Format("2006-01-02"), true
			}
		}
	}

	return "", false
}

func parseQueryMonth(s string) (time.Month, bool) {
	key := strings.ToLower(strings.TrimSpace(s))
	month, ok := queryMonthNameToNumber[key]
	return month, ok
}

func inferQueryYear(yearRaw string, month, day int, now time.Time) (int, bool) {
	if yearRaw != "" {
		y, err := strconv.Atoi(strings.TrimSpace(yearRaw))
		if err != nil {
			return 0, false
		}
		if y < 100 {
			y += 2000
		}
		return y, true
	}
	candidate, ok := buildSafeDate(now.Location(), now.Year(), time.Month(month), day)
	if !ok {
		return 0, false
	}
	// If implied date is still in the future, assume previous year.
	if candidate.After(now.Add(24 * time.Hour)) {
		return now.Year() - 1, true
	}
	return now.Year(), true
}

func buildSafeDate(loc *time.Location, year int, month time.Month, day int) (time.Time, bool) {
	if loc == nil {
		loc = time.Local
	}
	if day < 1 || day > 31 || int(month) < 1 || int(month) > 12 {
		return time.Time{}, false
	}
	dt := time.Date(year, month, day, 0, 0, 0, 0, loc)
	if dt.Year() != year || dt.Month() != month || dt.Day() != day {
		return time.Time{}, false
	}
	return dt, true
}

func inferTimeWindowFromQuestion(question string) (queryTimeWindow, bool) {
	q := strings.ToLower(strings.TrimSpace(question))
	if q == "" {
		return queryTimeWindow{}, false
	}

	switch {
	case strings.Contains(q, "morning"):
		return queryTimeWindow{StartMin: 5 * 60, EndMin: 12*60 - 1, Label: "morning"}, true
	case strings.Contains(q, "afternoon"):
		return queryTimeWindow{StartMin: 12 * 60, EndMin: 17*60 - 1, Label: "afternoon"}, true
	case strings.Contains(q, "evening"):
		return queryTimeWindow{StartMin: 17 * 60, EndMin: 21*60 - 1, Label: "evening"}, true
	case strings.Contains(q, "night"):
		return queryTimeWindow{StartMin: 21 * 60, EndMin: 23*60 + 59, Label: "night"}, true
	}

	times := extractTimesInOrder(q)
	if len(times) >= 2 {
		start := clampMinute(times[0])
		end := clampMinute(times[1])
		label := fmt.Sprintf("%s to %s", formatMinuteLabel(start), formatMinuteLabel(end))
		return queryTimeWindow{StartMin: start, EndMin: end, Label: label}, true
	}
	if len(times) == 1 {
		center := clampMinute(times[0])
		start := clampMinute(center - 60)
		end := clampMinute(center + 60)
		label := "around " + formatMinuteLabel(center)
		return queryTimeWindow{StartMin: start, EndMin: end, Label: label}, true
	}
	return queryTimeWindow{}, false
}

func extractTimesInOrder(q string) []int {
	matches := reQueryTimeAny.FindAllStringSubmatch(q, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]int, 0, len(matches))
	for _, m := range matches {
		// 12h format groups
		if len(m) >= 4 && m[1] != "" {
			h, _ := strconv.Atoi(m[1])
			min := 0
			if m[2] != "" {
				min, _ = strconv.Atoi(m[2])
			}
			ampm := strings.ToLower(strings.TrimSpace(m[3]))
			if h == 12 {
				h = 0
			}
			if ampm == "pm" {
				h += 12
			}
			out = append(out, h*60+min)
			continue
		}
		// 24h format groups
		if len(m) >= 6 && m[4] != "" {
			h, _ := strconv.Atoi(m[4])
			min, _ := strconv.Atoi(m[5])
			out = append(out, h*60+min)
		}
	}
	return out
}

func clampMinute(m int) int {
	if m < 0 {
		return 0
	}
	if m > 23*60+59 {
		return 23*60 + 59
	}
	return m
}

func formatMinuteLabel(min int) string {
	h := min / 60
	m := min % 60
	return fmt.Sprintf("%02d:%02d", h, m)
}

func timeWindowLabel(has bool, w queryTimeWindow) string {
	if !has {
		return ""
	}
	return strings.TrimSpace(w.Label)
}

var queryStopwords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true,
	"was": true, "were": true, "did": true, "do": true, "does": true,
	"has": true, "have": true, "had": true, "be": true, "been": true,
	"in": true, "on": true, "at": true, "to": true, "for": true,
	"of": true, "and": true, "or": true, "but": true, "with": true,
	"that": true, "this": true, "it": true, "i": true, "you": true,
	"he": true, "she": true, "they": true, "we": true, "my": true,
	"any": true, "all": true, "some": true, "today": true, "yesterday": true,
	"someone": true, "anybody": true, "anyone": true, "something": true,
	"please": true, "tell": true, "me": true, "about": true, "what": true,
	"when": true, "where": true, "who": true, "how": true, "why": true,
	"come": true, "came": true, "go": true, "went": true, "see": true,
	"seen": true, "happen": true, "happened": true, "show": true,
}

func detectTargetPerson(question string, knownPersons []string) string {
	q := strings.ToLower(strings.TrimSpace(question))
	if q == "" {
		return ""
	}
	for _, kp := range knownPersons {
		name := strings.TrimSpace(kp)
		if name == "" {
			continue
		}
		if strings.Contains(q, strings.ToLower(name)) {
			return name
		}
	}
	return ""
}

func detectTargetGlobalPersonID(question string) string {
	q := strings.TrimSpace(question)
	if q == "" {
		return ""
	}
	if m := reUnknownPerson.FindStringSubmatch(q); len(m) == 2 {
		return "unknown:" + m[1]
	}
	rePersonGlobal := regexp.MustCompile(`(?i)\bperson:([a-z0-9_-]+)\b`)
	if m := rePersonGlobal.FindStringSubmatch(q); len(m) == 2 {
		return "person:" + m[1]
	}
	return ""
}

func detectTargetCameras(question string, cameras []*Camera) []queryCameraTarget {
	q := strings.ToLower(strings.TrimSpace(question))
	if q == "" || len(cameras) == 0 {
		return nil
	}
	targets := make([]queryCameraTarget, 0, len(cameras))
	seen := map[string]bool{}
	for _, cam := range cameras {
		if cam == nil {
			continue
		}
		name := strings.TrimSpace(cam.Name)
		id := strings.TrimSpace(cam.ID)
		if name == "" && id == "" {
			continue
		}
		matched := false
		if name != "" {
			matched = queryContainsPhrase(q, name) || queryContainsPhrase(q, name+" camera") || queryContainsPhrase(q, "camera "+name)
		}
		if !matched && id != "" {
			matched = queryContainsPhrase(q, id)
		}
		if !matched {
			continue
		}
		key := strings.ToLower(id)
		if key == "" {
			key = strings.ToLower(name)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		targets = append(targets, queryCameraTarget{ID: id, Name: name})
	}
	return targets
}

func queryContainsPhrase(text, phrase string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	phrase = strings.ToLower(strings.TrimSpace(phrase))
	if text == "" || len(phrase) < 2 {
		return false
	}
	re := regexp.MustCompile(`(^|[^a-z0-9])` + regexp.QuoteMeta(phrase) + `([^a-z0-9]|$)`)
	return re.MatchString(text)
}

func classifyQueryIntent(question, targetPerson, targetGlobalPersonID string, targetCameras []queryCameraTarget, filters KnowledgeSearchFilters) string {
	q := strings.ToLower(strings.TrimSpace(question))
	docSignal := hasKnowledgeSearchFilters(filters)
	if !docSignal {
		for _, sig := range []string{
			"documentation", "document", "docs", "manual", "protocol", "version",
			"bridge proxy", "client protocol", "relay protocol", "federated workers",
			"configure", "configuration", "settings", "setup", "install", "deploy",
			"environment", ".env", "api", "endpoint", "schema", "architecture",
			"rag", "knowledge base", "worker", "workers",
		} {
			if strings.Contains(q, sig) {
				docSignal = true
				break
			}
		}
	}
	eventSignal := isSurveillanceEventQuery(q) || targetPerson != "" || targetGlobalPersonID != "" || len(targetCameras) > 0
	switch {
	case docSignal && eventSignal:
		return "hybrid"
	case docSignal:
		return "docs"
	case targetPerson != "" || targetGlobalPersonID != "":
		return "person_activity"
	case len(targetCameras) > 0:
		return "camera_activity"
	case strings.Contains(q, "incident") || strings.Contains(q, "violation") || strings.Contains(q, "alert"):
		return "incident"
	default:
		return "camera_activity"
	}
}

func queryIntentUsesTimeline(intent string) bool {
	switch strings.TrimSpace(intent) {
	case "person_activity", "camera_activity", "incident", "hybrid":
		return true
	default:
		return false
	}
}

func shouldRetrieveKnowledgeForQuery(question string, filters KnowledgeSearchFilters) bool {
	if hasKnowledgeSearchFilters(filters) {
		return true
	}
	q := strings.ToLower(strings.TrimSpace(question))
	if q == "" {
		return false
	}
	if isSurveillanceEventQuery(q) {
		return false
	}
	knowledgeSignals := []string{
		"documentation", "document", "docs", "manual", "protocol", "version",
		"bridge proxy", "client protocol", "relay protocol", "federated workers",
		"configure", "configuration", "settings", "setup", "install", "deploy",
		"environment", ".env", "api", "endpoint", "schema", "architecture",
		"rag", "knowledge base", "worker", "workers",
	}
	for _, sig := range knowledgeSignals {
		if strings.Contains(q, sig) {
			return true
		}
	}
	return false
}

func hasKnowledgeSearchFilters(filters KnowledgeSearchFilters) bool {
	return strings.TrimSpace(filters.Version) != "" ||
		strings.TrimSpace(filters.Environment) != "" ||
		strings.TrimSpace(filters.Language) != "" ||
		strings.TrimSpace(filters.SourceURI) != "" ||
		len(filters.AccessLevels) > 0
}

func isSurveillanceEventQuery(q string) bool {
	directSignals := []string{
		"who", "when", "where", "entered", "enter", "came", "went",
		"detected", "detection", "seen", "observed", "incident", "incidents",
		"event", "events", "person", "people", "unknown", "visit",
		"activity", "happened", "today", "yesterday",
	}
	for _, sig := range directSignals {
		if queryContainsPhrase(q, sig) {
			return true
		}
	}
	cameraSignals := []string{"camera", "main", "extra"}
	activitySignals := []string{"person", "people", "unknown", "visit", "activity", "detected", "entered", "seen"}
	hasCamera := false
	for _, sig := range cameraSignals {
		if queryContainsPhrase(q, sig) {
			hasCamera = true
			break
		}
	}
	if !hasCamera {
		return false
	}
	for _, sig := range activitySignals {
		if queryContainsPhrase(q, sig) {
			return true
		}
	}
	return false
}

// extractQueryKeywords strips stopwords from a question.
func extractQueryKeywords(question string) []string {
	clean := strings.Map(func(r rune) rune {
		if r == '?' || r == '.' || r == ',' || r == '!' || r == '\'' || r == '"' {
			return ' '
		}
		return r
	}, strings.ToLower(question))

	var keywords []string
	seen := map[string]bool{}
	for _, w := range strings.Fields(clean) {
		if !queryStopwords[w] && len(w) >= 3 && !seen[w] {
			keywords = append(keywords, w)
			seen[w] = true
		}
	}
	return keywords
}

func filterQueryEventsByDate(events []*AgentEvent, date string, loc *time.Location) []*AgentEvent {
	if loc == nil {
		loc = time.Local
	}
	var out []*AgentEvent
	for _, ev := range events {
		if ev.CreatedAt.In(loc).Format("2006-01-02") == date {
			out = append(out, ev)
		}
	}
	return out
}

func filterQueryEventsByTimeWindow(events []*AgentEvent, window queryTimeWindow, loc *time.Location) []*AgentEvent {
	if loc == nil {
		loc = time.Local
	}
	start := clampMinute(window.StartMin)
	end := clampMinute(window.EndMin)
	var out []*AgentEvent
	for _, ev := range events {
		t := ev.CreatedAt.In(loc)
		minute := t.Hour()*60 + t.Minute()
		if start <= end {
			if minute >= start && minute <= end {
				out = append(out, ev)
			}
			continue
		}
		// Wrap-around window (not currently used, but safe)
		if minute >= start || minute <= end {
			out = append(out, ev)
		}
	}
	return out
}

func deduplicateQueryEvents(events []*AgentEvent) []*AgentEvent {
	seen := make(map[string]bool, len(events))
	var out []*AgentEvent
	for _, ev := range events {
		if !seen[ev.ID] {
			seen[ev.ID] = true
			out = append(out, ev)
		}
	}
	return out
}

func dedupeTimelineMoments(moments []*PersonMoment) []*PersonMoment {
	seen := map[string]bool{}
	out := make([]*PersonMoment, 0, len(moments))
	for _, m := range moments {
		if m == nil {
			continue
		}
		key := strings.TrimSpace(m.ID)
		if key == "" {
			key = strings.TrimSpace(m.DedupeKey)
		}
		if key == "" {
			key = strings.TrimSpace(m.GlobalPersonID) + ":" + m.CreatedAt.Format(time.RFC3339Nano)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func filterTimelineMomentsByTimeWindow(moments []*PersonMoment, window queryTimeWindow, loc *time.Location) []*PersonMoment {
	if loc == nil {
		loc = time.Local
	}
	start := clampMinute(window.StartMin)
	end := clampMinute(window.EndMin)
	out := make([]*PersonMoment, 0, len(moments))
	for _, m := range moments {
		if m == nil {
			continue
		}
		t := m.CreatedAt.In(loc)
		minute := t.Hour()*60 + t.Minute()
		if start <= end {
			if minute >= start && minute <= end {
				out = append(out, m)
			}
			continue
		}
		if minute >= start || minute <= end {
			out = append(out, m)
		}
	}
	return out
}

func filterQueryEventsByCamera(events []*AgentEvent, targets []queryCameraTarget) []*AgentEvent {
	if len(targets) == 0 {
		return events
	}
	targetIDs := map[string]bool{}
	targetNames := map[string]bool{}
	for _, target := range targets {
		if id := strings.ToLower(strings.TrimSpace(target.ID)); id != "" {
			targetIDs[id] = true
		}
		if name := strings.ToLower(strings.TrimSpace(target.Name)); name != "" {
			targetNames[name] = true
		}
	}

	out := make([]*AgentEvent, 0, len(events))
	for _, ev := range events {
		ce := extractEventContext(ev, time.UTC)
		serviceID := strings.ToLower(strings.TrimSpace(ce.ServiceID))
		cameraName := strings.ToLower(strings.TrimSpace(ce.CameraName))
		if serviceID != "" && (targetIDs[serviceID] || targetNames[serviceID]) {
			out = append(out, ev)
			continue
		}
		if cameraName != "" && targetNames[cameraName] {
			out = append(out, ev)
			continue
		}
		if serviceID == "" && cameraName == "" {
			brief := strings.ToLower(strings.TrimSpace(ce.Brief))
			for targetName := range targetNames {
				if queryContainsPhrase(brief, targetName) {
					out = append(out, ev)
					break
				}
			}
		}
	}
	return out
}

func querySeverityScore(ev *AgentEvent) int {
	sev := ""
	if ev.Data != nil {
		sev, _ = ev.Data["severity"].(string)
	}
	if sev == "" && ev.Metadata != nil {
		sev, _ = ev.Metadata["severity"].(string)
	}
	switch strings.ToLower(sev) {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	}
	return 0
}

func extractEventContext(ev *AgentEvent, loc *time.Location) qsContextEvent {
	if loc == nil {
		loc = time.Local
	}
	meta := ev.Metadata
	if meta == nil {
		meta = map[string]interface{}{}
	}
	data := ev.Data
	if data == nil {
		data = map[string]interface{}{}
	}

	strField := func(maps []map[string]interface{}, keys ...string) string {
		for _, m := range maps {
			for _, k := range keys {
				if v, ok := m[k].(string); ok && v != "" {
					return v
				}
			}
		}
		return ""
	}

	brief := strField([]map[string]interface{}{data, meta},
		"vlm_summary", "summary", "response", "message", "answer", "result", "brief")
	if len(brief) > 300 {
		brief = brief[:300]
	}

	severity := strField([]map[string]interface{}{data, meta}, "severity")
	snapshotPath := strField([]map[string]interface{}{meta, data}, "snapshot_path")
	serviceID := strField([]map[string]interface{}{meta, data}, "service_id")
	personName := strField([]map[string]interface{}{meta, data}, "person_name", "name")
	globalPersonID := strField([]map[string]interface{}{meta, data}, "global_person_id")
	cameraName := strField([]map[string]interface{}{meta, data}, "camera_name")
	cameraTimestamp := strField([]map[string]interface{}{meta, data}, "camera_timestamp")

	return qsContextEvent{
		EventID:         ev.ID,
		Severity:        severity,
		Brief:           brief,
		SnapshotPath:    snapshotPath,
		ServiceID:       serviceID,
		PersonName:      personName,
		GlobalPersonID:  globalPersonID,
		CameraName:      cameraName,
		CameraTimestamp: cameraTimestamp,
		CreatedAt:       ev.CreatedAt.In(loc).Format(time.RFC3339),
	}
}

func toQSContextChunks(results []KnowledgeSearchResult) []qsContextChunk {
	if len(results) == 0 {
		return nil
	}
	out := make([]qsContextChunk, 0, len(results))
	for _, r := range results {
		text := strings.TrimSpace(r.Text)
		if len(text) > 1400 {
			text = strings.TrimSpace(text[:1399]) + "..."
		}
		out = append(out, qsContextChunk{
			ChunkID:     r.ID,
			Title:       r.Title,
			SourceURI:   r.SourceURI,
			Heading:     r.Heading,
			SectionPath: r.SectionPath,
			Version:     r.Version,
			Environment: r.Environment,
			Language:    r.Language,
			Text:        text,
			Score:       r.Score,
			ScoreReason: r.ScoreReason,
		})
	}
	return out
}

func toQSContextTimeline(moments []*PersonMoment, loc *time.Location) []qsContextTimeline {
	if len(moments) == 0 {
		return nil
	}
	if loc == nil {
		loc = time.Local
	}
	out := make([]qsContextTimeline, 0, len(moments))
	for _, m := range moments {
		if m == nil {
			continue
		}
		out = append(out, qsContextTimeline{
			ID:                 m.ID,
			GlobalPersonID:     m.GlobalPersonID,
			PersonID:           m.PersonID,
			PersonName:         m.PersonName,
			IdentitySource:     m.IdentitySource,
			IdentityConfidence: m.IdentityConfidence,
			ServiceID:          m.ServiceID,
			CameraName:         m.CameraName,
			EventID:            m.EventID,
			TrackID:            m.TrackID,
			SnapshotPath:       m.SnapshotPath,
			ActivityBrief:      m.ActivityBrief,
			EventKind:          m.EventKind,
			Severity:           m.Severity,
			CreatedAt:          m.CreatedAt.In(loc).Format(time.RFC3339),
		})
	}
	return out
}

func toAIQuerySources(results []KnowledgeSearchResult) []AIQuerySource {
	if len(results) == 0 {
		return nil
	}
	out := make([]AIQuerySource, 0, len(results))
	for _, r := range results {
		out = append(out, AIQuerySource{
			ID:          r.ID,
			Title:       r.Title,
			SourceURI:   r.SourceURI,
			Heading:     r.Heading,
			SectionPath: r.SectionPath,
			Version:     r.Version,
			Environment: r.Environment,
			Language:    r.Language,
			Score:       r.Score,
			Snippet:     r.Snippet,
		})
	}
	return out
}

func toAIQueryTimelineEvidence(items []qsContextTimeline) []AIQueryTimelineEvidence {
	if len(items) == 0 {
		return nil
	}
	out := make([]AIQueryTimelineEvidence, 0, len(items))
	for _, item := range items {
		out = append(out, AIQueryTimelineEvidence{
			ID:                 item.ID,
			GlobalPersonID:     item.GlobalPersonID,
			PersonID:           item.PersonID,
			PersonName:         item.PersonName,
			IdentitySource:     item.IdentitySource,
			IdentityConfidence: item.IdentityConfidence,
			ServiceID:          item.ServiceID,
			CameraName:         item.CameraName,
			EventID:            item.EventID,
			TrackID:            item.TrackID,
			SnapshotPath:       item.SnapshotPath,
			ActivityBrief:      item.ActivityBrief,
			EventKind:          item.EventKind,
			Severity:           item.Severity,
			CreatedAt:          item.CreatedAt,
		})
	}
	return out
}

func promptDigest(question string, events []qsContextEvent, chunks []qsContextChunk) string {
	h := sha256.New()
	h.Write([]byte(strings.TrimSpace(question)))
	for _, ev := range events {
		h.Write([]byte{0})
		h.Write([]byte(ev.EventID))
	}
	for _, chunk := range chunks {
		h.Write([]byte{0})
		h.Write([]byte(chunk.ChunkID))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// callQueryService POSTs to the Ask-AI worker and returns (answer, confidence).
func callQueryService(question, date string, events []qsContextEvent, timeline []qsContextTimeline, chunks []qsContextChunk, knownPersons []string, topSnapshotPath string, scaffold string, chatHistory []AIChatMessage) (string, string) {
	body, err := json.Marshal(qsRequest{
		Question:              question,
		Date:                  date,
		ContextEvents:         events,
		ContextTimeline:       timeline,
		ContextChunks:         chunks,
		KnownPersons:          knownPersons,
		TopSnapshotPath:       topSnapshotPath,
		DeterministicScaffold: scaffold,
		ChatHistory:           chatHistory,
	})
	if err != nil {
		return queryDeterministicAnswer(question, events, timeline, chunks, date, scaffold), "low"
	}

	client := &http.Client{Timeout: queryServiceTimeout}
	resp, err := client.Post(queryServiceURL(), "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("[QueryHTTP] Ask-AI worker unavailable: %v", err)
		return queryDeterministicAnswer(question, events, timeline, chunks, date, scaffold), "low"
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return queryDeterministicAnswer(question, events, timeline, chunks, date, scaffold), "low"
	}

	var qs qsResponse
	if err := json.Unmarshal(respBody, &qs); err != nil || qs.Answer == "" {
		return queryDeterministicAnswer(question, events, timeline, chunks, date, scaffold), "low"
	}

	return qs.Answer, qs.Confidence
}

func ensureGroundedAIAnswer(question, date string, events []qsContextEvent, timeline []qsContextTimeline, chunks []qsContextChunk, scaffold, answer, confidence string) (string, string) {
	clean := strings.TrimSpace(answer)
	conf := strings.ToLower(strings.TrimSpace(confidence))
	if conf == "" {
		conf = "medium"
	}

	if len(timeline) == 0 && len(events) == 0 && len(chunks) == 0 {
		if clean == "" {
			return fmt.Sprintf("I did not find meaningful activity related to '%s' on %s.", question, date), "low"
		}
		return clean, conf
	}

	if looksWeakAIAnswer(clean) || conf == "low" {
		return queryDeterministicAnswer(question, events, timeline, chunks, date, scaffold), "medium"
	}

	return clean, conf
}

func looksWeakAIAnswer(answer string) bool {
	if strings.TrimSpace(answer) == "" {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(answer))
	if len(strings.Fields(lower)) < 8 {
		return true
	}
	weakSignals := []string{
		"you are vyra",
		"on-site safety analyst",
		"answer using only",
		"if the evidence does not support",
		"do not invent",
		"write like a human operator update",
		"include concrete evidence",
		"reply in english unless",
		"reply in hindi because",
		"known persons:",
		"user question:",
		"knowledge-base excerpts:",
		"return only the final operator-facing answer",
		"do not repeat the prompt",
		"do not repeat or restate prompt instructions",
		"i don't know",
		"cannot determine",
		"unable to determine",
		"not enough context",
		"insufficient context",
		"cannot answer",
	}
	for _, sig := range weakSignals {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

func isUnknownQueryPersonName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "unknown" || strings.HasPrefix(n, "unknown #") || strings.HasPrefix(n, "unknown:")
}

type queryPersonActivity struct {
	Label   string
	Times   []string
	Cameras []string
	Count   int
}

func queryDeterministicTimelineAnswer(question string, timeline []qsContextTimeline, date string, targetCameras []queryCameraTarget, timeHint string) string {
	scope := queryEventScopePhrase(date, targetCameras, timeHint)
	if len(timeline) == 0 {
		if scope == "" {
			return fmt.Sprintf("I did not find person timeline evidence related to '%s'.", question)
		}
		return "I did not find person timeline evidence for " + scope + "."
	}

	sortedTimeline := append([]qsContextTimeline(nil), timeline...)
	sort.SliceStable(sortedTimeline, func(i, j int) bool {
		ti := queryContextTimelineTime(sortedTimeline[i])
		tj := queryContextTimelineTime(sortedTimeline[j])
		if ti.IsZero() || tj.IsZero() {
			return sortedTimeline[i].CreatedAt < sortedTimeline[j].CreatedAt
		}
		return ti.Before(tj)
	})

	var high, medium int
	cameraSet := map[string]bool{}
	cameras := make([]string, 0, 4)
	timeSet := map[string]bool{}
	times := make([]string, 0, len(sortedTimeline))
	personOrder := make([]string, 0, 6)
	persons := map[string]*queryPersonActivity{}

	for _, moment := range sortedTimeline {
		switch strings.ToLower(strings.TrimSpace(moment.Severity)) {
		case "high":
			high++
		case "medium":
			medium++
		}

		camera := queryTimelineCameraLabel(moment)
		if camera != "" && !cameraSet[camera] {
			cameraSet[camera] = true
			cameras = append(cameras, camera)
		}

		tlabel := queryTimelineTimeLabel(moment)
		if tlabel != "" && !timeSet[tlabel] {
			timeSet[tlabel] = true
			times = append(times, tlabel)
		}

		person := queryTimelinePersonLabel(moment)
		if person == "" {
			person = "Unknown person"
		}
		key := strings.ToLower(person)
		pa := persons[key]
		if pa == nil {
			pa = &queryPersonActivity{Label: person}
			persons[key] = pa
			personOrder = append(personOrder, key)
		}
		pa.Count++
		if tlabel != "" && !stringSliceContains(pa.Times, tlabel) {
			pa.Times = append(pa.Times, tlabel)
		}
		if camera != "" && !stringSliceContains(pa.Cameras, camera) {
			pa.Cameras = append(pa.Cameras, camera)
		}
	}

	scopeLabel := queryCameraListLabelFromTargets(targetCameras)
	if scopeLabel == "" {
		scopeLabel = queryJoinNatural(cameras)
	}
	dateLabel := formatQueryDateForHumans(date)
	if dateLabel == "" {
		dateLabel = "the selected date"
	}

	openingScope := dateLabel
	if scopeLabel != "" {
		openingScope = scopeLabel + " on " + dateLabel
	}
	timeRange := queryTimeRangeLabel(times)
	if strings.TrimSpace(timeHint) != "" {
		timeRange = strings.TrimSpace(timeHint)
	}

	parts := []string{
		fmt.Sprintf(
			"%s recorded %d person timeline %s",
			openingScope,
			len(sortedTimeline),
			pluralWord(len(sortedTimeline), "moment", "moments"),
		),
	}
	if timeRange != "" {
		parts[0] += " " + timeRange
	}
	parts[0] += "."

	if len(personOrder) > 0 {
		rows := make([]string, 0, len(personOrder))
		for _, key := range personOrder {
			if len(rows) >= 6 {
				rows = append(rows, fmt.Sprintf("%d more identities", len(personOrder)-len(rows)))
				break
			}
			pa := persons[key]
			when := queryCompactTimes(pa.Times)
			if when == "" {
				when = "time not recorded"
			}
			cameraSuffix := ""
			if scopeLabel == "" && len(pa.Cameras) > 0 {
				cameraSuffix = " on " + queryJoinNatural(pa.Cameras)
			}
			rows = append(rows, fmt.Sprintf("%s was seen at %s%s", pa.Label, when, cameraSuffix))
		}
		parts = append(parts, "People: "+strings.Join(rows, "; ")+".")
	}

	if high > 0 || medium > 0 {
		parts = append(parts, fmt.Sprintf("Severity: %d high and %d medium linked moments.", high, medium))
	} else {
		parts = append(parts, "No high-severity alert appears in the matching person timeline.")
	}
	parts = append(parts, "Timeline evidence below is restricted to the matching identity, camera, and time filters.")
	return strings.Join(parts, " ")
}

func queryDeterministicEventAnswer(question string, events []qsContextEvent, date string, targetCameras []queryCameraTarget, timeHint string) string {
	scope := queryEventScopePhrase(date, targetCameras, timeHint)
	if len(events) == 0 {
		if scope == "" {
			return fmt.Sprintf("I did not find recorded event evidence related to '%s'.", question)
		}
		return "I did not find recorded event evidence for " + scope + "."
	}

	sortedEvents := append([]qsContextEvent(nil), events...)
	sort.SliceStable(sortedEvents, func(i, j int) bool {
		ti := queryContextEventTime(sortedEvents[i])
		tj := queryContextEventTime(sortedEvents[j])
		if ti.IsZero() || tj.IsZero() {
			return sortedEvents[i].CreatedAt < sortedEvents[j].CreatedAt
		}
		return ti.Before(tj)
	})

	var high, medium int
	cameraSet := map[string]bool{}
	cameras := make([]string, 0, 4)
	timeSet := map[string]bool{}
	times := make([]string, 0, len(sortedEvents))
	personOrder := make([]string, 0, 6)
	persons := map[string]*queryPersonActivity{}

	for _, ev := range sortedEvents {
		switch strings.ToLower(strings.TrimSpace(ev.Severity)) {
		case "high":
			high++
		case "medium":
			medium++
		}

		camera := queryEventCameraLabel(ev)
		if camera != "" && !cameraSet[camera] {
			cameraSet[camera] = true
			cameras = append(cameras, camera)
		}

		tlabel := queryEventTimeLabel(ev)
		if tlabel != "" && !timeSet[tlabel] {
			timeSet[tlabel] = true
			times = append(times, tlabel)
		}

		person := queryEventPersonLabel(ev)
		if person == "" {
			person = "Unknown person"
		}
		key := strings.ToLower(person)
		pa := persons[key]
		if pa == nil {
			pa = &queryPersonActivity{Label: person}
			persons[key] = pa
			personOrder = append(personOrder, key)
		}
		pa.Count++
		if tlabel != "" && !stringSliceContains(pa.Times, tlabel) {
			pa.Times = append(pa.Times, tlabel)
		}
		if camera != "" && !stringSliceContains(pa.Cameras, camera) {
			pa.Cameras = append(pa.Cameras, camera)
		}
	}

	scopeLabel := queryCameraListLabelFromTargets(targetCameras)
	if scopeLabel == "" {
		scopeLabel = queryJoinNatural(cameras)
	}
	dateLabel := formatQueryDateForHumans(date)
	if dateLabel == "" {
		dateLabel = "the selected date"
	}

	openingScope := dateLabel
	if scopeLabel != "" {
		openingScope = scopeLabel + " on " + dateLabel
	}
	timeRange := queryTimeRangeLabel(times)
	if strings.TrimSpace(timeHint) != "" {
		timeRange = strings.TrimSpace(timeHint)
	}

	parts := []string{
		fmt.Sprintf(
			"%s recorded %d matching %s",
			openingScope,
			len(sortedEvents),
			pluralWord(len(sortedEvents), "event", "events"),
		),
	}
	if timeRange != "" {
		parts[0] += " " + timeRange
	}
	parts[0] += "."

	if len(personOrder) > 0 {
		rows := make([]string, 0, len(personOrder))
		for _, key := range personOrder {
			if len(rows) >= 5 {
				rows = append(rows, fmt.Sprintf("%d more identities", len(personOrder)-len(rows)))
				break
			}
			pa := persons[key]
			when := queryCompactTimes(pa.Times)
			if when == "" {
				when = "time not recorded"
			}
			cameraSuffix := ""
			if scopeLabel == "" && len(pa.Cameras) > 0 {
				cameraSuffix = " on " + queryJoinNatural(pa.Cameras)
			}
			rows = append(rows, fmt.Sprintf("%s was seen at %s%s", pa.Label, when, cameraSuffix))
		}
		parts = append(parts, "People: "+strings.Join(rows, "; ")+".")
	}

	if high > 0 {
		parts = append(parts, fmt.Sprintf("Severity: %d high and %d medium matching events.", high, medium))
	} else {
		parts = append(parts, "No high-severity alert appears in the matching event evidence.")
	}
	parts = append(parts, "The evidence list below is restricted to the matching camera/time filters.")
	return strings.Join(parts, " ")
}

func queryEventScopePhrase(date string, targetCameras []queryCameraTarget, timeHint string) string {
	dateLabel := formatQueryDateForHumans(date)
	cameraLabel := queryCameraListLabelFromTargets(targetCameras)
	parts := make([]string, 0, 3)
	if cameraLabel != "" {
		parts = append(parts, cameraLabel)
	}
	if dateLabel != "" {
		parts = append(parts, "on "+dateLabel)
	}
	if strings.TrimSpace(timeHint) != "" {
		parts = append(parts, "during "+strings.TrimSpace(timeHint))
	}
	return strings.Join(parts, " ")
}

func queryCameraListLabelFromTargets(targets []queryCameraTarget) string {
	if len(targets) == 0 {
		return ""
	}
	names := make([]string, 0, len(targets))
	seen := map[string]bool{}
	for _, target := range targets {
		name := strings.TrimSpace(target.Name)
		if name == "" {
			name = strings.TrimSpace(target.ID)
		}
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		names = append(names, name)
	}
	return queryJoinNatural(names)
}

func formatQueryDateForHumans(date string) string {
	if dt, err := time.Parse("2006-01-02", strings.TrimSpace(date)); err == nil {
		return dt.Format("Jan 2, 2006")
	}
	return strings.TrimSpace(date)
}

func queryContextEventTime(ev qsContextEvent) time.Time {
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(ev.CreatedAt)); err == nil {
		return t
	}
	return time.Time{}
}

func queryContextTimelineTime(moment qsContextTimeline) time.Time {
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(moment.CreatedAt)); err == nil {
		return t
	}
	return time.Time{}
}

func queryEventTimeLabel(ev qsContextEvent) string {
	t := queryContextEventTime(ev)
	if !t.IsZero() {
		return t.Format("15:04")
	}
	if len(ev.CreatedAt) >= 16 {
		return ev.CreatedAt[11:16]
	}
	return ""
}

func queryTimelineTimeLabel(moment qsContextTimeline) string {
	t := queryContextTimelineTime(moment)
	if !t.IsZero() {
		return t.Format("15:04")
	}
	if len(moment.CreatedAt) >= 16 {
		return moment.CreatedAt[11:16]
	}
	return ""
}

func queryEventCameraLabel(ev qsContextEvent) string {
	camera := strings.TrimSpace(ev.CameraName)
	if camera == "" {
		camera = strings.TrimSpace(ev.ServiceID)
	}
	return camera
}

func queryTimelineCameraLabel(moment qsContextTimeline) string {
	camera := strings.TrimSpace(moment.CameraName)
	if camera == "" {
		camera = strings.TrimSpace(moment.ServiceID)
	}
	return camera
}

func queryEventPersonLabel(ev qsContextEvent) string {
	if name := strings.TrimSpace(ev.PersonName); name != "" {
		return normalizeQueryPersonLabel(name)
	}
	if id := strings.TrimSpace(ev.GlobalPersonID); id != "" {
		return normalizeQueryGlobalPersonID(id)
	}
	if m := reUnknownPerson.FindStringSubmatch(ev.Brief); len(m) == 2 {
		return "Unknown #" + m[1]
	}
	return ""
}

func queryTimelinePersonLabel(moment qsContextTimeline) string {
	if name := strings.TrimSpace(moment.PersonName); name != "" {
		return normalizeQueryPersonLabel(name)
	}
	if id := strings.TrimSpace(moment.GlobalPersonID); id != "" {
		return normalizeQueryGlobalPersonID(id)
	}
	if id := strings.TrimSpace(moment.PersonID); id != "" {
		return "Person " + id
	}
	return ""
}

func normalizeQueryPersonLabel(name string) string {
	name = strings.TrimSpace(name)
	if m := reUnknownPerson.FindStringSubmatch(name); len(m) == 2 {
		return "Unknown #" + m[1]
	}
	return name
}

func normalizeQueryGlobalPersonID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	lower := strings.ToLower(id)
	if strings.Contains(lower, "unknown") {
		parts := strings.FieldsFunc(id, func(r rune) bool {
			return r == ':' || r == '/' || r == '#' || r == ' '
		})
		for i := len(parts) - 1; i >= 0; i-- {
			part := strings.TrimSpace(parts[i])
			if part == "" {
				continue
			}
			if _, err := strconv.Atoi(part); err == nil {
				return "Unknown #" + part
			}
		}
		return "Unknown"
	}
	return id
}

func queryTimeRangeLabel(times []string) string {
	if len(times) == 0 {
		return ""
	}
	if len(times) == 1 {
		return "around " + times[0]
	}
	return "between " + times[0] + " and " + times[len(times)-1]
}

func queryCompactTimes(times []string) string {
	if len(times) == 0 {
		return ""
	}
	if len(times) > 4 {
		return strings.Join(times[:4], ", ") + fmt.Sprintf(" and %d more", len(times)-4)
	}
	return queryJoinNatural(times)
}

func queryJoinNatural(items []string) string {
	clean := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			clean = append(clean, item)
		}
	}
	switch len(clean) {
	case 0:
		return ""
	case 1:
		return clean[0]
	case 2:
		return clean[0] + " and " + clean[1]
	default:
		return strings.Join(clean[:len(clean)-1], ", ") + ", and " + clean[len(clean)-1]
	}
}

func pluralWord(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func stringSliceContains(items []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item), target) {
			return true
		}
	}
	return false
}

func queryDeterministicAnswer(question string, events []qsContextEvent, timeline []qsContextTimeline, chunks []qsContextChunk, date string, scaffold string) string {
	if len(timeline) > 0 {
		return queryDeterministicTimelineAnswer(question, timeline, date, nil, "")
	}
	if len(events) == 0 && len(chunks) == 0 {
		return fmt.Sprintf("I did not find meaningful activity related to '%s' on %s.", question, date)
	}
	if len(events) == 0 && len(chunks) > 0 {
		parts := []string{
			fmt.Sprintf("I found relevant VYRA documentation for '%s', but no matching recorded events on %s.", question, date),
		}
		for i, chunk := range chunks {
			if i >= 3 {
				break
			}
			label := chunk.Title
			if chunk.Heading != "" {
				label += " / " + chunk.Heading
			}
			snippet := strings.TrimSpace(chunk.Text)
			if len(snippet) > 220 {
				snippet = strings.TrimSpace(snippet[:219]) + "..."
			}
			if label == "" {
				label = chunk.SourceURI
			}
			parts = append(parts, fmt.Sprintf("%s: %s", label, snippet))
		}
		return strings.Join(parts, "\n")
	}
	var high, medium, low int
	cameraSet := map[string]bool{}
	personSet := map[string]bool{}
	cameras := make([]string, 0, 8)
	people := make([]string, 0, 8)
	highlights := make([]string, 0, 3)
	seenHighlights := map[string]bool{}
	times := make([]string, 0, len(events))

	for _, e := range events {
		switch strings.ToLower(strings.TrimSpace(e.Severity)) {
		case "high":
			high++
		case "medium":
			medium++
		default:
			low++
		}

		camera := strings.TrimSpace(e.CameraName)
		if camera == "" {
			camera = strings.TrimSpace(e.ServiceID)
		}
		if camera != "" && !cameraSet[camera] {
			cameraSet[camera] = true
			cameras = append(cameras, camera)
		}

		person := strings.TrimSpace(e.PersonName)
		if person != "" && !isUnknownQueryPersonName(person) && !personSet[person] {
			personSet[person] = true
			people = append(people, person)
		}

		if len(e.CreatedAt) >= 16 {
			times = append(times, e.CreatedAt[11:16])
		}

		brief := strings.TrimSpace(e.Brief)
		if brief != "" {
			if len(brief) > 120 {
				brief = strings.TrimSpace(brief[:119]) + "…"
			}
			if !seenHighlights[brief] && len(highlights) < 2 {
				seenHighlights[brief] = true
				highlights = append(highlights, brief)
			}
		}
	}

	timeRange := "during the selected time window"
	if len(times) > 0 {
		minT := times[0]
		maxT := times[0]
		for _, t := range times[1:] {
			if t < minT {
				minT = t
			}
			if t > maxT {
				maxT = t
			}
		}
		if minT == maxT {
			timeRange = "around " + minT
		} else {
			timeRange = "between " + minT + " and " + maxT
		}
	}

	parts := []string{
		fmt.Sprintf(
			"On %s, relevant security activity related to '%s' was observed %s (%d matching signal(s)).",
			date, question, timeRange, len(events),
		),
	}
	if len(cameras) > 0 {
		camList := strings.Join(cameras, ", ")
		if len(cameras) > 4 {
			camList = strings.Join(cameras[:4], ", ") + ", and others"
		}
		parts = append(parts, fmt.Sprintf(
			"Activity appeared on %d camera(s): %s.",
			len(cameras), camList,
		))
	}
	if len(people) > 0 {
		nameList := strings.Join(people, ", ")
		if len(people) > 4 {
			nameList = strings.Join(people[:4], ", ") + ", and others"
		}
		parts = append(parts, "People identified include "+nameList+".")
	}
	if high > 0 {
		parts = append(parts, fmt.Sprintf(
			"Severity mix: %d high, %d medium, %d lower-priority event(s).",
			high, medium, low,
		))
	} else if medium > 0 {
		parts = append(parts, fmt.Sprintf(
			"Most activity was medium priority (%d event(s)) with no high-severity incidents in this result set.",
			medium,
		))
	} else {
		parts = append(parts, "No high- or medium-severity incidents appeared in this result set.")
	}
	for _, h := range highlights {
		parts = append(parts, "Example: "+h+".")
	}

	if scaffold != "" {
		return scaffold + "\n\n" + strings.Join(parts, "\n")
	}
	return strings.Join(parts, "\n")
}

func buildDeterministicScaffold(question, date string, events []qsContextEvent, knownPersons []string, timeHint string) string {
	if len(events) == 0 {
		return ""
	}
	q := strings.ToLower(question)
	targetName := ""
	for _, kp := range knownPersons {
		k := strings.TrimSpace(kp)
		if k == "" || isUnknownQueryPersonName(k) {
			continue
		}
		if strings.Contains(q, strings.ToLower(k)) {
			targetName = k
			break
		}
	}
	if targetName == "" {
		return ""
	}

	seenCamera := map[string]bool{}
	cameraList := []string{}
	activity := []string{}
	for _, ev := range events {
		if !strings.EqualFold(strings.TrimSpace(ev.PersonName), targetName) {
			continue
		}
		camera := strings.TrimSpace(ev.CameraName)
		if camera == "" {
			camera = strings.TrimSpace(ev.ServiceID)
		}
		if camera != "" && !seenCamera[camera] {
			seenCamera[camera] = true
			cameraList = append(cameraList, camera)
		}
		if ev.Brief != "" {
			activity = append(activity, ev.Brief)
		}
	}
	if len(cameraList) == 0 {
		return ""
	}

	brief := ""
	if len(activity) > 0 {
		brief = activity[0]
		if len(brief) > 180 {
			brief = brief[:180]
		}
	}
	when := date
	if when == "" {
		when = "today"
	}
	base := fmt.Sprintf("%s was seen on %s during %s.", targetName, strings.Join(cameraList, ", "), when)
	if strings.TrimSpace(timeHint) != "" {
		base += " Time focus: " + strings.TrimSpace(timeHint) + "."
	}
	if brief != "" {
		base += " Observed activity: " + brief
	}
	return base
}
