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

// CNI is invoked per-pod. A pod without the cni.projectcalico.org/ipv6pools
// annotation calls this with subNetV6==nil. Such a pod must NOT tear down a
// dual-stack network used by other pods on the node. Removing IPv6 entirely
// from the node network is operator-driven (FELIX_IPV6SUPPORT=false), handled
// at calico-node startup, not by per-pod CNI invocations.
//
// Regression test for the bug where any IPv4-only pod (host-network pod,
// system pod without ipv6 annotation) downgraded the dual-stack HNS network
// to IPv4-only, leaving Felix unable to program endpoints for existing
// dual-stack pods.
func TestNetworkNeedsRecreate_DualStackPreservedOnIPv4OnlyPod(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	existing := []HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		{AddressPrefix: "2001:db8::/122", GatewayAddress: "2001:db8::1"},
	}
	if networkNeedsRecreate(existing, subV4, nil) {
		t.Error("expected NO recreate: dual-stack network can serve an IPv4-only pod")
	}
}

// IPv6 prefix rotated (DHCPv6-PD assigned a different /64). The dual-stack
// pod's request now points at a different /122 than what the existing HNS
// network has. We must recreate.
func TestNetworkNeedsRecreate_DualStack_V6PrefixRotated(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:db8:9c01::/122")
	existing := []HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		{AddressPrefix: "2001:db8:2601::/122", GatewayAddress: "2001:db8:2601::1"},
	}
	if !networkNeedsRecreate(existing, subV4, subV6) {
		t.Error("expected recreate when IPv6 prefix rotated to a new /64")
	}
}

func TestNetworkNeedsRecreate_EmptyExisting(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	if !networkNeedsRecreate(nil, subV4, nil) {
		t.Error("expected recreate when no existing subnets")
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
