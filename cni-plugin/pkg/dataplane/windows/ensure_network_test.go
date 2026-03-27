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
	"fmt"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

// mockHNS simulates HNS network operations for testing.
type mockHNS struct {
	networks       map[string]*HNSNetworkInfo
	createCalls    int
	deleteCalls    int
	createErr      error
	createFailFor  int
	lastCreateJSON string
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
	net := &HNSNetworkInfo{
		Id:   fmt.Sprintf("mock-id-%d", m.createCalls),
		Name: "Calico",
		Type: "L2Bridge",
	}
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

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, testLogger(), mock)
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

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6, testLogger(), mock)
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

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, testLogger(), mock)
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

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6, testLogger(), mock)
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

func TestEnsureNetwork_IPv4OnlyToDualStack_WarnsAndKeeps(t *testing.T) {
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

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6, testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if net == nil {
		t.Fatal("expected existing network to be returned")
	}
	if mock.deleteCalls != 0 {
		t.Errorf("must NOT delete existing Calico network (breaks vSwitch), got %d delete calls", mock.deleteCalls)
	}
	if mock.createCalls != 0 {
		t.Errorf("must NOT create new network when keeping existing, got %d create calls", mock.createCalls)
	}
}

func TestEnsureNetwork_DualStackToIPv4_WarnsAndKeeps(t *testing.T) {
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

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if net == nil {
		t.Fatal("expected existing network to be returned")
	}
	if mock.deleteCalls != 0 {
		t.Errorf("must NOT delete existing Calico network, got %d delete calls", mock.deleteCalls)
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

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6, testLogger(), mock)
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

	_, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, testLogger(), mock)
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

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, testLogger(), mock)
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

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, testLogger(), mock)
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

	net, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, testLogger(), mock)
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

	_, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6, testLogger(), mock)
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

	_, err := ensureNetworkExistsWithAPI("Calico", subV4, nil, testLogger(), mock)
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

	_, err := ensureNetworkExistsWithAPI("Calico", subV4, subV6, testLogger(), mock)
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
