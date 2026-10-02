package main

import (
	matrix "github.com/appmana/labcontainers/pkg/kubernetes"
	k0s "github.com/appmana/labcontainers/pkg/kubernetes/k0s"
	native "github.com/k0sproject/k0s/pkg/apis/k0s/v1beta1"
	"k8s.io/kubernetes/pkg/controller/nodeipam/ipam/cidrset"
	"net"
	"strings"
	"testing"
)

func TestPrefixQualificationEnablesBothAddressFamilies(t *testing.T) {
	t.Setenv("LABCONTAINERS_KUBERNETES_IPV6", "1")
	o := stockBGPOptions()
	if o.IPv6AutodetectionMethod != "cidr=fd00:10::/64" {
		t.Fatal("prefix qualification disables IPv6 or chooses unstable host addresses")
	}
	config := &native.ClusterConfig{Spec: &native.ClusterSpec{Network: &native.Network{
		PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12", Calico: &native.Calico{},
	}}}
	if err := k0s.ConfigureNetwork(config, qualificationCandidate(matrix.CNICalicoBGP).Linux); err != nil {
		t.Fatal(err)
	}
	configureQualificationIPv6(config)
	// k0s defaults to /117; Kubernetes' IPv6 CIDR allocator permits at
	// most 16 bits between cluster and node masks. /56 -> /117 crashes
	// kube-controller-manager before it can issue worker bootstrap tokens.
	_, prefix, err := net.ParseCIDR(config.Spec.Network.DualStack.IPv6PodCIDR)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cidrset.NewCIDRSet(prefix, 117); err == nil {
		t.Fatal("negative control: k0s default should exceed the allocator range")
	}
	if config.Spec.ControllerManager == nil || config.Spec.ControllerManager.ExtraArgs["allocate-node-cidrs"] != "false" {
		t.Fatal("shared Calico specialization must match production: allocate-node-cidrs=false")
	}
	if config.Spec.ControllerManager.ExtraArgs["node-cidr-mask-size-ipv6"] != "" {
		t.Fatal("lab-only node mask workaround must not replace production's Calico IPAM policy")
	}
	if !config.Spec.Network.DualStack.Enabled || config.Spec.Network.DualStack.IPv6PodCIDR != "2001:db8:100::/56" || config.Spec.Network.DualStack.IPv6ServiceCIDR != "fd00:96::/108" {
		t.Fatal("native k0s dual-stack configuration is incomplete")
	}
	if config.Spec.Network.Calico.IPv6AutodetectionMethod != o.IPv6AutodetectionMethod {
		t.Fatal("Linux and Windows choose different host address policy")
	}
	if config.Spec.Network.PodCIDR != "10.244.0.0/16" || config.Spec.Network.ServiceCIDR != "10.96.0.0/12" {
		t.Fatal("IPv6 setup changed IPv4 ranges")
	}
	persistent, err := controllerNetworkConfig("02:00:00:00:00:10", false)
	if err != nil || !strings.Contains(persistent, "Address=fd00:10::10/64") || strings.Contains(persistent, "Destination=::/0") {
		t.Fatal("host ULA must persist without implicit IPv6 egress", err)
	}
	if !strings.Contains(strings.Join(stockBGPAdapterArgs(), " "), "--ipv6-autodetection-method="+o.IPv6AutodetectionMethod) {
		t.Fatal("adapter command dropped IPv6 policy")
	}
}

func TestIPv4QualificationRemainsUnchanged(t *testing.T) {
	t.Setenv("LABCONTAINERS_KUBERNETES_IPV6", "")
	c := &native.ClusterConfig{Spec: &native.ClusterSpec{Network: &native.Network{}}}
	configureQualificationIPv6(c)
	if c.Spec.Network.DualStack.Enabled || stockBGPOptions().IPv6AutodetectionMethod != "" {
		t.Fatal("enabled IPv6 implicitly")
	}
}
