package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type kbSearchHTTPBody struct {
	Query   string                 `json:"query"`
	Limit   int                    `json:"limit,omitempty"`
	Filters KnowledgeSearchFilters `json:"filters,omitempty"`
	Embed   *bool                  `json:"embed,omitempty"`
}

type kbReindexDocsBody struct {
	Embed       bool   `json:"embed,omitempty"`
	Version     string `json:"version,omitempty"`
	Environment string `json:"environment,omitempty"`
	Language    string `json:"language,omitempty"`
	AccessLevel string `json:"access_level,omitempty"`
}

type indexedKnowledgeDoc struct {
	Path       string `json:"path"`
	DocumentID string `json:"document_id,omitempty"`
	Chunks     int    `json:"chunks,omitempty"`
	Embedded   int    `json:"embedded,omitempty"`
	Skipped    bool   `json:"skipped,omitempty"`
}

type kbEmbeddingResponse struct {
	Embedding      []float64 `json:"embedding"`
	EmbeddingModel string    `json:"embedding_model,omitempty"`
}

type kbRerankResponse struct {
	Results []struct {
		ID    string  `json:"id"`
		Score float64 `json:"score"`
	} `json:"results"`
	RerankerModel string `json:"reranker_model,omitempty"`
}

var kbEmbeddingState = struct {
	sync.Mutex
	lastError string
	lastOK    time.Time
}{}

func setKnowledgeEmbeddingError(err error) {
	kbEmbeddingState.Lock()
	defer kbEmbeddingState.Unlock()
	if err == nil {
		kbEmbeddingState.lastError = ""
		kbEmbeddingState.lastOK = time.Now().UTC()
		return
	}
	kbEmbeddingState.lastError = err.Error()
}

func knowledgeEmbeddingLastError() string {
	kbEmbeddingState.Lock()
	defer kbEmbeddingState.Unlock()
	return kbEmbeddingState.lastError
}

func handleKBIndex(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req KnowledgeDocumentInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	chunks := ChunkKnowledgeText(req)
	embedded := 0
	if req.Embed {
		embedded = embedKnowledgeChunks(relay, chunks)
	}
	res, err := relay.db.IndexKnowledgeDocument(userID, req, chunks)
	if err != nil {
		http.Error(w, "Failed to index knowledge document: "+err.Error(), http.StatusBadRequest)
		return
	}
	if embedded > 0 {
		res.Embedded = embedded
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func handleKBReindexDocs(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req kbReindexDocsBody
	_ = json.NewDecoder(r.Body).Decode(&req)

	docsDir, out, err := reindexBundledKnowledgeDocs(relay, userID, req, false)
	if err != nil {
		if strings.Contains(err.Error(), "application/docs not found") {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, "Failed to reindex docs: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"docs_dir": docsDir,
		"indexed":  out,
	})
}

func reindexBundledKnowledgeDocs(relay *Relay, userID int64, req kbReindexDocsBody, skipUnchanged bool) (string, []indexedKnowledgeDoc, error) {
	docsDir, err := resolveVYRADocsDir(relay)
	if err != nil {
		return "", nil, err
	}
	files, err := filepath.Glob(filepath.Join(docsDir, "*.md"))
	if err != nil {
		return "", nil, fmt.Errorf("failed to scan docs: %w", err)
	}
	sort.Strings(files)

	out := make([]indexedKnowledgeDoc, 0, len(files))
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		title := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
		if h := firstMarkdownHeading(string(data)); h != "" {
			title = h
		}
		relPath := filepath.ToSlash(strings.TrimPrefix(file, filepath.Clean(relay.config.RootDir)+string(filepath.Separator)))
		if strings.HasPrefix(relPath, "/") || relay.config.RootDir == "" {
			relPath = filepath.ToSlash(file)
		}
		input := KnowledgeDocumentInput{
			SourceURI:   relPath,
			SourceType:  "markdown",
			Title:       title,
			Text:        string(data),
			Version:     req.Version,
			Environment: req.Environment,
			Language:    firstNonEmpty(req.Language, "en"),
			AccessLevel: firstNonEmpty(req.AccessLevel, "internal"),
			Metadata: map[string]interface{}{
				"indexed_from": "application/docs",
				"filename":     filepath.Base(file),
			},
		}
		checksum := checksumKnowledgeInput(input)
		if skipUnchanged {
			if existing, err := relay.db.KnowledgeDocumentChecksum(userID, input.SourceURI); err == nil && existing == checksum {
				out = append(out, indexedKnowledgeDoc{Path: relPath, Skipped: true})
				continue
			}
		}
		chunks := ChunkKnowledgeText(input)
		embedded := 0
		if req.Embed {
			embedded = embedKnowledgeChunks(relay, chunks)
		}
		res, err := relay.db.IndexKnowledgeDocument(userID, input, chunks)
		if err != nil {
			return docsDir, out, fmt.Errorf("failed to index %s: %w", filepath.Base(file), err)
		}
		if embedded > 0 {
			res.Embedded = embedded
		}
		out = append(out, indexedKnowledgeDoc{Path: relPath, DocumentID: res.DocumentID, Chunks: res.ChunkCount, Embedded: res.Embedded})
	}
	return docsDir, out, nil
}

func handleKBSearch(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req kbSearchHTTPBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		http.Error(w, "query is required", http.StatusBadRequest)
		return
	}
	useEmbedding := shouldUseRAGEmbeddings()
	if req.Embed != nil {
		useEmbedding = *req.Embed
	}
	var queryEmbedding []float64
	if useEmbedding {
		if emb, err := embedKnowledgeText(relay, query, "query"); err == nil {
			queryEmbedding = emb
		}
	}
	results, err := relay.db.SearchKnowledge(userID, KnowledgeSearchRequest{
		Query:          query,
		Limit:          max(req.Limit, ragRerankTopN()),
		Filters:        req.Filters,
		QueryEmbedding: queryEmbedding,
	})
	if err != nil {
		http.Error(w, "Search failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	finalLimit := req.Limit
	if finalLimit <= 0 {
		finalLimit = ragFinalTopK()
	}
	if reranked, err := rerankKnowledgeResults(relay, query, results, finalLimit); err == nil {
		results = reranked
	} else {
		log.Printf("[KnowledgeBase] rerank unavailable: %v", err)
		results = limitKnowledgeResults(results, finalLimit)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"results": results,
	})
}

func handleKBStats(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stats, err := relay.db.KnowledgeStats(userID)
	if err != nil {
		http.Error(w, "Failed to read KB stats: "+err.Error(), http.StatusInternalServerError)
		return
	}
	stats["embedding_enabled"] = shouldUseRAGEmbeddings()
	stats["embedding_endpoint"] = knowledgeEmbeddingEndpoint(relay)
	stats["embedding_model"] = knowledgeEmbeddingModel()
	if lastErr := knowledgeEmbeddingLastError(); lastErr != "" {
		stats["last_embedding_error"] = lastErr
	}
	chunks, _ := stats["chunks"].(int64)
	embedded, _ := stats["embedded_chunks"].(int64)
	switch {
	case !shouldUseRAGEmbeddings():
		stats["embedding_health"] = "disabled"
	case chunks > 0 && embedded == chunks:
		stats["embedding_health"] = "ok"
	case embedded > 0 && embedded < chunks:
		stats["embedding_health"] = "partial"
	default:
		stats["embedding_health"] = "unavailable"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stats)
}

func handleKBFeedback(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		TraceID string `json:"trace_id"`
		Rating  string `json:"rating"`
		Comment string `json:"comment,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if err := relay.db.StoreKnowledgeFeedback(userID, req.TraceID, req.Rating, req.Comment); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func resolveVYRADocsDir(relay *Relay) (string, error) {
	candidates := []string{}
	if relay != nil && relay.config != nil && strings.TrimSpace(relay.config.RootDir) != "" {
		candidates = append(candidates, filepath.Join(relay.config.RootDir, "application", "docs"))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "..", "docs"))
		candidates = append(candidates, filepath.Join(wd, "application", "docs"))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return filepath.Clean(candidate), nil
		}
	}
	return "", fmt.Errorf("application/docs not found; set VYRA_ROOT or POST documents to /kb/index")
}

func firstMarkdownHeading(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if m := kbHeadingRe.FindStringSubmatch(strings.TrimSpace(line)); len(m) == 3 {
			return strings.TrimSpace(m[2])
		}
	}
	return ""
}

func shouldUseRAGEmbeddings() bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("RAG_ENABLE_EMBEDDINGS")))
	return raw == "" || raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}

func knowledgeEmbeddingModel() string {
	return firstNonEmpty(os.Getenv("RAG_TEXT_EMBEDDING_MODEL"), "BAAI/bge-small-en-v1.5")
}

func knowledgeEmbeddingEndpoint(relay *Relay) string {
	base := strings.TrimRight(os.Getenv("RAG_EMBEDDING_URL"), "/")
	if base != "" {
		return base
	}
	port := "5000"
	if relay != nil && relay.config != nil && strings.TrimSpace(relay.config.EnginePort) != "" {
		port = strings.TrimSpace(relay.config.EnginePort)
	}
	return "http://127.0.0.1:" + port + "/api/worker/fast_embedding"
}

func embedKnowledgeChunks(relay *Relay, chunks []KnowledgeChunk) int {
	embedded := 0
	for i := range chunks {
		emb, err := embedKnowledgeText(relay, chunks[i].Text, "passage")
		if err != nil {
			log.Printf("[KnowledgeBase] embedding failed source=%s chunk=%d: %v", chunks[i].SourceURI, chunks[i].ChunkIndex, err)
			continue
		}
		chunks[i].Embedding = emb
		chunks[i].EmbeddingDim = len(emb)
		chunks[i].EmbeddingModel = knowledgeEmbeddingModel()
		embedded++
	}
	return embedded
}

func embedKnowledgeText(relay *Relay, text string, promptName string) ([]float64, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("text is empty")
	}
	if len(text) > 6000 {
		text = text[:6000]
	}
	if promptName == "" {
		promptName = "query"
	}
	base := knowledgeEmbeddingEndpoint(relay)
	body, _ := json.Marshal(map[string]interface{}{
		"job": map[string]interface{}{
			"text":        text,
			"prompt_name": promptName,
		},
	})
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post(base, "application/json", bytes.NewReader(body))
	if err != nil {
		setKnowledgeEmbeddingError(err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		err := fmt.Errorf("embedding endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
		setKnowledgeEmbeddingError(err)
		return nil, err
	}
	var parsed kbEmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		setKnowledgeEmbeddingError(err)
		return nil, err
	}
	if len(parsed.Embedding) == 0 {
		err := fmt.Errorf("empty embedding")
		setKnowledgeEmbeddingError(err)
		return nil, err
	}
	setKnowledgeEmbeddingError(nil)
	return parsed.Embedding, nil
}

func shouldUseRAGReranker() bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("RAG_ENABLE_RERANKER")))
	return raw == "" || raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}

func ragRerankTopN() int {
	return envInt("RAG_RERANK_TOP_N", 30, 5, 100)
}

func ragFinalTopK() int {
	return envInt("RAG_FINAL_TOP_K", 8, 1, 30)
}

func knowledgeRerankEndpoint(relay *Relay) string {
	base := strings.TrimRight(os.Getenv("RAG_RERANK_URL"), "/")
	if base != "" {
		return base
	}
	port := "5000"
	if relay != nil && relay.config != nil && strings.TrimSpace(relay.config.EnginePort) != "" {
		port = strings.TrimSpace(relay.config.EnginePort)
	}
	return "http://127.0.0.1:" + port + "/api/worker/rag_rerank"
}

func rerankKnowledgeResults(relay *Relay, query string, results []KnowledgeSearchResult, finalLimit int) ([]KnowledgeSearchResult, error) {
	if finalLimit <= 0 {
		finalLimit = ragFinalTopK()
	}
	if len(results) <= finalLimit || !shouldUseRAGReranker() {
		return limitKnowledgeResults(results, finalLimit), nil
	}
	candidates := make([]map[string]interface{}, 0, len(results))
	for _, r := range results {
		candidates = append(candidates, map[string]interface{}{
			"id":      r.ID,
			"title":   r.Title,
			"heading": r.Heading,
			"text":    r.Text,
		})
	}
	body, _ := json.Marshal(map[string]interface{}{
		"job": map[string]interface{}{
			"query":      strings.TrimSpace(query),
			"candidates": candidates,
		},
	})
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(knowledgeRerankEndpoint(relay), "application/json", bytes.NewReader(body))
	if err != nil {
		return limitKnowledgeResults(results, finalLimit), err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return limitKnowledgeResults(results, finalLimit), fmt.Errorf("rerank endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed kbRerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return limitKnowledgeResults(results, finalLimit), err
	}
	byID := make(map[string]*KnowledgeSearchResult, len(results))
	for i := range results {
		byID[results[i].ID] = &results[i]
	}
	ranked := make([]KnowledgeSearchResult, 0, len(parsed.Results))
	for _, rr := range parsed.Results {
		item := byID[strings.TrimSpace(rr.ID)]
		if item == nil {
			continue
		}
		next := *item
		next.Score = 1 / (1 + math.Exp(-rr.Score))
		next.ScoreReason = appendScoreReason(next.ScoreReason, "rerank")
		ranked = append(ranked, next)
		if len(ranked) >= finalLimit {
			break
		}
	}
	if len(ranked) == 0 {
		return limitKnowledgeResults(results, finalLimit), fmt.Errorf("reranker returned no matching IDs")
	}
	return ranked, nil
}

func limitKnowledgeResults(results []KnowledgeSearchResult, limit int) []KnowledgeSearchResult {
	if limit <= 0 || len(results) <= limit {
		return results
	}
	return results[:limit]
}

func appendScoreReason(reason, addition string) string {
	reason = strings.TrimSpace(reason)
	addition = strings.TrimSpace(addition)
	if addition == "" {
		return reason
	}
	if reason == "" {
		return addition
	}
	for _, part := range strings.Split(reason, "+") {
		if strings.TrimSpace(part) == addition {
			return reason
		}
	}
	return reason + "+" + addition
}

func (relay *Relay) autoIndexKnowledgeDocsOnStart() {
	if relay == nil || relay.db == nil {
		return
	}
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("RAG_AUTO_INDEX_DOCS")))
	if raw == "0" || raw == "false" || raw == "no" || raw == "off" {
		return
	}
	time.Sleep(2 * time.Second)
	userIDs, err := relay.db.KnowledgeUserIDs()
	if err != nil {
		log.Printf("[KnowledgeBase] startup auto-index user lookup failed: %v", err)
		return
	}
	if len(userIDs) == 0 {
		return
	}
	req := kbReindexDocsBody{
		Embed:       shouldUseRAGEmbeddings(),
		Language:    "en",
		AccessLevel: "internal",
	}
	if req.Embed && relay.processManager != nil && relay.processManager.IsAvailable() {
		if err := relay.processManager.EnsureRAGWorkers(); err != nil {
			log.Printf("[KnowledgeBase] startup RAG worker ensure failed: %v", err)
		} else {
			time.Sleep(time.Duration(envInt("RAG_AUTO_INDEX_WORKER_GRACE_MS", 3000, 0, 60000)) * time.Millisecond)
		}
	}
	for _, userID := range userIDs {
		docsDir, indexed, err := reindexBundledKnowledgeDocs(relay, userID, req, true)
		if err != nil {
			log.Printf("[KnowledgeBase] startup auto-index failed user=%d: %v", userID, err)
			continue
		}
		changed := 0
		for _, item := range indexed {
			if !item.Skipped {
				changed++
			}
		}
		if changed > 0 {
			log.Printf("[KnowledgeBase] startup auto-index user=%d docs_dir=%s changed=%d total=%d", userID, docsDir, changed, len(indexed))
		}
	}
}
