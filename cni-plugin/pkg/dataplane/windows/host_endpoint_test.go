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
	"net"
	"strings"
	"testing"
)

// mockHNSEndpointAPI simulates HNS endpoint operations for testing.
type mockHNSEndpointAPI struct {
	endpoints      map[string]*HNSEndpointInfo
	createCalls    int
	deleteCalls    int
	attachCalls    int
	lastCreateJSON string
}

func newMockHNSEndpointAPI() *mockHNSEndpointAPI {
	return &mockHNSEndpointAPI{endpoints: make(map[string]*HNSEndpointInfo)}
}

func (m *mockHNSEndpointAPI) GetByName(name string) (*HNSEndpointInfo, error) {
	ep, ok := m.endpoints[name]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return ep, nil
}

func (m *mockHNSEndpointAPI) Delete(endpoint *HNSEndpointInfo) error {
	m.deleteCalls++
	delete(m.endpoints, endpoint.Name)
	return nil
}

func (m *mockHNSEndpointAPI) Create(jsonRequest string) (*HNSEndpointInfo, error) {
	m.createCalls++
	m.lastCreateJSON = jsonRequest

	var req map[string]interface{}
	_ = json.Unmarshal([]byte(jsonRequest), &req)

	ep := &HNSEndpointInfo{
		Id:             fmt.Sprintf("mock-ep-id-%d", m.createCalls),
		Name:           req["Name"].(string),
		VirtualNetwork: req["VirtualNetwork"].(string),
		IPAddress:      net.ParseIP(req["IPAddress"].(string)),
	}
	m.endpoints[ep.Name] = ep
	return ep, nil
}

func (m *mockHNSEndpointAPI) HostAttach(endpoint *HNSEndpointInfo, compartmentID uint16) error {
	m.attachCalls++
	return nil
}

func TestCreateAndAttachHostEP_IPv4Only_NoIPv6Address(t *testing.T) {
	mock := newMockHNSEndpointAPI()
	network := &HNSNetworkInfo{
		Id:   "net-1",
		Name: "Calico",
		Type: "L2Bridge",
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		},
	}
	subV4 := mustParseCIDR("10.3.16.0/26")

	ep, err := createAndAttachHostEPWithAPI("Calico_ep", network, subV4, testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep == nil {
		t.Fatal("expected endpoint to be created")
	}
	if mock.createCalls != 1 {
		t.Errorf("expected 1 create call, got %d", mock.createCalls)
	}
	if strings.Contains(mock.lastCreateJSON, "IPv6Address") {
		t.Errorf("expected no IPv6Address in JSON for IPv4-only network, got: %s", mock.lastCreateJSON)
	}
}

func TestCreateAndAttachHostEP_DualStack_SetsIPv6Address(t *testing.T) {
	mock := newMockHNSEndpointAPI()
	network := &HNSNetworkInfo{
		Id:   "net-1",
		Name: "Calico",
		Type: "L2Bridge",
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
			{AddressPrefix: "2001:db8::/122", GatewayAddress: "2001:db8::1"},
		},
	}
	subV4 := mustParseCIDR("10.3.16.0/26")

	ep, err := createAndAttachHostEPWithAPI("Calico_ep", network, subV4, testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep == nil {
		t.Fatal("expected endpoint to be created")
	}
	if mock.createCalls != 1 {
		t.Errorf("expected 1 create call, got %d", mock.createCalls)
	}
	if !strings.Contains(mock.lastCreateJSON, `"IPv6Address"`) {
		t.Errorf("expected IPv6Address in JSON for dual-stack network, got: %s", mock.lastCreateJSON)
	}
	if !strings.Contains(mock.lastCreateJSON, "2001:db8::2") {
		t.Errorf("expected IPv6Address to be 2001:db8::2, got: %s", mock.lastCreateJSON)
	}
}

func TestCreateAndAttachHostEP_DualStack_IPv6AddressIsSecondInBlock(t *testing.T) {
	mock := newMockHNSEndpointAPI()
	network := &HNSNetworkInfo{
		Id:   "net-1",
		Name: "Calico",
		Type: "L2Bridge",
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
			{AddressPrefix: "2001:db8::40/122", GatewayAddress: "2001:db8::41"},
		},
	}
	subV4 := mustParseCIDR("10.3.16.0/26")

	_, err := createAndAttachHostEPWithAPI("Calico_ep", network, subV4, testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(mock.lastCreateJSON, "2001:db8::42") {
		t.Errorf("expected IPv6Address to be 2001:db8::42, got: %s", mock.lastCreateJSON)
	}
}

func TestCreateAndAttachHostEP_ExistingMatchingIP_Reused(t *testing.T) {
	mock := newMockHNSEndpointAPI()
	network := &HNSNetworkInfo{
		Id:   "net-1",
		Name: "Calico",
		Type: "L2Bridge",
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		},
	}
	subV4 := mustParseCIDR("10.3.16.0/26")

	mock.endpoints["Calico_ep"] = &HNSEndpointInfo{
		Id:             "existing-ep-id",
		Name:           "Calico_ep",
		IPAddress:      net.ParseIP("10.3.16.2"),
		VirtualNetwork: "net-1",
	}

	ep, err := createAndAttachHostEPWithAPI("Calico_ep", network, subV4, testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep == nil {
		t.Fatal("expected endpoint to be returned")
	}
	if ep.Id != "existing-ep-id" {
		t.Errorf("expected existing endpoint to be reused, got new ID: %s", ep.Id)
	}
	if mock.createCalls != 0 {
		t.Errorf("expected 0 create calls (reuse existing), got %d", mock.createCalls)
	}
	if mock.deleteCalls != 0 {
		t.Errorf("expected 0 delete calls, got %d", mock.deleteCalls)
	}
	if mock.attachCalls != 0 {
		t.Errorf("expected 0 attach calls (existing matches), got %d", mock.attachCalls)
	}
}

func TestCreateAndAttachHostEP_ExistingWrongIP_DeletedAndRecreated(t *testing.T) {
	mock := newMockHNSEndpointAPI()
	network := &HNSNetworkInfo{
		Id:   "net-1",
		Name: "Calico",
		Type: "L2Bridge",
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		},
	}
	subV4 := mustParseCIDR("10.3.16.0/26")

	mock.endpoints["Calico_ep"] = &HNSEndpointInfo{
		Id:             "stale-ep-id",
		Name:           "Calico_ep",
		IPAddress:      net.ParseIP("10.3.16.99"),
		VirtualNetwork: "net-1",
	}

	ep, err := createAndAttachHostEPWithAPI("Calico_ep", network, subV4, testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep == nil {
		t.Fatal("expected new endpoint to be created")
	}
	if mock.deleteCalls != 1 {
		t.Errorf("expected 1 delete call for stale endpoint, got %d", mock.deleteCalls)
	}
	if mock.createCalls != 1 {
		t.Errorf("expected 1 create call for new endpoint, got %d", mock.createCalls)
	}
	if !strings.Contains(mock.lastCreateJSON, "10.3.16.2") {
		t.Errorf("expected new endpoint IP to be 10.3.16.2, got: %s", mock.lastCreateJSON)
	}
}

func TestCreateAndAttachHostEP_ExistingWrongIP_DualStack_RecreatedWithIPv6(t *testing.T) {
	mock := newMockHNSEndpointAPI()
	network := &HNSNetworkInfo{
		Id:   "net-1",
		Name: "Calico",
		Type: "L2Bridge",
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
			{AddressPrefix: "2001:db8::/122", GatewayAddress: "2001:db8::1"},
		},
	}
	subV4 := mustParseCIDR("10.3.16.0/26")

	mock.endpoints["Calico_ep"] = &HNSEndpointInfo{
		Id:             "stale-ep-id",
		Name:           "Calico_ep",
		IPAddress:      net.ParseIP("10.3.16.99"),
		VirtualNetwork: "net-1",
	}

	_, err := createAndAttachHostEPWithAPI("Calico_ep", network, subV4, testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.deleteCalls != 1 {
		t.Errorf("expected 1 delete call for stale endpoint, got %d", mock.deleteCalls)
	}
	if mock.createCalls != 1 {
		t.Errorf("expected 1 create call, got %d", mock.createCalls)
	}
	if !strings.Contains(mock.lastCreateJSON, "2001:db8::2") {
		t.Errorf("expected recreated endpoint to include IPv6Address 2001:db8::2, got: %s", mock.lastCreateJSON)
	}
}

func TestCreateAndAttachHostEP_NewEndpoint_AttachedToHost(t *testing.T) {
	mock := newMockHNSEndpointAPI()
	network := &HNSNetworkInfo{
		Id:   "net-1",
		Name: "Calico",
		Type: "L2Bridge",
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		},
	}
	subV4 := mustParseCIDR("10.3.16.0/26")

	_, err := createAndAttachHostEPWithAPI("Calico_ep", network, subV4, testLogger(), mock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.attachCalls != 1 {
		t.Errorf("expected 1 attach call for new endpoint, got %d", mock.attachCalls)
	}
}
