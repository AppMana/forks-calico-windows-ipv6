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
	// k0s defaults to /117 node CIDRs. With this explicit /56 lab
	// supernet that exceeds Kubernetes' 16-bit IPv6 allocator limit.
	// Use k0s's supported native override, not a controller binary patch.
	if config.Spec.ControllerManager == nil {
		config.Spec.ControllerManager = &native.ControllerManagerSpec{}
	}
	if config.Spec.ControllerManager.ExtraArgs == nil {
		config.Spec.ControllerManager.ExtraArgs = map[string]string{}
	}
	config.Spec.ControllerManager.ExtraArgs["node-cidr-mask-size-ipv6"] = "64"
}
