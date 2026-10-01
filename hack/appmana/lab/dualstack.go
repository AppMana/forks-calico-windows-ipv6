package main

import (
	native "github.com/k0sproject/k0s/pkg/apis/k0s/v1beta1"
	"os"
)

const qualificationIPv6Autodetection = "cidr=fd00:10::/64"

// Prefix rotation stays inside the cluster's pod supernet. Individual /64
// IPPools can change without changing node addresses or API transport.
func configureQualificationIPv6(config *native.ClusterConfig) {
	if os.Getenv("LABCONTAINERS_KUBERNETES_IPV6") != "1" {
		return
	}
	config.Spec.Network.DualStack = native.DualStack{
		Enabled: true, IPv6PodCIDR: "2001:db8:100::/56", IPv6ServiceCIDR: "fd00:96::/108",
	}
	config.Spec.Network.Calico.IPv6AutodetectionMethod = qualificationIPv6Autodetection
}
