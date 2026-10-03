package main

import (
	"github.com/srl-labs/containerlab/types"
	"testing"
)

func TestQualificationUsesExplicitWANSubnet(t *testing.T) {
	t.Setenv("LABCONTAINERS_WAN_SUBNET", "172.31.253.0/24")
	config := qualificationTopology(&types.NodeDefinition{NetworkMode: "none"}, &types.NodeDefinition{NetworkMode: "none"}, "wan:verified")
	if config.Mgmt.IPv4Subnet != "172.31.253.0/24" {
		t.Fatal("explicit WAN subnet ignored")
	}
	if config.Topology.Nodes["linux"].NetworkMode != "none" || config.Topology.Nodes["windows"].NetworkMode != "none" {
		t.Fatal("WAN selection added VM management access")
	}
}

func TestUpgradeBaselineCannotQualifyPrefixRotations(t *testing.T) {
	for _, tc := range []struct {
		marker         string
		upgrade, valid bool
	}{
		{"PREFIX_ROTATION_COMPLETE", false, true},
		{"RUNTIME_WORKLOAD_BASELINE_COMPLETE", true, true},
		{"RUNTIME_WORKLOAD_BASELINE_COMPLETE", false, false},
		{"PREFIX_ROTATION_COMPLETE", true, false},
		{"SMOKE_COMPLETE", false, false},
		{"SMOKE_COMPLETE", true, false},
	} {
		if err := validateIPv6Consumer([]byte("pinned binary"), tc.marker, tc.upgrade); (err == nil) != tc.valid {
			t.Fatalf("marker=%s upgrade=%v: %v", tc.marker, tc.upgrade, err)
		}
		if err := validateIPv6Consumer(nil, tc.marker, tc.upgrade); err == nil {
			t.Fatal("accepted absent consumer")
		}
	}
}
