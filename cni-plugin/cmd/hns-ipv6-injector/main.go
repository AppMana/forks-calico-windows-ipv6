// hns-ipv6-injector: inject hns-ipv6-hook.dll into the svchost
// process hosting the HNS service so the desired host IPv6 (operator-
// chosen via -desired-mgmt-ipv6 / CALICO_DESIRED_HNS_MGMT_IPV6) ends
// up as Calico's HNS L2Bridge ManagementIPv6, without touching the
// addresses on the live NIC.
//
// Operator picks per-node what they want HNS to use. The hook itself
// is opinion-free — it filters iphlpapi!GetAdaptersAddresses to omit
// any IPv6 unicast that isn't the configured address (and isn't
// link-local).
//
// Builds Windows-only. Refuses to run unless the host reports as
// Server 2022 (LTSC2022, build 20348.x), because the hook DLL's
// length-aware prologue patcher is verified only against that build.

//go:build windows

package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	processCreateThread = 0x0002
	processVMOperation  = 0x0008
	processVMRead       = 0x0010
	processVMWrite      = 0x0020
	processQueryInfo    = 0x0400
	memCommit           = 0x1000
	memReserve          = 0x2000
	pageReadWrite       = 0x04
	memRelease          = 0x8000
)

var (
	dllPathFlag = flag.String("dll", `C:\CalicoWindows\hns-ipv6-hook.dll`,
		"absolute path to hns-ipv6-hook.dll readable by svchost-hns (SYSTEM)")
	cfgPathFlag = flag.String("cfg", `C:\CalicoWindows\hns-ipv6-hook.cfg`,
		"absolute path of the cfg file the DLL reads at DllMain")
	desiredFlag = flag.String("desired-mgmt-ipv6", "",
		"desired host IPv6 (no /prefix) HNS should pin as ManagementIPv6. Defaults to env CALICO_DESIRED_HNS_MGMT_IPV6.")
	skipBuildGate = flag.Bool("skip-build-gate", false,
		"INTERNAL: skip the Server 2022 build check. Only set if you know what you're doing.")
	dryRun = flag.Bool("dry-run", false, "log what would happen without injecting")
)

func main() {
	flag.Parse()

	desired := *desiredFlag
	if desired == "" {
		desired = os.Getenv("CALICO_DESIRED_HNS_MGMT_IPV6")
	}
	if desired == "" {
		fmt.Println("hns-ipv6-injector: no desired ManagementIPv6 configured; nothing to do")
		return
	}
	ip := net.ParseIP(desired)
	if ip == nil || ip.To4() != nil {
		fmt.Printf("hns-ipv6-injector: %q is not a valid IPv6 address; refusing\n", desired)
		os.Exit(2)
	}

	if !*skipBuildGate {
		ok, build, err := isServer2022()
		if err != nil {
			fmt.Printf("hns-ipv6-injector: cannot determine Win build: %v\n", err)
			os.Exit(1)
		}
		if !ok {
			fmt.Printf("hns-ipv6-injector: host build %s is not Server 2022 (LTSC2022, 20348.x); refusing\n", build)
			fmt.Println("hns-ipv6-injector: pass -skip-build-gate to override (only after re-verifying the prologue).")
			os.Exit(0)
		}
		fmt.Printf("hns-ipv6-injector: Win build %s -> proceeding\n", build)
	}

	dll, err := filepath.Abs(*dllPathFlag)
	if err != nil {
		fmt.Printf("hns-ipv6-injector: %v\n", err)
		os.Exit(1)
	}
	if _, err := os.Stat(dll); err != nil {
		fmt.Printf("hns-ipv6-injector: %s not readable: %v\n", dll, err)
		os.Exit(1)
	}

	if err := os.MkdirAll(filepath.Dir(*cfgPathFlag), 0755); err != nil {
		fmt.Printf("hns-ipv6-injector: cannot create cfg dir: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*cfgPathFlag, []byte(desired+"\n"), 0644); err != nil {
		fmt.Printf("hns-ipv6-injector: cannot write cfg file: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("hns-ipv6-injector: wrote cfg %s with desired=%s\n", *cfgPathFlag, desired)

	pid, err := findHNSPID()
	if err != nil {
		fmt.Printf("hns-ipv6-injector: cannot find HNS service PID: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("hns-ipv6-injector: HNS PID=%d\n", pid)

	if *dryRun {
		fmt.Println("hns-ipv6-injector: dry-run, exiting")
		return
	}

	if err := injectDLL(uint32(pid), dll); err != nil {
		fmt.Printf("hns-ipv6-injector: injection failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("hns-ipv6-injector: injection requested; check C:\\hns-ipv6-hook.log for confirmation")
}

func findHNSPID() (uint32, error) {
	m, err := mgr.Connect()
	if err != nil {
		return 0, fmt.Errorf("Service Control Manager: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService("hns")
	if err != nil {
		return 0, fmt.Errorf("OpenService(hns): %w", err)
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return 0, fmt.Errorf("Query: %w", err)
	}
	if st.ProcessId == 0 {
		return 0, fmt.Errorf("hns service has no PID (state=%v)", st.State)
	}
	return st.ProcessId, nil
}

// rtlOsVersionInfoEx mirrors RTL_OSVERSIONINFOEXW. We declare it here
// rather than use windows.OsVersionInfoEx because the latter's size
// field is unexported, and RtlGetVersion requires it to be set by the
// caller.
type rtlOsVersionInfoEx struct {
	OSVersionInfoSize uint32
	MajorVersion      uint32
	MinorVersion      uint32
	BuildNumber       uint32
	PlatformID        uint32
	CSDVersion        [128]uint16
	ServicePackMajor  uint16
	ServicePackMinor  uint16
	SuiteMask         uint16
	ProductType       byte
	Reserved          byte
}

// isServer2022 returns (true, "<major.minor.build>", nil) if the host
// is Windows Server 2022 (LTSC2022, build 20348.x). Other builds
// return (false, version, nil).
func isServer2022() (bool, string, error) {
	var ver rtlOsVersionInfoEx
	ver.OSVersionInfoSize = uint32(unsafe.Sizeof(ver))
	ntdll := windows.NewLazyDLL("ntdll.dll")
	proc := ntdll.NewProc("RtlGetVersion")
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(&ver)))
	if r != 0 {
		return false, "", fmt.Errorf("RtlGetVersion returned 0x%x", r)
	}
	build := fmt.Sprintf("%d.%d.%d", ver.MajorVersion, ver.MinorVersion, ver.BuildNumber)
	// LTSC2022 is build 20348. Major.Minor 10.0 covers Win10/11/Server 2019/2022.
	ok := ver.MajorVersion == 10 && ver.MinorVersion == 0 && ver.BuildNumber == 20348
	return ok, build, nil
}

func injectDLL(pid uint32, dll string) error {
	access := uint32(processCreateThread | processVMOperation | processVMRead | processVMWrite | processQueryInfo)
	hProc, err := windows.OpenProcess(access, false, pid)
	if err != nil {
		return fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(hProc)

	pathW, err := windows.UTF16FromString(dll)
	if err != nil {
		return err
	}
	pathBytes := uintptr(len(pathW)) * 2

	kernel32 := windows.NewLazyDLL("kernel32.dll")
	procVirtualAllocEx := kernel32.NewProc("VirtualAllocEx")
	procWriteProcessMemory := kernel32.NewProc("WriteProcessMemory")
	procCreateRemoteThread := kernel32.NewProc("CreateRemoteThread")
	procVirtualFreeEx := kernel32.NewProc("VirtualFreeEx")
	procWaitForSingleObject := kernel32.NewProc("WaitForSingleObject")

	addr, _, e := procVirtualAllocEx.Call(uintptr(hProc), 0, pathBytes,
		memCommit|memReserve, pageReadWrite)
	if addr == 0 {
		return fmt.Errorf("VirtualAllocEx: %v", e)
	}
	defer procVirtualFreeEx.Call(uintptr(hProc), addr, 0, memRelease)

	var written uintptr
	r, _, e := procWriteProcessMemory.Call(uintptr(hProc), addr,
		uintptr(unsafe.Pointer(&pathW[0])), pathBytes,
		uintptr(unsafe.Pointer(&written)))
	if r == 0 {
		return fmt.Errorf("WriteProcessMemory: %v", e)
	}
	if written != pathBytes {
		return fmt.Errorf("short write to target: %d/%d", written, pathBytes)
	}

	loadLibraryW, err := windows.GetProcAddress(
		windows.Handle(kernel32.Handle()), "LoadLibraryW")
	if err != nil {
		return fmt.Errorf("GetProcAddress LoadLibraryW: %w", err)
	}

	var threadID uint32
	hThread, _, e := procCreateRemoteThread.Call(
		uintptr(hProc), 0, 0, uintptr(loadLibraryW), addr, 0,
		uintptr(unsafe.Pointer(&threadID)))
	if hThread == 0 {
		return fmt.Errorf("CreateRemoteThread: %v", e)
	}
	defer windows.CloseHandle(windows.Handle(hThread))

	// Wait up to 10s for LoadLibraryW to complete in the target.
	procWaitForSingleObject.Call(hThread, 10000)

	// Read remote thread exit code; nonzero means LoadLibraryW returned
	// a module handle, which is what we want.
	procGetExitCode := kernel32.NewProc("GetExitCodeThread")
	var code uint32
	procGetExitCode.Call(hThread, uintptr(unsafe.Pointer(&code)))
	if code == 0 {
		return fmt.Errorf("LoadLibraryW returned NULL in target (DLL path readable, exists, signed?)")
	}
	fmt.Printf("hns-ipv6-injector: LoadLibraryW returned 0x%x in target\n", code)
	// Note: the 32-bit return code is truncated from the 64-bit HMODULE.
	// A nonzero value is sufficient signal of success.
	_ = strings.TrimSpace
	return nil
}
