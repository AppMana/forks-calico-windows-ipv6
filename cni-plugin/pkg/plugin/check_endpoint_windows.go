package plugin

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/Microsoft/hcsshim/hcn"
	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/plugins/pkg/hns"
	"github.com/sirupsen/logrus"

	"github.com/projectcalico/calico/cni-plugin/internal/pkg/utils/cri"
	"github.com/projectcalico/calico/cni-plugin/pkg/types"
)

func checkLocalEndpoint(args *skel.CmdArgs, conf types.NetConf, ips []string) error {
	return checkHCNEndpoint(args, conf, ips, hcn.GetEndpointByName)
}

func checkHCNEndpoint(args *skel.CmdArgs, conf types.NetConf, ips []string, get func(string) (*hcn.HostComputeEndpoint, error)) error {
	// Dockershim uses HNS v1 container attachment, not an HCN namespace.
	// Preserve its existing CHECK semantics rather than applying v2 rules.
	if cri.IsDockershimV1(args.Netns) {
		return nil
	}
	name := hns.ConstructEndpointName(args.ContainerID, args.Netns, conf.Name)
	endpoint, err := get(name)
	if hcn.IsNotFoundError(err) {
		return fmt.Errorf("sandbox %q endpoint %q is missing: %w", args.ContainerID, name, err)
	}
	if err != nil {
		// An HNS service/observation failure is not proof of endpoint loss.
		// Avoid restarting every workload during a transient outage.
		logrus.WithError(err).Warn("Unable to inspect Windows endpoint during CNI CHECK")
		return nil
	}
	if endpoint == nil {
		return fmt.Errorf("sandbox %q endpoint lookup returned no endpoint", args.ContainerID)
	}
	if endpoint.HostComputeNamespace == "" || !strings.EqualFold(endpoint.HostComputeNamespace, args.Netns) {
		return fmt.Errorf("sandbox %q endpoint %q is not attached to namespace %q", args.ContainerID, name, args.Netns)
	}
	for _, cidr := range ips {
		want, err := netip.ParsePrefix(cidr)
		if err != nil {
			return fmt.Errorf("invalid cached endpoint IP %q: %w", cidr, err)
		}
		found := false
		for _, config := range endpoint.IpConfigurations {
			actual, err := netip.ParseAddr(config.IpAddress)
			if err == nil && actual.Unmap() == want.Addr().Unmap() {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("sandbox %q endpoint %q does not have cached IP %s", args.ContainerID, name, want.Addr())
		}
	}
	return nil
}
