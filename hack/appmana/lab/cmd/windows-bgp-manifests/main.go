// Render native IPv4 Windows BGP resources for a declarative deployment or an
// explicit k0sctl hook. This command never contacts or modifies a cluster.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/projectcalico/calico/windows-adapter/pkg/deploy"
)

func main() {
	var o deploy.WindowsBGPOptions
	flag.StringVar(&o.NodeImage, "node-image", "", "immutable Calico Windows node image reference")
	flag.StringVar(&o.APIHost, "api-host", "", "reachable Kubernetes API IP")
	flag.StringVar(&o.APIPort, "api-port", "", "Kubernetes API port")
	flag.StringVar(&o.ServiceCIDR, "service-cidr", "", "IPv4 Kubernetes Service CIDR")
	flag.StringVar(&o.DNSAddress, "dns-address", "", "cluster DNS Service IP")
	flag.StringVar(&o.AutodetectionMethod, "autodetection-method", "", "explicit Calico IP autodetection method")
	flag.BoolVar(&o.Offline, "offline", false, "require the digest-pinned image to be preloaded")
	flag.Parse()
	plan, err := deploy.NewPlan(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(plan.Manifest()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
