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

### 2. Update the calico-windows-config ConfigMap

```yaml
data:
  FELIX_IPV6SUPPORT: "true"
  IP6: "autodetect"
  IP6_AUTODETECTION_METHOD: "first-found"
  CALICO_NETWORKING_BACKEND: "windows-bgp"
```

`FELIX_IPV6SUPPORT=true` gates the entire IPv6 code path. Without it, behavior is identical to upstream.

### 3. Update the DaemonSet

Use the fork's built image. No init container is needed. The calico-node container installs CNI binaries to `C:\opt\cni\bin` at startup via `node-service.ps1`. All scripts (confd, BGP config) are baked into the image and run from the container's sandbox path.

### 4. Configure BGP router for IPv6

Example for VyOS:
```
set protocols bgp address-family ipv6-unicast network 2001:db8:abcd::/48
set protocols bgp neighbor <windows-node-ipv6> address-family ipv6-unicast
```

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

# Full cluster health check (all 10 Windows nodes, cross-product pod-to-pod)
bash hacking/ipv6-health-check.sh appmana-003 appmana-008 appmana-009 appmana-018 appmana-019 appmana-020 appmana-021 appmana-022 appmana-023 appmana-025
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
