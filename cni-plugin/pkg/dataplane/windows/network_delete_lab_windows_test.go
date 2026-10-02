package windows

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Microsoft/hcsshim/hcn"
)

// This reproduces network-recreation cleanup without Kubernetes, IPAM, a
// physical adapter, or a container image. Only newly-created private-network
// resources are touched; opt-in is for a disposable Windows VM exclusively.
func TestLabNetworkDeleteRemovesNamespaceReferences(t *testing.T) {
	if os.Getenv("LABCONTAINERS_PRIVATE_HCN_REPRO") != "1" {
		t.Skip("requires an explicitly isolated Windows lab")
	}
	name := fmt.Sprintf("lc-delete-repro-%d", time.Now().UnixNano())
	network, err := (&hcn.HostComputeNetwork{
		Name: name, Type: hcn.Private, SchemaVersion: hcn.SchemaVersion{Major: 2},
		// HCN requires subnet gateway metadata even for a Private switch.
		// This gateway has no uplink or host routing configuration.
		Ipams: []hcn.Ipam{{Type: "Static", Subnets: []hcn.Subnet{{
			IpAddressPrefix: "198.18.231.0/24",
			Routes:          []hcn.Route{{DestinationPrefix: "0.0.0.0/0", NextHop: "198.18.231.1"}},
		}}}},
	}).Create()
	if err != nil {
		t.Fatal("create isolated private network:", err)
	}
	t.Cleanup(func() {
		if _, err := hcn.GetNetworkByID(network.Id); hcn.IsNotFoundError(err) {
			return
		}
		if err := network.Delete(); err != nil && !hcn.IsNotFoundError(err) {
			t.Error("private network cleanup:", err)
		}
	})
	ns, err := hcn.NewNamespace(hcn.NamespaceTypeHost).Create()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := hcn.GetNamespaceByID(ns.Id); hcn.IsNotFoundError(err) {
			return
		}
		if err := ns.Delete(); err != nil && !hcn.IsNotFoundError(err) {
			t.Error("namespace cleanup:", err)
		}
	})
	ep, err := (&hcn.HostComputeEndpoint{
		Name: name, HostComputeNetwork: network.Id,
		SchemaVersion:    hcn.SchemaVersion{Major: 2},
		IpConfigurations: []hcn.IpConfig{{IpAddress: "198.18.231.4", PrefixLength: 24}},
	}).Create()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Cleanup is not the assertion: results are checked before this runs.
		if _, err := hcn.GetEndpointByID(ep.Id); hcn.IsNotFoundError(err) {
			return
		}
		_ = ep.NamespaceDetach(ns.Id)
		if err := ep.Delete(); err != nil && !hcn.IsNotFoundError(err) {
			t.Error("endpoint cleanup:", err)
		}
	})
	if err := ep.NamespaceAttach(ns.Id); err != nil {
		t.Fatal(err)
	}
	t.Logf("owned private network=%s namespace=%s endpoint=%s", network.Id, ns.Id, ep.Id)
	api := &realHNS{}
	if err := api.Delete(&HNSNetworkInfo{Id: network.Id, Name: name, Type: "Private"}); err != nil {
		t.Fatal("production network deletion:", err)
	}
	ids, err := hcn.GetNamespaceEndpointIds(ns.Id)
	if err != nil || len(ids) != 0 {
		t.Errorf("deleted network left namespace endpoint references: %v (error %v)", ids, err)
	}
	if err := ns.Delete(); err != nil {
		t.Fatal("namespace must remain removable after network deletion:", err)
	}
}
