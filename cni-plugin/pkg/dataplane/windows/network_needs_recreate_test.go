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
	"testing"
)

func TestNetworkNeedsRecreate_IPv4Only_Match(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	existing := []HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
	}
	if networkNeedsRecreate(existing, subV4, nil) {
		t.Error("expected no recreate for matching IPv4-only network")
	}
}

func TestNetworkNeedsRecreate_IPv4Only_WrongGateway(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	existing := []HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.99"},
	}
	if !networkNeedsRecreate(existing, subV4, nil) {
		t.Error("expected recreate when gateway doesn't match")
	}
}

func TestNetworkNeedsRecreate_IPv4Only_WrongSubnet(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	existing := []HNSSubnet{
		{AddressPrefix: "10.3.32.0/26", GatewayAddress: "10.3.32.1"},
	}
	if !networkNeedsRecreate(existing, subV4, nil) {
		t.Error("expected recreate when subnet doesn't match")
	}
}

func TestNetworkNeedsRecreate_IPv4ToDualStack(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:5a8:42ae:5c01:a62c:8bd9:eff8:1000/122")
	existing := []HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
	}
	if !networkNeedsRecreate(existing, subV4, subV6) {
		t.Error("expected recreate when adding IPv6 to IPv4-only network")
	}
}

func TestNetworkNeedsRecreate_DualStack_Match(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:db8::/122")
	existing := []HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		{AddressPrefix: "2001:db8::/122", GatewayAddress: "2001:db8::1"},
	}
	if networkNeedsRecreate(existing, subV4, subV6) {
		t.Error("expected no recreate for matching dual-stack network")
	}
}

func TestNetworkNeedsRecreate_DualStack_WrongV6Subnet(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:db8::40/122")
	existing := []HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		{AddressPrefix: "2001:db8::/122", GatewayAddress: "2001:db8::1"},
	}
	if !networkNeedsRecreate(existing, subV4, subV6) {
		t.Error("expected recreate when IPv6 subnet changed")
	}
}

func TestNetworkNeedsRecreate_DualStackToIPv4Only(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	existing := []HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		{AddressPrefix: "2001:db8::/122", GatewayAddress: "2001:db8::1"},
	}
	if !networkNeedsRecreate(existing, subV4, nil) {
		t.Error("expected recreate when removing IPv6 from dual-stack network")
	}
}

func TestNetworkNeedsRecreate_EmptyExisting(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	if !networkNeedsRecreate(nil, subV4, nil) {
		t.Error("expected recreate when no existing subnets")
	}
}

func TestNetworkNeedsRecreate_ExtraSubnets(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	existing := []HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		{AddressPrefix: "172.16.0.0/24", GatewayAddress: "172.16.0.1"},
	}
	if !networkNeedsRecreate(existing, subV4, nil) {
		t.Error("expected recreate when extra subnets exist")
	}
}

func TestNetworkNeedsRecreate_DualStack_SubnetsReversed(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:db8::/122")
	existing := []HNSSubnet{
		{AddressPrefix: "2001:db8::/122", GatewayAddress: "2001:db8::1"},
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
	}
	if networkNeedsRecreate(existing, subV4, subV6) {
		t.Error("expected no recreate for matching dual-stack network with reversed subnet order")
	}
}

func TestNetworkNeedsRecreate_DualStack_V6GatewayChanged(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:db8::40/122")
	existing := []HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		{AddressPrefix: "2001:db8::40/122", GatewayAddress: "2001:db8::99"},
	}
	if !networkNeedsRecreate(existing, subV4, subV6) {
		t.Error("expected recreate when IPv6 gateway changed")
	}
}
