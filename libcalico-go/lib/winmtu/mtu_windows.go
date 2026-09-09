package winmtu

import (
	"context"
	"fmt"
	"os/exec"
	"reflect"
	"time"

	"github.com/Microsoft/hcsshim/hcn"
)

// ApplyEndpointMTU applies an upper bound once Windows has assigned the endpoint
// an isolated compartment. CNI ADD is too early on the tested Windows builds.
// Zero leaves platform defaults intact. No host interface is eligible.
func ApplyEndpointMTU(ctx context.Context, endpointID string, mtu int) error {
	if mtu == 0 {
		return nil
	}
	ep, err := hcn.GetEndpointByID(endpointID)
	if err != nil {
		return fmt.Errorf("reading MTU endpoint: %w", err)
	}
	ns, err := hcn.GetNamespaceByID(ep.HostComputeNamespace)
	if err != nil {
		return fmt.Errorf("reading MTU endpoint namespace: %w", err)
	}
	var addresses []string
	for _, ip := range ep.IpConfigurations {
		addresses = append(addresses, ip.IpAddress)
	}
	script, err := endpointMTUScript(ns.NamespaceId, addresses, mtu)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("applying endpoint MTU: %w: %s", err, output)
	}
	current, err := hcn.GetEndpointByID(endpointID)
	if err != nil {
		return fmt.Errorf("rechecking MTU endpoint: %w", err)
	}
	if current.HostComputeNamespace != ep.HostComputeNamespace || !reflect.DeepEqual(current.IpConfigurations, ep.IpConfigurations) {
		return fmt.Errorf("endpoint identity changed during MTU application")
	}
	return nil
}
