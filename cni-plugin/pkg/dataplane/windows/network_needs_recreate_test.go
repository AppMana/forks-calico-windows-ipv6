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

// nwInfo wraps a slice of subnets into an HNSNetworkInfo for tests that
// only care about the subnet matching path.
func nwInfo(subnets []HNSSubnet) *HNSNetworkInfo {
	return &HNSNetworkInfo{Subnets: subnets}
}

func TestNetworkNeedsRecreate_IPv4Only_Match(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	existing := nwInfo([]HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
	})
	if networkNeedsRecreate(existing, subV4, nil, "", "") {
		t.Error("expected no recreate for matching IPv4-only network")
	}
}

func TestNetworkNeedsRecreate_IPv4Only_WrongGateway(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	existing := nwInfo([]HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.99"},
	})
	if !networkNeedsRecreate(existing, subV4, nil, "", "") {
		t.Error("expected recreate when gateway doesn't match")
	}
}

func TestNetworkNeedsRecreate_IPv4Only_WrongSubnet(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	existing := nwInfo([]HNSSubnet{
		{AddressPrefix: "10.3.32.0/26", GatewayAddress: "10.3.32.1"},
	})
	if !networkNeedsRecreate(existing, subV4, nil, "", "") {
		t.Error("expected recreate when subnet doesn't match")
	}
}

func TestNetworkNeedsRecreate_IPv4ToDualStack(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:5a8:42ae:5c01:a62c:8bd9:eff8:1000/122")
	existing := nwInfo([]HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
	})
	if !networkNeedsRecreate(existing, subV4, subV6, "", "") {
		t.Error("expected recreate when adding IPv6 to IPv4-only network")
	}
}

func TestNetworkNeedsRecreate_DualStack_Match(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:db8::/122")
	existing := nwInfo([]HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		{AddressPrefix: "2001:db8::/122", GatewayAddress: "2001:db8::1"},
	})
	if networkNeedsRecreate(existing, subV4, subV6, "", "") {
		t.Error("expected no recreate for matching dual-stack network")
	}
}

func TestNetworkNeedsRecreate_DualStack_WrongV6Subnet(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:db8::40/122")
	existing := nwInfo([]HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		{AddressPrefix: "2001:db8::/122", GatewayAddress: "2001:db8::1"},
	})
	if !networkNeedsRecreate(existing, subV4, subV6, "", "") {
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
	existing := nwInfo([]HNSSubnet{
		{AddressPrefix: "10.3.48.192/26", GatewayAddress: "10.3.48.193"},
		{AddressPrefix: "2001:5a8:4294:9c01:430d:9038:5fa1:d000/122",
			GatewayAddress: "2001:5a8:4294:9c01:430d:9038:5fa1:d001"},
	})
	if networkNeedsRecreate(existing, subV4, nil, "", "") {
		t.Error("IPv4-only pod must not downgrade dual-stack network to IPv4-only")
	}
}

func TestNetworkNeedsRecreate_EmptyExisting(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	if !networkNeedsRecreate(nwInfo(nil), subV4, nil, "", "") {
		t.Error("expected recreate when no existing subnets")
	}
}

// Extra unrelated v4 subnet alongside the desired one is unusual but
// not a reason to tear down the network from a per-pod CNI call. We
// only require the desired v4 prefix to be present.
func TestNetworkNeedsRecreate_ExtraIPv4Subnet_NoRecreate(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	existing := nwInfo([]HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		{AddressPrefix: "172.16.0.0/24", GatewayAddress: "172.16.0.1"},
	})
	if networkNeedsRecreate(existing, subV4, nil, "", "") {
		t.Error("an extra unrelated v4 subnet alongside the desired one should not trigger recreate")
	}
}

func TestNetworkNeedsRecreate_DualStack_SubnetsReversed(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:db8::/122")
	existing := nwInfo([]HNSSubnet{
		{AddressPrefix: "2001:db8::/122", GatewayAddress: "2001:db8::1"},
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
	})
	if networkNeedsRecreate(existing, subV4, subV6, "", "") {
		t.Error("expected no recreate for matching dual-stack network with reversed subnet order")
	}
}

func TestNetworkNeedsRecreate_DualStack_V6GatewayChanged(t *testing.T) {
	subV4 := mustParseCIDR("10.3.16.0/26")
	subV6 := mustParseCIDR("2001:db8::40/122")
	existing := nwInfo([]HNSSubnet{
		{AddressPrefix: "10.3.16.0/26", GatewayAddress: "10.3.16.1"},
		{AddressPrefix: "2001:db8::40/122", GatewayAddress: "2001:db8::99"},
	})
	if !networkNeedsRecreate(existing, subV4, subV6, "", "") {
		t.Error("expected recreate when IPv6 gateway changed")
	}
}

// === ManagementIP / ManagementIPv6 mismatch tests ===
//
// The fork's IP autodetection can be re-pointed at runtime (e.g. switch
// from GUA to a stable ULA for BGP across DHCPv6-PD prefix rotations).
// When the autodetected IP no longer matches the HNS L2Bridge's pinned
// ManagementIP/v6, NDP for the new address is silently dropped by VFP.
// networkNeedsRecreate must detect this and trigger a recreate.

func TestNetworkNeedsRecreate_ManagementIPv6Mismatch(t *testing.T) {
	subV4 := mustParseCIDR("10.3.48.192/26")
	subV6 := mustParseCIDR("2001:5a8:4294:9c01:430d:9038:5fa1:d000/122")
	existing := &HNSNetworkInfo{
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.48.192/26", GatewayAddress: "10.3.48.193"},
			{AddressPrefix: "2001:5a8:4294:9c01:430d:9038:5fa1:d000/122",
				GatewayAddress: "2001:5a8:4294:9c01:430d:9038:5fa1:d001"},
		},
		ManagementIP:   "10.2.0.3",
		ManagementIPv6: "2001:5a8:4294:9c00:1ac0:4dff:fe89:5194",
	}
	if !networkNeedsRecreate(existing, subV4, subV6, "10.2.0.3", "fd5a:8000:1:0:1ac0:4dff:fe89:5194") {
		t.Error("expected recreate when ManagementIPv6 differs from autodetected ULA")
	}
}

func TestNetworkNeedsRecreate_ManagementIPMismatch(t *testing.T) {
	subV4 := mustParseCIDR("10.3.48.192/26")
	existing := &HNSNetworkInfo{
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.48.192/26", GatewayAddress: "10.3.48.193"},
		},
		ManagementIP: "192.168.1.99",
	}
	if !networkNeedsRecreate(existing, subV4, nil, "10.2.0.3", "") {
		t.Error("expected recreate when ManagementIP differs from autodetected value")
	}
}

func TestNetworkNeedsRecreate_ManagementIPv6Match(t *testing.T) {
	subV4 := mustParseCIDR("10.3.48.192/26")
	subV6 := mustParseCIDR("2001:5a8:4294:9c01:430d:9038:5fa1:d000/122")
	existing := &HNSNetworkInfo{
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.48.192/26", GatewayAddress: "10.3.48.193"},
			{AddressPrefix: "2001:5a8:4294:9c01:430d:9038:5fa1:d000/122",
				GatewayAddress: "2001:5a8:4294:9c01:430d:9038:5fa1:d001"},
		},
		ManagementIP:   "10.2.0.3",
		ManagementIPv6: "fd5a:8000:1:0:1ac0:4dff:fe89:5194",
	}
	if networkNeedsRecreate(existing, subV4, subV6, "10.2.0.3", "fd5a:8000:1:0:1ac0:4dff:fe89:5194") {
		t.Error("expected no recreate when ManagementIP/v6 match autodetected values")
	}
}

// When the caller can't query ManagementIPv6 (older HNS where the field
// isn't reported, or syscall failure), existing.ManagementIPv6 is "".
// We must not trigger recreate based on that — it's a "don't know"
// signal, not a confirmed mismatch.
func TestNetworkNeedsRecreate_ManagementIPv6Unknown_NoRecreate(t *testing.T) {
	subV4 := mustParseCIDR("10.3.48.192/26")
	subV6 := mustParseCIDR("2001:5a8:4294:9c01:430d:9038:5fa1:d000/122")
	existing := &HNSNetworkInfo{
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.48.192/26", GatewayAddress: "10.3.48.193"},
			{AddressPrefix: "2001:5a8:4294:9c01:430d:9038:5fa1:d000/122",
				GatewayAddress: "2001:5a8:4294:9c01:430d:9038:5fa1:d001"},
		},
		ManagementIP:   "10.2.0.3",
		ManagementIPv6: "",
	}
	if networkNeedsRecreate(existing, subV4, subV6, "10.2.0.3", "fd5a:8000:1:0:1ac0:4dff:fe89:5194") {
		t.Error("must not recreate when existing ManagementIPv6 is unknown (empty)")
	}
}

// Caller passing empty mgmtIP/v6 means "don't pin / legacy behaviour".
// Any existing value is acceptable in that case.
func TestNetworkNeedsRecreate_NoMgmtPin_NoRecreate(t *testing.T) {
	subV4 := mustParseCIDR("10.3.48.192/26")
	existing := &HNSNetworkInfo{
		Subnets: []HNSSubnet{
			{AddressPrefix: "10.3.48.192/26", GatewayAddress: "10.3.48.193"},
		},
		ManagementIP:   "10.2.0.3",
		ManagementIPv6: "2001:5a8:4294:9c00:1ac0:4dff:fe89:5194",
	}
	if networkNeedsRecreate(existing, subV4, nil, "", "") {
		t.Error("must not recreate when caller didn't pin ManagementIP/v6")
	}
}
