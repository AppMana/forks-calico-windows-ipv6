package plugin

import (
	"os"
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
