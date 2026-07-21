package relay

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

type eventsResponse struct {
	Events []*AgentEventWithMeta `json:"events"`
}

// AgentEventWithMeta is the shape consumed by the app UI.
type AgentEventWithMeta struct {
	ID         string                 `json:"id"`
	AgentID    string                 `json:"agent_id"`
	AgentName  string                 `json:"agent_name"`
	ServiceIDs []string               `json:"service_ids"`
	Data       map[string]interface{} `json:"data"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt  string                 `json:"created_at"`
}

func eventServiceIDsWithFallback(ev *AgentEvent, fallback []string) []string {
	serviceIDs := append([]string{}, fallback...)

	if ev.Metadata != nil {
		if sid, ok := ev.Metadata["service_id"].(string); ok && sid != "" {
			return []string{sid}
		}
		if sids, ok := ev.Metadata["service_ids"].([]interface{}); ok && len(sids) > 0 {
			out := make([]string, 0, len(sids))
			for _, s := range sids {
				if str, ok := s.(string); ok && str != "" {
					out = append(out, str)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}

	if ev.Data != nil {
		if sid, ok := ev.Data["service_id"].(string); ok && sid != "" {
			return []string{sid}
		}
		if sids, ok := ev.Data["service_ids"].([]interface{}); ok && len(sids) > 0 {
			out := make([]string, 0, len(sids))
			for _, s := range sids {
				if str, ok := s.(string); ok && str != "" {
					out = append(out, str)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}

	return serviceIDs
}

// handleEventCount returns total and today's event counts for the shared workspace.
// GET /events/count
func handleEventCount(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	total, today, err := relay.db.GetEventCountForUser(userID)
	if err != nil {
		http.Error(w, "Failed to count events", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int64{"total": total, "today": today})
}

func handleListEvents(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 50
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 1000 {
				n = 1000
			}
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	var events []*AgentEvent
	var err error
	if date := r.URL.Query().Get("date"); date != "" {
		events, err = relay.db.GetAgentEventsForUserFiltered(userID, date, limit, offset)
	} else {
		events, err = relay.db.GetRecentAgentEventsForUser(userID, limit, offset)
	}
	if err != nil {
		http.Error(w, "Failed to fetch events", http.StatusInternalServerError)
		return
	}

	// Enrich with agent name + service_ids for the UI
	resp := eventsResponse{Events: []*AgentEventWithMeta{}}
	for _, ev := range events {
		agentName := ev.AgentID
		serviceIDs := []string{}
		if ag, err := relay.agentTable.GetAgent(ev.AgentID); err == nil {
			agentName = ag.Name
			serviceIDs = ag.ServiceIDs
		}
		// Prefer event-level camera IDs over agent defaults.
		serviceIDs = eventServiceIDsWithFallback(ev, serviceIDs)

		resp.Events = append(resp.Events, &AgentEventWithMeta{
			ID:         ev.ID,
			AgentID:    ev.AgentID,
			AgentName:  agentName,
			ServiceIDs: serviceIDs,
			Data:       ev.Data,
			Metadata:   ev.Metadata,
			CreatedAt:  ev.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp.Events)
}

func handleSearchEvents(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query().Get("query")
	if q == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]*AgentEventWithMeta{})
		return
	}
	limit := 50
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 1000 {
				n = 1000
			}
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	events, err := relay.db.SearchAgentEventsForUser(userID, q, limit, offset)
	if err != nil {
		http.Error(w, "Failed to search events", http.StatusInternalServerError)
		return
	}

	resp := []*AgentEventWithMeta{}
	for _, ev := range events {
		agentName := ev.AgentID
		serviceIDs := []string{}
		if ag, err := relay.agentTable.GetAgent(ev.AgentID); err == nil {
			agentName = ag.Name
			serviceIDs = ag.ServiceIDs
		}
		serviceIDs = eventServiceIDsWithFallback(ev, serviceIDs)

		resp = append(resp, &AgentEventWithMeta{
			ID:         ev.ID,
			AgentID:    ev.AgentID,
			AgentName:  agentName,
			ServiceIDs: serviceIDs,
			Data:       ev.Data,
			Metadata:   ev.Metadata,
			CreatedAt:  ev.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
