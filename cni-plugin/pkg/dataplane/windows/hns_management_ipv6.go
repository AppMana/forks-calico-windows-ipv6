// Copyright (c) 2025 Tigera, Inc. All rights reserved.
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

//go:build windows

package windows

import (
	"encoding/json"
	"fmt"
	"syscall"
	"unsafe"
)

// queryHNSManagementIPv6 returns the ManagementIPv6 field for the HNS
// network with the given ID. The field is present on the wire but absent
// from hcsshim's typed HNSNetwork struct (as of v0.11.8 / v0.14.1). We
// call vmcompute.dll!HNSCall directly — same path the
// HostNetworkingService PowerShell module uses internally — and parse
// the raw JSON. Returns "" if the field is absent.
//
// The PowerShell wrapper for reference:
//
//	[DllImport("vmcompute.dll")]
//	public static extern void HNSCall(
//	    [MarshalAs(UnmanagedType.LPWStr)] string method,
//	    [MarshalAs(UnmanagedType.LPWStr)] string path,
//	    [MarshalAs(UnmanagedType.LPWStr)] string request,
//	    [MarshalAs(UnmanagedType.LPWStr)] out string response);
func queryHNSManagementIPv6(id string) (string, error) {
	raw, err := hnsCallRaw("GET", "/networks/"+id, "")
	if err != nil {
		return "", err
	}
	var resp struct {
		Success bool
		Error   string
		Output  struct {
			ManagementIPv6 string `json:"ManagementIPv6"`
		}
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return "", fmt.Errorf("parse HNS response: %w", err)
	}
	if !resp.Success {
		return "", fmt.Errorf("HNS returned error: %s", resp.Error)
	}
	return resp.Output.ManagementIPv6, nil
}

var (
	vmcomputeDLL = syscall.NewLazyDLL("vmcompute.dll")
	procHNSCall  = vmcomputeDLL.NewProc("HNSCall")
)

func hnsCallRaw(method, path, request string) (string, error) {
	mPtr, err := syscall.UTF16PtrFromString(method)
	if err != nil {
		return "", err
	}
	pPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	rPtr, err := syscall.UTF16PtrFromString(request)
	if err != nil {
		return "", err
	}
	var respPtr *uint16
	r1, _, callErr := procHNSCall.Call(
		uintptr(unsafe.Pointer(mPtr)),
		uintptr(unsafe.Pointer(pPtr)),
		uintptr(unsafe.Pointer(rPtr)),
		uintptr(unsafe.Pointer(&respPtr)),
	)
	// HNSCall returns void; r1 is the marshalled void return (0). We
	// rely on response content to detect failure — Success=false +
	// Error="..." in the JSON body.
	_ = r1
	if respPtr == nil {
		if callErr != nil && callErr != syscall.Errno(0) {
			return "", callErr
		}
		return "", nil
	}
	return utf16PtrToString(respPtr), nil
}

func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	// Walk to the NUL terminator.
	end := unsafe.Pointer(p)
	n := 0
	for *(*uint16)(end) != 0 {
		end = unsafe.Pointer(uintptr(end) + 2)
		n++
	}
	return string(utf16Decode(unsafe.Slice(p, n)))
}

func utf16Decode(s []uint16) []rune {
	out := make([]rune, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		// Surrogate pair handling.
		if c >= 0xD800 && c <= 0xDBFF && i+1 < len(s) {
			c2 := s[i+1]
			if c2 >= 0xDC00 && c2 <= 0xDFFF {
				r := (rune(c-0xD800) << 10) | rune(c2-0xDC00) | 0x10000
				out = append(out, r)
				i++
				continue
			}
		}
		out = append(out, rune(c))
	}
	return out
}
