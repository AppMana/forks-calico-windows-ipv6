// Copyright (c) 2024 Tigera, Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package windataplane

import (
	"net"
	"regexp"
	"testing"

	"github.com/projectcalico/calico/felix/dataplane/windows/hns"
	"github.com/projectcalico/calico/felix/dataplane/windows/policysets"
	"github.com/projectcalico/calico/felix/proto"
	"github.com/projectcalico/calico/libcalico-go/lib/set"
)

// mockPolicySets implements policysets.PolicySetsDataplane for testing.
type mockPolicySets struct {
	appliedRules map[string]bool
}

func (m *mockPolicySets) AddOrReplacePolicySet(setId string, policy interface{}) {}
func (m *mockPolicySets) RemovePolicySet(setId string)                           {}
func (m *mockPolicySets) NewRule(isInbound bool, priority uint16) *hns.ACLPolicy {
	return &hns.ACLPolicy{
		Type:      hns.ACL,
		Protocol:  256,
		Action:    hns.Block,
		Direction: hns.In,
		RuleType:  hns.Switch,
		Priority:  priority,
	}
}
func (m *mockPolicySets) GetPolicySetRules(setIds []string, isInbound, endOfTierDrop bool) []*hns.ACLPolicy {
	return nil
}
func (m *mockPolicySets) ProcessIpSetUpdate(ipSetId string) []string { return nil }
func (m *mockPolicySets) NewHostRule(isInbound bool) *hns.ACLPolicy {
	return &hns.ACLPolicy{
		Type:      hns.ACL,
		Protocol:  256,
		Action:    hns.Allow,
		Direction: hns.In,
		RuleType:  hns.Host,
		Priority:  policysets.HostToEndpointRulePriority,
	}
}

func newTestEndpointManagerWithPolicySets(mockHNS *hns.MockAPI, ps policysets.PolicySetsDataplane) *endpointManager {
	return &endpointManager{
		hns:                    mockHNS,
		hnsNetworkRegexp:       defaultNetworkRegexp(),
		policysetsDataplane:    ps,
		addressToEndpointId:    make(map[string]string),
		activeWlEndpoints:      map[proto.WorkloadEndpointID]*proto.WorkloadEndpoint{},
		pendingWlEpUpdates:     map[proto.WorkloadEndpointID]*proto.WorkloadEndpoint{},
		missingEndpointRetries: map[proto.WorkloadEndpointID]int{},
		pendingIPSetUpdate:     set.New[string](),
	}
}

// Test that CompleteDeferredWork resolves a dual-stack workload by IPv4.
func TestCompleteDeferredWork_DualStackResolvesViaIPv4(t *testing.T) {
	mock := &hns.MockAPI{
		Endpoints: []hns.HNSEndpoint{
			{
				Id:                 "ep-ds-1",
				IPAddress:          net.ParseIP("10.3.48.200"),
				IPv6Address:        net.ParseIP("fd00:10:3::c8"),
				VirtualNetworkName: "Calico",
				SharedContainers:   []string{"sandbox-1"},
			},
		},
	}
	ps := &mockPolicySets{}
	m := newTestEndpointManagerWithPolicySets(mock, ps)

	wepID := proto.WorkloadEndpointID{
		OrchestratorId: "k8s",
		WorkloadId:     "default/pod-a",
		EndpointId:     "eth0",
	}
	m.pendingWlEpUpdates[wepID] = &proto.WorkloadEndpoint{
		Name:       "pod-a",
		ProfileIds: []string{"default"},
		Ipv4Nets:   []string{"10.3.48.200/32"},
		Ipv6Nets:   []string{"fd00:10:3::c8/128"},
	}

	err := m.CompleteDeferredWork()
	if err != nil {
		t.Fatalf("CompleteDeferredWork failed: %v", err)
	}
	// Should have been processed (moved from pending to active).
	if _, pending := m.pendingWlEpUpdates[wepID]; pending {
		t.Error("workload should have been removed from pending updates")
	}
	if _, active := m.activeWlEndpoints[wepID]; !active {
		t.Error("workload should be in active endpoints")
	}
}

// Test that CompleteDeferredWork resolves a workload by IPv6 when IPv4 lookup fails.
func TestCompleteDeferredWork_ResolvesViaIPv6Fallback(t *testing.T) {
	// Endpoint only has IPv6 in the cache (IPv4 is 0.0.0.0, so cache key is "0.0.0.0/32").
	mock := &hns.MockAPI{
		Endpoints: []hns.HNSEndpoint{
			{
				Id:                 "ep-v6-only",
				IPAddress:          net.IPv4zero,
				IPv6Address:        net.ParseIP("fd00:10:3::99"),
				VirtualNetworkName: "Calico",
				SharedContainers:   []string{"sandbox-2"},
			},
		},
	}
	ps := &mockPolicySets{}
	m := newTestEndpointManagerWithPolicySets(mock, ps)

	wepID := proto.WorkloadEndpointID{
		OrchestratorId: "k8s",
		WorkloadId:     "default/pod-v6",
		EndpointId:     "eth0",
	}
	// Workload has non-matching IPv4 but matching IPv6.
	m.pendingWlEpUpdates[wepID] = &proto.WorkloadEndpoint{
		Name:       "pod-v6",
		ProfileIds: []string{"default"},
		Ipv4Nets:   []string{"10.99.99.99/32"}, // won't match any endpoint
		Ipv6Nets:   []string{"fd00:10:3::99/128"},
	}

	err := m.CompleteDeferredWork()
	if err != nil {
		t.Fatalf("CompleteDeferredWork failed: %v", err)
	}
	if _, active := m.activeWlEndpoints[wepID]; !active {
		t.Error("workload should be in active endpoints (resolved via IPv6 fallback)")
	}
}

// Test that CompleteDeferredWork correctly handles an IPv6-only workload (no Ipv4Nets).
func TestCompleteDeferredWork_IPv6OnlyWorkload(t *testing.T) {
	mock := &hns.MockAPI{
		Endpoints: []hns.HNSEndpoint{
			{
				Id:                 "ep-pure-v6",
				IPAddress:          net.IPv4zero,
				IPv6Address:        net.ParseIP("fd00:10:3::aa"),
				VirtualNetworkName: "Calico",
				SharedContainers:   []string{"sandbox-3"},
			},
		},
	}
	ps := &mockPolicySets{}
	m := newTestEndpointManagerWithPolicySets(mock, ps)

	wepID := proto.WorkloadEndpointID{
		OrchestratorId: "k8s",
		WorkloadId:     "default/pod-v6only",
		EndpointId:     "eth0",
	}
	m.pendingWlEpUpdates[wepID] = &proto.WorkloadEndpoint{
		Name:       "pod-v6only",
		ProfileIds: []string{"default"},
		Ipv4Nets:   nil, // no IPv4
		Ipv6Nets:   []string{"fd00:10:3::aa/128"},
	}

	err := m.CompleteDeferredWork()
	if err != nil {
		t.Fatalf("CompleteDeferredWork failed: %v", err)
	}
	if _, active := m.activeWlEndpoints[wepID]; !active {
		t.Error("IPv6-only workload should be in active endpoints")
	}
}

// Test that CompleteDeferredWork returns ErrorUnknownEndpoint when neither v4 nor v6 resolves.
func TestCompleteDeferredWork_UnresolvableEndpoint(t *testing.T) {
	mock := &hns.MockAPI{
		Endpoints: []hns.HNSEndpoint{}, // empty
	}
	ps := &mockPolicySets{}
	m := newTestEndpointManagerWithPolicySets(mock, ps)

	wepID := proto.WorkloadEndpointID{
		OrchestratorId: "k8s",
		WorkloadId:     "default/pod-ghost",
		EndpointId:     "eth0",
	}
	m.pendingWlEpUpdates[wepID] = &proto.WorkloadEndpoint{
		Name:       "pod-ghost",
		ProfileIds: []string{"default"},
		Ipv4Nets:   []string{"10.99.0.1/32"},
		Ipv6Nets:   []string{"fd00:99::1/128"},
	}

	err := m.CompleteDeferredWork()
	if err != ErrorUnknownEndpoint {
		t.Errorf("expected ErrorUnknownEndpoint, got %v", err)
	}
	// Should still be pending.
	if _, pending := m.pendingWlEpUpdates[wepID]; !pending {
		t.Error("unresolvable workload should remain pending")
	}
}

func TestCompleteDeferredWork_DropsStaleEndpointAfterPolicyRefresh(t *testing.T) {
	mock := &hns.MockAPI{
		Endpoints: []hns.HNSEndpoint{
			{
				Id:                 "ep-build-pod",
				IPAddress:          net.ParseIP("10.3.48.253"),
				IPv6Address:        net.ParseIP("2001:db8::253"),
				VirtualNetworkName: "Calico",
				SharedContainers:   []string{"sandbox-build-pod"},
			},
		},
	}
	ps := &mockPolicySets{}
	m := newTestEndpointManagerWithPolicySets(mock, ps)

	wepID := proto.WorkloadEndpointID{
		OrchestratorId: "k8s",
		WorkloadId:     "default/build-pod-gone",
		EndpointId:     "eth0",
	}
	workload := &proto.WorkloadEndpoint{
		Name:       "build-pod-gone",
		ProfileIds: []string{"default"},
		Ipv4Nets:   []string{"10.3.48.253/32"},
		Ipv6Nets:   []string{"2001:db8::253/128"},
	}
	m.pendingWlEpUpdates[wepID] = workload

	if err := m.CompleteDeferredWork(); err != nil {
		t.Fatalf("expected initial endpoint programming to succeed, got %v", err)
	}
	if _, active := m.activeWlEndpoints[wepID]; !active {
		t.Fatal("workload should be active after initial programming")
	}

	// This is the observed failure mode: the short-lived build pod sandbox
	// has already disappeared from HNS, but Felix has not observed the
	// corresponding workload endpoint delete. A later profile/policy update
	// queues the active workload for reprogramming and HNS lookup now fails.
	mock.Endpoints = []hns.HNSEndpoint{}
	m.ProcessPolicyProfileUpdate("profile-default")
	if _, pending := m.pendingWlEpUpdates[wepID]; !pending {
		t.Fatal("profile update should have queued the active workload for refresh")
	}

	for i := 0; i < maxMissingEndpointRetries; i++ {
		err := m.CompleteDeferredWork()
		if err != ErrorUnknownEndpoint {
			t.Fatalf("retry %d: expected ErrorUnknownEndpoint, got %v", i+1, err)
		}
		if _, pending := m.pendingWlEpUpdates[wepID]; !pending {
			t.Fatalf("retry %d: workload should remain pending until retry budget is exhausted", i+1)
		}
	}

	err := m.CompleteDeferredWork()
	if err != nil {
		t.Fatalf("expected stale missing endpoint to be dropped without error, got %v", err)
	}
	if _, pending := m.pendingWlEpUpdates[wepID]; pending {
		t.Error("stale missing workload should have been removed from pending updates")
	}
	if _, active := m.activeWlEndpoints[wepID]; active {
		t.Error("stale missing workload should have been removed from active endpoints")
	}
	if _, tracked := m.missingEndpointRetries[wepID]; tracked {
		t.Error("missing endpoint retry state should have been cleared")
	}
}

func TestCompleteDeferredWork_MissingEndpointCanRecoverBeforeRetryBudget(t *testing.T) {
	mock := &hns.MockAPI{
		Endpoints: []hns.HNSEndpoint{},
	}
	ps := &mockPolicySets{}
	m := newTestEndpointManagerWithPolicySets(mock, ps)

	wepID := proto.WorkloadEndpointID{
		OrchestratorId: "k8s",
		WorkloadId:     "default/pod-still-creating",
		EndpointId:     "eth0",
	}
	m.pendingWlEpUpdates[wepID] = &proto.WorkloadEndpoint{
		Name:       "pod-still-creating",
		ProfileIds: []string{"default"},
		Ipv4Nets:   []string{"10.3.48.201/32"},
		Ipv6Nets:   []string{"2001:db8::201/128"},
	}

	if err := m.CompleteDeferredWork(); err != ErrorUnknownEndpoint {
		t.Fatalf("expected first missing endpoint pass to retry, got %v", err)
	}

	mock.Endpoints = []hns.HNSEndpoint{
		{
			Id:                 "ep-still-creating",
			IPAddress:          net.ParseIP("10.3.48.201"),
			IPv6Address:        net.ParseIP("2001:db8::201"),
			VirtualNetworkName: "Calico",
			SharedContainers:   []string{"sandbox-still-creating"},
		},
	}

	if err := m.CompleteDeferredWork(); err != nil {
		t.Fatalf("expected endpoint to program after HNS endpoint appears, got %v", err)
	}
	if _, active := m.activeWlEndpoints[wepID]; !active {
		t.Error("workload should be active after endpoint appears")
	}
	if _, pending := m.pendingWlEpUpdates[wepID]; pending {
		t.Error("workload should not remain pending after endpoint appears")
	}
	if _, tracked := m.missingEndpointRetries[wepID]; tracked {
		t.Error("missing endpoint retry state should have been cleared after recovery")
	}
}

// Test that host address updates include IPv6 and the node-to-endpoint rule uses them.
func TestNodeToEndpointRules_IPv4FromHostAddrs_IPv6FromCallback(t *testing.T) {
	mock := &hns.MockAPI{}
	ps := &mockPolicySets{}
	m := newTestEndpointManagerWithPolicySets(mock, ps)
	// hostAddrs now contains only IPv4 (from the polling loop).
	m.hostAddrs = []string{"10.2.0.3/32"}
	// IPv6 is fetched on-demand via the injectable function.
	m.getIPv6Addrs = func() []string {
		return []string{"fd00:10:2::3/128"}
	}

	rules := m.nodeToEndpointRules()
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules (IPv4 + IPv6), got %d", len(rules))
	}
	if rules[0].RemoteAddresses != "10.2.0.3/32" {
		t.Errorf("expected IPv4 RemoteAddresses, got %q", rules[0].RemoteAddresses)
	}
	if rules[0].Id != "allow-host-to-endpoint" {
		t.Errorf("expected IPv4 rule Id 'allow-host-to-endpoint', got %q", rules[0].Id)
	}
	if rules[1].RemoteAddresses != "fd00:10:2::3/128" {
		t.Errorf("expected IPv6 RemoteAddresses, got %q", rules[1].RemoteAddresses)
	}
	if rules[1].Id != "allow-host-to-endpoint-v6" {
		t.Errorf("expected IPv6 rule Id 'allow-host-to-endpoint-v6', got %q", rules[1].Id)
	}
	for i, rule := range rules {
		if rule.Action != hns.Allow {
			t.Errorf("rule[%d]: expected Allow action, got %v", i, rule.Action)
		}
	}
}

func TestNodeToEndpointRules_IPv4Only_NoIPv6Callback(t *testing.T) {
	mock := &hns.MockAPI{}
	ps := &mockPolicySets{}
	m := newTestEndpointManagerWithPolicySets(mock, ps)
	m.hostAddrs = []string{"10.2.0.3/32", "10.2.0.4/32"}
	m.getIPv6Addrs = func() []string { return nil }

	rules := m.nodeToEndpointRules()
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule (IPv4 only), got %d", len(rules))
	}
	if rules[0].RemoteAddresses != "10.2.0.3/32,10.2.0.4/32" {
		t.Errorf("expected combined IPv4 RemoteAddresses, got %q", rules[0].RemoteAddresses)
	}
}

func TestNodeToEndpointRules_Empty(t *testing.T) {
	mock := &hns.MockAPI{}
	ps := &mockPolicySets{}
	m := newTestEndpointManagerWithPolicySets(mock, ps)
	m.hostAddrs = []string{}
	m.getIPv6Addrs = func() []string { return nil }

	rules := m.nodeToEndpointRules()
	if rules != nil {
		t.Fatalf("expected nil rules for empty hostAddrs, got %d rules", len(rules))
	}
}

// Verify that IPv6 address changes do NOT trigger markAllEndpointForRefresh.
// The polling loop sends only IPv4 addresses. If the IPv4 set is unchanged,
// no reprogram occurs even if IPv6 addresses changed underneath.
func TestCompleteDeferredWork_IPv6ChangeDoesNotTriggerRefresh(t *testing.T) {
	mock := &hns.MockAPI{}
	ps := &mockPolicySets{}
	m := newTestEndpointManagerWithPolicySets(mock, ps)
	m.hostAddrs = []string{"10.2.0.3/32"}
	m.getIPv6Addrs = func() []string { return []string{"fd00::1/128"} }

	// Add an active endpoint.
	epId := proto.WorkloadEndpointID{OrchestratorId: "k8s", WorkloadId: "ns/pod1", EndpointId: "eth0"}
	m.activeWlEndpoints[epId] = &proto.WorkloadEndpoint{}

	// Simulate a host address update with the SAME IPv4 addresses.
	// (The polling loop only sends IPv4 now.)
	m.pendingHostAddrs = []string{"10.2.0.3/32"}
	m.CompleteDeferredWork()

	// The endpoint should NOT have been queued for refresh.
	if _, ok := m.pendingWlEpUpdates[epId]; ok {
		t.Error("endpoint was queued for refresh even though IPv4 addresses didn't change; IPv6 fluctuation should not trigger reprogram")
	}
}

// Verify that IPv4 address changes DO trigger markAllEndpointForRefresh.
func TestCompleteDeferredWork_IPv4ChangeTriggerRefresh(t *testing.T) {
	mock := &hns.MockAPI{}
	ps := &mockPolicySets{}
	m := newTestEndpointManagerWithPolicySets(mock, ps)
	m.hostAddrs = []string{"10.2.0.3/32"}
	m.getIPv6Addrs = func() []string { return nil }

	epId := proto.WorkloadEndpointID{OrchestratorId: "k8s", WorkloadId: "ns/pod1", EndpointId: "eth0"}
	m.activeWlEndpoints[epId] = &proto.WorkloadEndpoint{}

	// Simulate a host address update with a NEW IPv4 address.
	m.pendingHostAddrs = []string{"10.2.0.3/32", "10.2.0.99/32"}
	m.CompleteDeferredWork()

	if _, ok := m.pendingWlEpUpdates[epId]; !ok {
		t.Error("endpoint was NOT queued for refresh even though IPv4 addresses changed")
	}
}

// Test that endpoint removal works (nil workload in pending).
func TestCompleteDeferredWork_EndpointRemoval(t *testing.T) {
	mock := &hns.MockAPI{}
	ps := &mockPolicySets{}
	m := newTestEndpointManagerWithPolicySets(mock, ps)

	wepID := proto.WorkloadEndpointID{
		OrchestratorId: "k8s",
		WorkloadId:     "default/pod-old",
		EndpointId:     "eth0",
	}
	// Pre-populate active endpoints.
	m.activeWlEndpoints[wepID] = &proto.WorkloadEndpoint{Name: "pod-old"}
	// Queue a removal.
	m.pendingWlEpUpdates[wepID] = nil

	err := m.CompleteDeferredWork()
	if err != nil {
		t.Fatalf("CompleteDeferredWork failed: %v", err)
	}
	if _, active := m.activeWlEndpoints[wepID]; active {
		t.Error("removed workload should not be in active endpoints")
	}
	if _, pending := m.pendingWlEpUpdates[wepID]; pending {
		t.Error("removed workload should not be in pending updates")
	}
}

func defaultNetworkRegexp() *regexp.Regexp {
	r, _ := regexp.Compile(defaultNetworkName)
	return r
}
