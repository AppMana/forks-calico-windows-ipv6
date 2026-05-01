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

// IPv4 carry: /24 with n=300 must wrap into the third byte (.1.44),
// not silently truncate to .0 of the same /24. Today's code uses
// buf[3]+=byte(n), which silently wraps within the byte and produces
// wrong results.
func TestGetNthIP_IPv4_Carry(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("10.0.0.0/16")
	got := getNthIP(subnet, 300)
	if got.String() != "10.0.1.44" {
		t.Errorf("expected 10.0.1.44 for n=300 in 10.0.0.0/16, got %s", got.String())
	}
}

// IPv4 carry from a non-zero last byte: 10.0.0.192/26 + 65 should be
// 10.0.1.1 (carries from 192+65=257).
func TestGetNthIP_IPv4_CarryFromOffsetSubnet(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("10.0.0.192/26")
	got := getNthIP(subnet, 65)
	if got.String() != "10.0.1.1" {
		t.Errorf("expected 10.0.1.1 for n=65 in 10.0.0.192/26, got %s", got.String())
	}
}

// IPv6 carry: /112 with n=300 = 0x12c spans bytes 14-15 (last hextet).
// Result: 2001:db8::12c (no carry into the second-to-last byte because
// 300 < 65536). Important: the buggy buf[15]+=byte(n) gave 2001:db8::2c,
// dropping the 0x01 high byte of 300.
func TestGetNthIP_IPv6_Carry_LastHextet(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("2001:db8::/112")
	got := getNthIP(subnet, 300)
	if got.String() != "2001:db8::12c" {
		t.Errorf("expected 2001:db8::12c for n=300 in 2001:db8::/112, got %s", got.String())
	}
}

// IPv6 carry across hextet boundary: /96 (4-byte host) with n=70000
// = 0x00011170 carries into the second-to-last hextet.
// Bytes 12-15 = 00,01,11,70 -> hextets 0x0001 and 0x1170 -> "1:1170".
func TestGetNthIP_IPv6_Carry_AcrossHextet(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("2001:db8::/96")
	got := getNthIP(subnet, 70000)
	if got.String() != "2001:db8::1:1170" {
		t.Errorf("expected 2001:db8::1:1170 for n=70000 in 2001:db8::/96, got %s", got.String())
	}
}

// IPv6 small-n on a /122 with non-zero last hextet: 2001:db8::40/122 + 2 = ::42.
// This is the case used by CreateAndAttachHostEPWithAPI for the v6 host
// endpoint address; we already test n<256 with no carry above. This
// test covers a /122 close to the byte boundary.
func TestGetNthIP_IPv6_122_NearByteBoundary(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("2001:db8::fc/126")
	got := getNthIP(subnet, 4)
	if got.String() != "2001:db8::100" {
		t.Errorf("expected 2001:db8::100 for n=4 in 2001:db8::fc/126, got %s", got.String())
	}
}
