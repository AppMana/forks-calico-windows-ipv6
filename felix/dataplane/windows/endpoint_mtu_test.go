package windataplane

import (
	"errors"
	"net"
	"testing"

	"github.com/projectcalico/calico/felix/dataplane/windows/hns"
	"github.com/projectcalico/calico/felix/dataplane/windows/policysets"
	"github.com/projectcalico/calico/felix/proto"
	"github.com/projectcalico/calico/felix/types"
)

func TestMTUDefersPolicyWithoutStarvingOtherEndpoints(t *testing.T) {
	api := &mtuHNS{Endpoints: []hns.HNSEndpoint{
		{Id: "waiting", IPAddress: net.ParseIP("10.244.1.2"), VirtualNetworkName: "Calico", SharedContainers: []string{"a"}, State: hns.Attached},
		{Id: "ready", IPAddress: net.ParseIP("10.244.1.3"), VirtualNetworkName: "Calico", SharedContainers: []string{"b"}, State: hns.Attached},
	}}
	m := newEndpointManager(api, &mtuPolicySets{})
	waiting := types.WorkloadEndpointID{OrchestratorId: "k8s", WorkloadId: "ns/a", EndpointId: "eth0"}
	ready := types.WorkloadEndpointID{OrchestratorId: "k8s", WorkloadId: "ns/b", EndpointId: "eth0"}
	w := &proto.WorkloadEndpoint{Name: "a", Ipv4Nets: []string{"10.244.1.2/32"}}
	m.pendingWlEpUpdates[waiting] = w
	m.pendingWlEpUpdates[ready] = &proto.WorkloadEndpoint{Name: "b", Ipv4Nets: []string{"10.244.1.3/32"}}
	notReady := errors.New("Windows has not assigned an isolated compartment")
	allow := false
	calls := map[string]int{}
	m.ensureEndpointMTU = func(id string) error {
		calls[id]++
		if id == "waiting" && !allow {
			return notReady
		}
		return nil
	}
	for i := 0; i < 8; i++ {
		if err := m.CompleteDeferredWork(); !errors.Is(err, notReady) {
			t.Fatalf("expected retryable MTU error, got %v", err)
		}
		if m.pendingWlEpUpdates[waiting] == nil || m.activeWlEndpoints[waiting] != nil || m.activeWlACLPolicies[waiting] != nil {
			t.Fatal("unverified endpoint was dropped or its policy published")
		}
	}
	if m.activeWlEndpoints[ready] == nil || m.pendingWlEpUpdates[ready] != nil {
		t.Fatal("ready endpoint was starved")
	}
	allow = true
	if err := m.CompleteDeferredWork(); err != nil {
		t.Fatal(err)
	}
	if m.activeWlEndpoints[waiting] != w || m.pendingWlEpUpdates[waiting] != nil {
		t.Fatal("verified endpoint did not converge")
	}
	// A policy refresh must still verify MTU even when the ACL cache matches.
	allow = false
	m.pendingWlEpUpdates[waiting] = w
	if err := m.CompleteDeferredWork(); !errors.Is(err, notReady) {
		t.Fatal("cached ACLs bypassed MTU verification")
	}
	m.pendingWlEpUpdates[waiting] = nil
	before := calls["waiting"]
	if err := m.CompleteDeferredWork(); err != nil {
		t.Fatal(err)
	}
	if calls["waiting"] != before || m.activeWlEndpoints[waiting] != nil {
		t.Fatal("deleted endpoint requires MTU or retains state")
	}
}

func TestMTURefreshRepairsDriftWithoutAnEndpointUpdate(t *testing.T) {
	api := &mtuHNS{Endpoints: []hns.HNSEndpoint{
		{Id: "pod", IPAddress: net.ParseIP("10.244.1.2"), VirtualNetworkName: "Calico", SharedContainers: []string{"a"}, State: hns.Attached},
	}}
	m := newEndpointManager(api, &mtuPolicySets{})
	id := types.WorkloadEndpointID{OrchestratorId: "k8s", WorkloadId: "ns/a", EndpointId: "eth0"}
	m.pendingWlEpUpdates[id] = &proto.WorkloadEndpoint{Name: "a", Ipv4Nets: []string{"10.244.1.2/32"}}
	mtu := 1500
	m.ensureEndpointMTU = func(string) error { mtu = 1370; return nil }
	if err := m.CompleteDeferredWork(); err != nil {
		t.Fatal(err)
	}
	// Native adapter restarts reset MTU to 1500 while the workload is unchanged.
	mtu = 1500
	if !m.queueMTURefresh() {
		t.Fatal("active endpoint was not queued")
	}
	if err := m.CompleteDeferredWork(); err != nil {
		t.Fatal(err)
	}
	if mtu != 1370 {
		t.Fatal("cached endpoint policy bypassed drift repair")
	}
}

func TestMTURefreshIsBoundedFairAndPreservesPendingWork(t *testing.T) {
	m := newEndpointManager(&mtuHNS{}, &mtuPolicySets{})
	ids := []types.WorkloadEndpointID{{WorkloadId: "a"}, {WorkloadId: "b"}, {WorkloadId: "c"}}
	for _, id := range ids {
		m.activeWlEndpoints[id] = &proto.WorkloadEndpoint{Name: id.WorkloadId}
	}
	if m.queueMTURefresh() || len(m.pendingWlEpUpdates) != 0 {
		t.Fatal("disabled MTU enforcement scheduled work")
	}
	m.ensureEndpointMTU = func(string) error { return nil }
	seen := map[types.WorkloadEndpointID]bool{}
	for range ids {
		if !m.queueMTURefresh() || len(m.pendingWlEpUpdates) != 1 {
			t.Fatal("expected one endpoint per tick")
		}
		for id := range m.pendingWlEpUpdates {
			if seen[id] {
				t.Fatal("refresh starved another endpoint")
			}
			seen[id] = true
			delete(m.pendingWlEpUpdates, id)
		}
	}
	update := &proto.WorkloadEndpoint{Name: "replacement"}
	m.pendingWlEpUpdates[ids[0]] = nil // Pending deletion must not be resurrected.
	m.pendingWlEpUpdates[ids[1]] = update
	m.mtuRefreshQueue = append([]types.WorkloadEndpointID(nil), ids...)
	delete(m.activeWlEndpoints, ids[2]) // Deleted since the queue snapshot.
	if m.queueMTURefresh() || len(m.pendingWlEpUpdates) != 2 || m.pendingWlEpUpdates[ids[0]] != nil || m.pendingWlEpUpdates[ids[1]] != update {
		t.Fatal("refresh replaced pending work or resurrected a deleted endpoint")
	}
}

type mtuHNS struct{ Endpoints []hns.HNSEndpoint }

func (m *mtuHNS) GetHNSSupportedFeatures() hns.HNSSupportedFeatures {
	return hns.HNSSupportedFeatures{}
}
func (m *mtuHNS) HNSListEndpointRequest() ([]hns.HNSEndpoint, error) { return m.Endpoints, nil }

type mtuPolicySets struct {
	appliedRules map[string]bool
}

func (m *mtuPolicySets) AddOrReplacePolicySet(setId string, policy interface{}) {}
func (m *mtuPolicySets) RemovePolicySet(setId string)                           {}
func (m *mtuPolicySets) NewRule(isInbound bool, priority uint16) *hns.ACLPolicy {
	return &hns.ACLPolicy{
		Type:      hns.ACL,
		Protocol:  256,
		Action:    hns.Block,
		Direction: hns.In,
		RuleType:  hns.Switch,
		Priority:  priority,
	}
}
func (m *mtuPolicySets) GetPolicySetRules(setIds []string, isInbound, endOfTierDrop bool) []*hns.ACLPolicy {
	return nil
}
func (m *mtuPolicySets) ProcessIpSetUpdate(ipSetId string) []string { return nil }
func (m *mtuPolicySets) NewHostRule(isInbound bool) *hns.ACLPolicy {
	return &hns.ACLPolicy{
		Type:      hns.ACL,
		Protocol:  256,
		Action:    hns.Allow,
		Direction: hns.In,
		RuleType:  hns.Host,
		Priority:  policysets.HostToEndpointRulePriority,
	}
}
