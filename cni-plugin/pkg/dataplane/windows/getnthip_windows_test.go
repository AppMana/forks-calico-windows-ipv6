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

package windows

import (
	"net"
	"testing"
)

func TestGetNthIP_IPv4_26(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("10.244.0.0/26")
	gw := getNthIP(subnet, 1)
	if gw.String() != "10.244.0.1" {
		t.Errorf("expected 10.244.0.1, got %s", gw.String())
	}
	ep := getNthIP(subnet, 2)
	if ep.String() != "10.244.0.2" {
		t.Errorf("expected 10.244.0.2, got %s", ep.String())
	}
}

func TestGetNthIP_IPv4_24(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("192.168.1.0/24")
	gw := getNthIP(subnet, 1)
	if gw.String() != "192.168.1.1" {
		t.Errorf("expected 192.168.1.1, got %s", gw.String())
	}
}

func TestGetNthIP_IPv6_64(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("fd00:10:244::/64")
	gw := getNthIP(subnet, 1)
	if gw.String() != "fd00:10:244::1" {
		t.Errorf("expected fd00:10:244::1, got %s", gw.String())
	}
	ep := getNthIP(subnet, 2)
	if ep.String() != "fd00:10:244::2" {
		t.Errorf("expected fd00:10:244::2, got %s", ep.String())
	}
}

func TestGetNthIP_IPv6_126(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("fd00:10:244::40/126")
	gw := getNthIP(subnet, 1)
	if gw.String() != "fd00:10:244::41" {
		t.Errorf("expected fd00:10:244::41, got %s", gw.String())
	}
	ep := getNthIP(subnet, 2)
	if ep.String() != "fd00:10:244::42" {
		t.Errorf("expected fd00:10:244::42, got %s", ep.String())
	}
}

func TestGetNthIP_IPv6_122(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("2001:db8::/122")
	gw := getNthIP(subnet, 1)
	if gw.String() != "2001:db8::1" {
		t.Errorf("expected 2001:db8::1, got %s", gw.String())
	}
}
