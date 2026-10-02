package template

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	texttemplate "text/template"

	"github.com/kelseyhightower/memkv"
	"github.com/projectcalico/calico/confd/pkg/backends"
	"github.com/projectcalico/calico/confd/pkg/backends/types"
)

type windowsPeeringStore struct{ backends.StoreClient }

func (windowsPeeringStore) GetBirdBGPConfig(int) (*types.BirdBGPConfig, error) {
	return &types.BirdBGPConfig{NodeName: "windows", RouterID: "192.0.2.20"}, nil
}

// Execute the shipped template with the real confd and memkv template helpers.
// Mesh-only IPv6 tests cannot detect omission of configured external BGPPeers.
func TestWindowsTemplateExternalIPv6Peers(t *testing.T) {
	for _, scope := range []string{"global", "node"} {
		for _, mesh := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mesh=%t", scope, mesh), func(t *testing.T) {
				store := memkv.New()
				for key, value := range map[string]string{
					"/host/windows/ip_addr_v4": "192.0.2.20",
					"/host/windows/ip_addr_v6": "fd00:10::20",
					"/host/linux/ip_addr_v4":   "192.0.2.10",
					"/host/linux/ip_addr_v6":   "fd00:10::10",
					"/global/as_num":           "64512",
					"/global/node_mesh":        fmt.Sprintf(`{"enabled":%t}`, mesh),
				} {
					store.Set(key, value)
				}
				prefix, name := "/global", "Global"
				if scope == "node" {
					prefix, name = "/host/windows", "Node"
				}
				store.Set(prefix+"/peer_v4/192.0.2.1", `{"ip":"192.0.2.1","as_num":64513,"keep_next_hop":false}`)
				store.Set(prefix+"/peer_v6/fd00:10::1", `{"ip":"fd00:10::1","as_num":64513,"keep_next_hop":true}`)
				functions := newFuncMap()
				addFuncs(functions, store.FuncMap)
				addCalicoFuncs(functions)
				tmpl, err := texttemplate.New("peerings.ps1.template").Funcs(functions).ParseFiles("../../../windows-packaging/templates/peerings.ps1.template")
				if err != nil {
					t.Fatal(err)
				}
				var output bytes.Buffer
				if err := tmpl.Execute(&output, windowsPeeringStore{}); err != nil {
					t.Fatal(err)
				}
				for _, want := range []string{
					fmt.Sprintf(`@{ Name = "%s_192_0_2_1"; IP = "192.0.2.1"; AS = 64513; KeepOriginalNextHop = $false }`, name),
					fmt.Sprintf(`@{ Name = "%s6_fd00_10__1"; IP = "fd00:10::1"; LocalIP = "fd00:10::20"; AS = 64513; KeepOriginalNextHop = $true }`, name),
				} {
					if !strings.Contains(output.String(), want) {
						t.Errorf("missing configured peer %s\nrendered:\n%s", want, output.String())
					}
				}
			})
		}
	}
}
