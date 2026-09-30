// Render native IPv4 Windows BGP resources for a declarative deployment or an
// explicit k0sctl hook. This command never contacts or modifies a cluster.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/projectcalico/calico/hack/appmana/lab/deploy"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func main() {
	var o deploy.WindowsBGPOptions
	flag.StringVar(&o.NodeImage, "node-image", "", "immutable Calico Windows node image reference")
	flag.StringVar(&o.APIHost, "api-host", "", "reachable Kubernetes API IP")
	flag.StringVar(&o.APIPort, "api-port", "", "Kubernetes API port")
	flag.StringVar(&o.ServiceCIDR, "service-cidr", "", "IPv4 Kubernetes Service CIDR")
	flag.StringVar(&o.DNSAddress, "dns-address", "", "cluster DNS Service IP")
	flag.StringVar(&o.AutodetectionMethod, "autodetection-method", "", "explicit Calico IP autodetection method")
	flag.Parse()
	objects, err := deploy.WindowsBGP(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	list := &meta.List{TypeMeta: meta.TypeMeta{APIVersion: "v1", Kind: "List"}}
	for _, object := range objects {
		list.Items = append(list.Items, runtime.RawExtension{Object: object})
	}
	if err := json.NewEncoder(os.Stdout).Encode(list); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
