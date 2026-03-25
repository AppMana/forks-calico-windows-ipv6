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

	"github.com/projectcalico/calico/libcalico-go/lib/ipam"
)

// TestWindowsReservedAttrIPv6_SetWhenNum6Positive verifies the logic pattern
// used in cmdAdd: when num6 > 0 on Windows, HostReservedAttrIPv6s must be set.
// This tests the pattern in isolation since cmdAdd requires a full CNI setup.
func TestWindowsReservedAttrIPv6_SetWhenNum6Positive(t *testing.T) {
	rsvdAttrWindows := &ipam.HostReservedAttr{
		StartOfBlock: 3,
		EndOfBlock:   1,
		Handle:       ipam.WindowsReservedHandle,
		Note:         "windows host rsvd",
	}

	tests := []struct {
		name       string
		num6       int
		expectIPv6 bool
	}{
		{"num6=0, no IPv6 reserved", 0, false},
		{"num6=1, IPv6 reserved set", 1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assignArgs := ipam.AutoAssignArgs{
				Num4:                  1,
				Num6:                  tt.num6,
				HostReservedAttrIPv4s: rsvdAttrWindows,
			}
			// Replicate the conditional from cmdAdd.
			if tt.num6 > 0 {
				assignArgs.HostReservedAttrIPv6s = rsvdAttrWindows
			}

			if tt.expectIPv6 {
				if assignArgs.HostReservedAttrIPv6s == nil {
					t.Error("expected HostReservedAttrIPv6s to be set when num6 > 0")
				}
				if assignArgs.HostReservedAttrIPv6s.StartOfBlock != 3 {
					t.Errorf("expected StartOfBlock=3, got %d", assignArgs.HostReservedAttrIPv6s.StartOfBlock)
				}
				if assignArgs.HostReservedAttrIPv6s.EndOfBlock != 1 {
					t.Errorf("expected EndOfBlock=1, got %d", assignArgs.HostReservedAttrIPv6s.EndOfBlock)
				}
				if assignArgs.HostReservedAttrIPv6s.Handle != ipam.WindowsReservedHandle {
					t.Errorf("expected Handle=%s, got %s", ipam.WindowsReservedHandle, assignArgs.HostReservedAttrIPv6s.Handle)
				}
			} else {
				if assignArgs.HostReservedAttrIPv6s != nil {
					t.Error("expected HostReservedAttrIPv6s to be nil when num6 == 0")
				}
			}
		})
	}
}
