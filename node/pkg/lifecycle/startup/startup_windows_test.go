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

package startup

import (
	"os"
	"testing"
)

func TestIPv6Supported_Unset(t *testing.T) {
	os.Unsetenv("FELIX_IPV6SUPPORT")
	if ipv6Supported() {
		t.Error("expected ipv6Supported() to be false when FELIX_IPV6SUPPORT is unset")
	}
}

func TestIPv6Supported_True(t *testing.T) {
	os.Setenv("FELIX_IPV6SUPPORT", "true")
	defer os.Unsetenv("FELIX_IPV6SUPPORT")
	if !ipv6Supported() {
		t.Error("expected ipv6Supported() to be true when FELIX_IPV6SUPPORT=true")
	}
}

func TestIPv6Supported_TrueMixedCase(t *testing.T) {
	os.Setenv("FELIX_IPV6SUPPORT", "True")
	defer os.Unsetenv("FELIX_IPV6SUPPORT")
	if !ipv6Supported() {
		t.Error("expected ipv6Supported() to be true when FELIX_IPV6SUPPORT=True")
	}
}

func TestIPv6Supported_False(t *testing.T) {
	os.Setenv("FELIX_IPV6SUPPORT", "false")
	defer os.Unsetenv("FELIX_IPV6SUPPORT")
	if ipv6Supported() {
		t.Error("expected ipv6Supported() to be false when FELIX_IPV6SUPPORT=false")
	}
}

func TestIPv6Supported_Empty(t *testing.T) {
	os.Setenv("FELIX_IPV6SUPPORT", "")
	defer os.Unsetenv("FELIX_IPV6SUPPORT")
	if ipv6Supported() {
		t.Error("expected ipv6Supported() to be false when FELIX_IPV6SUPPORT is empty")
	}
}
