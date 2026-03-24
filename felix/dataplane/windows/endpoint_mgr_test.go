// Copyright (c) 2024 Tigera, Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package windataplane

import (
	"net"
	"sort"
	"testing"

	"github.com/projectcalico/calico/felix/dataplane/windows/hns"
)

// --- extractUnicastAddrs tests ---

func TestExtractUnicastAddrs_IPv4Only(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("10.0.0.1"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("192.168.1.5"), Mask: net.CIDRMask(24, 32)},
	}
	result := extractUnicastAddrs(addrs)
	sort.Strings(result)
	expected := []string{"10.0.0.1/32", "192.168.1.5/32"}
	sort.Strings(expected)
	assertStringSlicesEqual(t, expected, result)
}

func TestExtractUnicastAddrs_IPv6Only(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("fd00::1"), Mask: net.CIDRMask(64, 128)},
		&net.IPNet{IP: net.ParseIP("2001:db8::5"), Mask: net.CIDRMask(48, 128)},
	}
	result := extractUnicastAddrs(addrs)
	sort.Strings(result)
	expected := []string{"2001:db8::5/128", "fd00::1/128"}
	sort.Strings(expected)
	assertStringSlicesEqual(t, expected, result)
}

func TestExtractUnicastAddrs_MixedV4V6(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("10.2.0.58"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("fd00:10:2::58"), Mask: net.CIDRMask(64, 128)},
		&net.IPAddr{IP: net.ParseIP("172.16.0.1")},
	}
	result := extractUnicastAddrs(addrs)
	if len(result) != 3 {
		t.Fatalf("expected 3 results, got %d: %v", len(result), result)
	}
	assertContains(t, result, "10.2.0.58/32")
	assertContains(t, result, "fd00:10:2::58/128")
	assertContains(t, result, "172.16.0.1/32")
}

func TestExtractUnicastAddrs_SkipsLoopback(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.ParseIP("::1"), Mask: net.CIDRMask(128, 128)},
		&net.IPNet{IP: net.ParseIP("10.0.0.1"), Mask: net.CIDRMask(24, 32)},
	}
	result := extractUnicastAddrs(addrs)
	assertStringSlicesEqual(t, []string{"10.0.0.1/32"}, result)
}

func TestExtractUnicastAddrs_SkipsLinkLocal(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("169.254.1.1"), Mask: net.CIDRMask(16, 32)},
		&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)},
		&net.IPNet{IP: net.ParseIP("fd00::1"), Mask: net.CIDRMask(64, 128)},
	}
	result := extractUnicastAddrs(addrs)
	assertStringSlicesEqual(t, []string{"fd00::1/128"}, result)
}

func TestExtractUnicastAddrs_NilInput(t *testing.T) {
	result := extractUnicastAddrs(nil)
	if len(result) != 0 {
		t.Errorf("expected empty result for nil input, got %v", result)
	}
}

// --- endpointManager cache tests ---

// newTestEndpointManager creates an endpointManager with a mock HNS API.
func newTestEndpointManager(mockHNS *hns.MockAPI) *endpointManager {
	return &endpointManager{
		hns:                 mockHNS,
		hnsNetworkRegexp:    defaultNetworkRegexp(),
		addressToEndpointId: make(map[string]string),
	}
}

func TestRefreshCache_IPv4OnlyEndpoints(t *testing.T) {
	mock := &hns.MockAPI{
		Endpoints: []hns.HNSEndpoint{
			{
				Id:                 "ep-1",
				IPAddress:          net.ParseIP("10.3.48.200"),
				VirtualNetworkName: "Calico",
				SharedContainers:   []string{"container-1"},
			},
			{
				Id:                 "ep-2",
				IPAddress:          net.ParseIP("10.3.48.201"),
				VirtualNetworkName: "Calico",
				SharedContainers:   []string{"container-2"},
			},
		},
	}
	m := newTestEndpointManager(mock)
	err := m.RefreshHnsEndpointCache(true)
	if err != nil {
		t.Fatalf("RefreshHnsEndpointCache failed: %v", err)
	}
	if len(m.addressToEndpointId) != 2 {
		t.Fatalf("expected 2 cache entries, got %d: %v", len(m.addressToEndpointId), m.addressToEndpointId)
	}
	assertCacheEntry(t, m, "10.3.48.200/32", "ep-1")
	assertCacheEntry(t, m, "10.3.48.201/32", "ep-2")
}

func TestRefreshCache_DualStackEndpoints(t *testing.T) {
	mock := &hns.MockAPI{
		Endpoints: []hns.HNSEndpoint{
			{
				Id:                 "ep-ds-1",
				IPAddress:          net.ParseIP("10.3.48.200"),
				IPv6Address:        net.ParseIP("fd00:10:3::c8"),
				VirtualNetworkName: "Calico",
				SharedContainers:   []string{"container-1"},
			},
		},
	}
	m := newTestEndpointManager(mock)
	err := m.RefreshHnsEndpointCache(true)
	if err != nil {
		t.Fatalf("RefreshHnsEndpointCache failed: %v", err)
	}
	// Should have both IPv4 and IPv6 entries pointing to same endpoint.
	if len(m.addressToEndpointId) != 2 {
		t.Fatalf("expected 2 cache entries (v4+v6), got %d: %v", len(m.addressToEndpointId), m.addressToEndpointId)
	}
	assertCacheEntry(t, m, "10.3.48.200/32", "ep-ds-1")
	assertCacheEntry(t, m, "fd00:10:3::c8/128", "ep-ds-1")
}

func TestRefreshCache_IPv6OnlyEndpoint(t *testing.T) {
	// Endpoint with unspecified IPv4 but valid IPv6 (unlikely in practice but tests the code path).
	mock := &hns.MockAPI{
		Endpoints: []hns.HNSEndpoint{
			{
				Id:                 "ep-v6",
				IPAddress:          net.IPv4zero,
				IPv6Address:        net.ParseIP("fd00:10:3::1"),
				VirtualNetworkName: "Calico",
				SharedContainers:   []string{"container-1"},
			},
		},
	}
	m := newTestEndpointManager(mock)
	_ = m.RefreshHnsEndpointCache(true)
	// IPv4 entry will be "0.0.0.0/32" (the zero IP), IPv6 should be present.
	assertCacheEntry(t, m, "fd00:10:3::1/128", "ep-v6")
}

func TestRefreshCache_SkipsNilIPv6(t *testing.T) {
	mock := &hns.MockAPI{
		Endpoints: []hns.HNSEndpoint{
			{
				Id:                 "ep-no-v6",
				IPAddress:          net.ParseIP("10.3.48.200"),
				VirtualNetworkName: "Calico",
				SharedContainers:   []string{"container-1"},
			},
		},
	}
	m := newTestEndpointManager(mock)
	_ = m.RefreshHnsEndpointCache(true)
	// Should only have 1 entry (IPv4), not 2.
	if len(m.addressToEndpointId) != 1 {
		t.Fatalf("expected 1 cache entry, got %d: %v", len(m.addressToEndpointId), m.addressToEndpointId)
	}
	assertCacheEntry(t, m, "10.3.48.200/32", "ep-no-v6")
}

func TestRefreshCache_SkipsUnspecifiedIPv6(t *testing.T) {
	mock := &hns.MockAPI{
		Endpoints: []hns.HNSEndpoint{
			{
				Id:                 "ep-unspec-v6",
				IPAddress:          net.ParseIP("10.3.48.200"),
				IPv6Address:        net.IPv6unspecified,
				VirtualNetworkName: "Calico",
				SharedContainers:   []string{"container-1"},
			},
		},
	}
	m := newTestEndpointManager(mock)
	_ = m.RefreshHnsEndpointCache(true)
	if len(m.addressToEndpointId) != 1 {
		t.Fatalf("expected 1 cache entry (unspecified v6 skipped), got %d: %v", len(m.addressToEndpointId), m.addressToEndpointId)
	}
}

func TestRefreshCache_SkipsRemoteEndpoints(t *testing.T) {
	mock := &hns.MockAPI{
		Endpoints: []hns.HNSEndpoint{
			{
				Id:                 "ep-remote",
				IPAddress:          net.ParseIP("10.3.48.200"),
				IPv6Address:        net.ParseIP("fd00::1"),
				VirtualNetworkName: "Calico",
				IsRemoteEndpoint:   true,
				SharedContainers:   []string{"container-1"},
			},
		},
	}
	m := newTestEndpointManager(mock)
	_ = m.RefreshHnsEndpointCache(true)
	if len(m.addressToEndpointId) != 0 {
		t.Fatalf("expected 0 cache entries (remote skipped), got %d: %v", len(m.addressToEndpointId), m.addressToEndpointId)
	}
}

func TestRefreshCache_SkipsStaleEndpoints(t *testing.T) {
	mock := &hns.MockAPI{
		Endpoints: []hns.HNSEndpoint{
			{
				Id:                 "ep-stale",
				IPAddress:          net.ParseIP("10.3.48.200"),
				IPv6Address:        net.ParseIP("fd00::1"),
				VirtualNetworkName: "Calico",
				SharedContainers:   []string{}, // no containers attached
			},
		},
	}
	m := newTestEndpointManager(mock)
	_ = m.RefreshHnsEndpointCache(true)
	if len(m.addressToEndpointId) != 0 {
		t.Fatalf("expected 0 cache entries (stale skipped), got %d: %v", len(m.addressToEndpointId), m.addressToEndpointId)
	}
}

func TestRefreshCache_SkipsOtherNetworks(t *testing.T) {
	mock := &hns.MockAPI{
		Endpoints: []hns.HNSEndpoint{
			{
				Id:                 "ep-other-net",
				IPAddress:          net.ParseIP("10.3.48.200"),
				IPv6Address:        net.ParseIP("fd00::1"),
				VirtualNetworkName: "SomeOtherNetwork",
				SharedContainers:   []string{"container-1"},
			},
		},
	}
	m := newTestEndpointManager(mock)
	_ = m.RefreshHnsEndpointCache(true)
	if len(m.addressToEndpointId) != 0 {
		t.Fatalf("expected 0 cache entries (wrong network), got %d: %v", len(m.addressToEndpointId), m.addressToEndpointId)
	}
}

func TestRefreshCache_MultipleDualStackEndpoints(t *testing.T) {
	// Simulates the real HNS state from our experiment: multiple pods with dual-stack IPs.
	mock := &hns.MockAPI{
		Endpoints: []hns.HNSEndpoint{
			{
				Id:                 "ep-pod-a",
				IPAddress:          net.ParseIP("10.3.48.200"),
				IPv6Address:        net.ParseIP("fd00:10:3::c8"),
				VirtualNetworkName: "Calico",
				SharedContainers:   []string{"sandbox-a"},
			},
			{
				Id:                 "ep-pod-b",
				IPAddress:          net.ParseIP("10.3.48.201"),
				IPv6Address:        net.ParseIP("fd00:10:3::c9"),
				VirtualNetworkName: "Calico",
				SharedContainers:   []string{"sandbox-b"},
			},
			{
				Id:                 "ep-host",
				Name:               "Calico_ep",
				IPAddress:          net.ParseIP("10.3.48.194"),
				VirtualNetworkName: "Calico",
				SharedContainers:   []string{"host"},
			},
		},
	}
	m := newTestEndpointManager(mock)
	_ = m.RefreshHnsEndpointCache(true)

	// pod-a: 2 entries (v4+v6), pod-b: 2 entries (v4+v6), host ep: 1 entry (v4 only)
	if len(m.addressToEndpointId) != 5 {
		t.Fatalf("expected 5 cache entries, got %d: %v", len(m.addressToEndpointId), m.addressToEndpointId)
	}
	assertCacheEntry(t, m, "10.3.48.200/32", "ep-pod-a")
	assertCacheEntry(t, m, "fd00:10:3::c8/128", "ep-pod-a")
	assertCacheEntry(t, m, "10.3.48.201/32", "ep-pod-b")
	assertCacheEntry(t, m, "fd00:10:3::c9/128", "ep-pod-b")
	assertCacheEntry(t, m, "10.3.48.194/32", "ep-host")
}

func TestGetHnsEndpointId_IPv6Lookup(t *testing.T) {
	mock := &hns.MockAPI{
		Endpoints: []hns.HNSEndpoint{
			{
				Id:                 "ep-ds",
				IPAddress:          net.ParseIP("10.3.48.200"),
				IPv6Address:        net.ParseIP("fd00:10:3::c8"),
				VirtualNetworkName: "Calico",
				SharedContainers:   []string{"sandbox"},
			},
		},
	}
	m := newTestEndpointManager(mock)
	_ = m.RefreshHnsEndpointCache(true)

	// Look up by IPv4.
	id, err := m.getHnsEndpointId("10.3.48.200/32")
	if err != nil || id != "ep-ds" {
		t.Errorf("IPv4 lookup: got id=%q err=%v, want ep-ds", id, err)
	}

	// Look up by IPv6.
	id, err = m.getHnsEndpointId("fd00:10:3::c8/128")
	if err != nil || id != "ep-ds" {
		t.Errorf("IPv6 lookup: got id=%q err=%v, want ep-ds", id, err)
	}

	// Look up unknown address.
	id, err = m.getHnsEndpointId("10.99.99.99/32")
	if err == nil {
		t.Errorf("unknown IP lookup should fail, got id=%q", id)
	}
}

// --- helpers ---

func assertCacheEntry(t *testing.T, m *endpointManager, ip, expectedId string) {
	t.Helper()
	id, ok := m.addressToEndpointId[ip]
	if !ok {
		t.Errorf("cache missing entry for %s; cache: %v", ip, m.addressToEndpointId)
	} else if id != expectedId {
		t.Errorf("cache[%s] = %q, want %q", ip, id, expectedId)
	}
}

func assertContains(t *testing.T, slice []string, val string) {
	t.Helper()
	for _, s := range slice {
		if s == val {
			return
		}
	}
	t.Errorf("slice %v does not contain %q", slice, val)
}

func assertStringSlicesEqual(t *testing.T, expected, actual []string) {
	t.Helper()
	if len(expected) != len(actual) {
		t.Fatalf("length mismatch: expected %d %v, got %d %v", len(expected), expected, len(actual), actual)
	}
	for i := range expected {
		if expected[i] != actual[i] {
			t.Errorf("index %d: expected %q, got %q", i, expected[i], actual[i])
		}
	}
}
