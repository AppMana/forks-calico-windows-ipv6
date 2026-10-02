package main

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/appmana/labcontainers/pkg/network/vyos"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

var vyosPortMACs = []string{"02:00:00:00:00:01", "02:00:00:00:00:02", "02:00:00:00:00:03"}

func vyosToRConfiguration(ports []string, ipv6 bool) []vyos.Command {
	set := func(path ...string) vyos.Command { return vyos.Command{Operation: "set", Path: path} }
	commands := []vyos.Command{
		set("system", "host-name", "qualification-tor"),
		set("interfaces", "ethernet", ports[0], "description", "Linux LAN"),
		set("interfaces", "ethernet", ports[1], "description", "Windows LAN"),
		set("interfaces", "bridge", "br0", "member", "interface", ports[0]),
		set("interfaces", "bridge", "br0", "member", "interface", ports[1]),
		set("interfaces", "bridge", "br0", "address", "192.0.2.1/24"),
		set("interfaces", "ethernet", ports[2], "address", "198.18.0.2/30"),
		set("protocols", "static", "route", "0.0.0.0/0", "next-hop", "198.18.0.1"),
		// Do not mask a Calico NAT regression by masquerading pod addresses.
		set("nat", "source", "rule", "100", "outbound-interface", "name", ports[2]),
		set("nat", "source", "rule", "100", "source", "address", "192.0.2.0/24"),
		set("nat", "source", "rule", "100", "translation", "address", "masquerade"),
		set("protocols", "bgp", "system-as", "64513"),
		set("protocols", "bgp", "parameters", "router-id", "192.0.2.1"),
		set("protocols", "bgp", "address-family", "ipv4-unicast", "network", "198.18.0.0/30"),
	}
	peers := map[string]string{"192.0.2.10": "ipv4-unicast", "192.0.2.20": "ipv4-unicast"}
	if ipv6 {
		commands = append(commands,
			set("interfaces", "bridge", "br0", "address", "fd00:10::1/64"),
			set("interfaces", "ethernet", ports[2], "address", "2001:db8:ffff::2/64"),
			set("protocols", "bgp", "address-family", "ipv6-unicast", "network", "2001:db8:ffff::/64"))
		peers["fd00:10::10"], peers["fd00:10::20"] = "ipv6-unicast", "ipv6-unicast"
	}
	var addresses []string
	for peer := range peers {
		addresses = append(addresses, peer)
	}
	sort.Strings(addresses)
	for _, peer := range addresses {
		family := peers[peer]
		commands = append(commands,
			set("protocols", "bgp", "neighbor", peer, "remote-as", "64512"),
			set("protocols", "bgp", "neighbor", peer, "address-family", family))
	}
	return commands
}

func vyosBGPPeers(ipv6 bool) []runtime.Object {
	addresses := []string{"192.0.2.1"}
	if ipv6 {
		addresses = append(addresses, "fd00:10::1")
	}
	var objects []runtime.Object
	for i, address := range addresses {
		objects = append(objects, &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "crd.projectcalico.org/v1", "kind": "BGPPeer",
			"metadata": map[string]interface{}{"name": fmt.Sprintf("qualification-vyos-%d", i)},
			"spec":     map[string]interface{}{"nodeSelector": "all()", "peerIP": address, "asNumber": int64(64513)},
		}})
	}
	return objects
}

// Require the actual BGP FIB entry through the owning node. A default route,
// static test route, or an indirect route through the other node cannot pass.
func requireVyOSBGPRoute(data []byte, pod, nextHop string) error {
	address, err := netip.ParseAddr(pod)
	if err != nil {
		return err
	}
	wantHop, err := netip.ParseAddr(nextHop)
	if err != nil {
		return err
	}
	var routes []struct {
		Dst      string `json:"dst"`
		Protocol string `json:"protocol"`
		Gateway  string `json:"gateway"`
	}
	if err := json.Unmarshal(data, &routes); err != nil {
		return err
	}
	best, matched := -1, false
	for _, route := range routes {
		prefix, err := netip.ParsePrefix(route.Dst)
		hop, hopErr := netip.ParseAddr(route.Gateway)
		if err == nil && prefix.Bits() > 0 && prefix.Contains(address) && prefix.Bits() >= best {
			valid := hopErr == nil && route.Protocol == "bgp" && hop == wantHop
			if prefix.Bits() > best {
				best, matched = prefix.Bits(), valid
			} else {
				matched = matched && valid
			}
		}
	}
	if matched {
		return nil
	}
	return fmt.Errorf("no BGP FIB route to %s through owning node %s: %s", pod, nextHop, data)
}

func TestVyOSRouteRequiresBGPAndOwningNextHop(t *testing.T) {
	for _, tc := range []struct {
		data, pod, hop string
		pass           bool
	}{
		{`[{"dst":"10.244.3.0/26","protocol":"bgp","gateway":"192.0.2.20"}]`, "10.244.3.5", "192.0.2.20", true},
		{`[{"dst":"2001:db8:100::/122","protocol":"bgp","gateway":"fd00:10::20"}]`, "2001:db8:100::5", "fd00:0010::0020", true},
		{`[{"dst":"10.244.3.0/26","protocol":"static","gateway":"192.0.2.20"}]`, "10.244.3.5", "192.0.2.20", false},
		{`[{"dst":"10.244.3.0/26","protocol":"bgp","gateway":"192.0.2.10"}]`, "10.244.3.5", "192.0.2.20", false},
		{`[{"dst":"0.0.0.0/0","protocol":"bgp","gateway":"192.0.2.20"}]`, "10.244.3.5", "192.0.2.20", false},
		{`[{"dst":"10.244.3.0/26","protocol":"bgp","gateway":"192.0.2.20"},{"dst":"10.244.3.5/32","protocol":"static","gateway":"192.0.2.10"}]`, "10.244.3.5", "192.0.2.20", false},
		{`[]`, "10.244.3.5", "192.0.2.20", false},
		{`invalid`, "10.244.3.5", "192.0.2.20", false},
	} {
		if err := requireVyOSBGPRoute([]byte(tc.data), tc.pod, tc.hop); (err == nil) != tc.pass {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestVyOSNativeConfigurationDeclaresPortsAndLimitsNAT(t *testing.T) {
	ports := []string{"eth9", "eth10", "eth8"}
	for _, dual := range []bool{false, true} {
		commands := vyosToRConfiguration(ports, dual)
		if !reflect.DeepEqual(commands, vyosToRConfiguration(ports, dual)) {
			t.Fatal("configuration must be deterministic")
		}
		joined := map[string]bool{}
		for _, command := range commands {
			if command.Operation != "set" {
				t.Fatal("unexpected destructive operation")
			}
			path := strings.Join(command.Path, " ")
			joined[path] = true
			if strings.HasPrefix(path, "protocols static route ") && path != "protocols static route 0.0.0.0/0 next-hop 198.18.0.1" {
				t.Fatalf("static route bypasses BGP: %s", path)
			}
			if strings.HasPrefix(path, "nat source rule 100 source address ") && path != "nat source rule 100 source address 192.0.2.0/24" {
				t.Fatal("NAT hides pod masquerade failures")
			}
		}
		for _, required := range []string{
			"interfaces ethernet eth9 description Linux LAN", "interfaces ethernet eth10 description Windows LAN",
			"interfaces bridge br0 member interface eth9", "interfaces bridge br0 member interface eth10",
			"interfaces ethernet eth8 address 198.18.0.2/30", "protocols bgp system-as 64513",
			"nat source rule 100 outbound-interface name eth8",
		} {
			if !joined[required] {
				t.Fatalf("missing native configuration %s", required)
			}
		}
		if joined["protocols bgp neighbor fd00:10::20 address-family ipv6-unicast"] != dual {
			t.Fatal("IPv6 must be explicit")
		}
		if len(vyosBGPPeers(dual)) != 1+map[bool]int{false: 0, true: 1}[dual] {
			t.Fatal("missing explicit BGP peer")
		}
	}
}
