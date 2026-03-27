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

Use the fork's built image. No other DaemonSet changes needed; the ConfigMap provides the IPv6 env vars.

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

The HNS network JSON:
```json
{
  "Name": "Calico",
  "Type": "L2Bridge",
  "Subnets": [
    {"AddressPrefix": "10.244.0.0/26", "GatewayAddress": "10.244.0.1"},
    {"AddressPrefix": "2001:db8:abcd:1::/64", "GatewayAddress": "2001:db8:abcd:1::1"}
  ]
}
```

The resulting network has `"IPv6": true` and a `ManagementIPv6` assigned via SLAAC.

### CNI Plugin (Pod Networking)

When a pod is created:

1. `DoNetworking()` iterates all IPs from the IPAM result, separating IPv4 and IPv6
2. The HCN endpoint is created with both `IpConfigurations` and dual-stack `Routes` (0.0.0.0/0 and ::/0)
3. IPv6 forwarding is enabled on the management interface (best-effort)

### Felix (Policy Enforcement)

- `extractUnicastAddrs()` returns both `/32` (IPv4) and `/128` (IPv6) host addresses
- The endpoint cache indexes by both IPv4 and IPv6 addresses
- `CompleteDeferredWork()` resolves workloads by IPv4 first, falls back to IPv6
- `nodeToEndpointRules()` returns separate IPv4 and IPv6 ACL rules for host-to-endpoint traffic (split to avoid HNS buffer overflow when combining address families in one rule)
- Policy rules: `ipVersion` is set to 0 (dual-stack), so `filterNets()` passes all CIDRs. IPv6 policy rules are no longer dropped.
- IPv6 IP sets are registered and tracked alongside IPv4

### Confd/BGP

- `blocks.ps1.template` emits `$blocks_v6` from `/host/<node>/ipv6/block`
- `peerings.ps1.template` emits `$local_ipv6` from `/host/<node>/ip_addr_v6`
- `ProcessBgpRouterIPv6` calls `Set-BgpRouter -IPv6Routing Enabled -LocalIPv6Address $local_ipv6`
- `ProcessBgpBlocks` accepts both `$blocks` and `$blocks_v6`, advertises all via `Add-BgpCustomRoute`
- IPv6 routes appear in the RRAS BGP RIB and are advertised to peers

### keepOriginalNextHop

This fork includes the keepOriginalNextHop workaround. For eBGP peers with `keepOriginalNextHop: true`, per-prefix egress routing policies preserve the original iBGP next-hop in eBGP advertisements. Works for both IPv4 and IPv6 prefixes.

## HNS Limitations

Tested on Windows Server 2022 build 20348.4773:

| Operation | Result |
|-----------|--------|
| Dual-stack L2Bridge (v4+v6 subnets together) | Works |
| IPv6-only L2Bridge | Fails ("adapter not found") |
| Adding IPv6 subnet to existing v4-only network | Not supported |
| `Set-BgpRouter -IPv6Routing Enabled` without IPv6 on interface | Hangs |
| `Add-BgpRoutingPolicy` with IPv6 prefix | Works |

Key insight: the L2Bridge network must be created with both subnets from the start. You cannot retrofit IPv6 onto an existing IPv4-only network. This is why the node startup code passes both subnets to `EnsureNetworkExists()` in a single call, and why enabling IPv6 requires a calico-node restart (which recreates the HNS network).

## Verification

```bash
# HNS network has both subnets
ssh administrator@<node> 'powershell "Get-HNSNetwork | Where-Object Name -eq Calico | Select-Object IPv6, ManagementIPv6"'

# Pods get dual-stack IPs
kubectl get pod -o wide

# BGP advertises IPv6 routes
ssh administrator@<node> 'powershell "Get-BgpCustomRoute"'

# RRAS IPv6 routing enabled
ssh administrator@<node> 'powershell "(Get-BgpRouter).IPv6Routing"'

# Upstream router receives IPv6 routes
vtysh -c "show bgp ipv6 unicast summary"
```

## Building

Push to the branch to trigger GitHub Actions, or build locally:

```bash
# Build Windows image (requires remote Windows buildkitd or windows-2022 runner)
make -C node image-windows WINDOWS_VERSIONS=ltsc2022

# Build Linux image
make -C node image ARCH=amd64
```

## Testing

```bash
# Go unit tests on Linux (mock HNS shims)
go test ./felix/dataplane/windows/...

# Go unit tests on Windows (cross-compile and copy)
GOOS=windows GOARCH=amd64 go test -c -vet=off -o test.exe ./cni-plugin/pkg/dataplane/windows/
scp test.exe administrator@<node>:/tmp/ && ssh administrator@<node> '/tmp/test.exe -test.v'

# PowerShell Pester tests (Linux or Windows)
pwsh -Command "Import-Module Pester; Invoke-Pester -Path confd/windows-packaging/tests/ -Output Detailed"
```
