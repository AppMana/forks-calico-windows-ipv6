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

// Critical regression test for the rancher-local-path-provisioner /
// "helper-pod-delete-pvc" symptom: a pod without the
// `cni.projectcalico.org/ipv6pools` annotation hits CNI with subV6=nil.
// The existing HNS network is dual-stack (created by calico-node startup
// with FELIX_IPV6SUPPORT=true). networkNeedsRecreate must NOT trigger a
// recreate here, because:
//   - The IPv4-only pod's subV4 is satisfied by the existing dual-stack network.
//   - Recreating would delete the network, destroying every running
//     dual-stack pod's HNS endpoint and leaving Felix in an
//     "Could not resolve hns endpoint id" loop until the next reboot.
//
// Removing IPv6 entirely from the node network is operator-driven via
// FELIX_IPV6SUPPORT=false at calico-node startup, NOT a per-pod CNI
// side effect.
func TestNetworkNeedsRecreate_IPv4OnlyPod_DualStackNetwork_NoRecreate(t *testing.T) {
	subV4 := mustParseCIDR("10.3.48.192/26")
	existing := []HNSSubnet{
		{AddressPrefix: "10.3.48.192/26", GatewayAddress: "10.3.48.193"},
		{AddressPrefix: "2001:5a8:4294:9c01:430d:9038:5fa1:d000/122",
			GatewayAddress: "2001:5a8:4294:9c01:430d:9038:5fa1:d001"},
	}
	if networkNeedsRecreate(existing, subV4, nil) {
		t.Error("IPv4-only pod must not downgrade dual-stack network to IPv4-only")
	}
}

func TestNetworkNeedsRecreate_EmptyExisting(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	if !networkNeedsRecreate(nil, subV4, nil) {
		t.Error("expected recreate when no existing subnets")
	}
}

// Extra unrelated v4 subnet alongside the desired one is unusual but
// not a reason to tear down the network from a per-pod CNI call. We
// only require the desired v4 prefix to be present.
func TestNetworkNeedsRecreate_ExtraIPv4Subnet_NoRecreate(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	existing := []HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		{AddressPrefix: "172.16.0.0/24", GatewayAddress: "172.16.0.1"},
	}
	if networkNeedsRecreate(existing, subV4, nil) {
		t.Error("an extra unrelated v4 subnet alongside the desired one should not trigger recreate")
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
