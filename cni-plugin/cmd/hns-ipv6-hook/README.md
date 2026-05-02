# hns-ipv6-hook

A small Windows DLL that filters `iphlpapi.dll!GetAdaptersAddresses`
inside the HNS service process so HNS picks the operator-chosen IPv6
address as the L2Bridge network's `ManagementIPv6`. The host NIC
keeps every auto-configured address (link-local + ULA + GUA from
DHCPv6-PD); only HNS's view of the NIC is filtered.

## Why this exists

HNS L2Bridge installs a VFP rule
(`HNS::Service::Core::NetworkEntityManager::EnableOverrideReceiveRoutingForLocalAddressesIpv6`,
visible as a public symbol in `HostNetSvc.dll`) that delivers Neighbor
Solicitations to the management OS only for the network's registered
`ManagementIP` and `ManagementIPv6`. NS for any other host IPv6 is
silently dropped at the vSwitch.

HNS picks `ManagementIPv6` by scanning the NIC at network create or
service-restart time and taking the first non-link-local IPv6 it
finds. There is no input field that overrides the auto-pick:

- `hcsshim`'s `HNSNetwork` struct (v0.11.x, v0.14.x, current main)
  declares only `ManagementIP`. No IPv6 variant.
- The HCN schema (`microsoft/hnslib`'s `HostComputeNetwork`) doesn't
  have a management-IPv6 field either.
- HNS's `POST /networks` silently drops any `ManagementIPv6` field
  passed in the JSON request — verified empirically on
  Windows Server 2022 build 20348.4773.
- `vmcompute.dll` exports only `HNSCall(method,path,body,response)`
  for the HNS surface. `computenetwork.dll` exports `HcnCreateNetwork`
  / `HcnModifyNetwork` etc, but `ModifyNetwork`'s `ResourceType` is
  constrained to `{Policy, DNS, Extension}`.
- `HostNetSvc.dll` has the right INTERNAL method —
  `?UpdateManagementIp@SDNLayer@Network@Service@HNS@@QEAAXAEBV?$basic_string@G...0@Z`,
  i.e. `void SDNLayer::UpdateManagementIp(wstring const&, wstring const&)` —
  but it is not exported and is not RPC-reachable.
- Microsoft AKS (`Azure/AgentBaker/.../windowsnodereset.ps1`) confirms
  there is no setter: when their node has the wrong IPv6 they
  `Restart-Service hns` to retrigger the auto-pick.

So the only practical lever is what's on the NIC at HNS's scan time.
The previous fix removed the GUA from the NIC before HNS scanned
(`Strip-NonClusterIPv6` in `node-service.ps1`). That worked, but it
disturbed IPv6 auto-configuration: the GUA was removed transiently
until SLAAC re-added it from the next RA.

This hook leaves the NIC alone and instead patches what HNS sees.
HNS still calls `iphlpapi!GetAdaptersAddresses`; our trampoline drops
every IPv6 unicast that isn't the configured "desired" address.

## How it works

1. `DllMain` (DLL_PROCESS_ATTACH) reads the desired IPv6 string from
   `C:\CalicoWindows\hns-ipv6-hook.cfg`. Services don't inherit
   caller envvars, so a file is the cleanest config channel.
2. Resolves `iphlpapi.dll!GetAdaptersAddresses` via `LoadLibraryA` +
   `GetProcAddress`. (System DLLs are loaded at the same address
   across processes per boot session, so this is consistent with
   what HNS resolved.)
3. Verifies the function's first 14 bytes match the known prologue
   for Server 2022 LTSC2022 build 20348.4773:
   ```
   48 89 5C 24 18      mov [rsp+0x18], rbx        (5)
   55 56 57 41 56 41 57 push rbp / rsi / rdi / r14 / r15 (7)
   48 8B EC            mov rbp, rsp               (3)
   ```
   = 15 bytes, all position-independent.
4. Allocates a trampoline page and copies those 15 bytes into it,
   then appends a 14-byte `FF 25` absolute JMP back to (target+15).
5. Patches the target's first 14 bytes with another `FF 25` absolute
   JMP to our detour function.
6. The detour calls the trampoline (= the original prologue + JMP
   back), then walks the returned `IP_ADAPTER_UNICAST_ADDRESS` lists
   on every adapter and unlinks any IPv6 unicast whose 16 bytes don't
   match the desired address. IPv4 entries are untouched.
7. **Idempotent re-injection**: if a previous DllMain already patched
   the prologue, the second call sees `FF 25 ...` instead of the
   recognised prologue. We detect that, skip re-patching, and log
   "already hooked, skipping". The original patch is still active.

If the prologue doesn't match (different Win build, different
compiler), the hook refuses to install and writes a warning to
`C:\hns-ipv6-hook.log`. The injector independently gates on
`RtlGetVersion` BuildNumber == 20348.

## Components

| File | What it is |
|---|---|
| `hook.c` | The Win DLL. Cross-compiled with MinGW. Drops everything that isn't the desired IPv6. |
| `probe.c` | Self-test harness — calls `GetAdaptersAddresses` before and after `LoadLibrary`-ing the hook DLL, in its own process. |
| `../hns-ipv6-injector/main.go` | SYSTEM-privileged injector. Resolves HNS service PID via SCM, opens process, allocates remote memory, writes wide-char DLL path, fires `CreateRemoteThread → kernel32!LoadLibraryW`. |

## Building

The Dockerfile-windows builds the DLL in a Linux multi-stage builder
with `mingw-w64`. To build by hand from a Linux dev machine:

```bash
sudo apt-get install -y gcc-mingw-w64-x86-64-win32

# Hook DLL
x86_64-w64-mingw32-gcc -O2 -Wall -shared \
    -o hns-ipv6-hook.dll hook.c \
    -lws2_32 -liphlpapi -static-libgcc

# Optional self-test harness
x86_64-w64-mingw32-gcc -O2 -o probe.exe probe.c \
    -lws2_32 -liphlpapi -static-libgcc

# Injector (Go cross-compile)
GOOS=windows GOARCH=amd64 go build \
    -o hns-ipv6-injector.exe \
    ./cni-plugin/cmd/hns-ipv6-injector
```

## Testing

`probe.exe` validates the filter end-to-end without touching the
live HNS service:

```
== probe.exe == BEFORE hook injection ==
[before]  vEthernet (Calico_ep)  2001:5a8:4294:9c01:430d:9038:5fa1:d002
[before]  vEthernet (Calico_ep)  fe80::215:5dff:fedf:c9e0
[before]  vEthernet (Ethernet)   2001:5a8:4294:9c00:1ac0:4dff:fe89:5194  (SLAAC GUA)
[before]  vEthernet (Ethernet)   fd5a:8000:1:0:1ac0:4dff:fe89:5194       (cluster ULA)
[before]  vEthernet (Ethernet)   fe80::1ac0:4dff:fe89:5194
[before]  Loopback Pseudo-Interface 1  ::1
[before] total IPv6 unicasts: 6

== Hook DLL loaded at 00007ff882630000 ==

== probe.exe == AFTER hook injection ==
[after]  vEthernet (Ethernet)   fd5a:8000:1:0:1ac0:4dff:fe89:5194
[after] total IPv6 unicasts: 1
```

Go-side mocks model HNS's selective field handling — see
`cni-plugin/pkg/dataplane/windows/ensure_network_test.go` for tests
that exercise the recreate-on-`ManagementIPv6`-mismatch path with the
mock simulating real HNS's silently-drop-then-auto-pick behaviour.

## Configuration

| Env var | Effect |
|---|---|
| `CALICO_HNS_IPV6_HOOK=true` | Force-enable the hook injection. |
| `CALICO_HNS_IPV6_HOOK=false` | Force-disable; falls back to `Strip-NonClusterIPv6`. |
| `CALICO_HNS_IPV6_HOOK` unset | Hook enabled if `CALICO_DESIRED_HNS_MGMT_IPV6` is non-empty, else `Strip-NonClusterIPv6`. |
| `CALICO_DESIRED_HNS_MGMT_IPV6` | Exact IPv6 (no `/prefix`) for HNS to pin as `ManagementIPv6`. If empty, `node-service.ps1` derives it by picking the first non-link-local address on `vEthernet (Ethernet)` that matches the `IP6_AUTODETECTION_METHOD=cidr=...` prefix. |

## Compatibility

Windows Server 2022, LTSC2022, build 20348.x. The `hns-ipv6-injector`
binary refuses to run on other builds (verified via `RtlGetVersion`)
because the trampoline's prologue match is build-specific.

To verify against a new build, dump the first 32 bytes of
`iphlpapi!GetAdaptersAddresses`:

```powershell
# (loaded into the live process — system DLLs are at fixed addresses
#  per boot session)
$signature = @'
[DllImport("kernel32.dll", CharSet=CharSet.Ansi)]
public static extern IntPtr LoadLibraryA(string s);
[DllImport("kernel32.dll", CharSet=CharSet.Ansi)]
public static extern IntPtr GetProcAddress(IntPtr h, string s);
'@
$nm = Add-Type -MemberDefinition $signature -Namespace P -Name N -PassThru
$h = $nm::LoadLibraryA('iphlpapi.dll')
$p = $nm::GetProcAddress($h, 'GetAdaptersAddresses')
$bytes = [byte[]]::new(32)
[System.Runtime.InteropServices.Marshal]::Copy($p, $bytes, 0, 32)
($bytes | ForEach-Object { '{0:X2}' -f $_ }) -join ' '
```

If the bytes match the documented prologue, the hook is safe to
install. Otherwise, update `recognised_prologue_len` in `hook.c`.

## Security

The hook runs in `svchost.exe -k NetSvcs` (SYSTEM). A bug here can
crash host networking. Audit `hook.c` line-by-line before changes.
The injector is signed/built reproducibly via the Dockerfile so the
DLL bytes that end up on the host are deterministic.
