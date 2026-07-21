package relay

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

type createCameraGroupRequest struct {
	Name string `json:"name"`
}

type renameCameraGroupRequest struct {
	Name string `json:"name"`
}

type cameraGroupMemberRequest struct {
	ServiceID string `json:"service_id"`
}

func handleCameraGroups(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64) {
	switch r.Method {
	case http.MethodGet:
		groups, err := relay.db.ListCameraGroups(userID)
		if err != nil {
			http.Error(w, "Failed to list camera groups", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(groups)
	case http.MethodPost:
		var req createCameraGroupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" {
			http.Error(w, "Missing name", http.StatusBadRequest)
			return
		}
		group, err := relay.db.CreateCameraGroup(userID, req.Name)
		if err != nil {
			http.Error(w, "Failed to create group", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(group)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleCameraGroupsWithID(w http.ResponseWriter, r *http.Request, relay *Relay, userID int64, path string) {
	// path after /camera-groups/
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	groupID := parts[0]

	// /camera-groups/{id}
	if len(parts) == 1 || parts[1] == "" {
		switch r.Method {
		case http.MethodPut:
			var req renameCameraGroupRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "Invalid JSON", http.StatusBadRequest)
				return
			}
			req.Name = strings.TrimSpace(req.Name)
			if req.Name == "" {
				http.Error(w, "Missing name", http.StatusBadRequest)
				return
			}
			if err := relay.db.RenameCameraGroup(userID, groupID, req.Name); err != nil {
				if err == sql.ErrNoRows {
					http.Error(w, "Group not found", http.StatusNotFound)
					return
				}
				http.Error(w, "Failed to rename group", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			if err := relay.db.DeleteCameraGroup(userID, groupID); err != nil {
				if err == sql.ErrNoRows {
					http.Error(w, "Group not found", http.StatusNotFound)
					return
				}
				http.Error(w, "Failed to delete group", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	// /camera-groups/{id}/members
	if parts[1] == "members" {
		if len(parts) == 2 || parts[2] == "" {
			// POST /camera-groups/{id}/members {service_id}
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var req cameraGroupMemberRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "Invalid JSON", http.StatusBadRequest)
				return
			}
			req.ServiceID = strings.TrimSpace(req.ServiceID)
			if req.ServiceID == "" {
				http.Error(w, "Missing service_id", http.StatusBadRequest)
				return
			}
			if err := relay.db.AddCameraToGroup(userID, groupID, req.ServiceID); err != nil {
				if err == sql.ErrNoRows {
					http.Error(w, "Group not found", http.StatusNotFound)
					return
				}
				http.Error(w, "Failed to add member", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// DELETE /camera-groups/{id}/members/{service_id}
		if r.Method != http.MethodDelete {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		serviceID := parts[2]
		if serviceID == "" {
			http.Error(w, "Missing service_id", http.StatusBadRequest)
			return
		}
		if err := relay.db.RemoveCameraFromGroup(userID, groupID, serviceID); err != nil {
			if err == sql.ErrNoRows {
				http.Error(w, "Group not found", http.StatusNotFound)
				return
			}
			http.Error(w, "Failed to remove member", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	http.Error(w, "Not found", http.StatusNotFound)
}
