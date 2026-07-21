package relay

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type QueryChat struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
	Messages  []AIChatMessage `json:"messages,omitempty"`
}

func (db *Database) EnsureQueryChat(userID int64, chatID, title string) (string, error) {
	if userID <= 0 {
		return "", nil
	}
	chatID = strings.TrimSpace(chatID)
	now := time.Now().UTC()
	if chatID == "" {
		chatID = uuid.New().String()
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New chat"
	}
	if len(title) > 80 {
		title = strings.TrimSpace(title[:80])
	}
	_, err := db.execWrite(`
		INSERT INTO query_chats (id, user_id, title, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = CASE WHEN query_chats.title = '' OR query_chats.title = 'New chat' THEN excluded.title ELSE query_chats.title END,
			updated_at = excluded.updated_at
	`, chatID, userID, title, now, now)
	return chatID, err
}

func (db *Database) StoreQueryChatMessage(userID int64, chatID, role, content, traceID string, createdAt time.Time) error {
	if userID <= 0 || strings.TrimSpace(chatID) == "" || strings.TrimSpace(content) == "" {
		return nil
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role != "user" && role != "assistant" {
		return nil
	}
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := db.execWrite(`
		INSERT INTO query_chat_messages (id, chat_id, user_id, role, content, trace_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, uuid.New().String(), chatID, userID, role, strings.TrimSpace(content), strings.TrimSpace(traceID), createdAt.UTC())
	if err != nil {
		return err
	}
	_, err = db.execWrite(`UPDATE query_chats SET updated_at = ? WHERE id = ? AND user_id = ?`, createdAt.UTC(), chatID, userID)
	return err
}

func (db *Database) ListQueryChats(userID int64, limit int) ([]QueryChat, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := db.DB.Query(`
		SELECT id, title, created_at, updated_at
		FROM query_chats
		WHERE user_id = ?
		ORDER BY updated_at DESC
		LIMIT ?
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]QueryChat, 0, limit)
	for rows.Next() {
		var c QueryChat
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&c.ID, &c.Title, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		c.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		c.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (db *Database) GetQueryChat(userID int64, chatID string) (QueryChat, error) {
	var c QueryChat
	var createdAt, updatedAt time.Time
	err := db.DB.QueryRow(`
		SELECT id, title, created_at, updated_at
		FROM query_chats
		WHERE user_id = ? AND id = ?
	`, userID, strings.TrimSpace(chatID)).Scan(&c.ID, &c.Title, &createdAt, &updatedAt)
	if err != nil {
		return c, err
	}
	c.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	c.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)

	rows, err := db.DB.Query(`
		SELECT role, content
		FROM query_chat_messages
		WHERE user_id = ? AND chat_id = ?
		ORDER BY created_at ASC
	`, userID, c.ID)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var m AIChatMessage
		if err := rows.Scan(&m.Role, &m.Content); err != nil {
			return c, err
		}
		c.Messages = append(c.Messages, m)
	}
	return c, rows.Err()
}

func (db *Database) DeleteQueryChat(userID int64, chatID string) error {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return nil
	}
	if _, err := db.execWrite(`DELETE FROM query_chat_messages WHERE user_id = ? AND chat_id = ?`, userID, chatID); err != nil {
		return err
	}
	_, err := db.execWrite(`DELETE FROM query_chats WHERE user_id = ? AND id = ?`, userID, chatID)
	return err
}

func (db *Database) RecentQueryChatMessages(userID int64, chatID string, limit int) ([]AIChatMessage, error) {
	if strings.TrimSpace(chatID) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 12
	}
	rows, err := db.DB.Query(`
		SELECT role, content
		FROM (
			SELECT role, content, created_at
			FROM query_chat_messages
			WHERE user_id = ? AND chat_id = ?
			ORDER BY created_at DESC
			LIMIT ?
		)
		ORDER BY created_at ASC
	`, userID, strings.TrimSpace(chatID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AIChatMessage
	for rows.Next() {
		var m AIChatMessage
		if err := rows.Scan(&m.Role, &m.Content); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func handleQueryChats(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	chats, err := relay.db.ListQueryChats(userID, 30)
	if err != nil {
		http.Error(w, "Failed to fetch chats", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(chats)
}

func handleQueryChat(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	chatID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/query/chats/"), "/")
	if chatID == "" {
		http.Error(w, "chat id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		chat, err := relay.db.GetQueryChat(userID, chatID)
		if err == sql.ErrNoRows {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "Failed to fetch chat", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chat)
	case http.MethodDelete:
		if err := relay.db.DeleteQueryChat(userID, chatID); err != nil {
			http.Error(w, "Failed to delete chat", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
