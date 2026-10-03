package plugin

import (
	"errors"
	"testing"

	"github.com/Microsoft/hcsshim/hcn"
	"github.com/containernetworking/cni/pkg/skel"
	"github.com/stretchr/testify/require"

	"github.com/projectcalico/calico/cni-plugin/pkg/types"
)

func TestCheckHCNEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name, namespace, ip, netns string
		lookupErr                  error
		nilEndpoint, wantError     bool
	}{
		{name: "attached cached IP", namespace: "ABC", ip: "10.0.0.2", netns: "abc"},
		{name: "missing endpoint", lookupErr: hcn.EndpointNotFoundError{EndpointName: "sandbox_Calico"}, wantError: true},
		{name: "wrapped missing endpoint", lookupErr: errors.Join(errors.New("lookup"), hcn.EndpointNotFoundError{}), wantError: true},
		{name: "transient HNS observation", lookupErr: errors.New("HNS service unavailable")},
		{name: "detached endpoint", ip: "10.0.0.2", netns: "abc", wantError: true},
		{name: "wrong namespace", namespace: "other", ip: "10.0.0.2", netns: "abc", wantError: true},
		{name: "wrong IP", namespace: "abc", ip: "10.0.0.3", netns: "abc", wantError: true},
		{name: "nil endpoint", nilEndpoint: true, wantError: true},
		{name: "legacy dockershim", netns: "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := checkHCNEndpoint(&skel.CmdArgs{ContainerID: "sandbox", Netns: tc.netns}, types.NetConf{Name: "Calico"}, []string{"10.0.0.2/32"}, func(name string) (*hcn.HostComputeEndpoint, error) {
				calls++
				require.Equal(t, "sandbox_Calico", name)
				if tc.nilEndpoint || tc.lookupErr != nil {
					return nil, tc.lookupErr
				}
				return &hcn.HostComputeEndpoint{HostComputeNamespace: tc.namespace, IpConfigurations: []hcn.IpConfig{{IpAddress: tc.ip}}}, nil
			})
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if tc.netns == "none" {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls, "one native lookup per CHECK")
			}
		})
	}
}

func TestCheckHCNEndpointRequiresBothCachedFamilies(t *testing.T) {
	args := &skel.CmdArgs{ContainerID: "sandbox", Netns: "abc"}
	endpoint := &hcn.HostComputeEndpoint{HostComputeNamespace: "abc", IpConfigurations: []hcn.IpConfig{{IpAddress: "10.0.0.2"}}}
	get := func(string) (*hcn.HostComputeEndpoint, error) { return endpoint, nil }
	ips := []string{"10.0.0.2/32", "2001:db8::2/128"}
	require.ErrorContains(t, checkHCNEndpoint(args, types.NetConf{}, ips, get), "2001:db8::2")
	endpoint.IpConfigurations = append(endpoint.IpConfigurations, hcn.IpConfig{IpAddress: "2001:db8:0:0::2"})
	require.NoError(t, checkHCNEndpoint(args, types.NetConf{}, ips, get))
}
