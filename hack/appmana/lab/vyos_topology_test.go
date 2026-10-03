package main

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/shlex"
	"github.com/srl-labs/containerlab/core"
	"github.com/srl-labs/containerlab/links"
	"github.com/srl-labs/containerlab/types"
)

func TestVyOSWANConfigurationBelongsToContainerStartup(t *testing.T) {
	cfg := qualificationVyOSTopology(&types.NodeDefinition{NetworkMode: "none"}, &types.NodeDefinition{NetworkMode: "none"}, "wan:verified", "vyos:verified")
	wan := cfg.Topology.Nodes["gateway"]
	argv, err := shlex.Split(wan.Cmd) // Same tokenizer as Containerlab's Docker runtime.
	if err != nil || len(argv) != 2 || argv[0] != "-ec" || argv[1] != vyosWANStartup {
		t.Fatalf("startup script changed during native command tokenization: %q: %v", argv, err)
	}
	check := exec.Command("sh", "-n")
	check.Stdin = strings.NewReader(argv[1])
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("invalid startup shell syntax: %s: %v", output, err)
	}
	if wan.Entrypoint != "/bin/sh" {
		t.Fatal("WAN addressing must run on every container start, not only initial test setup")
	}
	for _, required := range []string{"198.18.0.1/30", "2001:db8:ffff::1/64", "10.244.0.0/16", "2001:db8:100::/56", "iptables -P FORWARD DROP", "exec sleep infinity"} {
		if !strings.Contains(wan.Cmd, required) {
			t.Fatalf("WAN startup lacks %q", required)
		}
	}
}

// qualificationVyOSTopology retains the existing explicit WAN edge but places
// a real VyOS VM in the routing path. No QEMU user network or management NIC is
// permitted on the router or either workload VM. The external gateway is only
// the WAN attachment, not a substitute for the VyOS control/data plane.
func qualificationVyOSTopology(linux, windows *types.NodeDefinition, wanImage, vyosImage string) *core.Config {
	config := qualificationTopology(linux, windows, wanImage)
	// Container network namespaces are recreated on restart. Configure the
	// declared WAN in PID 1, not in a one-time caller-side exec or verifier.
	config.Topology.Nodes["gateway"].Entrypoint = "/bin/sh"
	config.Topology.Nodes["gateway"].Cmd = "-ec '" + strings.ReplaceAll(vyosWANStartup, "'", "'\"'\"'") + "'"
	config.Topology.Nodes["tor"] = &types.NodeDefinition{Kind: "generic_vm", Image: vyosImage, ImagePullPolicy: "Never", NetworkMode: "none"}
	config.Topology.Links = []*links.LinkDefinition{
		{Link: &links.LinkVEthRaw{Endpoints: []*links.EndpointRaw{{Node: "linux", Iface: "eth1"}, {Node: "tor", Iface: "eth1", MAC: vyosPortMACs[0]}}}},
		{Link: &links.LinkVEthRaw{Endpoints: []*links.EndpointRaw{{Node: "windows", Iface: "eth1"}, {Node: "tor", Iface: "eth2", MAC: vyosPortMACs[1]}}}},
		{Link: &links.LinkVEthRaw{Endpoints: []*links.EndpointRaw{{Node: "tor", Iface: "eth3", MAC: vyosPortMACs[2]}, {Node: "gateway", Iface: "eth1"}}}},
	}
	return config
}

const vyosWANStartup = `
i=0
until ip link show eth1 >/dev/null 2>&1; do
  i=$((i+1)); test "$i" -lt 60 || exit 1
  sleep 1
done
ip link set eth1 up
ip addr add 198.18.0.1/30 dev eth1
ip -6 addr add 2001:db8:ffff::1/64 dev eth1
ip route add 10.244.0.0/16 via 198.18.0.2
ip -6 route add 2001:db8:100::/56 via 2001:db8:ffff::2
iptables -P FORWARD DROP
iptables -A FORWARD -i eth0 -o eth1 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -A FORWARD -i eth1 -o eth0 -s 198.18.0.2/32 -d 1.1.1.1/32 -p tcp --dport 443 -j ACCEPT
iptables -t nat -A POSTROUTING -s 198.18.0.2/32 -o eth0 -j MASQUERADE
exec sleep infinity
`

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
