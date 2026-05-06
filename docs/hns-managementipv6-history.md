# HNS ManagementIPv6 pinning — repair history

This is the chronological record of every approach we tried to make HNS L2Bridge pin a chosen IPv6 address as `ManagementIPv6` on a Windows Server 2022 (LTSC2022, build 20348) node, and why each one ultimately gave way to the next.

## Why we need to pin it

HNS L2Bridge installs a VFP rule (internally `HNS::Service::Core::NetworkEntityManager::EnableOverrideReceiveRoutingForLocalAddressesIpv6`) that delivers Neighbor Solicitations to the management OS only for the network's registered `ManagementIP` and `ManagementIPv6`. Solicits for any other host IPv6 are silently dropped by the vSwitch.

If HNS picks the wrong IPv6, Linux peers cannot resolve our Win host's IPv6 via NDP, the IPv6 BGP mesh sessions never establish, and IPv6 cross-node pod traffic is broken in one direction. This must be deterministic across DHCPv6-PD prefix rotations.

## Confirmed: HNS has no setter for ManagementIPv6

We exhaustively searched:
- `microsoft/hcsshim` (v0.11.x, v0.14.x, current main): typed `HNSNetwork` struct declares `ManagementIP` only.
- `microsoft/hnslib` (the new shim): `HostComputeNetwork` uses `Ipams` + `Policies`; no IPv6 management field.
- HNS REST API on the wire: POST `/networks` silently drops any `ManagementIPv6` in the input JSON. Verified empirically.
- `vmcompute.dll` exports: only `HNSCall(method,path,body,response)` — no setter.
- `computenetwork.dll` exports `HcnModifyNetwork` but `ResourceType` is bounded to `{Policy, DNS, Extension}`.
- `HostNetSvc.dll` PDB symbols include `HNS::Service::Network::SDNLayer::UpdateManagementIp(wstring const&, wstring const&)` — public C++ method but NOT exported and not RPC-reachable.
- AKS's `Azure/AgentBaker/.../windowsnodereset.ps1` confirms there is no setter; their fallback is `Restart-Service hns` to retrigger HNS's auto-pick.

HNS picks `ManagementIPv6` by scanning the underlying NIC asynchronously after the L2Bridge is created and taking the first non-link-local IPv6 it finds. So the lever we have is **what the NIC looks like when HNS scans it**.

## Approach 1 — Strip-NonClusterIPv6 (PowerShell, in `node-service.ps1`)

Written: early in the dual-stack work. Lives in `node/windows-packaging/CalicoWindows/node/node-service.ps1`.

Removes RA-derived IPv6 addresses from `vEthernet (Ethernet*)` whose value or prefix doesn't match the operator's chosen ManagementIPv6, immediately before invoking `calico-node.exe -startup`. SLAAC re-adds them from the next RA, so the host NIC keeps its full set of auto-configured addresses (ULA + GUA + link-local) in steady state.

**Why it sometimes worked**: when the RA period was long and HNS's NIC scan happened during a SLAAC gap, HNS picked the chosen ManagementIPv6.

**Why it broke**: VyOS DHCPv6-PD prefix rotations got more frequent (every few minutes), which means more RAs, which means smaller SLAAC gaps. The HNS scan window (~5–10s) is now usually wide enough for SLAAC to re-add the GUA before HNS picks. End state: HNS pinned the GUA more often than the ULA, and the prefix that HNS pinned changed every rotation.

The strip ALSO has a structural problem: it runs in `node-service.ps1` once, before `calico-node.exe -startup`. The actual HNS recreate happens 30+ seconds later inside `calico-node`, by which time SLAAC has re-added everything.

## Approach 2 — Re-strip from Go right before `api.Create`

Added in `cni-plugin/pkg/dataplane/windows/hns_types.go::ensureNetworkExistsWithAPI` via a new HNSNetworkAPI method `StripNonDesiredHostIPv6`. Calls `Remove-NetIPAddress` via `winutils.Powershell` immediately before `api.Create`, much closer to the moment HNS scans the NIC.

**Why it didn't fix the race**: HNS scans asynchronously AFTER Create returns. The strip happens before Create. SLAAC has the entire ~5–10s post-Create async-scan window to re-add the GUA. We always lost.

## Approach 3 — Post-create poll-and-retry

Added a verify loop after Create: poll `Get-HnsNetwork` for up to 15s, re-stripping every 3s, deleting and retrying up to 3× if HNS pinned the wrong address. Tuning vars `createMgmtIPv6Retries`, `createMgmtIPv6PollSteps`, `createMgmtIPv6PollSleep`.

**Why it didn't fix the race**: HNS only scans the NIC ONCE per Create (or once per service restart). Re-stripping mid-poll doesn't make HNS re-scan; it just keeps the NIC clean while HNS is already done with its single scan. The retry path with Delete + re-Create did force fresh scans, but each scan still hit the same race window.

## Approach 4 — Disable RouterDiscovery during create + restore in defer

Added `DisableHostIPv6RouterDiscovery` / `RestoreHostIPv6RouterDiscovery` to the API. Wrap the create+verify block in `Disable → defer Restore`. While disabled, SLAAC won't process incoming RAs, so addresses we strip stay gone.

**Why it didn't fix anything**: `Set-NetIPInterface -RouterDiscovery Disabled` against `vEthernet (Ethernet)/IPv6` failed with `No matching MSFT_NetIPInterface objects found` — the StandardCimv2 WMI provider is unreliable in HostProcess containers when HNS is touching the vNIC. Internal 5× retry helped slightly but still failed often. When it did succeed, HNS sometimes pinned the link-local instead of the desired IPv6.

`netsh interface ipv6 set interface ... routerdiscovery=` works (different API path, no WMI provider involvement) but only if invoked with the absolute path `C:\Windows\System32\netsh.exe` because HostProcess containers don't have System32 in PATH. Even with that fix, HNS's pick was inconsistent — the GUA flickered back during HNS's multi-second async scan.

## Approach 5 — DLL hook on `iphlpapi!GetAdaptersAddresses` in svchost-hns (current)

A small Windows DLL (`cni-plugin/cmd/hns-ipv6-hook/hook.c`, cross-compiled with mingw-w64) installed as a 14-byte absolute-JMP trampoline at the start of `iphlpapi!GetAdaptersAddresses` inside the svchost process hosting the HNS service. The detour calls the real function, then walks the returned `IP_ADAPTER_UNICAST_ADDRESS` lists and unlinks every IPv6 unicast that doesn't equal the operator-chosen address (kept in `C:\CalicoWindows\hns-ipv6-hook.cfg`).

The injector (`cni-plugin/cmd/hns-ipv6-injector/main.go`) resolves the svchost-hns PID via the SCM, opens the process with `OpenProcess(PROCESS_CREATE_THREAD|PROCESS_VM_OPERATION|PROCESS_VM_READ|PROCESS_VM_WRITE|PROCESS_QUERY_INFORMATION)`, allocates remote memory for the DLL path string, writes it, calls `CreateRemoteThread → kernel32!LoadLibraryW`. DllMain installs the trampoline.

Win Server 2022 build 20348 only — the trampoline's prologue parser accepts exactly the byte sequence at `iphlpapi!GetAdaptersAddresses` for that build:

```
48 89 5C 24 18    mov [rsp+0x18], rbx     (5)
55 56 57 41 56 41 57 push rbp/rsi/rdi/r14/r15 (7)
48 8B EC          mov rbp, rsp            (3)
```

15 bytes of position-independent instructions, comfortably fitting our 14-byte FF 25 absolute JMP. PDB GUID for the verified version: `D75200CB36A54032D8E8E6479C933D1E` age 1.

**Why it works deterministically**: HNS only knows what `GetAdaptersAddresses` tells it. The hook intercepts at the system-call boundary, so HNS literally cannot see any address other than the chosen one (and link-local) regardless of NIC state, SLAAC timing, RA period, or anything else.

**Why we briefly abandoned it (and how the lifecycle fix puts that to rest)**:

The first deploy of the hook injected the DLL into the running svchost-hns and left it there. Subsequent calico-node restarts re-ran the injector, but the DLL stayed loaded across calico-node lifetimes — and earlier-build DLL bytes stayed resident in svchost-hns memory long after we'd updated the hook code. `Copy-Item` over the DLL on disk failed because svchost-hns held a handle. Stale code accumulated. HCS/HNS misbehaved.

The fix is a clean lifecycle on every calico-node startup:

1. `Restart-Service hns -Force` → kills svchost-hns and the loaded DLL with it. Brief (~5s) HNS interruption; existing pods reattach when the service comes back.
2. Wait for the service to return to Running with a new PID.
3. Inject the freshly-built DLL into the new svchost-hns.

Result: the hook is always the current-build version. No stale code paths. No file-locking fights with `Copy-Item`. The DLL on disk is always replaceable because the previous svchost-hns has been killed.

## Lifecycle diagram

```
calico-node-windows pod restart
  └→ node-service.ps1 startup
       ├→ Apply-WeakHost (early, idempotent)
       ├→ Install hook artifacts to C:\opt\calico-hns-ipv6\ (host-visible path)
       ├→ Resolve desired ManagementIPv6 from
       │    CALICO_DESIRED_HNS_MGMT_IPV6, or
       │    IP6_AUTODETECTION_METHOD=cidr=<prefix> + NIC's existing in-prefix address
       ├→ Restart-Service hns
       │    └─ kills svchost-hns, evicts old DLL
       ├→ Wait for hns service Running (new PID)
       ├→ hns-ipv6-injector.exe -desired-mgmt-ipv6 <ip> -dll <path>
       │    └─ OpenProcess + WriteProcessMemory + CreateRemoteThread → LoadLibraryW
       │    └─ DllMain installs FF 25 JMP at iphlpapi!GetAdaptersAddresses
       ├→ calico-node.exe -startup
       │    └─ ensureNetworkExistsWithAPI
       │         └─ HNS scans NIC via the hooked GetAdaptersAddresses
       │              └─ HNS sees only the desired ManagementIPv6 + link-local
       │              └─ HNS pins the desired ManagementIPv6
       └→ Apply-WeakHost (post-startup, after recreate resets WeakHost)
```

## Configuration

| Env var (configmap key) | Effect |
|---|---|
| `CALICO_DESIRED_HNS_MGMT_IPV6` | Exact IPv6 (no prefix) for HNS to pin. Most explicit — overrides everything else. |
| `IP6_AUTODETECTION_METHOD=cidr=<prefix>` | Used to auto-derive the desired ManagementIPv6 by picking the first address on `vEthernet (Ethernet*)` matching the prefix. Same value calico-node uses for BGP source autodetect. |
| `CALICO_HNS_IPV6_HOOK=false` | Disables the hook and falls back to the strip-only path (race-prone — only useful for debugging or older Win builds where the prologue differs). |

Everything else inherits from the existing dual-stack configmap.

## Files

| Path | What |
|---|---|
| `cni-plugin/cmd/hns-ipv6-hook/hook.c` | The detour DLL. Cross-compiled by mingw-w64 in the Dockerfile-windows builder stage. |
| `cni-plugin/cmd/hns-ipv6-hook/probe.c` | Self-test harness: dumps `GetAdaptersAddresses` before and after `LoadLibrary`-ing the hook DLL, in its own process. |
| `cni-plugin/cmd/hns-ipv6-injector/main.go` | SYSTEM-privileged Win64 injector. SCM PID lookup, `OpenProcess`/`VirtualAllocEx`/`WriteProcessMemory`/`CreateRemoteThread`. Win 2022 build gate. |
| `node/windows-packaging/CalicoWindows/node/node-service.ps1` | Startup script. Owns the `Restart-Service hns` + invoke-injector lifecycle. |
| `node/Dockerfile-windows` | Multi-stage: Linux mingw builder for the DLL, Windows nanoserver final stage. `--build-context hooksrc=cni-plugin/cmd/hns-ipv6-hook` exposes the C source to the builder stage. |

## Marker pair tracking (2026-05-05 fix)

The first version of `Inject-HnsMgmtIpHook` used a marker file (`C:\opt\calico-hns-ipv6\injected.flag`) recording only a timestamp: if the file's mtime was newer than the OS last-boot time, skip `Restart-Service hns + re-inject` to avoid bricking a working bridge. That heuristic is fine when the desired ManagementIP/ManagementIPv6 pair stays constant for the lifetime of the boot.

It is wrong when the pair changes mid-boot. Concrete failure mode observed on the qemu lab (Windows Server 2022 build 20348.5020):

1. Boot. NIC briefly carries a RandomizeIdentifiers-generated IPv6 like `fd5a:8000:1:0:cf95:b85d:32e:531f`.
2. First `calico-node` pod injects the hook with desired = `fd5a:8000:1:0:cf95:b85d:32e:531f`. Marker mtime > boot time.
3. `node-service.ps1` later runs `Set-NetIPInterface -RandomizeIdentifiers Disabled` (commit `23c23dfd80`, "Stable SLAAC"). NIC rotates to stable EUI-64 `fd5a:8000:1:0:5054:ff:fe01:2345`. The randomized address is gone.
4. A subsequent `calico-node` container restart reads the marker, sees mtime > boot time, returns `'skip'`. Hook stays loaded with the now-stale desired IPv6.
5. `calico-node.exe -startup` calls HNS with the *new* desired pair. HNS scans the NIC via the hooked `iphlpapi!GetAdaptersAddresses`. The hook unlinks every IPv6 unicast that doesn't equal the *old* address. The NIC no longer has that address, so HNS sees zero IPv6 unicasts and rejects the dual-stack create with `HCN_E_ADAPTER_NOT_FOUND (0x803b0006)`.

Fix: encode the desired pair in the marker file as `<v4>\t<v6>`. `Test-HnsMgmtIpHookMarker` (in `calico.psm1`) returns one of:

- `inject-fresh` — no marker file; first injection.
- `reinject-stale` — marker predates the last boot, or no boot time available.
- `reinject-no-bridge` — marker exists but no Calico HNS network is up; safe to re-inject (no working bridge to destroy).
- `reinject-mismatch` — marker records a different desired pair than the current one; re-inject so the hook reflects the live NIC.
- `skip` — marker matches AND the Calico bridge is up; preserve the working state.

The Pester tests in `node/windows-packaging/tests/calico.tests.ps1` cover each branch including the exact qemu scenario above. Running `Invoke-Pester` against that file should be the first sanity check before changing the marker logic again.

## Why this is the right call and not over-engineering

The DLL hook is the ONLY approach that doesn't fight against either:
- HNS's async NIC scan timing
- HostProcess WMI provider unreliability
- SLAAC RA timing
- Server 2022 vSwitch teardown/recreate windows

Every PowerShell-only and Go-only alternative has been tried and fails on at least one of those four. The hook intercepts at the system-call layer that HNS uses, so it can't be raced. The price (a small C DLL injected into svchost-hns) is auditable, reproducibly built from source by the Dockerfile, and gated on a known Windows build version.
