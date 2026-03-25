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

package ipamplugin

import (
	"testing"
)

func strPtr(s string) *string {
	return &s
}

func TestCalculateAssignCounts_DefaultIPv4Only(t *testing.T) {
	// With no explicit configuration, should assign 1 IPv4 and 0 IPv6.
	num4, num6 := calculateAssignCounts(nil, nil, nil)
	if num4 != 1 {
		t.Errorf("expected num4=1, got %d", num4)
	}
	if num6 != 0 {
		t.Errorf("expected num6=0, got %d", num6)
	}
}

func TestCalculateAssignCounts_AssignIpv6True(t *testing.T) {
	// When AssignIpv6 is explicitly "true", num6 should be 1.
	num4, num6 := calculateAssignCounts(nil, strPtr("true"), nil)
	if num4 != 1 {
		t.Errorf("expected num4=1, got %d", num4)
	}
	if num6 != 1 {
		t.Errorf("expected num6=1, got %d", num6)
	}
}

func TestCalculateAssignCounts_IPv6PoolsNonEmpty(t *testing.T) {
	// When IPv6Pools is non-empty (even if AssignIpv6 is nil), num6 should be 1.
	num4, num6 := calculateAssignCounts(nil, nil, []string{"2001:db8::/48"})
	if num4 != 1 {
		t.Errorf("expected num4=1, got %d", num4)
	}
	if num6 != 1 {
		t.Errorf("expected num6=1, got %d", num6)
	}
}

func TestCalculateAssignCounts_AssignIpv6False_NoIPv6Pools(t *testing.T) {
	// When AssignIpv6 is "false" and no IPv6 pools, num6 should be 0.
	num4, num6 := calculateAssignCounts(nil, strPtr("false"), nil)
	if num4 != 1 {
		t.Errorf("expected num4=1, got %d", num4)
	}
	if num6 != 0 {
		t.Errorf("expected num6=0, got %d", num6)
	}
}

func TestCalculateAssignCounts_AssignIpv6False_WithIPv6Pools(t *testing.T) {
	// When AssignIpv6 is "false" but IPv6Pools are provided, AssignIpv6 "false"
	// does not match "true", so the else-if branch kicks in for pools.
	num4, num6 := calculateAssignCounts(nil, strPtr("false"), []string{"2001:db8::/48"})
	if num4 != 1 {
		t.Errorf("expected num4=1, got %d", num4)
	}
	if num6 != 1 {
		t.Errorf("expected num6=1 (IPv6 pools trigger assignment), got %d", num6)
	}
}

func TestCalculateAssignCounts_AssignIpv4False(t *testing.T) {
	// When AssignIpv4 is "false", num4 should be 0.
	num4, num6 := calculateAssignCounts(strPtr("false"), nil, nil)
	if num4 != 0 {
		t.Errorf("expected num4=0, got %d", num4)
	}
	if num6 != 0 {
		t.Errorf("expected num6=0, got %d", num6)
	}
}

func TestCalculateAssignCounts_DualStack(t *testing.T) {
	// Both IPv4 and IPv6 explicitly enabled.
	num4, num6 := calculateAssignCounts(strPtr("true"), strPtr("true"), nil)
	if num4 != 1 {
		t.Errorf("expected num4=1, got %d", num4)
	}
	if num6 != 1 {
		t.Errorf("expected num6=1, got %d", num6)
	}
}

func TestCalculateAssignCounts_IPv6OnlyViaAssignIpv4False(t *testing.T) {
	// IPv4 disabled, IPv6 enabled.
	num4, num6 := calculateAssignCounts(strPtr("false"), strPtr("true"), nil)
	if num4 != 0 {
		t.Errorf("expected num4=0, got %d", num4)
	}
	if num6 != 1 {
		t.Errorf("expected num6=1, got %d", num6)
	}
}

func TestCalculateAssignCounts_MultipleIPv6Pools(t *testing.T) {
	// Multiple IPv6 pools should still result in num6=1 (one address).
	num4, num6 := calculateAssignCounts(nil, nil, []string{"2001:db8::/48", "fd00::/64"})
	if num4 != 1 {
		t.Errorf("expected num4=1, got %d", num4)
	}
	if num6 != 1 {
		t.Errorf("expected num6=1, got %d", num6)
	}
}

func TestCalculateAssignCounts_EmptyIPv6Pools(t *testing.T) {
	// Empty slice should not trigger IPv6 assignment.
	num4, num6 := calculateAssignCounts(nil, nil, []string{})
	if num4 != 1 {
		t.Errorf("expected num4=1, got %d", num4)
	}
	if num6 != 0 {
		t.Errorf("expected num6=0 for empty IPv6 pools, got %d", num6)
	}
}
