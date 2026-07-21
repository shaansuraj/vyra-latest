package relay

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Agent represents an agent in the system
type Agent struct {
	ID         string
	Name       string
	WorkerID   string
	Config     AgentConfig
	ServiceIDs []string
	UserID     int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// AgentConfig holds the agent configuration (stored as JSONB)
type AgentConfig struct {
	Instruction string            `json:"instruction"`
	CustomYOLO  *CustomYOLOConfig `json:"custom_yolo,omitempty"`
}

// CustomYOLOConfig configures dedicated runtime settings for a custom YOLO agent.
type CustomYOLOConfig struct {
	Enabled                   bool   `json:"enabled"`
	ModelRelPath              string `json:"model_rel_path"`
	ModelFilename             string `json:"model_filename"`
	ModelSizeBytes            int64  `json:"model_size_bytes"`
	RuntimeDefaultsEnableVLM  bool   `json:"runtime_defaults_enable_vlm"`
	RuntimeDefaultsEnableFace bool   `json:"runtime_defaults_enable_face"`
	RuntimeDesiredReplicas    int    `json:"runtime_desired_replicas"`
}

// AgentTable handles agent data access
type AgentTable struct {
	db *sql.DB
}

// NewAgentTable creates a new AgentTable
func NewAgentTable(db *sql.DB) *AgentTable {
	return &AgentTable{db: db}
}

func (at *AgentTable) createAgentWithConfig(id, name, workerID string, config AgentConfig, serviceIDs []string, userID int64) (*Agent, error) {
	// Validation
	if name == "" {
		return nil, errors.New("agent name is required")
	}
	if config.Instruction == "" {
		return nil, errors.New("agent instruction is required")
	}
	if workerID == "" {
		return nil, errors.New("worker ID is required")
	}

	// Generate UUID when caller does not provide one
	if strings.TrimSpace(id) == "" {
		id = uuid.New().String()
	}

	// Prepare config as JSON
	configJSON, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}

	// Prepare service_ids as JSON
	if serviceIDs == nil {
		serviceIDs = []string{}
	}
	serviceIDsJSON, err := json.Marshal(serviceIDs)
	if err != nil {
		return nil, err
	}

	// Insert into database
	_, err = at.db.Exec(`
		INSERT INTO agents (id, name, worker_id, config, service_ids, user_id, created_at, updated_at)
		VALUES (?, ?, ?, json(?), json(?), ?, datetime('now'), datetime('now'))
	`, id, name, workerID, string(configJSON), string(serviceIDsJSON), userID)
	if err != nil {
		return nil, err
	}

	log.Printf("[AgentTable] Agent %s created by user %d", id, userID)
	return at.GetAgentByID(id)
}

// CreateAgent creates a new agent
func (at *AgentTable) CreateAgent(name, instruction, workerID string, serviceIDs []string, userID int64) (*Agent, error) {
	config := AgentConfig{Instruction: instruction}
	return at.createAgentWithConfig("", name, workerID, config, serviceIDs, userID)
}

// CreateCustomYOLOAgent creates a custom YOLO agent with a caller-defined ID and config.
func (at *AgentTable) CreateCustomYOLOAgent(id, name, workerID string, serviceIDs []string, userID int64, config *CustomYOLOConfig, instruction string) (*Agent, error) {
	if config == nil {
		return nil, errors.New("custom YOLO config is required")
	}
	cfg := AgentConfig{
		Instruction: instruction,
		CustomYOLO:  config,
	}
	return at.createAgentWithConfig(id, name, workerID, cfg, serviceIDs, userID)
}

// GetAgent fetches a single agent by ID.
// Used by events/search endpoints to enrich events with agent info.
// GetAgent fetches a single agent by ID.
// Kept for compatibility with events/search code.
func (at *AgentTable) GetAgent(agentID string) (*Agent, error) {
	return at.GetAgentByID(agentID)
}

// GetAgentByID retrieves an agent by ID
func (at *AgentTable) GetAgentByID(agentID string) (*Agent, error) {
	var agent Agent
	var configJSON, serviceIDsJSON string

	err := at.db.QueryRow(`
		SELECT id, name, worker_id, config, service_ids, user_id, created_at, updated_at
		FROM agents WHERE id = ?
	`, agentID).Scan(
		&agent.ID, &agent.Name, &agent.WorkerID, &configJSON, &serviceIDsJSON,
		&agent.UserID, &agent.CreatedAt, &agent.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, errors.New("agent not found")
	}
	if err != nil {
		return nil, err
	}

	// Parse config JSON
	if err := json.Unmarshal([]byte(configJSON), &agent.Config); err != nil {
		return nil, err
	}

	// Parse service_ids JSON
	if serviceIDsJSON == "" || serviceIDsJSON == "null" {
		agent.ServiceIDs = []string{}
	} else {
		if err := json.Unmarshal([]byte(serviceIDsJSON), &agent.ServiceIDs); err != nil {
			return nil, err
		}
	}

	return &agent, nil
}

// GetAgentsByUser retrieves all agents in the shared workspace.
// userID is ignored to preserve call-site compatibility.
func (at *AgentTable) GetAgentsByUser(userID int64) ([]*Agent, error) {
	_ = userID
	rows, err := at.db.Query(`
		SELECT id, name, worker_id, config, service_ids, user_id, created_at, updated_at
		FROM agents
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*Agent
	for rows.Next() {
		var agent Agent
		var configJSON, serviceIDsJSON string

		err := rows.Scan(
			&agent.ID, &agent.Name, &agent.WorkerID, &configJSON, &serviceIDsJSON,
			&agent.UserID, &agent.CreatedAt, &agent.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		// Parse JSONs
		if err := json.Unmarshal([]byte(configJSON), &agent.Config); err != nil {
			return nil, err
		}

		// Parse service_ids JSON
		if serviceIDsJSON == "" || serviceIDsJSON == "null" {
			agent.ServiceIDs = []string{}
		} else {
			if err := json.Unmarshal([]byte(serviceIDsJSON), &agent.ServiceIDs); err != nil {
				return nil, err
			}
		}

		agents = append(agents, &agent)
	}

	return agents, nil
}

// UpdateAgent performs a partial update on an agent (name, instruction, and/or service_ids)
// Pass nil for fields that should not be updated
func (at *AgentTable) UpdateAgent(agentID string, name, instruction *string, serviceIDs *[]string) error {
	// Build dynamic SQL update
	var updates []string
	var args []interface{}

	if name != nil {
		if *name == "" {
			return errors.New("agent name cannot be empty")
		}
		updates = append(updates, "name = ?")
		args = append(args, *name)
	}

	if instruction != nil {
		if *instruction == "" {
			return errors.New("agent instruction cannot be empty")
		}
		existing, err := at.GetAgentByID(agentID)
		if err != nil {
			return err
		}
		config := existing.Config
		config.Instruction = *instruction
		configJSON, err := json.Marshal(config)
		if err != nil {
			return err
		}
		updates = append(updates, "config = json(?)")
		args = append(args, string(configJSON))
	}

	if serviceIDs != nil {
		serviceIDsJSON, err := json.Marshal(*serviceIDs)
		if err != nil {
			return err
		}
		updates = append(updates, "service_ids = json(?)")
		args = append(args, string(serviceIDsJSON))
	}

	if len(updates) == 0 {
		return errors.New("no fields to update")
	}

	// Add updated_at
	updates = append(updates, "updated_at = datetime('now')")

	// Add agentID to args
	args = append(args, agentID)

	// Execute update
	query := "UPDATE agents SET " + strings.Join(updates, ", ") + " WHERE id = ?"
	result, err := at.db.Exec(query, args...)

	if err != nil {
		return err
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return errors.New("agent not found")
	}

	log.Printf("[AgentTable] Updated agent %s", agentID)
	return nil
}

// MigrateLegacyFaceAgents folds any legacy face-only agent bindings into the
// canonical Person Detection agent, then deletes the obsolete face-only rows.
func (at *AgentTable) MigrateLegacyFaceAgents() error {
	allAgents, err := at.GetAgentsByUser(0)
	if err != nil {
		return err
	}

	legacyAgents := make([]*Agent, 0, 2)
	for _, agent := range allAgents {
		if isLegacyFaceWorkerID(agent.WorkerID) {
			legacyAgents = append(legacyAgents, agent)
		}
	}
	if len(legacyAgents) == 0 {
		return nil
	}

	personTpl := predefinedAgentTemplates[1]
	personMatches := matchAgentsForTemplate(allAgents, personTpl)
	legacyServiceIDs := make([]string, 0, len(legacyAgents))
	for _, agent := range legacyAgents {
		legacyServiceIDs = mergeServiceIDs(legacyServiceIDs, agent.ServiceIDs)
	}

	target := canonicalAgentTarget(personMatches)
	if target == nil {
		userID := legacyAgents[0].UserID
		created, err := at.CreateAgent(personTpl.Name, personTpl.Instruction, personTpl.WorkerID, legacyServiceIDs, userID)
		if err != nil {
			return err
		}
		target = created
	} else {
		mergedServiceIDs := mergeServiceIDs(target.ServiceIDs, legacyServiceIDs)
		name := personTpl.Name
		instruction := personTpl.Instruction
		if err := at.UpdateAgent(target.ID, &name, &instruction, &mergedServiceIDs); err != nil {
			return err
		}
		if !strings.EqualFold(target.WorkerID, personTpl.WorkerID) {
			if _, err := at.db.Exec(
				"UPDATE agents SET worker_id = ?, updated_at = datetime('now') WHERE id = ?",
				personTpl.WorkerID, target.ID,
			); err != nil {
				return err
			}
		}
	}

	for _, agent := range legacyAgents {
		if err := at.DeleteAgent(agent.ID); err != nil {
			return err
		}
	}

	log.Printf("[AgentTable] Migrated %d legacy face-only agent(s) into Person Detection", len(legacyAgents))
	return nil
}

// UpdateCustomYOLORuntime updates persisted runtime defaults for a custom YOLO agent.
func (at *AgentTable) UpdateCustomYOLORuntime(agentID string, enableVLM, enableFace bool, desiredReplicas int) error {
	agent, err := at.GetAgentByID(agentID)
	if err != nil {
		return err
	}
	if agent.Config.CustomYOLO == nil || !agent.Config.CustomYOLO.Enabled {
		return errors.New("agent is not a custom YOLO agent")
	}

	agent.Config.CustomYOLO.RuntimeDefaultsEnableVLM = enableVLM
	agent.Config.CustomYOLO.RuntimeDefaultsEnableFace = enableFace
	agent.Config.CustomYOLO.RuntimeDesiredReplicas = desiredReplicas

	configJSON, err := json.Marshal(agent.Config)
	if err != nil {
		return err
	}

	result, err := at.db.Exec(
		"UPDATE agents SET config = json(?), updated_at = datetime('now') WHERE id = ?",
		string(configJSON), agentID,
	)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return errors.New("agent not found")
	}
	return nil
}

// DeleteAgent removes an agent
func (at *AgentTable) DeleteAgent(agentID string) error {
	result, err := at.db.Exec("DELETE FROM agents WHERE id = ?", agentID)
	if err != nil {
		return err
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return errors.New("agent not found")
	}

	log.Printf("[AgentTable] Deleted agent %s", agentID)
	return nil
}

// UserOwnsAgent checks whether an agent exists in the shared workspace.
// userID is ignored to preserve call-site compatibility.
func (at *AgentTable) UserOwnsAgent(userID int64, agentID string) bool {
	_ = userID
	var id string
	err := at.db.QueryRow("SELECT id FROM agents WHERE id = ?", agentID).Scan(&id)
	return err == nil
}
