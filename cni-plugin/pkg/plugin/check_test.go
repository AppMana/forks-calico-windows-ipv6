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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/containernetworking/cni/pkg/skel"
	api "github.com/projectcalico/api/pkg/apis/projectcalico/v3"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/projectcalico/calico/cni-plugin/internal/pkg/utils"
	"github.com/projectcalico/calico/cni-plugin/pkg/types"
	internalapi "github.com/projectcalico/calico/libcalico-go/lib/apis/internalapi"
)

// Kubernetes retains the old Pod.status IPs until a replacement sandbox becomes
// ready. CHECK must validate the runtime's cached ADD result for this sandbox,
// otherwise the replacement is rejected before its new status can be published.
func TestCheckUsesSandboxCachedResult(t *testing.T) {
	for _, tc := range []struct {
		name, result, statusIP string
		wantError              bool
	}{
		{"replacement with stale pod status", `{"cniVersion":"1.0.0","ips":[{"address":"10.3.7.10/32"},{"address":"2001:db8:2::10/128"}]}`, "2001:db8:1::10", false},
		{"old sandbox with fresh pod status", `{"cniVersion":"1.0.0","ips":[{"address":"2001:db8:1::10/128"}]}`, "2001:db8:2::10", true},
		{"empty cached result", `{"cniVersion":"1.0.0"}`, "2001:db8:2::10", true},
		{"malformed cached address", `{"cniVersion":"1.0.0","ips":[{"address":"broken"}]}`, "2001:db8:2::10", true},
		{"null cached result", `null`, "2001:db8:2::10", true},
		{"null cached IP", `{"cniVersion":"1.0.0","ips":[null]}`, "2001:db8:2::10", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var podReads, poolReads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api":
					_ = json.NewEncoder(w).Encode(&metav1.APIVersions{Versions: []string{"v1"}})
				case "/apis":
					_ = json.NewEncoder(w).Encode(&metav1.APIGroupList{})
				case "/apis/crd.projectcalico.org/v1/ippools":
					poolReads.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "crd.projectcalico.org/v1", "kind": "IPPoolList", "items": []any{
						map[string]any{"metadata": map[string]string{"name": "v4"}, "spec": map[string]any{"cidr": "10.3.0.0/16"}},
						map[string]any{"metadata": map[string]string{"name": "old"}, "spec": map[string]any{"cidr": "2001:db8:1::/64", "disabled": true}},
						map[string]any{"metadata": map[string]string{"name": "new"}, "spec": map[string]any{"cidr": "2001:db8:2::/64"}},
					}})
				case "/api/v1/namespaces/test/pods/pod":
					podReads.Add(1)
					_ = json.NewEncoder(w).Encode(&corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "test", Annotations: map[string]string{"cni.projectcalico.org/podIPs": "2001:db8:2::10/128"}}, Spec: corev1.PodSpec{NodeName: "node"}, Status: corev1.PodStatus{PodIPs: []corev1.PodIP{{IP: tc.statusIP}}}})
				default:
					t.Errorf("unexpected API request: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
			require.NoError(t, clientcmd.WriteToFile(clientcmdapi.Config{
				Clusters: map[string]*clientcmdapi.Cluster{"test": {Server: server.URL}}, AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {}},
				Contexts: map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test"}}, CurrentContext: "test",
			}, kubeconfig))
			t.Setenv("DATASTORE_TYPE", "kubernetes")
			t.Setenv("KUBECONFIG", kubeconfig)
			conf, err := json.Marshal(map[string]any{"cniVersion": "1.0.0", "name": "test", "type": "calico", "nodename": "node", "datastore_type": "kubernetes", "kubernetes": map[string]string{"kubeconfig": kubeconfig}, "prevResult": json.RawMessage(tc.result)})
			require.NoError(t, err)
			err = cmdCheckWithLocalEndpointCheck(&skel.CmdArgs{ContainerID: "replacement-sandbox", IfName: "eth0", Args: "K8S_POD_NAMESPACE=test;K8S_POD_NAME=pod;K8S_POD_INFRA_CONTAINER_ID=replacement-sandbox", StdinData: conf}, func(*skel.CmdArgs, types.NetConf, []string) error { return nil })
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Zero(t, podReads.Load(), "sandbox CHECK must not depend on eventually consistent pod status")
			if !tc.wantError {
				require.EqualValues(t, 1, poolReads.Load(), "one pool lookup on happy path")
			}
		})
	}
}

func TestSandboxIPsForCheckLegacyAndMalformed(t *testing.T) {
	ips, present, err := sandboxIPsForCheck([]byte(`{"cniVersion":"1.0.0"}`))
	require.NoError(t, err)
	require.False(t, present)
	require.Empty(t, ips)
	ips, present, err = sandboxIPsForCheck([]byte(`{"cniVersion":"0.4.0","prevResult":{"ips":[{"version":"6","address":"2001:db8:2::10/128"}]}}`))
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, []string{"2001:db8:2::10/128"}, ips)
	for _, input := range []string{
		`{"cniVersion":"1.0.0","prevResult":42}`,
		`{"cniVersion":"9.9.9","prevResult":{"ips":[{"address":"2001:db8:2::10/128"}]}}`,
		`{"cniVersion":"1.0.0","prevResult":{"ips":[{}]}}`,
	} {
		_, present, err := sandboxIPsForCheck([]byte(input))
		require.True(t, present)
		require.Error(t, err)
	}
}

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
			endpoint := &internalapi.WorkloadEndpoint{}
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

func TestCheckPoolBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ips   []string
		pools []api.IPPool
		fail  bool
	}{
		{"first IPv6 address", []string{"2001:db8:1::/128"}, []api.IPPool{{Spec: api.IPPoolSpec{CIDR: "2001:db8:1::/64"}}}, false},
		{"last IPv6 address", []string{"2001:db8:1:0:ffff:ffff:ffff:ffff/128"}, []api.IPPool{{Spec: api.IPPoolSpec{CIDR: "2001:db8:1::/64"}}}, false},
		{"adjacent prefix", []string{"2001:db8:1:1::/128"}, []api.IPPool{{Spec: api.IPPoolSpec{CIDR: "2001:db8:1::/64"}}}, true},
		{"no pools", []string{"2001:db8:1::1/128"}, nil, true},
		{"wrong family", []string{"2001:db8:1::1/128"}, []api.IPPool{{Spec: api.IPPoolSpec{CIDR: "10.0.0.0/8"}}}, true},
		{"invalid endpoint", []string{"bad/128"}, []api.IPPool{{Spec: api.IPPoolSpec{CIDR: "2001:db8:1::/64"}}}, true},
		{"invalid pool cannot authorize", []string{"2001:db8:1::1/128"}, []api.IPPool{{Spec: api.IPPoolSpec{CIDR: "bad"}}}, true},
		{"overlap still enabled", []string{"2001:db8:1::1/128"}, []api.IPPool{{Spec: api.IPPoolSpec{CIDR: "2001:db8:1::/64", Disabled: true}}, {Spec: api.IPPoolSpec{CIDR: "2001:db8::/32"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ipNetworksInEnabledPools("boundary", tc.ips, &api.IPPoolList{Items: tc.pools})
			if tc.fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	require.Error(t, workloadEndpointIPsInEnabledPools(nil, &api.IPPoolList{}))
	require.Error(t, workloadEndpointIPsInEnabledPools(&internalapi.WorkloadEndpoint{}, &api.IPPoolList{}))
	require.Error(t, ipNetworksInEnabledPools("nil pools", []string{"2001:db8::1/128"}, nil))
}

func TestCheckPoolRotationLifecycle(t *testing.T) {
	old := api.IPPool{Spec: api.IPPoolSpec{CIDR: "2001:db8:1::/64"}}
	next := api.IPPool{Spec: api.IPPoolSpec{CIDR: "2001:db8:2::/64"}}
	oldIP, newIP := []string{"2001:db8:1::10/128"}, []string{"2001:db8:2::10/128"}
	require.NoError(t, ipNetworksInEnabledPools("old", oldIP, &api.IPPoolList{Items: []api.IPPool{old}}))
	// Adding a new pool alone must not invalidate a still-enabled old pool.
	pools := &api.IPPoolList{Items: []api.IPPool{old, next}}
	require.NoError(t, ipNetworksInEnabledPools("overlap", oldIP, pools))
	pools.Items[0].Spec.Disabled = true
	require.Error(t, ipNetworksInEnabledPools("stale", oldIP, pools))
	require.NoError(t, ipNetworksInEnabledPools("replacement", newIP, pools))
}

func TestCheckKubernetesAPIBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		ips        []corev1.PodIP
		annotation string
		fail       bool
	}{
		{"healthy", http.StatusOK, []corev1.PodIP{{IP: "2001:db8:2::10"}}, "", false},
		{"stale", http.StatusOK, []corev1.PodIP{{IP: "2001:db8:1::10"}}, "", true},
		{"annotation fallback", http.StatusOK, nil, "2001:db8:2::10/128", false},
		{"stale annotation with fresh status", http.StatusOK, []corev1.PodIP{{IP: "2001:db8:2::10"}}, "2001:db8:1::10/128", true},
		{"no recorded IP", http.StatusOK, nil, "", true},
		{"API unavailable must not invalidate", http.StatusServiceUnavailable, nil, "", false},
		{"API forbidden must not invalidate", http.StatusForbidden, nil, "", false},
		{"pod gone must not invalidate", http.StatusNotFound, nil, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requested := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case requested <- r.Method + " " + r.URL.Path:
				default:
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if tc.status != http.StatusOK {
					_ = json.NewEncoder(w).Encode(&metav1.Status{Status: "Failure", Code: int32(tc.status)})
					return
				}
				_ = json.NewEncoder(w).Encode(&corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "test", Annotations: map[string]string{"cni.projectcalico.org/podIPs": tc.annotation}}, Status: corev1.PodStatus{PodIPs: tc.ips}})
			}))
			defer server.Close()
			ids := &utils.WEPIdentifiers{}
			ids.Orchestrator, ids.Pod, ids.Namespace = "k8s", "pod", "test"
			kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
			require.NoError(t, clientcmd.WriteToFile(clientcmdapi.Config{
				Clusters:  map[string]*clientcmdapi.Cluster{"test": {Server: server.URL}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {}},
				Contexts:  map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test"}}, CurrentContext: "test",
			}, kubeconfig))
			conf := types.NetConf{Kubernetes: types.Kubernetes{Kubeconfig: kubeconfig}}
			pools := &api.IPPoolList{Items: []api.IPPool{{Spec: api.IPPoolSpec{CIDR: "2001:db8:2::/64"}}}}
			err := checkKubernetesPodIPsInEnabledPools(conf, ids, pools)
			if tc.fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			select {
			case path := <-requested:
				require.Equal(t, "GET /api/v1/namespaces/test/pods/pod", path)
			default:
				t.Fatal("CHECK never queried the API")
			}
		})
	}
}
