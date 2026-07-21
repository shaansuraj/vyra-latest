package relay

import (
	"sync"

	"github.com/unblink/unblink/node"
)

// RegisteredService represents a service registered by a node
type RegisteredService struct {
	Service node.Service
	NodeID  string
}

// ServiceRegistry manages registered services
type ServiceRegistry struct {
	mu       sync.RWMutex
	services map[string]map[string]*RegisteredService // service_id -> node_id -> registered service
}

// NewServiceRegistry creates a new service registry
func NewServiceRegistry() *ServiceRegistry {
	return &ServiceRegistry{
		services: make(map[string]map[string]*RegisteredService),
	}
}

// Register adds or updates a service
func (r *ServiceRegistry) Register(service node.Service, nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.services[service.ID]; !ok {
		r.services[service.ID] = make(map[string]*RegisteredService)
	}
	r.services[service.ID][nodeID] = &RegisteredService{
		Service: service,
		NodeID:  nodeID,
	}
}

// Get returns a registered service by ID
// If a service exists on multiple nodes, this returns an arbitrary node instance.
// Prefer GetForNode when the node context is available.
func (r *ServiceRegistry) Get(serviceID string) *RegisteredService {
	r.mu.RLock()
	defer r.mu.RUnlock()
	byNode, ok := r.services[serviceID]
	if !ok {
		return nil
	}
	for _, reg := range byNode {
		return reg
	}
	return nil
}

// GetForNode returns a registered service for a specific node.
func (r *ServiceRegistry) GetForNode(serviceID, nodeID string) *RegisteredService {
	r.mu.RLock()
	defer r.mu.RUnlock()
	byNode, ok := r.services[serviceID]
	if !ok {
		return nil
	}
	return byNode[nodeID]
}

// GetByID returns a service by ID (convenience method)
func (r *ServiceRegistry) GetByID(serviceID string) *node.Service {
	reg := r.Get(serviceID)
	if reg == nil {
		return nil
	}
	return &reg.Service
}

// GetNodeID returns the node ID for a service
func (r *ServiceRegistry) GetNodeID(serviceID string) string {
	reg := r.Get(serviceID)
	if reg == nil {
		return ""
	}
	return reg.NodeID
}

// Remove removes a service by ID
func (r *ServiceRegistry) Remove(serviceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.services, serviceID)
}

// RemoveByNode removes all services from a specific node
func (r *ServiceRegistry) RemoveByNode(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for id, byNode := range r.services {
		delete(byNode, nodeID)
		if len(byNode) == 0 {
			delete(r.services, id)
		}
	}
}

// List returns all registered services
func (r *ServiceRegistry) List() []*RegisteredService {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*RegisteredService, 0)
	for _, byNode := range r.services {
		for _, svc := range byNode {
			result = append(result, svc)
		}
	}
	return result
}

// ListByNode returns all services from a specific node
func (r *ServiceRegistry) ListByNode(nodeID string) []*RegisteredService {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*RegisteredService
	for _, byNode := range r.services {
		if svc, ok := byNode[nodeID]; ok {
			result = append(result, svc)
		}
	}
	return result
}

// Count returns the number of registered services
func (r *ServiceRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	total := 0
	for _, byNode := range r.services {
		total += len(byNode)
	}
	return total
}
