package main

import (
	"fmt"
	"testing"

	"github.com/srl-labs/containerlab/core"
	"github.com/srl-labs/containerlab/links"
	"github.com/srl-labs/containerlab/types"
)

// qualificationVyOSTopology retains the existing explicit WAN edge but places
// a real VyOS VM in the routing path. No QEMU user network or management NIC is
// permitted on the router or either workload VM. The external gateway is only
// the WAN attachment, not a substitute for the VyOS control/data plane.
func qualificationVyOSTopology(linux, windows *types.NodeDefinition, wanImage, vyosImage string) *core.Config {
	config := qualificationTopology(linux, windows, wanImage)
	config.Topology.Nodes["tor"] = &types.NodeDefinition{Kind: "generic_vm", Image: vyosImage, ImagePullPolicy: "Never", NetworkMode: "none"}
	config.Topology.Links = []*links.LinkDefinition{
		{Link: &links.LinkVEthRaw{Endpoints: []*links.EndpointRaw{{Node: "linux", Iface: "eth1"}, {Node: "tor", Iface: "eth1", MAC: vyosPortMACs[0]}}}},
		{Link: &links.LinkVEthRaw{Endpoints: []*links.EndpointRaw{{Node: "windows", Iface: "eth1"}, {Node: "tor", Iface: "eth2", MAC: vyosPortMACs[1]}}}},
		{Link: &links.LinkVEthRaw{Endpoints: []*links.EndpointRaw{{Node: "tor", Iface: "eth3", MAC: vyosPortMACs[2]}, {Node: "gateway", Iface: "eth1"}}}},
	}
	return config
}

func TestVyOSToRHasOnlyDeclaredLANAndWANInterfaces(t *testing.T) {
	cfg := qualificationVyOSTopology(&types.NodeDefinition{NetworkMode: "none"}, &types.NodeDefinition{NetworkMode: "none"}, "wan:verified", "vyos:verified")
	if len(cfg.Topology.Nodes) != 4 || len(cfg.Topology.Links) != 3 || cfg.Mgmt == nil {
		t.Fatal("expected two workload VMs, real ToR and explicit WAN attachment")
	}
	for _, name := range []string{"linux", "windows", "tor"} {
		if cfg.Topology.Nodes[name].NetworkMode != "none" {
			t.Fatalf("%s has implicit access", name)
		}
	}
	if cfg.Topology.Nodes["tor"].Kind != "generic_vm" {
		t.Fatal("VyOS must be a VM")
	}
	want := map[string]string{"linux:eth1": "tor:eth1", "windows:eth1": "tor:eth2", "tor:eth3": "gateway:eth1"}
	for _, link := range cfg.Topology.Links {
		ep := link.Link.(*links.LinkVEthRaw).Endpoints
		left, right := fmt.Sprintf("%s:%s", ep[0].Node, ep[0].Iface), fmt.Sprintf("%s:%s", ep[1].Node, ep[1].Iface)
		if len(ep) != 2 || want[left] != right {
			t.Fatalf("unexpected topology link: %v", ep)
		}
		for _, endpoint := range ep {
			if endpoint.Node == "tor" && endpoint.MAC == "" {
				t.Fatal("guest port identity must be explicit")
			}
		}
		delete(want, left)
	}
	if len(want) != 0 {
		t.Fatal("missing routed WAN link")
	}
}
