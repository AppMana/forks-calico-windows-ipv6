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
	"net"
	"testing"

	apiv3 "github.com/projectcalico/api/pkg/apis/projectcalico/v3"
)

func TestFilterIPAMPools_IPv4Only(t *testing.T) {
	pools := []apiv3.IPPool{
		{Spec: apiv3.IPPoolSpec{CIDR: "10.3.0.0/16", NATOutgoing: true}},
		{Spec: apiv3.IPPoolSpec{CIDR: "10.4.0.0/16", NATOutgoing: true}},
	}
	podIP := net.ParseIP("10.3.16.5")

	cidrs, natOutgoing := filterIPAMPools(pools, podIP)
	if len(cidrs) != 2 {
		t.Errorf("expected 2 CIDRs, got %d", len(cidrs))
	}
	if !natOutgoing {
		t.Error("expected natOutgoing to be true")
	}
}

func TestFilterIPAMPools_IPv6Excluded(t *testing.T) {
	// IPv6 pools must be excluded from the returned CIDRs because HCN on
	// Windows rejects OutBoundNAT policies containing IPv6 CIDRs.
	pools := []apiv3.IPPool{
		{Spec: apiv3.IPPoolSpec{CIDR: "10.3.0.0/16", NATOutgoing: true}},
		{Spec: apiv3.IPPoolSpec{CIDR: "2001:db8::/48", NATOutgoing: true}},
	}
	podIP := net.ParseIP("10.3.16.5")

	cidrs, _ := filterIPAMPools(pools, podIP)
	if len(cidrs) != 1 {
		t.Errorf("expected 1 CIDR (IPv4 only), got %d", len(cidrs))
	}
	if cidrs[0].String() != "10.3.0.0/16" {
		t.Errorf("expected 10.3.0.0/16, got %s", cidrs[0].String())
	}
}

func TestFilterIPAMPools_IPv6Only_ReturnsNone(t *testing.T) {
	// If all pools are IPv6, no CIDRs should be returned.
	pools := []apiv3.IPPool{
		{Spec: apiv3.IPPoolSpec{CIDR: "2001:db8::/48", NATOutgoing: true}},
		{Spec: apiv3.IPPoolSpec{CIDR: "fd00:10:244::/64", NATOutgoing: false}},
	}
	podIP := net.ParseIP("2001:db8::5")

	cidrs, natOutgoing := filterIPAMPools(pools, podIP)
	if len(cidrs) != 0 {
		t.Errorf("expected 0 CIDRs for IPv6-only pools, got %d", len(cidrs))
	}
	// natOutgoing should remain the default (true) since no IPv4 pool
	// contained the pod IP.
	if !natOutgoing {
		t.Error("expected natOutgoing to be true (default)")
	}
}

func TestFilterIPAMPools_Mixed_OnlyIPv4Returned(t *testing.T) {
	pools := []apiv3.IPPool{
		{Spec: apiv3.IPPoolSpec{CIDR: "10.3.0.0/16", NATOutgoing: true}},
		{Spec: apiv3.IPPoolSpec{CIDR: "10.4.0.0/16", NATOutgoing: false}},
		{Spec: apiv3.IPPoolSpec{CIDR: "2001:db8::/48", NATOutgoing: true}},
		{Spec: apiv3.IPPoolSpec{CIDR: "fd00:10:244::/64", NATOutgoing: false}},
	}
	podIP := net.ParseIP("10.3.16.5")

	cidrs, _ := filterIPAMPools(pools, podIP)
	if len(cidrs) != 2 {
		t.Errorf("expected 2 IPv4 CIDRs, got %d", len(cidrs))
	}
	for _, c := range cidrs {
		if c.IP.To4() == nil {
			t.Errorf("expected only IPv4 CIDRs, got IPv6: %s", c.String())
		}
	}
}

func TestFilterIPAMPools_NATOutgoing_FromContainingPool(t *testing.T) {
	// natOutgoing should be determined from the pool that contains the pod IP.
	pools := []apiv3.IPPool{
		{Spec: apiv3.IPPoolSpec{CIDR: "10.3.0.0/16", NATOutgoing: false}},
		{Spec: apiv3.IPPoolSpec{CIDR: "10.4.0.0/16", NATOutgoing: true}},
	}
	podIP := net.ParseIP("10.3.16.5")

	_, natOutgoing := filterIPAMPools(pools, podIP)
	if natOutgoing {
		t.Error("expected natOutgoing=false from the 10.3.0.0/16 pool containing the pod IP")
	}
}

func TestFilterIPAMPools_NATOutgoing_DefaultTrueWhenPodNotInAnyPool(t *testing.T) {
	pools := []apiv3.IPPool{
		{Spec: apiv3.IPPoolSpec{CIDR: "10.3.0.0/16", NATOutgoing: false}},
	}
	podIP := net.ParseIP("192.168.1.5")

	_, natOutgoing := filterIPAMPools(pools, podIP)
	if !natOutgoing {
		t.Error("expected natOutgoing=true (default) when pod IP is not in any pool")
	}
}

func TestFilterIPAMPools_NATOutgoing_IPv6PoolIgnored(t *testing.T) {
	// Even if the pod IP is in an IPv6 pool, natOutgoing should use the
	// default (true) since IPv6 pools are filtered out.
	pools := []apiv3.IPPool{
		{Spec: apiv3.IPPoolSpec{CIDR: "10.3.0.0/16", NATOutgoing: true}},
		{Spec: apiv3.IPPoolSpec{CIDR: "2001:db8::/48", NATOutgoing: false}},
	}
	podIP := net.ParseIP("10.3.16.5")

	_, natOutgoing := filterIPAMPools(pools, podIP)
	if !natOutgoing {
		t.Error("expected natOutgoing=true from the IPv4 pool")
	}
}

func TestFilterIPAMPools_BadCIDR_Ignored(t *testing.T) {
	pools := []apiv3.IPPool{
		{Spec: apiv3.IPPoolSpec{CIDR: "not-a-cidr", NATOutgoing: true}},
		{Spec: apiv3.IPPoolSpec{CIDR: "10.3.0.0/16", NATOutgoing: true}},
	}
	podIP := net.ParseIP("10.3.16.5")

	cidrs, _ := filterIPAMPools(pools, podIP)
	if len(cidrs) != 1 {
		t.Errorf("expected 1 CIDR (bad CIDR skipped), got %d", len(cidrs))
	}
}

func TestFilterIPAMPools_Empty(t *testing.T) {
	cidrs, natOutgoing := filterIPAMPools(nil, net.ParseIP("10.3.16.5"))
	if len(cidrs) != 0 {
		t.Errorf("expected 0 CIDRs for nil pools, got %d", len(cidrs))
	}
	if !natOutgoing {
		t.Error("expected natOutgoing=true (default) for nil pools")
	}
}
