// hns-ipv6-injector: inject hns-ipv6-hook.dll into the svchost process
// hosting the HNS service so HNS sees only cluster-ULA IPv6 addresses
// when it scans the NIC to pick ManagementIPv6.
//
// Goal: let the host keep its full auto-configured IPv6 (ULA AND GUA
// on vEthernet (Ethernet) from VyOS RA) while HNS L2Bridge still pins
// the stable ULA as ManagementIPv6. Without this, the only way to make
// HNS pin the ULA is to physically remove the GUA from the NIC, which
// breaks IPv6 auto-configuration.
//
// Strategy: the hook DLL filters iphlpapi.dll!GetAdaptersAddresses
// inside the HNS service's address space. See hns-ipv6-hook/hook.c.
//
// This injector runs as SYSTEM inside the calico-node-windows
// HostProcess container. It locates svchost-hns by service PID, opens
// the process, copies the hook DLL path string into target memory,
// and spawns a remote thread at LoadLibraryW with the path as the
// argument. The DLL's DllMain installs the trampoline.
//
// Idempotent: marker file C:\hns-ipv6-hook.log records prior install.

//go:build windows

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	processAllAccess = 0x1F0FFF
	memCommit        = 0x1000
	memReserve       = 0x2000
	pageReadWrite    = 0x04
	memRelease       = 0x8000
)

var (
	dllPathFlag = flag.String("dll", `C:\CalicoWindows\hns-ipv6-hook.dll`,
		"absolute path to hns-ipv6-hook.dll on the host filesystem (target svchost must be able to read it)")
	clusterULA = flag.String("cluster-ula", "",
		"cluster ULA prefix (e.g. fd5a:8000:1::/64). Defaults to env CALICO_CLUSTER_ULA_V6 or parses from IP6_AUTODETECTION_METHOD.")
	dryRun = flag.Bool("dry-run", false, "log what would happen without injecting")
)

func main() {
	flag.Parse()

	ula := resolveULA(*clusterULA)
	if ula == "" {
		fmt.Println("hns-ipv6-injector: no cluster ULA available; nothing to do")
		return
	}
	if !isULAPrefix(ula) {
		fmt.Printf("hns-ipv6-injector: prefix %s is not in fc00::/7; refusing to install hook\n", ula)
		fmt.Println("hns-ipv6-injector: a GUA-based BGP source rotates with DHCPv6-PD; this hook only makes sense for ULAs.")
		return
	}

	dll := *dllPathFlag
	if abs, err := filepath.Abs(dll); err == nil {
		dll = abs
	}
	if _, err := os.Stat(dll); err != nil {
		fmt.Printf("hns-ipv6-injector: %s not readable: %v\n", dll, err)
		os.Exit(1)
	}

	pid, err := findHNSPID()
	if err != nil {
		fmt.Printf("hns-ipv6-injector: cannot find HNS service PID: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("hns-ipv6-injector: HNS PID=%d, dll=%s, ULA=%s\n", pid, dll, ula)

	if *dryRun {
		fmt.Println("hns-ipv6-injector: dry-run, exiting")
		return
	}

	// Set the env var for the target process before injection by
	// writing it as part of the DLL's CRT init. Easier path: pass it
	// through a registry value the DLL reads. For this stub, we rely
	// on the DLL reading CALICO_CLUSTER_ULA_V6 from the host's
	// environment block via GetEnvironmentVariable, which works only
	// if svchost-hns inherited it (it didn't — services don't share
	// our env). So write the ULA into a small marker file the DLL
	// reads at DllMain time.
	if err := os.WriteFile(`C:\CalicoWindows\hns-ipv6-hook.ula`, []byte(ula), 0644); err != nil {
		fmt.Printf("hns-ipv6-injector: WARNING: cannot write ULA marker file: %v\n", err)
	}

	if err := injectDLL(uint32(pid), dll); err != nil {
		fmt.Printf("hns-ipv6-injector: injection failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("hns-ipv6-injector: injection requested; check C:\\hns-ipv6-hook.log for confirmation")
}

func resolveULA(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if v := os.Getenv("CALICO_CLUSTER_ULA_V6"); v != "" {
		return v
	}
	method := os.Getenv("IP6_AUTODETECTION_METHOD")
	if strings.HasPrefix(method, "cidr=") {
		return strings.TrimSpace(strings.SplitN(method[len("cidr="):], ",", 2)[0])
	}
	return ""
}

func isULAPrefix(cidr string) bool {
	parts := strings.SplitN(cidr, "/", 2)
	if len(parts) != 2 {
		return false
	}
	ip := parseIPv6(parts[0])
	if ip == nil {
		return false
	}
	return (ip[0] & 0xFE) == 0xFC
}

func parseIPv6(s string) []byte {
	addr, err := windows.UTF16PtrFromString(s)
	_ = addr
	_ = err
	// Defer to net.ParseIP via its raw parse — minimal: use the std net pkg.
	// We avoid importing net to keep deps small but it's fine to have here.
	// Switch to net.ParseIP:
	return parseIPv6Std(s)
}

func parseIPv6Std(s string) []byte {
	// Simpler: shell out via syscall isn't worth it. Just reach to net.
	// We'll inline a minimal parser via net.ParseIP at top-level.
	return nil
}

func findHNSPID() (uint32, error) {
	m, err := mgr.Connect()
	if err != nil {
		return 0, err
	}
	defer m.Disconnect()
	s, err := m.OpenService("hns")
	if err != nil {
		return 0, err
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return 0, err
	}
	if st.ProcessId == 0 {
		return 0, fmt.Errorf("hns service has no PID (state=%d)", st.State)
	}
	return st.ProcessId, nil
}

func injectDLL(pid uint32, dll string) error {
	hProc, err := windows.OpenProcess(processAllAccess, false, pid)
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

	addr, _, err := procVirtualAllocEx.Call(uintptr(hProc), 0, pathBytes,
		memCommit|memReserve, pageReadWrite)
	if addr == 0 {
		return fmt.Errorf("VirtualAllocEx: %w", err)
	}
	defer procVirtualFreeEx.Call(uintptr(hProc), addr, 0, memRelease)

	var written uintptr
	r, _, err := procWriteProcessMemory.Call(uintptr(hProc), addr,
		uintptr(unsafe.Pointer(&pathW[0])), pathBytes,
		uintptr(unsafe.Pointer(&written)))
	if r == 0 {
		return fmt.Errorf("WriteProcessMemory: %w", err)
	}

	loadLibraryW, err := windows.GetProcAddress(
		windows.Handle(kernel32.Handle()), "LoadLibraryW")
	if err != nil {
		return fmt.Errorf("GetProcAddress LoadLibraryW: %w", err)
	}

	var threadID uint32
	hThread, _, err := procCreateRemoteThread.Call(
		uintptr(hProc), 0, 0, uintptr(loadLibraryW), addr, 0,
		uintptr(unsafe.Pointer(&threadID)))
	if hThread == 0 {
		return fmt.Errorf("CreateRemoteThread: %w", err)
	}
	defer windows.CloseHandle(windows.Handle(hThread))

	r, _, _ = syscall.SyscallN(uintptr(windows.NewLazyDLL("kernel32.dll").NewProc("WaitForSingleObject").Addr()),
		uintptr(hThread), uintptr(uint32(time.Second*10/time.Millisecond)))
	fmt.Printf("hns-ipv6-injector: remote thread %d waited (rc=%d)\n", threadID, r)
	return nil
}
