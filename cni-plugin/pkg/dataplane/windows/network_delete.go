package windows

import (
	"fmt"
	"strings"
)

type networkDeletionEndpoint struct {
	id, networkID, namespaceID string
}

type networkDeletionAPI interface {
	endpoints(networkID string) ([]networkDeletionEndpoint, error)
	detach(namespaceID, endpointID string) error
	deleteNetwork(networkID string) error
}

// HNS network deletion removes its endpoints but can leave their references in
// HCN namespaces. Detach while the endpoints still exist: attempting to detach
// an already-deleted endpoint may report success without removing the reference.
// Namespace/container lifetime remains exclusively the runtime's responsibility.
func deleteNetworkWithEndpoints(networkID string, api networkDeletionAPI) error {
	if networkID == "" {
		return fmt.Errorf("network deletion requires an explicit network ID")
	}
	endpoints, err := api.endpoints(networkID)
	if err != nil {
		return fmt.Errorf("list network %s endpoints before deletion: %w", networkID, err)
	}
	for _, ep := range endpoints {
		if !strings.EqualFold(ep.networkID, networkID) {
			return fmt.Errorf("refusing endpoint %s belonging to another network %s", ep.id, ep.networkID)
		}
		if ep.id == "" {
			return fmt.Errorf("network %s has an endpoint without an ID", networkID)
		}
	}
	for _, ep := range endpoints {
		if ep.namespaceID == "" {
			continue
		}
		if err := api.detach(ep.namespaceID, ep.id); err != nil {
			return fmt.Errorf("detach endpoint %s from namespace %s before deleting network %s: %w", ep.id, ep.namespaceID, networkID, err)
		}
	}
	return api.deleteNetwork(networkID)
}
