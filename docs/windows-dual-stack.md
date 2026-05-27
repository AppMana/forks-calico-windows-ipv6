# Windows Dual-Stack IPv6 for Calico BGP Mode

## Overview

This fork adds IPv6 dual-stack support to the Windows calico-node for the `windows-bgp` (L2Bridge) networking backend. Windows Server 2022 (build 20348+) supports dual-stack L2Bridge networks when both IPv4 and IPv6 subnets are provided at network creation time.

A typical use case is assigning publicly routable IPv6 addresses from an ISP allocation to pods. Since BGP advertises the pod subnets to the upstream router, pods become directly reachable over IPv6 without NAT.

VXLAN dual-stack is not supported by HNS and is out of scope.

## Requirements

- Windows Server 2022, build 20348.2031 or later
- Kubernetes 1.23+ with dual-stack feature gate enabled
- Calico v3.29.6 base
- `windows-bgp` networking backend (not `vxlan`)
- Routable IPv6 address on each Windows node's physical interface (SLAAC or static)
- BGP router (VyOS, FRR, etc.) configured for IPv6 peering
- An IPv6 prefix to use for pod addressing (e.g. a /48 from your ISP delegation)

## Cluster Configuration

### 1. Create an IPv6 IPPool

Use a publicly routable prefix delegated from your ISP or upstream provider. The pool does not need `natOutgoing` since pods will have globally routable addresses. Use `nodeSelector` to restrict which nodes get IPv6 blocks if you only want specific workloads to receive IPv6.

```yaml
apiVersion: projectcalico.org/v3
kind: IPPool
metadata:
  name: public-ipv6
spec:
  cidr: "2001:db8:abcd::/48"      # Replace with your ISP-delegated prefix
  blockSize: 64
  natOutgoing: false
  nodeSelector: "ipv6-pool == 'public'"
```

To assign IPv6 only to specific pods, annotate them:
```yaml
cni.projectcalico.org/ipv6pools: '["public-ipv6"]'
```

### 2. Deploy the calico-node-windows DaemonSet

The fork ships a ready-to-apply manifest at [`manifests/calico-windows-bgp-dualstack.yaml`](../manifests/calico-windows-bgp-dualstack.yaml). Replace the `IMAGE` placeholders with your built `calico-node-windows` image and apply:

```bash
kubectl apply -f manifests/calico-windows-bgp-dualstack.yaml
```

The manifest contains a `ConfigMap` (`calico-windows-config`) and a `DaemonSet` (`calico-node-windows`) with three containers — `node`, `felix`, `confd`. It uses the HostProcess pattern (`securityContext.windowsOptions.hostProcess: true`, `runAsUserName: "NT AUTHORITY\\system"`) so the pod has the privileges required to drive HNS, restart the `hns` service, install CNI binaries to the host filesystem, and inject the `hns-ipv6-hook` DLL into `svchost-hns`. No init container, no init script on the host, no separate Calico install zip — `node-service.ps1` mirrors the in-sandbox `CalicoWindows` tree to `C:\CalicoWindows` on the host on every container start, so a fresh node only needs the right kubelet and containerd configuration before this DaemonSet schedules.

For the full RBAC / ServiceAccount set, the manifest reuses the `calico-node` SA that the upstream Calico Linux install (`manifests/calico.yaml`, `manifests/calico-typha.yaml`, or the operator) already provisions. Don't fork a separate set unless you also fork the operator's RBAC.

The exact in-pod startup sequence is documented in `docs/dual-stack-internals.md` § *node container boot*; the highlights are: token refresher → `Inject-HnsMgmtIpHook` → `Remove-BrokenCalicoHnsNetwork` → `External` placeholder L2Bridge → `calico-node.exe -startup` → kubelet-watch loop. See `docs/hns-managementipv6-history.md` for why the hook exists.

### 3. ConfigMap reference

Every key in `calico-windows-config` is consumed either by `node-service.ps1` (PowerShell), `calico.psm1::Build-CNIConfigSubstitutions` (CNI conf rendering), or `calico-node.exe` itself (Felix / confd). Keys not listed in the table are passed through as Felix env vars (`FELIX_*`) or Calico startup env vars and follow the upstream semantics.

| Key | Required | Default if unset | What it controls |
|---|---|---|---|
| `CALICO_NETWORKING_BACKEND` | yes | `vxlan` (config-hpc.ps1) | `windows-bgp` for L2Bridge + RRAS BGP, `vxlan` for VXLAN, `none` to disable Calico CNI. IPv6 dual-stack requires `windows-bgp`. |
| `KUBERNETES_SERVICE_HOST` | yes | (none) | Apiserver Service IP. HostProcess pods can't rely on kube-proxy's CNI hop at the moment they boot. |
| `KUBERNETES_SERVICE_PORT` | yes | (none) | Apiserver port. |
| `KUBECONFIG` | yes | (none — must be explicitly set in HPC mode) | Path that 10-calico.conf's `kubeconfig` field is rendered to. The CNI plugin (calico.exe) reads its kubeconfig from this path. **Must equal the path the in-pod token refresher writes** — see `CALICO_CNI_KUBECONFIG_PATH`. Default rendering: `c:\etc\cni\net.d\calico-kubeconfig`. |
| `K8S_SERVICE_CIDR` | yes | (none) | Service ClusterIP CIDR. Rendered into 10-calico.conf as `serviceCIDR`. |
| `DNS_NAME_SERVERS` | yes | (none) | Cluster DNS server IP(s), comma-separated. Rendered into 10-calico.conf. |
| `CNI_BIN_DIR` | yes | (none) | Host directory where containerd looks for CNI binaries. `node-service.ps1` copies `calico.exe` and `calico-ipam.exe` from the sandbox to this path on every container start. Must match containerd's `cni.bin_dir`. |
| `CNI_CONF_DIR` | yes | (none) | Host directory where containerd looks for CNI conf files. `node-service.ps1` writes `10-calico.conf` here. Must match containerd's `cni.conf_dir`. |
| `FELIX_IPV6SUPPORT` | for dual-stack | `false` | Master switch for the fork's IPv6 code path. Without it, behaviour is identical to upstream IPv4-only. |
| `IP` | yes | `autodetect` | IPv4 source for BGP peer / HNS ManagementIP. Setting it to a bare address pins it; `autodetect` defers to `IP_AUTODETECTION_METHOD`. |
| `IP_AUTODETECTION_METHOD` | yes | `first-found` | How Felix picks the IPv4. `cidr=<prefix>` is recommended on multi-NIC hosts; `first-found` and `interface=<regex>` follow upstream semantics. |
| `IP6` | for dual-stack | (none) | IPv6 source for BGP peer / HNS ManagementIPv6. Set to `autodetect` and pair with `IP6_AUTODETECTION_METHOD`. |
| `IP6_AUTODETECTION_METHOD` | for dual-stack | `first-found` | How Felix picks the IPv6. `cidr=<prefix>` is the only sensible choice on hosts that carry multiple IPv6 prefixes (link-local + ULA + GUA + RA-injected). |
| `CALICO_DSR_DISABLE` | no | (unset → DSR enabled if OS supports it) | Set to `"true"` to disable CNI loopback DSR. Required in mixed Linux/Windows clusters where Linux pods reply to ClusterIP traffic with their pod IP as source. |
| `CALICO_HNS_IPV6_HOOK` | no | (unset → hook ENABLED) | Set to `"false"` to disable the iphlpapi `GetAdaptersAddresses` hook and fall back to PowerShell strip-only ManagementIPv6 pinning. Race-prone — only useful for debugging, see `docs/hns-managementipv6-history.md`. |
| `CALICO_DESIRED_HNS_MGMT_IPV4` | no | derived from `IP_AUTODETECTION_METHOD` | Explicit override for the IPv4 the hook pins. Takes precedence over autodetection. |
| `CALICO_DESIRED_HNS_MGMT_IPV6` | no | derived from `IP6_AUTODETECTION_METHOD` | Explicit override for the IPv6 the hook pins. Takes precedence over autodetection. |
| `CALICO_HNS_MGMT_ADDRESS_INTERFACE_PREFERENCE` | no | `vEthernet (Ethernet),Ethernet,vEthernet (Ethernet*),Ethernet*,vEthernet (Calico*)` | Comma-separated PowerShell wildcard patterns used to rank candidate interfaces when deriving the desired HNS ManagementIP / ManagementIPv6. The default prefers the canonical management vNIC after Hyper-V moves the host address, falls back to the physical management NIC before vSwitch creation, then considers numbered and Calico fallback vNICs. |
| `CALICO_CNI_KUBECONFIG_PATH` | no | `/host/etc/cni/net.d/calico-kubeconfig` | In-sandbox path the calico-cni-plugin SA token refresher writes its kubeconfig to. Pair with `KUBECONFIG` (which is in the host's view) — they MUST resolve to the same file. |
| `CALICO_NODENAME_FILE_HOST_PATH` | no | `C:\CalicoWindows\nodename` | Host path where `calico-node.exe -startup` writes the Kubernetes node name. Rendered into 10-calico.conf as `nodename_file`; the CNI plugin reads it from there at every CNI ADD. |
| `CALICO_HOST_INSTALL_DIR` | no | `C:\CalicoWindows` | Host directory where `node-service.ps1` mirrors the in-sandbox `CalicoWindows` tree (config.ps1, libs, calico-kube-config.template, hooks). Anything that runs on the host and expects the legacy install layout reads from here. |
| `CALICO_HNS_HOOK_INSTALL_DIR` | no | `C:\opt\calico-hns-ipv6` | Host directory where `node-service.ps1` writes `hns-ipv6-hook.dll`, `hns-ipv6-injector.exe`, and the `injected.flag` marker. Per-file overrides below take precedence. |
| `CALICO_HNS_HOOK_DLL_PATH` | no | `<install-dir>\hns-ipv6-hook.dll` | Per-file override for the DLL path. Useful when WDAC or code-integrity rules constrain where signed DLLs may live. |
| `CALICO_HNS_HOOK_INJECTOR_PATH` | no | `<install-dir>\hns-ipv6-injector.exe` | Per-file override for the injector path. |
| `CALICO_HNS_HOOK_MARKER_PATH` | no | `<install-dir>\injected.flag` | Per-file override for the desired-pair marker. |
| `CALICO_HNS_HOOK_CFG_PATH` | no | `C:\CalicoWindows\hns-ipv6-hook.cfg` | Path the injector writes the cfg file to and the DLL reads from at `DllMain`. **Changing this requires a matching DLL rebuild** with `-DDEFAULT_CFG_PATH=...` because `svchost-hns` (where the DLL ends up loaded) doesn't inherit env vars from the injector. |
| `CALICO_HNS_HOOK_LOG_PATH` | no | resolved as: explicit override → `<$CALICO_LOG_DIR>\hook.log` → `C:\var\log\calico\hook.log` | Where the DLL appends its diagnostic log. The injector writes the resolved path into the cfg file as `log=<path>`; the DLL reads it at `DllMain` and creates parent dirs on first write. No DLL rebuild needed. |

### 4. Windows host paths reference

Every path that calico-node-windows touches on the host filesystem, where it comes from, and what reads it.

| Path | Set by | Read by | Default value | Override |
|---|---|---|---|---|
| `C:\opt\cni\bin\calico.exe`, `calico-ipam.exe` | `node-service.ps1` (copied from sandbox at every container start) | containerd → CNI plugin (`calico.exe`) | hardcoded `C:\opt\cni\bin` in containerd config | configmap `CNI_BIN_DIR` (must match containerd's `cni.bin_dir`) |
| `C:\etc\cni\net.d\10-calico.conf` | `node-service.ps1` → `Write-CNIConfig` (calico.psm1) | containerd → kubelet | hardcoded in containerd config | configmap `CNI_CONF_DIR` (must match containerd's `cni.conf_dir`) |
| `C:\etc\cni\net.d\calico-kubeconfig` | `calico-node.exe -monitor-token` (token refresher) | calico CNI plugin (calico.exe) reading 10-calico.conf's `kubeconfig` field | `/host/etc/cni/net.d/calico-kubeconfig` (in-sandbox view), maps to `C:\etc\cni\net.d\calico-kubeconfig` on host | configmap `CALICO_CNI_KUBECONFIG_PATH` (in-sandbox path) + `KUBECONFIG` (host path); they must agree |
| `C:\CalicoWindows\` (full mirror of the in-sandbox `/CalicoWindows` tree) | `node-service.ps1` (robocopy on every container start) | helper scripts that expect the legacy non-HPC install layout (e.g. `start-calico.ps1`, `uninstall-calico.ps1`, debugging tools) | `C:\CalicoWindows` | configmap `CALICO_HOST_INSTALL_DIR` |
| `C:\CalicoWindows\calico-kube-config.template` | mirror copy from sandbox | (debugging only) | (read-only) | n/a |
| `C:\CalicoWindows\nodename` | `calico-node.exe -startup` writes the Kubernetes node name | calico CNI plugin reads via `nodename_file` field of 10-calico.conf | `C:\CalicoWindows\nodename` | configmap `CALICO_NODENAME_FILE_HOST_PATH` |
| `C:\opt\calico-hns-ipv6\` (install dir for DLL + injector + marker) | `node-service.ps1` creates and populates from sandbox at every container start | `Inject-HnsMgmtIpHook`, debugging tools | `C:\opt\calico-hns-ipv6` | configmap `CALICO_HNS_HOOK_INSTALL_DIR` (per-path overrides below also accepted) |
| `<install-dir>\hns-ipv6-hook.dll` | `node-service.ps1` copies from sandbox | `hns-ipv6-injector.exe` calls `LoadLibraryW` in `svchost-hns` to inject this DLL | `C:\opt\calico-hns-ipv6\hns-ipv6-hook.dll` | configmap `CALICO_HNS_HOOK_DLL_PATH` |
| `<install-dir>\hns-ipv6-injector.exe` | `node-service.ps1` copies from sandbox | invoked synchronously by `Inject-HnsMgmtIpHook` | `C:\opt\calico-hns-ipv6\hns-ipv6-injector.exe` | configmap `CALICO_HNS_HOOK_INJECTOR_PATH` |
| `<install-dir>\injected.flag` | `Inject-HnsMgmtIpHook` writes `<v4>\t<v6>` before `Restart-Service hns` | `Test-HnsMgmtIpHookMarker` on next pod start decides skip vs. re-inject | `C:\opt\calico-hns-ipv6\injected.flag` | configmap `CALICO_HNS_HOOK_MARKER_PATH` |
| `C:\CalicoWindows\hns-ipv6-hook.cfg` | `hns-ipv6-injector.exe` writes desired addresses + log path here | `hns-ipv6-hook.dll` reads at `DllMain` (compile-time `DEFAULT_CFG_PATH`) | `C:\CalicoWindows\hns-ipv6-hook.cfg` | configmap `CALICO_HNS_HOOK_CFG_PATH` — note: changing this requires a matching DLL rebuild with `-DDEFAULT_CFG_PATH=...` because `svchost-hns` doesn't inherit env vars from the injector |
| `<hook log dir>\hook.log` | `hns-ipv6-hook.dll` appends every diagnostic line here | troubleshooting only — auto-creates parent dirs on first write | resolved by precedence: 1) `$CALICO_HNS_HOOK_LOG_PATH` if set 2) else `<$CALICO_LOG_DIR>\hook.log` if `CALICO_LOG_DIR` is set (this honours the existing Calico Windows log-dir convention used by NSSM-installed legacy services — `calico-node.log`, `calico-felix.log`, `calico-confd.log` all live in that directory) 3) else `C:\var\log\calico\hook.log` (matches Linux `/var/log/calico` convention) | configmap `CALICO_HNS_HOOK_LOG_PATH` to override the resolved value; the injector relays the resolved path to the DLL via a `log=<path>` line in the cfg file (no DLL rebuild needed) |

### 5. Configure BGP router for IPv6

Example for VyOS — the cluster's "router" peers with each Windows node over IPv6, accepts the per-node `/64` (or smaller) pod prefix, and re-advertises it upstream:

```
# Apply your IPv6 prefix and replace the neighbor placeholders.

# 1. Tell BGP what local IPv6 networks to originate.
set protocols bgp 64512 address-family ipv6-unicast network 2001:db8:abcd::/48

# 2. Peer with each Windows node over its routable IPv6.
set protocols bgp 64512 neighbor 2001:db8:abcd::100 remote-as 64512
set protocols bgp 64512 neighbor 2001:db8:abcd::100 address-family ipv6-unicast
set protocols bgp 64512 neighbor 2001:db8:abcd::100 update-source <vyos-ipv6>

# 3. Same for any Linux nodes running calico-node — Calico's bird template
#    handles its end automatically.
```

For FRR / BIRD / other RRs, the equivalent is: enable `address-family ipv6-unicast`, configure each Windows node as a neighbor at its IPv6 (the same one Felix autodetects via `IP6_AUTODETECTION_METHOD`), and accept the prefix advertised by Calico's confd-emitted policies. RRAS limitations (no `next hop keep`, no per-prefix export filter) are documented in §"RRAS BGP CIM Interface".

## How It Works

### Node Startup

When `FELIX_IPV6SUPPORT=true` and the backend is `windows-bgp`:

1. `ipv6Supported()` returns true (reads `FELIX_IPV6SUPPORT` env var)
2. `ensureNetworkForOS()` sets both `HostReservedAttrIPv4s` and `HostReservedAttrIPv6s` on the IPAM `BlockArgs`
3. `EnsureBlock()` allocates both an IPv4 and IPv6 block for the node
4. `EnsureNetworkExists()` creates the HNS L2Bridge network with both subnets in a single JSON request
5. `CreateAndAttachHostEP()` sets the IPv6 address on the host endpoint (Calico_ep)
6. The Calico_ep IPv6 default route is removed to prevent conflict with the SLAAC default route

The HNS network JSON includes `"IPv6": true` to enable NDP on the L2Bridge vSwitch.

### CNI Plugin (Pod Networking)

When a pod is created:

1. `DoNetworking()` iterates all IPs from the IPAM result, separating IPv4 and IPv6
2. `SetupL2bridgeNetwork()` is called with BOTH `subNet` and `subNetV6`. This is critical: passing nil for IPv6 would cause the existing dual-stack network to be flagged as mismatched
3. `EnsureNetworkExists()` finds the existing dual-stack network matches and reuses it (no recreation)
4. The HCN endpoint is created with both `IpConfigurations` and dual-stack `Routes` (0.0.0.0/0 and ::/0)
5. IPv6 CIDRs are excluded from the OutBoundNAT policy (HNS rejects IPv6 in OutBoundNAT)

### Felix (Policy Enforcement)

- The polling loop (`loopPollingForInterfaceAddrs`) tracks only **IPv4** addresses for change detection. IPv6 addresses fluctuate when HNS creates new vSwitch endpoints for pods. If IPv6 were included, every pod creation would trigger `markAllEndpointForRefresh()`, reprogramming HNS ACLs on every existing pod and causing TCP RSTs (a known HNS limitation).
- IPv6 addresses for host-to-endpoint ACL rules are fetched on demand via `getCurrentIPv6Addrs()` at rule-build time, so the ACL content is always up-to-date without triggering full-endpoint reprograms.
- `nodeToEndpointRules()` returns separate IPv4 and IPv6 ACL rules. Combining them in a single rule caused `ERROR_BUFFER_OVERFLOW` (0x6f) in HNS.
- `CompleteDeferredWork()` resolves workloads by IPv4 first, falls back to IPv6.
- Policy rules: `ipVersion` is set to 0 (dual-stack), so `filterNets()` passes all CIDRs.

### Confd/BGP

- `blocks.ps1.template` emits `$blocks_v6` from `/host/<node>/ipv6/block`
- `peerings.ps1.template` emits `$local_ipv6` from `/host/<node>/ip_addr_v6`
- `ProcessBgpRouterIPv6` calls `Set-BgpRouter -IPv6Routing Enabled -LocalIPv6Address $local_ipv6 -Force`
- `ProcessBgpBlocks` accepts both `$blocks` and `$blocks_v6`, advertises all via `Add-BgpCustomRoute`
- `ProcessBgpIPv4NextHopPolicies` / `ProcessBgpIPv6NextHopPolicies` set the correct next-hop for locally-originated routes (the node's management IP for IPv4, SLAAC address for IPv6)

### Route Advertisement (DenyMeshEgress)

On Linux, BIRD uses `next hop keep;` to preserve original next-hops when re-advertising iBGP routes to eBGP peers, and `reject;` in the export filter to only advertise locally-originated routes. RRAS has no equivalent to either.

Without intervention, RRAS re-advertises all mesh-learned routes to eBGP peers (e.g. VyOS) with itself as next-hop. This creates routing loops: VyOS receives the same prefix from multiple nodes with wrong next-hops.

The fix uses a Deny policy that matches mesh peer next-hops:

```powershell
# Collect all iBGP mesh peer IPs (these are the next-hops on mesh-learned routes).
$meshNextHops = @("192.0.2.4", "192.0.2.60")  # other nodes in the mesh
Add-BgpRoutingPolicy -Name "DenyMeshEgress" -PolicyType Deny -MatchNextHop $meshNextHops -Force
Add-BgpRoutingPolicyForPeer -PeerName $eBGPPeer -PolicyName "DenyMeshEgress" -Direction Egress
```

This denies routes whose next-hop matches an iBGP mesh peer on eBGP egress. Locally-originated custom routes (the node's own IPAM blocks) have no remote next-hop and pass through, getting the correct next-hop set by the `SetNH4_`/`SetNH6_` ModifyAttribute policies.

The DenyMeshEgress policy is updated by confd whenever the mesh peer list changes.

### HNS Network Lifecycle

The HNS L2Bridge network cannot have subnets added or removed dynamically (microsoft/hcsshim#786). Deleting the network destroys all existing pod endpoints. Therefore:

- **On rollout (DaemonSet restart, same boot)**: `EnsureNetworkExists()` finds the existing network. If subnets don't match (e.g. IPv4-only but dual-stack requested), it logs a warning and keeps the existing network. Pods continue working. The correct dual-stack network is created on the next reboot.
- **On reboot**: `node-service.ps1` cleans up all non-NAT HNS networks. Calico-node creates a fresh dual-stack network.

`node-service.ps1` creates a placeholder "External" L2Bridge to trigger vSwitch creation before calico-node starts. Calico-node then replaces it with the "Calico" network. This two-step process is required because calico-node needs a working management IP (which requires the vSwitch) before it can start.

## RRAS BGP CIM Interface

The PowerShell BGP cmdlets (`Add-BgpRouter`, `Add-BgpPeer`, `Add-BgpRoutingPolicy`, etc.) are CDXML wrappers around WMI/CIM classes in the `root/Microsoft/Windows/RemoteAccess` namespace. The CDXML definitions are at `C:\Windows\System32\WindowsPowerShell\v1.0\Modules\RemoteAccess\PS_Bgp*.cdxml`.

Key CIM classes:
- `BgpRouterConfig`: Router ID, ASN, IPv6 routing state
- `BgpPeerConfig`: Peer address, ASN, ingress/egress policy lists, connectivity status
- `BgpRoutingPolicyConfig`: Policy name, type, match criteria, actions
- `BgpCustomNetworkInfo`: Locally-originated routes (custom routes)
- `PS_BgpRoutingPolicy`: CIM methods for Add/Get/Set/Remove

### Undocumented/Underused Parameters

`Add-BgpRoutingPolicy` supports several match criteria beyond `MatchPrefix`:

| Parameter | Type | Use |
|-----------|------|-----|
| `MatchNextHop` | IPAddress[] | Match routes by their BGP next-hop address |
| `MatchASNRange` | UInt32[] | Match routes by AS path |
| `MatchCommunity` | String[] | Match routes by BGP community string |
| `AddCommunity` | String[] | Tag routes with a community on match |
| `IgnorePrefix` | String[] | Prefixes to exclude from matching |

These could enable more sophisticated route filtering in the future:
- Tag locally-originated routes with a community via `AddCommunity`, then use `MatchCommunity` on egress to only allow tagged routes
- Use `MatchNextHop` to deny routes with specific mesh peer next-hops
- Use `MatchASNRange` to filter by AS path length

### Known RRAS Limitations

- `Set-BgpRoutingPolicy` silently drops IPv6 prefixes from `MatchPrefix` when updating. Use remove + re-add instead.
- `Add-BgpRoutingPolicy -PolicyType Deny` without any `MatchPrefix` fails ("Invalid handle"). Use wildcard `0.0.0.0/0` + `::/0` instead.
- RRAS BGP sessions may not exchange routes after a service restart until `Restart-Service RemoteAccess` is called.
- BGP state (peers, policies, custom routes) persists in the RRAS service's internal state across restarts but is lost on `Remove-BgpRouter`.
- `Set-BgpRouter -IPv6Routing Enabled` hangs without `-Force` in non-interactive sessions.

### Future: Declarative CIM Configuration

The CIM interface could be used to make BGP configuration more declarative. Instead of imperative PowerShell cmdlets, render the desired state as CIM instances and apply atomically. This would be analogous to how confd renders BIRD config files on Linux:

1. Query current CIM state (`BgpPeerConfig`, `BgpRoutingPolicyConfig` instances)
2. Compute desired state from confd template data
3. Diff and apply changes (add new, remove stale, update changed)

This would eliminate the need for `config-bgp.ps1` and make the configuration model closer to BIRD's declarative approach.

## HNS Limitations

Tested on Windows Server 2022 build 20348.4773:

| Operation | Result |
|-----------|--------|
| Dual-stack L2Bridge (v4+v6 subnets together) | Works |
| IPv6-only L2Bridge | Fails ("adapter not found") |
| Adding IPv6 subnet to existing v4-only network | Not supported (must recreate) |
| Deleting and recreating network | Destroys all pod endpoints |
| `Set-BgpRouter -IPv6Routing Enabled` without `-Force` | Hangs in non-interactive |
| `Add-BgpRoutingPolicy` with IPv6 prefix | Works |
| `Set-BgpRoutingPolicy -MatchPrefix` with IPv6 | Silently drops IPv6 prefixes |
| Felix HNS ACL reprogram | Causes TCP RSTs on existing connections |
| Combined IPv4+IPv6 in single ACL RemoteAddresses | ERROR_BUFFER_OVERFLOW (0x6f) |

## Building

Use the build script which handles cross-compilation, dependency download, and remote BuildKit push:

```bash
./bin/build-and-push.sh                    # tags: v3.29.6-dualstack + commit SHA
./bin/build-and-push.sh --tag custom       # custom tag + SHA
./bin/build-and-push.sh --no-push          # build only
```

The script:
1. Cross-compiles calico-node.exe, calico.exe, calico-ipam.exe (Linux to windows/amd64)
2. Copies confd scripts from the fork
3. Downloads nssm.exe and hns.psm1 (cached)
4. Fetches BuildKit mTLS certs from Kubernetes
5. Builds via remote Windows BuildKit and pushes to Harbor

## Testing

```bash
# All Go tests on Linux (cross-platform mocks)
go test ./felix/dataplane/windows/... ./cni-plugin/pkg/dataplane/windows/... ./cni-plugin/pkg/ipamplugin/...

# PowerShell Pester tests (Linux or Windows)
pwsh -Command "Import-Module Pester; Invoke-Pester -Path confd/windows-packaging/tests/ -Output Detailed"

# Full cluster health check (all Windows nodes, cross-product pod-to-pod)
bash hacking/ipv6-health-check.sh win-node-1 win-node-2 win-node-3
```

Go tests cover:
- HNS network creation, recreation, External cleanup (`ensure_network_test.go`)
- HNS endpoint creation with IPv6 (`host_endpoint_test.go`)
- Network subnet matching (`network_needs_recreate_test.go`)
- IPAM pool filtering for NAT exclusion (`ipam_pools_test.go`)
- IPv6 address calculation (`getnthip_test.go`)
- Felix endpoint rules split (IPv4/IPv6 ACL separation)
- Felix polling loop (IPv4-only change detection, IPv6 changes don't trigger reprogram)
- IPAM assign counts (auto-assign IPv6 from pool annotation)
- Pester tests for BGP next-hop policies (SetNH4_, SetNH6_, DenyMeshEgress)
