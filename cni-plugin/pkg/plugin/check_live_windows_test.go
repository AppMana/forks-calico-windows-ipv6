package plugin

import (
	"encoding/json"
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/Microsoft/hcsshim/hcn"
	"github.com/containernetworking/cni/pkg/skel"
	"github.com/stretchr/testify/require"
)

// Runs against native HCN in the isolated Windows VM, without creating,
// deleting or repairing any endpoint. An unavailable API must not mask a
// conclusively missing local endpoint during CHECK.
func TestLiveCheckRejectsMissingWindowsEndpoint(t *testing.T) {
	if os.Getenv("CALICO_LIVE_HNS_CHECK") != "1" {
		t.Skip("requires the Windows lab VM with native HNS")
	}
	_, err := hcn.GetEndpointByName("calico-check-missing-endpoint_Calico")
	require.True(t, hcn.IsNotFoundError(err), "native HCN must confirm absence, not fail observation: %v", err)
	args := &skel.CmdArgs{
		ContainerID: "calico-check-missing-endpoint",
		Netns:       "11111111-2222-3333-4444-555555555555",
		IfName:      "eth0",
		// Invalid datastore makes the existing API-unavailable fail-open
		// path deterministic. Local endpoint validation must precede it.
		StdinData: []byte(`{"cniVersion":"1.0.0","name":"Calico","type":"calico","datastore_type":"unavailable-test-datastore","prevResult":{"cniVersion":"1.0.0","ips":[{"address":"10.244.163.27/32"}]}}`),
	}
	err = cmdCheck(args)
	require.ErrorContains(t, err, "endpoint")
}

func TestLiveCheckAcceptsAttachedWindowsEndpoints(t *testing.T) {
	if os.Getenv("CALICO_LIVE_HNS_CHECK") != "1" {
		t.Skip("requires the Windows lab VM with native HNS")
	}
	endpoints, err := hcn.ListEndpoints()
	require.NoError(t, err)
	checked := 0
	for _, endpoint := range endpoints {
		if !strings.HasSuffix(endpoint.Name, "_Calico") || endpoint.HostComputeNamespace == "" {
			continue
		}
		var ips []map[string]string
		for _, config := range endpoint.IpConfigurations {
			ip, err := netip.ParseAddr(config.IpAddress)
			require.NoError(t, err)
			ips = append(ips, map[string]string{"address": netip.PrefixFrom(ip, ip.BitLen()).String()})
		}
		require.NotEmpty(t, ips)
		conf, err := json.Marshal(map[string]any{
			"cniVersion": "1.0.0", "name": "Calico", "type": "calico", "datastore_type": "unavailable-test-datastore",
			"prevResult": map[string]any{"cniVersion": "1.0.0", "ips": ips},
		})
		require.NoError(t, err)
		require.NoError(t, cmdCheck(&skel.CmdArgs{ContainerID: strings.TrimSuffix(endpoint.Name, "_Calico"), Netns: endpoint.HostComputeNamespace, IfName: "eth0", StdinData: conf}))
		checked++
	}
	require.Positive(t, checked, "live qualification must check at least one actual attached workload")
	t.Logf("validated %d native attached workload endpoints", checked)
}
