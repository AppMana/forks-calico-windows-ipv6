# Calico Windows Dual-Stack IPv6 (BGP Mode)

Fork of [projectcalico/calico](https://github.com/projectcalico/calico) v3.29.6 adding IPv6 dual-stack support to the Windows calico-node for the `windows-bgp` (L2Bridge) networking backend.

Pods on Windows Server 2022 nodes get both IPv4 and IPv6 addresses. The IPv6 addresses can be publicly routable (e.g. from an ISP DHCPv6-PD allocation), eliminating NAT for IPv6 traffic.

## Requirements

- Windows Server 2022, build 20348.2031+
- Calico v3.29.6 with `windows-bgp` backend (not VXLAN)
- BGP router peering with the Windows nodes (VyOS, FRR, etc.)
- Routable IPv6 address on each Windows node's physical interface (SLAAC or static)
- An IPv6 prefix for pod addressing

VXLAN dual-stack is not supported by HNS.

## Configuration

### IPv6 IPPool

Create an IPv6 IPPool using a prefix from your ISP or provider allocation. `natOutgoing` is false because the addresses are globally routable.

```yaml
apiVersion: projectcalico.org/v3
kind: IPPool
metadata:
  name: public-ipv6
spec:
  cidr: "2001:db8:abcd:100::/64"
  blockSize: 122
  natOutgoing: false
  nodeSelector: "ipv6-pool == 'public'"
```

Use `nodeSelector` to control which nodes receive IPv6 blocks. To assign IPv6 to specific pods only, annotate them:

```yaml
metadata:
  annotations:
    cni.projectcalico.org/ipv6pools: '["public-ipv6"]'
```

Pods without this annotation get only IPv4.

### calico-windows-config ConfigMap

```yaml
data:
  CALICO_NETWORKING_BACKEND: "windows-bgp"
  FELIX_IPV6SUPPORT: "true"
  IP6: "autodetect"
  IP6_AUTODETECTION_METHOD: "first-found"
```

`FELIX_IPV6SUPPORT=true` gates the entire IPv6 code path. Without it, behavior is identical to upstream Calico.

### DaemonSet Image

Use the image built by this fork's CI:

```
ghcr.io/appmana/node-windows:<version>
```

No init container is needed. The container installs CNI binaries at startup.

## BGP Router Configuration

### Residential WAN with ISP DHCPv6-PD

A typical residential setup: the ISP delegates a /56 via DHCPv6-PD. The router assigns /64s from this prefix to internal interfaces. One /64 is used for the pod network.

Assumptions for this example:
- ISP delegates `2001:db8:abcd::/56` via DHCPv6-PD to the WAN interface
- `2001:db8:abcd:100::/64` is the LAN where Windows nodes connect
- `2001:db8:abcd:101::/64` is allocated as the pod IPv6 pool
- The router's LAN IPv6 is `2001:db8:abcd:100::1`
- A Windows node's LAN IPv6 (SLAAC) is `2001:db8:abcd:100::a`
- IPv4 node mesh ASN is 65414, router eBGP ASN is 65000

VyOS configuration:

```
# IPv6 on the LAN interface
set interfaces ethernet eth1 address '2001:db8:abcd:100::1/64'
set service router-advert interface eth1 prefix 2001:db8:abcd:100::/64

# BGP neighbor (the Windows node)
set protocols bgp neighbor 2001:db8:abcd:100::a remote-as 65414
set protocols bgp neighbor 2001:db8:abcd:100::a address-family ipv6-unicast

# Also peer over IPv4 (existing)
set protocols bgp neighbor 192.0.2.10 remote-as 65414
set protocols bgp neighbor 192.0.2.10 address-family ipv4-unicast
```

The Windows node advertises its allocated /122 block (e.g. `2001:db8:abcd:101::/122`) via BGP. The router installs a route pointing to the node's SLAAC address as next-hop. Upstream, the ISP routes the entire /56 to your WAN, so the pod /122 is reachable from the internet.

DHCPv6-PD prefix changes (e.g. ISP reassignment) require recreating the HNS network, which means restarting calico-node on affected Windows nodes. The IPAM blocks and BGP advertisements update automatically.

### Top-of-Rack BGP

In a datacenter, the ToR switch peers with each node via eBGP. Each node has a static IPv6 /64 from a provider allocation.

Assumptions:
- Provider allocation: `2001:db8:abcd::/48`
- Node subnet: `2001:db8:abcd:100::/64`
- Pod pool: `2001:db8:abcd:200::/56`, blockSize 64
- ToR ASN: 65000, node mesh ASN: 65414
- ToR IPv4: 192.0.2.1, node IPv4: 192.0.2.10

VyOS (or FRR) on the ToR:

```
set protocols bgp system-as 65000

# IPv4 peering
set protocols bgp neighbor 192.0.2.10 remote-as 65414
set protocols bgp neighbor 192.0.2.10 address-family ipv4-unicast

# IPv6 peering
set protocols bgp neighbor 2001:db8:abcd:100::a remote-as 65414
set protocols bgp neighbor 2001:db8:abcd:100::a address-family ipv6-unicast

# Announce the aggregate upstream
set protocols bgp address-family ipv6-unicast aggregate-address 2001:db8:abcd:200::/56
```

Each node advertises its /64 block. The ToR aggregates and announces upstream.

### Calico BGPConfiguration

To configure the eBGP peer from the Calico side, create a BGPPeer resource. Set `keepOriginalNextHop: true` so that when a node re-advertises routes learned from the mesh to the eBGP peer, the original next-hop is preserved (preventing routing loops where the ToR sends traffic back to the wrong node).

```yaml
apiVersion: projectcalico.org/v3
kind: BGPPeer
metadata:
  name: tor-switch
spec:
  peerIP: "192.0.2.1"
  asNumber: 65000
  keepOriginalNextHop: true
```

The `keepOriginalNextHop` field causes the confd template to emit `KeepOriginalNextHop = $true` in the peerings list, which triggers the `DenyMeshEgress` routing policy on Windows RRAS. This policy blocks re-advertisement of mesh-learned routes to eBGP peers. Without it, RRAS rewrites the next-hop to itself for all re-advertised routes, creating routing loops.

## Limitations

### HNS L2Bridge

- Dual-stack requires both IPv4 and IPv6 subnets at network creation time. You cannot add IPv6 to an existing IPv4-only network.
- IPv6-only L2Bridge networks fail with "adapter not found".
- Deleting the HNS network destroys all pod endpoints. Subnet changes require a node reboot (the network is recreated on boot).
- `Set-BgpRoutingPolicy` silently drops IPv6 prefixes from `MatchPrefix` when updating. The code works around this with remove + re-add.
- Combined IPv4+IPv6 in a single HNS ACL `RemoteAddresses` field causes `ERROR_BUFFER_OVERFLOW` (0x6f). Felix creates separate ACL rules per address family.

### RRAS BGP

- `Set-BgpRouter -IPv6Routing Enabled` hangs without `-Force` in non-interactive sessions.
- RRAS re-advertises all learned routes (including iBGP mesh routes) to eBGP peers with itself as next-hop. The `DenyMeshEgress` policy blocks this.
- RRAS has no equivalent to BIRD's `next hop keep` or export filters. Route control is done entirely through routing policies.

### Felix Policy Enforcement

- The Felix polling loop tracks only IPv4 addresses for HNS endpoint change detection. IPv6 addresses fluctuate when HNS creates vSwitch endpoints for pods; including them would trigger full ACL reprograms on every pod creation, causing TCP RSTs on existing connections.
- IPv6 addresses for ACL rules are fetched on demand at rule-build time.

### VXLAN

HNS does not support dual-stack VXLAN. This fork only works with the `windows-bgp` backend.

## Building

CI builds run on every push to `windows-dual-stack-v3.29.6`. The image is published to `ghcr.io/appmana/node-windows:<version>`.

To build locally:

```bash
# Cross-compile from Linux
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o node/dist/bin/calico-node.exe ./node/cmd/calico-node/main.go
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o node/dist/bin/calico.exe ./cni-plugin/cmd/calico/
cp node/dist/bin/calico.exe node/dist/bin/calico-ipam.exe
```

## Testing

```bash
# Go tests (Linux, cross-platform mocks)
go test ./felix/dataplane/windows/... ./cni-plugin/pkg/dataplane/windows/... ./cni-plugin/pkg/ipamplugin/...

# PowerShell Pester tests (Linux or Windows)
pwsh -Command "Import-Module Pester; Invoke-Pester -Path confd/windows-packaging/tests/ -Output Detailed"
```

## License

Apache License 2.0, same as upstream Calico.
