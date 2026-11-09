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
		if isUnattachedNamespaceID(ep.namespaceID) {
			continue
		}
		if err := api.detach(ep.namespaceID, ep.id); err != nil {
			return fmt.Errorf("detach endpoint %s from namespace %s before deleting network %s: %w", ep.id, ep.namespaceID, networkID, err)
		}
	}
	return api.deleteNetwork(networkID)
}

func isUnattachedNamespaceID(namespaceID string) bool {
	return namespaceID == "" || strings.EqualFold(namespaceID, "00000000-0000-0000-0000-000000000000")
}

type endpointDeletionAPI interface {
	detach(namespaceID, endpointID string) error
	deleteEndpoint(endpointID string) error
}

// HNS endpoint deletion while the endpoint is still attached to an HCN
// namespace cascade-removes the endpoint object without clearing its reference
// from HostComputeNamespace.Resources, causing subsequent HcnDeleteNamespace
// calls to fail permanently with 0x803b0015 (HCN_E_UNSUPPORTED / The specified
// request is unsupported). Always detach before deleting the endpoint.
func deleteEndpointWithDetach(endpointID, namespaceID string, api endpointDeletionAPI) error {
	if endpointID == "" {
		return fmt.Errorf("endpoint deletion requires an explicit endpoint ID")
	}
	if !isUnattachedNamespaceID(namespaceID) {
		if err := api.detach(namespaceID, endpointID); err != nil {
			return fmt.Errorf("detach endpoint %s from namespace %s before deleting endpoint: %w", endpointID, namespaceID, err)
		}
	}
	return api.deleteEndpoint(endpointID)
}

type namespaceCleanupEndpoint struct {
	id, name, networkName, namespaceID string
}

type namespaceCleanupAPI interface {
	endpointDeletionAPI
	endpointByName(name string) (*namespaceCleanupEndpoint, error)
	namespaceEndpoints(namespaceID string) ([]namespaceCleanupEndpoint, error)
}

// cleanUpNamespaceEndpoints detaches and deletes the named CNI endpoint as well
// as any remaining endpoints on the target Calico network still attached to
// namespaceID. Errors are returned (never swallowed) so the CRI runtime does
// not attempt HcnDeleteNamespace while a live endpoint remains attached.
func cleanUpNamespaceEndpoints(endpointName, namespaceID, networkName string, api namespaceCleanupAPI) error {
	removed := map[string]bool{}
	if endpointName != "" {
		ep, err := api.endpointByName(endpointName)
		if err != nil {
			return fmt.Errorf("lookup endpoint %s for cleanup: %w", endpointName, err)
		}
		if ep != nil {
			nsID := ep.namespaceID
			if isUnattachedNamespaceID(nsID) && !isUnattachedNamespaceID(namespaceID) {
				// Only detach from namespaceID if the endpoint actually reports attachment.
				nsID = ""
			}
			if err := deleteEndpointWithDetach(ep.id, nsID, api); err != nil {
				return err
			}
			removed[strings.ToLower(ep.id)] = true
		}
	}
	if isUnattachedNamespaceID(namespaceID) {
		return nil
	}
	remaining, err := api.namespaceEndpoints(namespaceID)
	if err != nil {
		return fmt.Errorf("list namespace %s endpoints during cleanup: %w", namespaceID, err)
	}
	for _, ep := range remaining {
		if removed[strings.ToLower(ep.id)] {
			continue
		}
		if endpointName != "" && strings.EqualFold(ep.name, endpointName) ||
			(networkName != "" && strings.EqualFold(ep.networkName, networkName)) {
			nsID := ep.namespaceID
			if isUnattachedNamespaceID(nsID) {
				nsID = namespaceID
			}
			if err := deleteEndpointWithDetach(ep.id, nsID, api); err != nil {
				return err
			}
		}
	}
	return nil
}
