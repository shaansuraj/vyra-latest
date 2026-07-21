package relay

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	defaultKBChunkTargetChars = 1800
	defaultKBChunkOverlap     = 260
	defaultKBSearchLimit      = 8
	defaultKBCandidateLimit   = 500
	defaultKBHybridStreamTopN = 80
)

type KnowledgeDocumentInput struct {
	SourceURI    string                 `json:"source_uri"`
	SourceType   string                 `json:"source_type,omitempty"`
	Title        string                 `json:"title"`
	Text         string                 `json:"text"`
	Version      string                 `json:"version,omitempty"`
	Environment  string                 `json:"environment,omitempty"`
	Language     string                 `json:"language,omitempty"`
	AccessLevel  string                 `json:"access_level,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	Embed        bool                   `json:"embed,omitempty"`
	EmbeddingURL string                 `json:"embedding_url,omitempty"`
}

type KnowledgeDocumentIndexResult struct {
	DocumentID string `json:"document_id"`
	ChunkCount int    `json:"chunk_count"`
	Checksum   string `json:"checksum"`
	Embedded   int    `json:"embedded"`
}

type KnowledgeChunk struct {
	ID             string                 `json:"id"`
	DocumentID     string                 `json:"document_id"`
	UserID         int64                  `json:"user_id"`
	ChunkIndex     int                    `json:"chunk_index"`
	Text           string                 `json:"text"`
	TokenCount     int                    `json:"token_count"`
	Title          string                 `json:"title"`
	Heading        string                 `json:"heading"`
	SectionPath    string                 `json:"section_path"`
	SourceURI      string                 `json:"source_uri"`
	Version        string                 `json:"version"`
	Environment    string                 `json:"environment"`
	Language       string                 `json:"language"`
	AccessLevel    string                 `json:"access_level"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
	Embedding      []float64              `json:"-"`
	EmbeddingModel string                 `json:"embedding_model,omitempty"`
	EmbeddingDim   int                    `json:"embedding_dim,omitempty"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

type KnowledgeSearchFilters struct {
	Version      string   `json:"version,omitempty"`
	Environment  string   `json:"environment,omitempty"`
	Language     string   `json:"language,omitempty"`
	AccessLevels []string `json:"access_levels,omitempty"`
	SourceURI    string   `json:"source_uri,omitempty"`
}

type KnowledgeSearchRequest struct {
	Query          string                 `json:"query"`
	Limit          int                    `json:"limit,omitempty"`
	Filters        KnowledgeSearchFilters `json:"filters,omitempty"`
	QueryEmbedding []float64              `json:"-"`
}

type KnowledgeSearchResult struct {
	KnowledgeChunk
	Score        float64 `json:"score"`
	FTSScore     float64 `json:"fts_score,omitempty"`
	LexicalScore float64 `json:"lexical_score,omitempty"`
	VectorScore  float64 `json:"vector_score,omitempty"`
	ScoreReason  string  `json:"score_reason,omitempty"`
	Snippet      string  `json:"snippet,omitempty"`
}

type knowledgeRankSignal struct {
	Rank  int
	Score float64
}

type KnowledgeTraceInput struct {
	UserID                int64
	ChatID                string
	Intent                string
	Question              string
	RewrittenQuery        string
	Filters               KnowledgeSearchFilters
	RetrievedTimelineJSON []byte
	RetrievedChunksJSON   []byte
	RetrievedEventsJSON   []byte
	PromptDigest          string
	Answer                string
	Confidence            string
	LatencyMS             int64
}

var (
	kbHeadingRe     = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
	kbTokenRe       = regexp.MustCompile(`[a-zA-Z0-9][a-zA-Z0-9_-]*`)
	kbWhitespaceRe  = regexp.MustCompile(`\s+`)
	kbUnsafeFTSChar = regexp.MustCompile(`[^a-zA-Z0-9_]+`)
)

func ensureKnowledgeFTS(db *sql.DB) {
	_, err := db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS kb_chunk_fts USING fts5(
			chunk_id UNINDEXED,
			user_id UNINDEXED,
			source_uri UNINDEXED,
			title,
			heading,
			section_path,
			text,
			tokenize = 'unicode61'
		)
	`)
	if err != nil {
		log.Printf("[KnowledgeBase] FTS5 unavailable; falling back to LIKE search: %v", err)
	}
}

func (db *Database) IndexKnowledgeDocument(userID int64, input KnowledgeDocumentInput, chunks []KnowledgeChunk) (KnowledgeDocumentIndexResult, error) {
	if userID <= 0 {
		return KnowledgeDocumentIndexResult{}, fmt.Errorf("user_id is required")
	}
	input.SourceURI = strings.TrimSpace(input.SourceURI)
	input.Title = strings.TrimSpace(input.Title)
	input.Text = strings.TrimSpace(input.Text)
	if input.SourceURI == "" {
		return KnowledgeDocumentIndexResult{}, fmt.Errorf("source_uri is required")
	}
	if input.Text == "" {
		return KnowledgeDocumentIndexResult{}, fmt.Errorf("text is required")
	}
	if input.Title == "" {
		input.Title = input.SourceURI
	}
	input.SourceType = normalizeKnowledgeValue(input.SourceType, "text")
	input.Language = normalizeKnowledgeValue(input.Language, "en")
	input.AccessLevel = normalizeKnowledgeValue(input.AccessLevel, "internal")
	input.Version = strings.TrimSpace(input.Version)
	input.Environment = strings.TrimSpace(input.Environment)

	if len(chunks) == 0 {
		chunks = ChunkKnowledgeText(input)
	}
	if len(chunks) == 0 {
		return KnowledgeDocumentIndexResult{}, fmt.Errorf("no chunks generated")
	}

	checksum := checksumKnowledgeInput(input)
	metadataJSON := mustJSON(input.Metadata, "{}")
	now := time.Now().UTC()
	documentID := uuid.New().String()

	db.writeMu.Lock()
	defer db.writeMu.Unlock()

	tx, err := db.DB.Begin()
	if err != nil {
		return KnowledgeDocumentIndexResult{}, err
	}
	defer tx.Rollback()

	var existingID string
	err = tx.QueryRow(`SELECT id FROM kb_documents WHERE user_id = ? AND source_uri = ?`, userID, input.SourceURI).Scan(&existingID)
	if err == nil && strings.TrimSpace(existingID) != "" {
		documentID = existingID
	} else if err != nil && err != sql.ErrNoRows {
		return KnowledgeDocumentIndexResult{}, err
	}

	_, err = tx.Exec(`
		INSERT INTO kb_documents (
			id, user_id, source_uri, source_type, title, version, environment,
			language, access_level, checksum, metadata, indexed_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, source_uri) DO UPDATE SET
			source_type = excluded.source_type,
			title = excluded.title,
			version = excluded.version,
			environment = excluded.environment,
			language = excluded.language,
			access_level = excluded.access_level,
			checksum = excluded.checksum,
			metadata = excluded.metadata,
			indexed_at = excluded.indexed_at,
			updated_at = excluded.updated_at
	`, documentID, userID, input.SourceURI, input.SourceType, input.Title, input.Version, input.Environment,
		input.Language, input.AccessLevel, checksum, metadataJSON, now, now)
	if err != nil {
		return KnowledgeDocumentIndexResult{}, err
	}

	_, _ = tx.Exec(`DELETE FROM kb_chunk_fts WHERE user_id = ? AND source_uri = ?`, userID, input.SourceURI)
	if _, err := tx.Exec(`DELETE FROM kb_chunks WHERE document_id = ?`, documentID); err != nil {
		return KnowledgeDocumentIndexResult{}, err
	}

	embedded := 0
	for idx, chunk := range chunks {
		chunk.ID = uuid.New().String()
		chunk.DocumentID = documentID
		chunk.UserID = userID
		chunk.ChunkIndex = idx
		chunk.Title = firstNonEmpty(chunk.Title, input.Title)
		chunk.SourceURI = firstNonEmpty(chunk.SourceURI, input.SourceURI)
		chunk.Version = firstNonEmpty(chunk.Version, input.Version)
		chunk.Environment = firstNonEmpty(chunk.Environment, input.Environment)
		chunk.Language = firstNonEmpty(chunk.Language, input.Language)
		chunk.AccessLevel = firstNonEmpty(chunk.AccessLevel, input.AccessLevel)
		chunk.TokenCount = approximateTokenCount(chunk.Text)
		chunk.Text = normalizeKnowledgeText(chunk.Text)
		if chunk.Text == "" {
			continue
		}
		if len(chunk.Metadata) == 0 {
			chunk.Metadata = input.Metadata
		}
		if len(chunk.Embedding) > 0 {
			chunk.EmbeddingDim = len(chunk.Embedding)
			embedded++
		}
		embeddingJSON := ""
		if len(chunk.Embedding) > 0 {
			embeddingJSON = mustJSON(chunk.Embedding, "[]")
		}
		chunkMetaJSON := mustJSON(chunk.Metadata, "{}")
		_, err := tx.Exec(`
			INSERT INTO kb_chunks (
				id, document_id, user_id, chunk_index, text, token_count, title,
				heading, section_path, source_uri, version, environment, language,
				access_level, metadata, embedding, embedding_model, embedding_dim,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, chunk.ID, documentID, userID, chunk.ChunkIndex, chunk.Text, chunk.TokenCount, chunk.Title,
			chunk.Heading, chunk.SectionPath, chunk.SourceURI, chunk.Version, chunk.Environment, chunk.Language,
			chunk.AccessLevel, chunkMetaJSON, embeddingJSON, chunk.EmbeddingModel, chunk.EmbeddingDim, now, now)
		if err != nil {
			return KnowledgeDocumentIndexResult{}, err
		}
		_, _ = tx.Exec(`
			INSERT INTO kb_chunk_fts (chunk_id, user_id, source_uri, title, heading, section_path, text)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, chunk.ID, userID, chunk.SourceURI, chunk.Title, chunk.Heading, chunk.SectionPath, chunk.Text)
	}

	if err := tx.Commit(); err != nil {
		return KnowledgeDocumentIndexResult{}, err
	}
	return KnowledgeDocumentIndexResult{DocumentID: documentID, ChunkCount: len(chunks), Checksum: checksum, Embedded: embedded}, nil
}

func ChunkKnowledgeText(input KnowledgeDocumentInput) []KnowledgeChunk {
	text := normalizeKnowledgeText(input.Text)
	if text == "" {
		return nil
	}

	type block struct {
		text        string
		heading     string
		sectionPath string
	}

	var blocks []block
	var headings [6]string
	currentHeading := ""
	currentSection := ""
	var para []string

	flushPara := func() {
		if len(para) == 0 {
			return
		}
		p := normalizeKnowledgeText(strings.Join(para, "\n"))
		if p != "" {
			blocks = append(blocks, block{text: p, heading: currentHeading, sectionPath: currentSection})
		}
		para = nil
	}

	for _, rawLine := range strings.Split(input.Text, "\n") {
		line := strings.TrimRight(rawLine, " \t")
		if m := kbHeadingRe.FindStringSubmatch(line); len(m) == 3 {
			flushPara()
			level := len(m[1])
			title := strings.TrimSpace(m[2])
			if level >= 1 && level <= len(headings) {
				headings[level-1] = title
				for i := level; i < len(headings); i++ {
					headings[i] = ""
				}
			}
			currentHeading = title
			parts := make([]string, 0, level)
			for _, h := range headings[:] {
				if strings.TrimSpace(h) != "" {
					parts = append(parts, strings.TrimSpace(h))
				}
			}
			currentSection = strings.Join(parts, " > ")
			continue
		}
		if strings.TrimSpace(line) == "" {
			flushPara()
			continue
		}
		para = append(para, line)
	}
	flushPara()

	if len(blocks) == 0 {
		blocks = append(blocks, block{text: text})
	}

	target := defaultKBChunkTargetChars
	overlap := defaultKBChunkOverlap
	var chunks []KnowledgeChunk
	var buf []string
	bufLen := 0
	activeHeading := ""
	activeSection := ""

	flushChunk := func() {
		chunkText := normalizeKnowledgeText(strings.Join(buf, "\n\n"))
		if chunkText == "" {
			buf = nil
			bufLen = 0
			return
		}
		chunks = append(chunks, KnowledgeChunk{
			Text:        chunkText,
			Title:       strings.TrimSpace(input.Title),
			Heading:     activeHeading,
			SectionPath: activeSection,
			SourceURI:   strings.TrimSpace(input.SourceURI),
			Version:     strings.TrimSpace(input.Version),
			Environment: strings.TrimSpace(input.Environment),
			Language:    normalizeKnowledgeValue(input.Language, "en"),
			AccessLevel: normalizeKnowledgeValue(input.AccessLevel, "internal"),
			Metadata:    input.Metadata,
			TokenCount:  approximateTokenCount(chunkText),
		})
		tail := rightOverlap(chunkText, overlap)
		buf = nil
		bufLen = 0
		if tail != "" {
			buf = append(buf, tail)
			bufLen = len(tail)
		}
	}

	for _, b := range blocks {
		p := normalizeKnowledgeText(b.text)
		if p == "" {
			continue
		}
		if bufLen > 0 && activeSection != "" && b.sectionPath != "" && b.sectionPath != activeSection {
			flushChunk()
			buf = nil
			bufLen = 0
			activeHeading = b.heading
			activeSection = b.sectionPath
		}
		if activeHeading == "" && b.heading != "" {
			activeHeading = b.heading
			activeSection = b.sectionPath
		}
		if bufLen > 0 && bufLen+len(p)+2 > target {
			flushChunk()
			activeHeading = b.heading
			activeSection = b.sectionPath
		}
		if len(p) > target+overlap {
			for len(p) > 0 {
				part := p
				if len(part) > target {
					cut := strings.LastIndexAny(part[:target], ".!?\n")
					if cut < target/2 {
						cut = strings.LastIndex(part[:target], " ")
					}
					if cut < target/2 {
						cut = target
					}
					part = strings.TrimSpace(part[:cut])
				}
				if part == "" {
					break
				}
				buf = append(buf, part)
				bufLen += len(part)
				flushChunk()
				if len(part) >= len(p) {
					break
				}
				start := len(part) - min(len(part), overlap)
				p = strings.TrimSpace(p[start:])
				activeHeading = b.heading
				activeSection = b.sectionPath
			}
			continue
		}
		buf = append(buf, p)
		bufLen += len(p) + 2
		activeHeading = firstNonEmpty(activeHeading, b.heading)
		activeSection = firstNonEmpty(activeSection, b.sectionPath)
	}
	if bufLen > 0 {
		flushChunk()
	}

	for i := range chunks {
		chunks[i].ChunkIndex = i
	}
	return chunks
}

func (db *Database) SearchKnowledge(userID int64, req KnowledgeSearchRequest) ([]KnowledgeSearchResult, error) {
	query := strings.TrimSpace(req.Query)
	if userID <= 0 {
		return nil, fmt.Errorf("user_id is required")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultKBSearchLimit
	}
	if limit > 30 {
		limit = 30
	}

	streamLimit := max(defaultKBHybridStreamTopN, limit)
	chunksByID := map[string]KnowledgeChunk{}
	ftsSignals := map[string]knowledgeRankSignal{}
	if ftsQuery := buildFTSQuery(query); ftsQuery != "" {
		if scores, err := db.searchKnowledgeFTS(userID, ftsQuery, req.Filters, streamLimit); err == nil {
			ftsSignals = scores
			ids := make([]string, 0, len(scores))
			for id := range scores {
				ids = append(ids, id)
			}
			if chunks, err := db.loadKnowledgeChunksByIDs(userID, ids); err == nil {
				for _, c := range chunks {
					chunksByID[c.ID] = c
				}
			}
		}
	}

	vectorSignals, vectorChunks, err := db.searchKnowledgeVector(userID, req.QueryEmbedding, req.Filters, streamLimit)
	if err != nil {
		return nil, err
	}
	for _, c := range vectorChunks {
		chunksByID[c.ID] = c
	}

	terms := queryTerms(query)
	lexicalSignals, lexicalChunks, err := db.searchKnowledgeLexical(userID, terms, query, req.Filters, streamLimit)
	if err != nil {
		return nil, err
	}
	for _, c := range lexicalChunks {
		chunksByID[c.ID] = c
	}

	if len(chunksByID) == 0 {
		return nil, nil
	}

	results := make([]KnowledgeSearchResult, 0, len(chunksByID))
	for _, c := range chunksByID {
		fts := 0.0
		vec := 0.0
		lex := 0.0
		combined := 0.0
		reasonParts := []string{"rrf"}
		if sig, ok := ftsSignals[c.ID]; ok {
			fts = sig.Score
			combined += reciprocalRankScore(sig.Rank)
			reasonParts = append(reasonParts, "fts")
		}
		if sig, ok := vectorSignals[c.ID]; ok {
			vec = sig.Score
			combined += reciprocalRankScore(sig.Rank)
			reasonParts = append(reasonParts, "vector")
		}
		if sig, ok := lexicalSignals[c.ID]; ok {
			lex = sig.Score
			combined += reciprocalRankScore(sig.Rank)
			reasonParts = append(reasonParts, "lexical")
		}
		if combined <= 0 {
			continue
		}
		results = append(results, KnowledgeSearchResult{
			KnowledgeChunk: c,
			Score:          combined,
			FTSScore:       fts,
			LexicalScore:   lex,
			VectorScore:    vec,
			ScoreReason:    strings.Join(reasonParts, "+"),
			Snippet:        makeKnowledgeSnippet(c.Text, terms),
		})
	}

	sort.SliceStable(results, func(i, j int) bool {
		if math.Abs(results[i].Score-results[j].Score) > 0.0001 {
			return results[i].Score > results[j].Score
		}
		return results[i].UpdatedAt.After(results[j].UpdatedAt)
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func reciprocalRankScore(rank int) float64 {
	if rank <= 0 {
		return 0
	}
	return 1.0 / float64(60+rank)
}

func (db *Database) searchKnowledgeFTS(userID int64, ftsQuery string, filters KnowledgeSearchFilters, limit int) (map[string]knowledgeRankSignal, error) {
	where, args := knowledgeFilterSQL("c", filters)
	args = append([]interface{}{ftsQuery, userID}, args...)
	args = append(args, limit)
	rows, err := db.DB.Query(fmt.Sprintf(`
		SELECT c.id, bm25(kb_chunk_fts) AS rank
		FROM kb_chunk_fts
		JOIN kb_chunks c ON c.id = kb_chunk_fts.chunk_id
		WHERE kb_chunk_fts MATCH ? AND c.user_id = ? %s
		ORDER BY rank ASC
		LIMIT ?
	`, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	scores := map[string]knowledgeRankSignal{}
	rank := 1
	for rows.Next() {
		var id string
		var bm25Rank float64
		if err := rows.Scan(&id, &bm25Rank); err != nil {
			return nil, err
		}
		rrf := 1.0 / float64(60+rank)
		score := (1.0 / (1.0 + math.Abs(bm25Rank))) + rrf
		if existing, ok := scores[id]; !ok || score > existing.Score {
			scores[id] = knowledgeRankSignal{Rank: rank, Score: score}
		}
		rank++
	}
	return scores, rows.Err()
}

func (db *Database) searchKnowledgeVector(userID int64, queryEmbedding []float64, filters KnowledgeSearchFilters, limit int) (map[string]knowledgeRankSignal, []KnowledgeChunk, error) {
	if len(queryEmbedding) == 0 {
		return nil, nil, nil
	}
	where, args := knowledgeFilterSQL("c", filters)
	scanLimit := envInt("RAG_VECTOR_SCAN_LIMIT", 2000, defaultKBHybridStreamTopN, 20000)
	args = append([]interface{}{userID, len(queryEmbedding)}, args...)
	args = append(args, scanLimit)
	rows, err := db.DB.Query(fmt.Sprintf(`
		SELECT
			c.id, c.document_id, c.user_id, c.chunk_index, c.text, c.token_count,
			c.title, c.heading, c.section_path, c.source_uri, c.version,
			c.environment, c.language, c.access_level, c.metadata, c.embedding,
			c.embedding_model, c.embedding_dim, c.updated_at
		FROM kb_chunks c
		WHERE c.user_id = ? AND c.embedding_dim = ? AND c.embedding != '' %s
		ORDER BY c.updated_at DESC
		LIMIT ?
	`, where), args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	chunks, err := scanKnowledgeChunks(rows)
	if err != nil {
		return nil, nil, err
	}
	sort.SliceStable(chunks, func(i, j int) bool {
		return cosineScore(queryEmbedding, chunks[i].Embedding) > cosineScore(queryEmbedding, chunks[j].Embedding)
	})
	if len(chunks) > limit {
		chunks = chunks[:limit]
	}
	signals := make(map[string]knowledgeRankSignal, len(chunks))
	for i, c := range chunks {
		signals[c.ID] = knowledgeRankSignal{Rank: i + 1, Score: cosineScore(queryEmbedding, c.Embedding)}
	}
	return signals, chunks, nil
}

func (db *Database) searchKnowledgeLexical(userID int64, terms []string, query string, filters KnowledgeSearchFilters, limit int) (map[string]knowledgeRankSignal, []KnowledgeChunk, error) {
	if len(terms) == 0 {
		return nil, nil, nil
	}
	where, args := knowledgeFilterSQL("c", filters)
	likeClauses := []string{}
	for _, term := range terms {
		if len(term) < 2 {
			continue
		}
		likeClauses = append(likeClauses, `(lower(c.text) LIKE ? OR lower(c.title) LIKE ? OR lower(c.heading) LIKE ? OR lower(c.section_path) LIKE ?)`)
		like := "%" + strings.ToLower(term) + "%"
		args = append(args, like, like, like, like)
		if len(likeClauses) >= 8 {
			break
		}
	}
	if len(likeClauses) == 0 {
		return nil, nil, nil
	}
	args = append([]interface{}{userID}, args...)
	args = append(args, max(limit*4, defaultKBHybridStreamTopN))
	rows, err := db.DB.Query(fmt.Sprintf(`
		SELECT
			c.id, c.document_id, c.user_id, c.chunk_index, c.text, c.token_count,
			c.title, c.heading, c.section_path, c.source_uri, c.version,
			c.environment, c.language, c.access_level, c.metadata, c.embedding,
			c.embedding_model, c.embedding_dim, c.updated_at
		FROM kb_chunks c
		WHERE c.user_id = ? %s AND (%s)
		ORDER BY c.updated_at DESC
		LIMIT ?
	`, where, strings.Join(likeClauses, " OR ")), args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	chunks, err := scanKnowledgeChunks(rows)
	if err != nil {
		return nil, nil, err
	}
	sort.SliceStable(chunks, func(i, j int) bool {
		return lexicalKnowledgeScore(terms, query, chunks[i]) > lexicalKnowledgeScore(terms, query, chunks[j])
	})
	if len(chunks) > limit {
		chunks = chunks[:limit]
	}
	signals := make(map[string]knowledgeRankSignal, len(chunks))
	for i, c := range chunks {
		signals[c.ID] = knowledgeRankSignal{Rank: i + 1, Score: lexicalKnowledgeScore(terms, query, c)}
	}
	return signals, chunks, nil
}

func (db *Database) loadKnowledgeChunksByIDs(userID int64, ids []string) ([]KnowledgeChunk, error) {
	clean := cleanStringList(ids, 200)
	if len(clean) == 0 {
		return nil, nil
	}
	placeholders := make([]string, 0, len(clean))
	args := []interface{}{userID}
	for _, id := range clean {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	rows, err := db.DB.Query(fmt.Sprintf(`
		SELECT
			c.id, c.document_id, c.user_id, c.chunk_index, c.text, c.token_count,
			c.title, c.heading, c.section_path, c.source_uri, c.version,
			c.environment, c.language, c.access_level, c.metadata, c.embedding,
			c.embedding_model, c.embedding_dim, c.updated_at
		FROM kb_chunks c
		WHERE c.user_id = ? AND c.id IN (%s)
	`, strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanKnowledgeChunks(rows)
}

func scanKnowledgeChunks(rows *sql.Rows) ([]KnowledgeChunk, error) {
	var chunks []KnowledgeChunk
	for rows.Next() {
		var c KnowledgeChunk
		var metadataJSON, embeddingJSON sql.NullString
		if err := rows.Scan(
			&c.ID, &c.DocumentID, &c.UserID, &c.ChunkIndex, &c.Text, &c.TokenCount,
			&c.Title, &c.Heading, &c.SectionPath, &c.SourceURI, &c.Version,
			&c.Environment, &c.Language, &c.AccessLevel, &metadataJSON, &embeddingJSON,
			&c.EmbeddingModel, &c.EmbeddingDim, &c.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if metadataJSON.Valid && strings.TrimSpace(metadataJSON.String) != "" {
			_ = json.Unmarshal([]byte(metadataJSON.String), &c.Metadata)
		}
		if embeddingJSON.Valid && strings.TrimSpace(embeddingJSON.String) != "" {
			_ = json.Unmarshal([]byte(embeddingJSON.String), &c.Embedding)
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

func (db *Database) loadKnowledgeCandidates(userID int64, query string, filters KnowledgeSearchFilters, limit int) ([]KnowledgeChunk, error) {
	where, args := knowledgeFilterSQL("c", filters)
	args = append([]interface{}{userID}, args...)

	terms := queryTerms(query)
	likeWhere := ""
	for _, term := range terms {
		if len(term) < 2 {
			continue
		}
		likeWhere += ` AND (
			lower(c.text) LIKE ? OR lower(c.title) LIKE ? OR lower(c.heading) LIKE ? OR lower(c.section_path) LIKE ?
		)`
		like := "%" + strings.ToLower(term) + "%"
		args = append(args, like, like, like, like)
		if len(args) > 18 {
			break
		}
	}
	args = append(args, limit)

	rows, err := db.DB.Query(fmt.Sprintf(`
		SELECT
			c.id, c.document_id, c.user_id, c.chunk_index, c.text, c.token_count,
			c.title, c.heading, c.section_path, c.source_uri, c.version,
			c.environment, c.language, c.access_level, c.metadata, c.embedding,
			c.embedding_model, c.embedding_dim, c.updated_at
		FROM kb_chunks c
		WHERE c.user_id = ? %s %s
		ORDER BY c.updated_at DESC
		LIMIT ?
	`, where, likeWhere), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chunks []KnowledgeChunk
	for rows.Next() {
		var c KnowledgeChunk
		var metadataJSON, embeddingJSON sql.NullString
		if err := rows.Scan(
			&c.ID, &c.DocumentID, &c.UserID, &c.ChunkIndex, &c.Text, &c.TokenCount,
			&c.Title, &c.Heading, &c.SectionPath, &c.SourceURI, &c.Version,
			&c.Environment, &c.Language, &c.AccessLevel, &metadataJSON, &embeddingJSON,
			&c.EmbeddingModel, &c.EmbeddingDim, &c.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if metadataJSON.Valid && strings.TrimSpace(metadataJSON.String) != "" {
			_ = json.Unmarshal([]byte(metadataJSON.String), &c.Metadata)
		}
		if embeddingJSON.Valid && strings.TrimSpace(embeddingJSON.String) != "" {
			_ = json.Unmarshal([]byte(embeddingJSON.String), &c.Embedding)
		}
		chunks = append(chunks, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// If strict LIKE terms found nothing, return a broader candidate pool so FTS
	// hits and vector matches can still participate.
	if len(chunks) == 0 && len(terms) > 0 {
		return db.loadKnowledgeCandidates(userID, "", filters, limit)
	}
	return chunks, nil
}

func knowledgeFilterSQL(alias string, filters KnowledgeSearchFilters) (string, []interface{}) {
	var clauses []string
	var args []interface{}
	col := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	if strings.TrimSpace(filters.Version) != "" {
		clauses = append(clauses, col("version")+" = ?")
		args = append(args, strings.TrimSpace(filters.Version))
	}
	if strings.TrimSpace(filters.Environment) != "" {
		clauses = append(clauses, col("environment")+" = ?")
		args = append(args, strings.TrimSpace(filters.Environment))
	}
	if strings.TrimSpace(filters.Language) != "" {
		clauses = append(clauses, col("language")+" = ?")
		args = append(args, strings.TrimSpace(filters.Language))
	}
	if strings.TrimSpace(filters.SourceURI) != "" {
		clauses = append(clauses, col("source_uri")+" = ?")
		args = append(args, strings.TrimSpace(filters.SourceURI))
	}
	levels := cleanStringList(filters.AccessLevels, 8)
	if len(levels) > 0 {
		placeholders := make([]string, 0, len(levels))
		for _, level := range levels {
			placeholders = append(placeholders, "?")
			args = append(args, level)
		}
		clauses = append(clauses, col("access_level")+" IN ("+strings.Join(placeholders, ",")+")")
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(clauses, " AND "), args
}

func (db *Database) StoreKnowledgeTrace(input KnowledgeTraceInput) (string, error) {
	if input.UserID <= 0 {
		return "", fmt.Errorf("user_id is required")
	}
	traceID := uuid.New().String()
	now := time.Now().UTC()
	filtersJSON := mustJSON(input.Filters, "{}")
	chunksJSON := string(input.RetrievedChunksJSON)
	if strings.TrimSpace(chunksJSON) == "" {
		chunksJSON = "[]"
	}
	eventsJSON := string(input.RetrievedEventsJSON)
	if strings.TrimSpace(eventsJSON) == "" {
		eventsJSON = "[]"
	}
	timelineJSON := string(input.RetrievedTimelineJSON)
	if strings.TrimSpace(timelineJSON) == "" {
		timelineJSON = "[]"
	}
	_, err := db.execWrite(`
		INSERT INTO kb_query_traces (
			id, chat_id, user_id, intent, question, rewritten_query, filters_json, retrieved_timeline_json, retrieved_chunks_json,
			retrieved_events_json, prompt_digest, answer, confidence, latency_ms, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, traceID, strings.TrimSpace(input.ChatID), input.UserID, strings.TrimSpace(input.Intent), input.Question, input.RewrittenQuery, filtersJSON, timelineJSON, chunksJSON,
		eventsJSON, input.PromptDigest, input.Answer, input.Confidence, input.LatencyMS, now)
	if err != nil {
		return "", err
	}
	return traceID, nil
}

func (db *Database) KnowledgeDocumentChecksum(userID int64, sourceURI string) (string, error) {
	var checksum string
	err := db.QueryRow(`
		SELECT checksum
		FROM kb_documents
		WHERE user_id = ? AND source_uri = ?
	`, userID, strings.TrimSpace(sourceURI)).Scan(&checksum)
	return checksum, err
}

func (db *Database) KnowledgeUserIDs() ([]int64, error) {
	rows, err := db.Query(`SELECT id FROM users ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (db *Database) StoreKnowledgeFeedback(userID int64, traceID, rating, comment string) error {
	traceID = strings.TrimSpace(traceID)
	rating = strings.ToLower(strings.TrimSpace(rating))
	if userID <= 0 || traceID == "" {
		return fmt.Errorf("trace_id is required")
	}
	switch rating {
	case "up", "down", "neutral":
	default:
		return fmt.Errorf("rating must be up, down, or neutral")
	}
	_, err := db.execWrite(`
		INSERT INTO kb_feedback (id, trace_id, user_id, rating, comment, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, uuid.New().String(), traceID, userID, rating, strings.TrimSpace(comment), time.Now().UTC())
	return err
}

func (db *Database) KnowledgeStats(userID int64) (map[string]interface{}, error) {
	var docs, chunks, embedded int64
	var embeddingDim int64
	var embeddingModel sql.NullString
	if err := db.QueryRow(`SELECT COUNT(*) FROM kb_documents WHERE user_id = ?`, userID).Scan(&docs); err != nil {
		return nil, err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM kb_chunks WHERE user_id = ?`, userID).Scan(&chunks); err != nil {
		return nil, err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM kb_chunks WHERE user_id = ? AND embedding_dim > 0`, userID).Scan(&embedded); err != nil {
		return nil, err
	}
	_ = db.QueryRow(`SELECT COALESCE(MAX(embedding_dim), 0) FROM kb_chunks WHERE user_id = ?`, userID).Scan(&embeddingDim)
	_ = db.QueryRow(`
		SELECT embedding_model
		FROM kb_chunks
		WHERE user_id = ? AND embedding_model != ''
		ORDER BY updated_at DESC
		LIMIT 1
	`, userID).Scan(&embeddingModel)
	return map[string]interface{}{
		"documents":       docs,
		"chunks":          chunks,
		"embedded_chunks": embedded,
		"embedding_dim":   embeddingDim,
		"stored_embedding_model": func() string {
			if embeddingModel.Valid {
				return embeddingModel.String
			}
			return ""
		}(),
	}, nil
}

func checksumKnowledgeInput(input KnowledgeDocumentInput) string {
	h := sha256.New()
	h.Write([]byte(input.SourceURI))
	h.Write([]byte{0})
	h.Write([]byte(input.Title))
	h.Write([]byte{0})
	h.Write([]byte(input.Text))
	h.Write([]byte{0})
	h.Write([]byte(mustJSON(input.Metadata, "{}")))
	return hex.EncodeToString(h.Sum(nil))
}

func normalizeKnowledgeValue(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	return value
}

func normalizeKnowledgeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	s = strings.Join(lines, "\n")
	s = kbWhitespaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func approximateTokenCount(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	words := len(strings.Fields(s))
	chars := max(1, len([]rune(s))/4)
	return max(words, chars)
}

func rightOverlap(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	start := len(s) - n
	if cut := strings.Index(s[start:], " "); cut >= 0 {
		start += cut + 1
	}
	return strings.TrimSpace(s[start:])
}

func buildFTSQuery(query string) string {
	terms := queryTerms(query)
	if len(terms) == 0 {
		return ""
	}
	out := make([]string, 0, min(len(terms), 8))
	for _, term := range terms {
		clean := kbUnsafeFTSChar.ReplaceAllString(term, "")
		if len(clean) >= 2 {
			out = append(out, clean+"*")
		}
		if len(out) >= 8 {
			break
		}
	}
	return strings.Join(out, " OR ")
}

func queryTerms(query string) []string {
	matches := kbTokenRe.FindAllString(strings.ToLower(query), -1)
	seen := map[string]bool{}
	terms := make([]string, 0, len(matches))
	for _, term := range matches {
		term = strings.Trim(term, "_-")
		if len(term) < 2 || queryStopwords[term] || seen[term] {
			continue
		}
		seen[term] = true
		terms = append(terms, term)
	}
	return terms
}

func lexicalKnowledgeScore(terms []string, query string, chunk KnowledgeChunk) float64 {
	if len(terms) == 0 {
		return 0
	}
	haystack := strings.ToLower(strings.Join([]string{
		chunk.Title,
		chunk.Heading,
		chunk.SectionPath,
		chunk.Text,
	}, " "))
	hits := 0
	weighted := 0.0
	for _, term := range terms {
		if strings.Contains(haystack, term) {
			hits++
			weight := 1.0
			if strings.Contains(strings.ToLower(chunk.Title), term) {
				weight += 0.7
			}
			if strings.Contains(strings.ToLower(chunk.Heading), term) {
				weight += 0.5
			}
			weighted += weight
		}
	}
	if hits == 0 {
		return 0
	}
	score := weighted / float64(len(terms)+2)
	phrase := strings.ToLower(strings.TrimSpace(query))
	if len(phrase) >= 8 && strings.Contains(haystack, phrase) {
		score += 0.25
	}
	if score > 1 {
		return 1
	}
	return score
}

func cosineScore(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, aa, bb float64
	for i := range a {
		dot += a[i] * b[i]
		aa += a[i] * a[i]
		bb += b[i] * b[i]
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	cos := dot / (math.Sqrt(aa) * math.Sqrt(bb))
	return (cos + 1) / 2
}

func makeKnowledgeSnippet(text string, terms []string) string {
	text = normalizeKnowledgeText(text)
	if len(text) <= 360 {
		return text
	}
	lower := strings.ToLower(text)
	pos := -1
	for _, term := range terms {
		if idx := strings.Index(lower, term); idx >= 0 && (pos < 0 || idx < pos) {
			pos = idx
		}
	}
	if pos < 0 {
		return strings.TrimSpace(text[:360]) + "..."
	}
	start := max(0, pos-120)
	end := min(len(text), start+360)
	snippet := strings.TrimSpace(text[start:end])
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(text) {
		snippet += "..."
	}
	return snippet
}

func mustJSON(v interface{}, fallback string) string {
	if v == nil {
		return fallback
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fallback
	}
	return string(b)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func cleanStringList(values []string, maxValues int) []string {
	out := make([]string, 0, min(len(values), maxValues))
	seen := map[string]bool{}
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
		if len(out) >= maxValues {
			break
		}
	}
	return out
}
