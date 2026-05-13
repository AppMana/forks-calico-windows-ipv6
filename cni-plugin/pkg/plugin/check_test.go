// Copyright (c) 2015-2024 Tigera, Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package plugin

import (
	"testing"

	api "github.com/projectcalico/api/pkg/apis/projectcalico/v3"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	libapi "github.com/projectcalico/calico/libcalico-go/lib/apis/v3"
)

func TestWorkloadEndpointIPsInEnabledPools(t *testing.T) {
	for _, test := range []struct {
		name        string
		ipNetworks  []string
		pools       []api.IPPool
		expectError bool
	}{
		{
			name:       "dual stack endpoint in current pools",
			ipNetworks: []string{"10.3.7.10/32", "2001:db8:2::10/128"},
			pools: []api.IPPool{
				{Spec: api.IPPoolSpec{CIDR: "10.3.0.0/16"}},
				{Spec: api.IPPoolSpec{CIDR: "2001:db8:2::/64"}},
			},
		},
		{
			name:       "stale IPv6 endpoint outside rotated pool",
			ipNetworks: []string{"10.3.7.10/32", "2001:db8:1::10/128"},
			pools: []api.IPPool{
				{Spec: api.IPPoolSpec{CIDR: "10.3.0.0/16"}},
				{Spec: api.IPPoolSpec{CIDR: "2001:db8:2::/64"}},
			},
			expectError: true,
		},
		{
			name:       "disabled old pool is ignored",
			ipNetworks: []string{"2001:db8:1::10/128"},
			pools: []api.IPPool{
				{Spec: api.IPPoolSpec{CIDR: "2001:db8:1::/64", Disabled: true}},
				{Spec: api.IPPoolSpec{CIDR: "2001:db8:2::/64"}},
			},
			expectError: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoint := &libapi.WorkloadEndpoint{}
			endpoint.Name = "node-k8s-pod-eth0"
			endpoint.Spec.IPNetworks = test.ipNetworks
			pools := &api.IPPoolList{Items: test.pools}

			err := workloadEndpointIPsInEnabledPools(endpoint, pools)
			if test.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestPodIPNetworksForCheckUsesStatusAndAnnotation(t *testing.T) {
	pod := &corev1.Pod{}
	pod.Annotations = map[string]string{
		"cni.projectcalico.org/podIPs": "10.3.7.10/32,2001:db8:old::99/128",
	}
	pod.Status.PodIPs = []corev1.PodIP{
		{IP: "10.3.7.10"},
		{IP: "2001:db8:2::10"},
	}

	require.Equal(t, []string{"10.3.7.10/32", "2001:db8:2::10/128", "2001:db8:old::99/128"}, podIPNetworksForCheck(pod))
}

func TestPodIPNetworksForCheckFallsBackToAnnotation(t *testing.T) {
	pod := &corev1.Pod{}
	pod.Annotations = map[string]string{
		"cni.projectcalico.org/podIPs": "10.3.7.10/32, 2001:db8:2::10/128",
	}

	require.Equal(t, []string{"10.3.7.10/32", "2001:db8:2::10/128"}, podIPNetworksForCheck(pod))
}

func TestKubernetesPodIPsInEnabledPoolsDetectsRotatedIPv6Pool(t *testing.T) {
	pools := &api.IPPoolList{Items: []api.IPPool{
		{Spec: api.IPPoolSpec{CIDR: "10.3.0.0/16"}},
		{Spec: api.IPPoolSpec{CIDR: "2001:db8:1::/64", Disabled: true}},
		{Spec: api.IPPoolSpec{CIDR: "2001:db8:2::/64"}},
	}}

	err := ipNetworksInEnabledPools(
		`pod "default/stale-ipv6"`,
		[]string{"10.3.7.10/32", "2001:db8:1::10/128"},
		pools,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside current enabled IPPools")
}
