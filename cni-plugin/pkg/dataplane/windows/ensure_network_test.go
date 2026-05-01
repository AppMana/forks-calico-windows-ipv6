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

package windows

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

// mockHNS simulates HNS network operations for testing.
//
// === Modeling HNS's real ManagementIPv6 picker behaviour ===
//
// On Server 2022 build 20348, the live HNS service has these properties
// (verified empirically and against HostNetSvc.dll PDB symbols):
//
//   - Honours ManagementIP (IPv4) from the create-request JSON.
//   - Silently drops ManagementIPv6 from the create-request JSON.
//   - Async auto-picks ManagementIPv6 by scanning the underlying NIC
//     after the network is created — picks the first non-link-local
//     IPv6 it finds, which on a node with both a SLAAC GUA and a
//     SLAAC ULA on the same vNIC is typically the GUA (the order
//     depends on prefix arrival order from RA, hence "random" /
//     "wrong" from our perspective). There is no input field that
//     can override this. AKS's windowsnodereset.ps1 confirms the
//     same — they Restart-Service hns to retrigger the pick because
//     there's no setter.
//
// The mock's autoPickIPv6 field models that behaviour. Tests opt in
// to a specific "wrong" pick by setting it. wantIPv6Drop=true tells
// the mock to assert it never reflects an input ManagementIPv6 back —
// a regression check against accidentally trusting the input field.
type mockHNS struct {
	networks       map[string]*HNSNetworkInfo
	createCalls    int
	deleteCalls    int
	createErr      error
	createFailFor  int
	lastCreateJSON string
	// autoPickIPv6 simulates HNS's NIC-scan auto-pick. If set, every
	// successful Create produces a network whose ManagementIPv6
	// equals this value, regardless of what the caller passed in
	// the JSON. Tests that want to exercise the "HNS picked the
	// wrong v6" path set this to a GUA that doesn't match the
	// caller's expected ULA.
	autoPickIPv6 string
	// reflectInputManagementIPv6=true makes the mock incorrectly
	// honour the input ManagementIPv6 — the OPPOSITE of how real
	// HNS behaves. Useful only for negative tests.
	reflectInputManagementIPv6 bool
}

func newMockHNS() *mockHNS {
	return &mockHNS{networks: make(map[string]*HNSNetworkInfo)}
}

func (m *mockHNS) GetByName(name string) (*HNSNetworkInfo, error) {
	n, ok := m.networks[name]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return n, nil
}

func (m *mockHNS) Delete(network *HNSNetworkInfo) error {
	m.deleteCalls++
	delete(m.networks, network.Name)
	return nil
}

func (m *mockHNS) Create(jsonRequest string) (*HNSNetworkInfo, error) {
	m.createCalls++
	m.lastCreateJSON = jsonRequest
	if m.createFailFor > 0 {
		m.createFailFor--
		if m.createErr != nil {
			return nil, m.createErr
		}
		return nil, fmt.Errorf("adapter not found")
	}

	// Parse the request to model HNS's selective field handling.
	// This is what real HNS does:
	//   - ManagementIP (v4) is honoured (input -> output).
	//   - ManagementIPv6 is silently dropped (input is ignored).
	//   - Subnets[] are honoured.
	//   - ManagementIPv6 is auto-picked from the NIC asynchronously;
	//     we model that with autoPickIPv6.
	var req struct {
		Name           string `json:"Name"`
		Type           string `json:"Type"`
		ManagementIP   string `json:"ManagementIP"`
		ManagementIPv6 string `json:"ManagementIPv6"`
		Subnets        []struct {
			AddressPrefix  string `json:"AddressPrefix"`
			GatewayAddress string `json:"GatewayAddress"`
		} `json:"Subnets"`
	}
	_ = json.Unmarshal([]byte(jsonRequest), &req)

	net := &HNSNetworkInfo{
		Id:           fmt.Sprintf("mock-id-%d", m.createCalls),
		Name:         req.Name,
		Type:         req.Type,
		ManagementIP: req.ManagementIP,
	}
	if net.Name == "" {
		net.Name = "Calico"
	}
	if net.Type == "" {
		net.Type = "L2Bridge"
	}
	for _, s := range req.Subnets {
		net.Subnets = append(net.Subnets, HNSSubnet{
			AddressPrefix:  s.AddressPrefix,
			GatewayAddress: s.GatewayAddress,
		})
	}

	// Real HNS: drops input ManagementIPv6, async auto-picks from
	// NIC. Tests that care about this set autoPickIPv6 to model
	// what the NIC scan would produce — typically a SLAAC GUA when
	// the caller wanted a ULA.
	if m.reflectInputManagementIPv6 {
		net.ManagementIPv6 = req.ManagementIPv6
	} else if m.autoPickIPv6 != "" {
		net.ManagementIPv6 = m.autoPickIPv6
	}
	// If neither is set, ManagementIPv6 stays "" — the mock equivalent
	// of "HNS hasn't finished picking yet" or "no IPv6 on NIC".

	m.networks[net.Name] = net
	return net, nil
}

func testLogger() *logrus.Entry {
	l := logrus.New()
	l.SetLevel(logrus.DebugLevel)
	return logrus.NewEntry(l)
}

func TestEnsureNetwork_NoExisting_CreatesIPv4Only(t *testing.T) {
	mock := newMockHNS()
	subV4 := mustParseCIDR("10.3.16.0/26")

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, "", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if net == nil {
		t.Fatal("expected network to be created")
	}
	if mock.createCalls != 1 {
		t.Errorf("expected 1 create call, got %d", mock.createCalls)
	}
}

func TestEnsureNetwork_NoExisting_CreatesDualStack(t *testing.T) {
	mock := newMockHNS()
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:db8::/122")

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6, "", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if net == nil {
		t.Fatal("expected network to be created")
	}
	if mock.createCalls != 1 {
		t.Errorf("expected 1 create call, got %d", mock.createCalls)
	}
}

func TestEnsureNetwork_ExistingMatch_NoRecreate(t *testing.T) {
	mock := newMockHNS()
	mock.networks["Calico"] = &HNSNetworkInfo{
		Name: "Calico",
		Type: "L2Bridge",
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		},
	}
	subV4 := mustParseCIDR("10.3.16.0/26")

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, "", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if net == nil {
		t.Fatal("expected network to be returned")
	}
	if mock.createCalls != 0 {
		t.Errorf("expected 0 create calls (reuse existing), got %d", mock.createCalls)
	}
	if mock.deleteCalls != 0 {
		t.Errorf("expected 0 delete calls, got %d", mock.deleteCalls)
	}
}

func TestEnsureNetwork_ExistingDualStackMatch_NoRecreate(t *testing.T) {
	mock := newMockHNS()
	mock.networks["Calico"] = &HNSNetworkInfo{
		Name: "Calico",
		Type: "L2Bridge",
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
			{AddressPrefix: "2001:db8::/122", GatewayAddress: "2001:db8::1"},
		},
	}
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:db8::/122")

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6, "", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if net == nil {
		t.Fatal("expected network to be returned")
	}
	if mock.createCalls != 0 {
		t.Errorf("expected 0 create calls (reuse existing), got %d", mock.createCalls)
	}
}

func TestEnsureNetwork_IPv4OnlyToDualStack_RecreatesNetwork(t *testing.T) {
	// When the existing network is IPv4-only but dual-stack is requested,
	// the code must delete and recreate the network so pods get correct
	// dual-stack IPs.  Existing pods are disrupted but will be rescheduled.
	mock := newMockHNS()
	mock.networks["Calico"] = &HNSNetworkInfo{
		Name: "Calico",
		Type: "L2Bridge",
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		},
	}
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:db8::/122")

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6, "", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if net == nil {
		t.Fatal("expected network to be recreated")
	}
	if mock.deleteCalls == 0 {
		t.Errorf("expected delete call to remove mismatched network, got 0")
	}
	if mock.createCalls != 1 {
		t.Errorf("expected 1 create call for new dual-stack network, got %d", mock.createCalls)
	}
}

// CNI invocation for a v4-only pod (no ipv6pools annotation, e.g.
// rancher-local-path-provisioner helper) must NOT recreate a dual-stack
// network. That would tear down every running dual-stack pod's HNS
// endpoint. Removing v6 from the node network is an operator-level
// (FELIX_IPV6SUPPORT=false) decision, not a per-pod CNI one.
func TestEnsureNetwork_IPv4OnlyPod_DualStackNetwork_NoRecreate(t *testing.T) {
	mock := newMockHNS()
	mock.networks["Calico"] = &HNSNetworkInfo{
		Id:   "preexisting-id",
		Name: "Calico",
		Type: "L2Bridge",
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
			{AddressPrefix: "2001:db8::/122", GatewayAddress: "2001:db8::1"},
		},
	}
	subV4 := mustParseCIDR("10.3.16.0/26")

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, "", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if net == nil {
		t.Fatal("expected existing network to be returned")
	}
	if net.Id != "preexisting-id" {
		t.Errorf("expected to reuse pre-existing network id, got %s", net.Id)
	}
	if mock.deleteCalls != 0 {
		t.Errorf("MUST NOT delete dual-stack network for v4-only pod, got %d delete calls", mock.deleteCalls)
	}
	if mock.createCalls != 0 {
		t.Errorf("MUST NOT recreate, got %d create calls", mock.createCalls)
	}
}

// Suppressed: kept for reference; replaced by the test above.
// Original behavior was wrong; per-pod CNI must not delete the network.
func TestEnsureNetwork_DualStackToIPv4_LegacyAlwaysRecreate_DELETED(t *testing.T) {
	t.Skip("Replaced by TestEnsureNetwork_IPv4OnlyPod_DualStackNetwork_NoRecreate; per-pod CNI does not recreate.")
	mock := newMockHNS()
	mock.networks["Calico"] = &HNSNetworkInfo{
		Name: "Calico",
		Type: "L2Bridge",
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
			{AddressPrefix: "2001:db8::/122", GatewayAddress: "2001:db8::1"},
		},
	}
	subV4 := mustParseCIDR("10.3.16.0/26")

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, "", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if net == nil {
		t.Fatal("expected network to be recreated")
	}
	if mock.deleteCalls == 0 {
		t.Errorf("expected delete call to remove mismatched network, got 0")
	}
	if mock.createCalls != 1 {
		t.Errorf("expected 1 create call, got %d", mock.createCalls)
	}
}

func TestEnsureNetwork_IPv6PrefixChange_RecreatesNetwork(t *testing.T) {
	// When the IPv6 prefix changes (e.g. DHCPv6-PD reassignment), the
	// existing HNS network has the old prefix.  The code must delete and
	// recreate so new pods get IPs from the new prefix.
	mock := newMockHNS()
	mock.networks["Calico"] = &HNSNetworkInfo{
		Name: "Calico",
		Type: "L2Bridge",
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
			{AddressPrefix: "2001:5a8:42ae:5c01::/122", GatewayAddress: "2001:5a8:42ae:5c01::1"},
		},
	}
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:5a8:428e:ea01::/122") // new prefix

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6, "", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if net == nil {
		t.Fatal("expected network to be recreated with new prefix")
	}
	if mock.deleteCalls == 0 {
		t.Error("expected delete call to remove network with old IPv6 prefix")
	}
	if mock.createCalls != 1 {
		t.Errorf("expected 1 create call for new prefix, got %d", mock.createCalls)
	}
	if !strings.Contains(mock.lastCreateJSON, "2001:5a8:428e:ea01::/122") {
		t.Errorf("expected new IPv6 prefix in create JSON, got: %s", mock.lastCreateJSON)
	}
}

func TestEnsureNetwork_ExternalNetworkDeleted_BeforeCreate(t *testing.T) {
	mock := newMockHNS()
	mock.networks["External"] = &HNSNetworkInfo{
		Name: "External",
		Type: "L2Bridge",
		Subnets: []HNSSubnet{
			{AddressPrefix: "192.168.255.0/30", GatewayAddress: "192.168.255.1"},
		},
	}
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:db8::/122")

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6, "", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if net == nil {
		t.Fatal("expected network to be created")
	}
	if mock.deleteCalls != 1 {
		t.Errorf("expected 1 delete call (External network), got %d", mock.deleteCalls)
	}
	if _, exists := mock.networks["External"]; exists {
		t.Error("External network should have been deleted")
	}
	if mock.createCalls != 1 {
		t.Errorf("expected 1 create call, got %d", mock.createCalls)
	}
}

func TestEnsureNetwork_ExternalOverlay_NotDeleted(t *testing.T) {
	mock := newMockHNS()
	mock.networks["External"] = &HNSNetworkInfo{
		Name: "External",
		Type: "Overlay",
	}
	subV4 := mustParseCIDR("10.3.16.0/26")

	_, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, "", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.deleteCalls != 0 {
		t.Errorf("should NOT delete External Overlay network, got %d delete calls", mock.deleteCalls)
	}
}

func TestEnsureNetwork_CreateRetriesOnAdapterNotFound(t *testing.T) {
	mock := newMockHNS()
	mock.createFailFor = 2
	mock.createErr = fmt.Errorf("hnsCall failed: adapter not found (0x803b0006)")
	subV4 := mustParseCIDR("10.3.16.0/26")

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, "", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if net == nil {
		t.Fatal("expected network to be created after retries")
	}
	if mock.createCalls != 3 {
		t.Errorf("expected 3 create calls (2 failed + 1 success), got %d", mock.createCalls)
	}
}

func TestEnsureNetwork_CreateExhaustsRetries(t *testing.T) {
	mock := newMockHNS()
	mock.createFailFor = 100
	mock.createErr = fmt.Errorf("adapter not found")
	subV4 := mustParseCIDR("10.3.16.0/26")

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, "", "", testLogger(), mock)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if net != nil {
		t.Error("expected nil network on failure")
	}
	if mock.createCalls != 10 {
		t.Errorf("expected 10 create attempts, got %d", mock.createCalls)
	}
	if !strings.Contains(err.Error(), "adapter not found") {
		t.Errorf("expected adapter error, got: %v", err)
	}
}

func TestEnsureNetwork_NoExternalNetwork_CreateDirectly(t *testing.T) {
	mock := newMockHNS()
	subV4 := mustParseCIDR("10.3.16.0/26")

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, "", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if net == nil {
		t.Fatal("expected network to be created")
	}
	if mock.deleteCalls != 0 {
		t.Errorf("expected 0 delete calls (no External), got %d", mock.deleteCalls)
	}
	if mock.createCalls != 1 {
		t.Errorf("expected 1 create call, got %d", mock.createCalls)
	}
}

func TestEnsureNetwork_DualStack_JSONContainsIPv6True(t *testing.T) {
	mock := newMockHNS()
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:db8::/122")

	_, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6, "", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.createCalls != 1 {
		t.Fatalf("expected 1 create call, got %d", mock.createCalls)
	}
	if !strings.Contains(mock.lastCreateJSON, `"IPv6":true`) {
		t.Errorf("expected JSON to contain '\"IPv6\":true', got: %s", mock.lastCreateJSON)
	}
}

func TestEnsureNetwork_IPv4Only_JSONDoesNotContainIPv6(t *testing.T) {
	mock := newMockHNS()
	subV4 := mustParseCIDR("10.3.16.0/26")

	_, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, "", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.createCalls != 1 {
		t.Fatalf("expected 1 create call, got %d", mock.createCalls)
	}
	if strings.Contains(mock.lastCreateJSON, `"IPv6"`) {
		t.Errorf("expected JSON to NOT contain 'IPv6' key for IPv4-only, got: %s", mock.lastCreateJSON)
	}
}

func TestEnsureNetwork_DualStack_JSONContainsBothSubnets(t *testing.T) {
	mock := newMockHNS()
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:db8::/122")

	_, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6, "", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(mock.lastCreateJSON, "10.3.16.0/26") {
		t.Errorf("expected JSON to contain IPv4 subnet, got: %s", mock.lastCreateJSON)
	}
	if !strings.Contains(mock.lastCreateJSON, "2001:db8::/122") {
		t.Errorf("expected JSON to contain IPv6 subnet, got: %s", mock.lastCreateJSON)
	}
	if !strings.Contains(mock.lastCreateJSON, "2001:db8::1") {
		t.Errorf("expected JSON to contain IPv6 gateway, got: %s", mock.lastCreateJSON)
	}
}

// === ManagementIP / ManagementIPv6 plumbing tests ===
//
// These tests exercise the full ensureNetworkExistsWithAPI codepath
// against a mock HNS that models real HNS's selective ManagementIPv6
// dropping behaviour (see the mockHNS comment for the empirical
// reasoning).

// When the caller passes mgmtIP, the create-request JSON should
// include "ManagementIP":"<value>" — HNS does honour this field.
func TestEnsureNetwork_PassesManagementIPInJSON(t *testing.T) {
	mock := newMockHNS()
	subV4 := mustParseCIDR("10.3.48.192/26")
	_, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, "10.2.0.3", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(mock.lastCreateJSON, `"ManagementIP":"10.2.0.3"`) {
		t.Errorf("expected JSON to contain ManagementIP, got: %s", mock.lastCreateJSON)
	}
}

// When the caller passes mgmtIPv6, the create-request JSON should
// include "ManagementIPv6":"<value>" — even though real HNS will
// silently drop it. We send it anyway so that a future Windows build
// that DOES honour the field will pick the right address. The drop
// behaviour is modeled by the mock's default (autoPickIPv6=="").
func TestEnsureNetwork_PassesManagementIPv6InJSONEvenThoughHNSDropsIt(t *testing.T) {
	mock := newMockHNS()
	subV4 := mustParseCIDR("10.3.48.192/26")
	subV6 := mustParseCIDR("2001:5a8:4294:9c01:430d:9038:5fa1:d000/122")
	_, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6,
		"10.2.0.3", "fd5a:8000:1:0:1ac0:4dff:fe89:5194", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(mock.lastCreateJSON, `"ManagementIPv6":"fd5a:8000:1:0:1ac0:4dff:fe89:5194"`) {
		t.Errorf("expected JSON to contain ManagementIPv6, got: %s", mock.lastCreateJSON)
	}
	// And confirm the result reflects HNS's drop: result.ManagementIPv6
	// is empty (mock default), NOT what we passed.
	created, _ := mock.GetByName("Calico")
	if created.ManagementIPv6 != "" {
		t.Errorf("mock should model HNS dropping ManagementIPv6 input; got %q", created.ManagementIPv6)
	}
}

// The realistic worst-case at Calico bring-up: Linux nodes cannot
// resolve Win's ULA via NDP because HNS auto-picked the GUA as
// ManagementIPv6. On the next pod creation, ensureNetworkExistsWithAPI
// must detect the mismatch and recreate the network. After recreation
// the operator-driven NIC strip has happened, so the mock's auto-pick
// switches to the ULA.
func TestEnsureNetwork_ManagementIPv6Mismatch_TriggersRecreate(t *testing.T) {
	mock := newMockHNS()
	subV4 := mustParseCIDR("10.3.48.192/26")
	subV6 := mustParseCIDR("2001:5a8:4294:9c01:430d:9038:5fa1:d000/122")

	// Step 1: HNS auto-picks the GUA — the bug we're fixing.
	mock.autoPickIPv6 = "2001:5a8:4294:9c00:1ac0:4dff:fe89:5194"
	_, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6,
		"10.2.0.3", "fd5a:8000:1:0:1ac0:4dff:fe89:5194", testLogger(), mock)
	if err != nil {
		t.Fatalf("step 1 unexpected error: %v", err)
	}
	if mock.createCalls != 1 {
		t.Fatalf("step 1: expected 1 create call, got %d", mock.createCalls)
	}
	created, _ := mock.GetByName("Calico")
	if created.ManagementIPv6 != "2001:5a8:4294:9c00:1ac0:4dff:fe89:5194" {
		t.Fatalf("step 1: mock should have auto-picked GUA, got %q", created.ManagementIPv6)
	}

	// Step 2: next pod hits CNI. The autodetected ULA still doesn't
	// match the existing ManagementIPv6 (GUA). networkNeedsRecreate
	// must trigger a delete+recreate. This time the operator's
	// NIC-strip has happened, so the mock auto-picks the ULA.
	mock.autoPickIPv6 = "fd5a:8000:1:0:1ac0:4dff:fe89:5194"
	_, err = ensureNetworkExistsWithAPI("Calico", subV4, subV6,
		"10.2.0.3", "fd5a:8000:1:0:1ac0:4dff:fe89:5194", testLogger(), mock)
	if err != nil {
		t.Fatalf("step 2 unexpected error: %v", err)
	}
	if mock.deleteCalls != 1 {
		t.Errorf("step 2: expected 1 delete call (mismatch -> recreate), got %d", mock.deleteCalls)
	}
	if mock.createCalls != 2 {
		t.Errorf("step 2: expected 2 total create calls, got %d", mock.createCalls)
	}
	finalNW, _ := mock.GetByName("Calico")
	if finalNW.ManagementIPv6 != "fd5a:8000:1:0:1ac0:4dff:fe89:5194" {
		t.Errorf("step 2: expected ManagementIPv6=ULA after recreate, got %q", finalNW.ManagementIPv6)
	}

	// Step 3: third pod. Existing network already matches; no recreate.
	_, err = ensureNetworkExistsWithAPI("Calico", subV4, subV6,
		"10.2.0.3", "fd5a:8000:1:0:1ac0:4dff:fe89:5194", testLogger(), mock)
	if err != nil {
		t.Fatalf("step 3 unexpected error: %v", err)
	}
	if mock.createCalls != 2 {
		t.Errorf("step 3: expected no additional create (idempotent reuse), got %d total", mock.createCalls)
	}
	if mock.deleteCalls != 1 {
		t.Errorf("step 3: expected no additional delete, got %d total", mock.deleteCalls)
	}
}

// Stable steady state: HNS already has the correct ManagementIPv6;
// every subsequent CNI invocation reuses without churn.
func TestEnsureNetwork_ManagementIPv6Match_NoRecreate(t *testing.T) {
	mock := newMockHNS()
	subV4 := mustParseCIDR("10.3.48.192/26")
	subV6 := mustParseCIDR("2001:5a8:4294:9c01:430d:9038:5fa1:d000/122")
	mock.autoPickIPv6 = "fd5a:8000:1:0:1ac0:4dff:fe89:5194"

	// 5 invocations in a row.
	for i := 0; i < 5; i++ {
		_, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6,
			"10.2.0.3", "fd5a:8000:1:0:1ac0:4dff:fe89:5194", testLogger(), mock)
		if err != nil {
			t.Fatalf("iter %d: unexpected error: %v", i, err)
		}
	}
	if mock.createCalls != 1 {
		t.Errorf("expected exactly 1 create across 5 calls, got %d", mock.createCalls)
	}
	if mock.deleteCalls != 0 {
		t.Errorf("expected 0 deletes (network kept stable), got %d", mock.deleteCalls)
	}
}

// HNS hasn't finished picking ManagementIPv6 yet (returns ""): we treat
// "" as "don't know" and DO NOT trigger recreate. The next CNI
// invocation will see whatever HNS settled on.
func TestEnsureNetwork_HNSReturnsEmptyManagementIPv6_NoRecreate(t *testing.T) {
	mock := newMockHNS()
	subV4 := mustParseCIDR("10.3.48.192/26")
	subV6 := mustParseCIDR("2001:5a8:4294:9c01:430d:9038:5fa1:d000/122")
	// autoPickIPv6 not set -> mock leaves it "" on Create, modeling
	// the brief window where HNS hasn't completed its NIC scan.

	_, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6,
		"10.2.0.3", "fd5a:8000:1:0:1ac0:4dff:fe89:5194", testLogger(), mock)
	if err != nil {
		t.Fatalf("step 1: unexpected error: %v", err)
	}
	if mock.createCalls != 1 {
		t.Fatalf("step 1: expected 1 create, got %d", mock.createCalls)
	}

	// Subsequent invocation: existing.ManagementIPv6 == "". Even though
	// we "want" the ULA, "" is not a confirmed mismatch, so no
	// recreate.
	_, err = ensureNetworkExistsWithAPI("Calico", subV4, subV6,
		"10.2.0.3", "fd5a:8000:1:0:1ac0:4dff:fe89:5194", testLogger(), mock)
	if err != nil {
		t.Fatalf("step 2: unexpected error: %v", err)
	}
	if mock.deleteCalls != 0 {
		t.Errorf("expected no delete on '' ManagementIPv6, got %d", mock.deleteCalls)
	}
	if mock.createCalls != 1 {
		t.Errorf("expected no extra create on '' ManagementIPv6, got %d", mock.createCalls)
	}
}

// IPv4-only ManagementIP mismatch alone is a recreate trigger — covers
// pure IPv4 clusters that change node IP autodetection.
func TestEnsureNetwork_ManagementIPv4Mismatch_TriggersRecreate(t *testing.T) {
	mock := newMockHNS()
	subV4 := mustParseCIDR("10.3.48.192/26")
	// Step 1: existing network created with the wrong ManagementIP.
	mock.networks["Calico"] = &HNSNetworkInfo{
		Id:           "preexisting",
		Name:         "Calico",
		Type:         "L2Bridge",
		ManagementIP: "192.168.99.99",
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.48.192/26", GatewayAddress: "10.3.48.193"},
		},
	}
	_, err := ensureNetworkExistsWithAPI("Calico", subV4, nil,
		"10.2.0.3", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.deleteCalls != 1 {
		t.Errorf("expected 1 delete (IPv4 mgmt mismatch), got %d", mock.deleteCalls)
	}
	if mock.createCalls != 1 {
		t.Errorf("expected 1 create after recreate, got %d", mock.createCalls)
	}
	created, _ := mock.GetByName("Calico")
	if created.ManagementIP != "10.2.0.3" {
		t.Errorf("expected ManagementIP=10.2.0.3 after recreate, got %q", created.ManagementIP)
	}
}

// Caller passes mgmtIP="" (legacy / VXLAN / IPv4-only without
// autodetect plumbing): mismatch checks are skipped — never recreate.
// Real-world: legacy callers that haven't been updated to thread
// autodetect IPs through must continue to work unchanged.
func TestEnsureNetwork_LegacyCaller_NoMgmtPin_NeverRecreates(t *testing.T) {
	mock := newMockHNS()
	subV4 := mustParseCIDR("10.3.48.192/26")
	mock.networks["Calico"] = &HNSNetworkInfo{
		Id:             "preexisting",
		Name:           "Calico",
		Type:           "L2Bridge",
		ManagementIP:   "10.2.0.3",
		ManagementIPv6: "2001:5a8:4294:9c00:1ac0:4dff:fe89:5194", // wrong, but caller doesn't care
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.48.192/26", GatewayAddress: "10.3.48.193"},
		},
	}
	_, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, "", "", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.deleteCalls != 0 {
		t.Errorf("expected no delete when caller didn't pin, got %d", mock.deleteCalls)
	}
	if mock.createCalls != 0 {
		t.Errorf("expected no create (network reused), got %d", mock.createCalls)
	}
}

// Negative test: if a hypothetical future HNS build actually honoured
// the input ManagementIPv6, our current code would Just Work (no
// mismatch triggered, ULA gets pinned). This is more documentation
// than regression — it makes the wiring explicit so a reviewer can
// confirm the "we send the input even though HNS drops it" path.
func TestEnsureNetwork_HypotheticalHNSHonoursInput_HappyPath(t *testing.T) {
	mock := newMockHNS()
	mock.reflectInputManagementIPv6 = true // pretend HNS started honouring it
	subV4 := mustParseCIDR("10.3.48.192/26")
	subV6 := mustParseCIDR("2001:5a8:4294:9c01:430d:9038:5fa1:d000/122")

	_, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6,
		"10.2.0.3", "fd5a:8000:1:0:1ac0:4dff:fe89:5194", testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	created, _ := mock.GetByName("Calico")
	if created.ManagementIPv6 != "fd5a:8000:1:0:1ac0:4dff:fe89:5194" {
		t.Errorf("hypothetical-honoring HNS: expected ManagementIPv6=ULA, got %q", created.ManagementIPv6)
	}
	// Idempotent on re-invocation.
	_, err = ensureNetworkExistsWithAPI("Calico", subV4, subV6,
		"10.2.0.3", "fd5a:8000:1:0:1ac0:4dff:fe89:5194", testLogger(), mock)
	if err != nil {
		t.Fatalf("re-invocation error: %v", err)
	}
	if mock.deleteCalls != 0 || mock.createCalls != 1 {
		t.Errorf("expected idempotent reuse on re-invocation, got %d delete / %d create",
			mock.deleteCalls, mock.createCalls)
	}
}
