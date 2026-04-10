Calico Windows Dual-Stack IPv6 (BGP Mode)

Fork of [projectcalico/calico](https://github.com/projectcalico/calico) v3.29.6 adding IPv6 dual-stack to the Windows calico-node for the `windows-bgp` (L2Bridge) networking backend.

The scenario this solves: you have Windows Server 2022 nodes on a network with a publicly routable IPv6 prefix, typically from an ISP DHCPv6-PD delegation on a residential or small business WAN connection. You want pods on those nodes to get globally routable IPv6 addresses so they are directly reachable from the internet without NAT. The nodes already have IPv4 BGP peering with a router (VyOS, FRR, etc.) for Calico pod networking, and you want to extend that to IPv6.

This only works with the `windows-bgp` backend. HNS does not support dual-stack VXLAN.


**Requirements**

Windows Server 2022 build 20348.2031 or later. Calico v3.29.6 with `windows-bgp` backend. A BGP router peering with the Windows nodes. A routable IPv6 address on each node's physical interface (SLAAC or static). An IPv6 prefix to carve pod addresses from.


**Example: residential WAN with ISP DHCPv6-PD**

The ISP delegates `2001:db8:abcd::/56` via DHCPv6-PD to your WAN interface. Your router (VyOS in this example) assigns /64s from that prefix to LAN interfaces. The nodes sit on `2001:db8:abcd:100::/64` and get SLAAC addresses. You allocate `2001:db8:abcd:101::/64` as the pod IPv6 pool.

The ISP routes the entire /56 to your WAN, so any address within it is reachable from the internet. When a Windows node advertises its /122 pod block via BGP, the router installs a route to that node's SLAAC address. Traffic from the internet to a pod IPv6 address reaches the ISP, goes to your WAN, the router forwards it to the correct node, and the node delivers it to the pod.

IPv6 IPPool:

```yaml
apiVersion: projectcalico.org/v3
kind: IPPool
metadata:
  name: public-ipv6
spec:
  cidr: "2001:db8:abcd:101::/64"
  blockSize: 122
  natOutgoing: false
  nodeSelector: "ipv6-pool == 'public'"
```

`natOutgoing` is false because these are globally routable addresses. `nodeSelector` controls which nodes get IPv6 blocks. To give IPv6 to specific pods only, annotate them:

```yaml
metadata:
  annotations:
    cni.projectcalico.org/ipv6pools: '["public-ipv6"]'
```

Pods without this annotation get only IPv4.

calico-windows-config ConfigMap:

```yaml
data:
  CALICO_NETWORKING_BACKEND: "windows-bgp"
  FELIX_IPV6SUPPORT: "true"
  IP6: "autodetect"
  IP6_AUTODETECTION_METHOD: "first-found"
```

`FELIX_IPV6SUPPORT=true` gates the entire IPv6 code path. Without it, behavior is identical to upstream Calico.

VyOS router configuration (assuming the node's SLAAC address is `2001:db8:abcd:100::a`, node mesh AS is 65414, router AS is 65000):

```
set interfaces ethernet eth1 address '2001:db8:abcd:100::1/64'
set service router-advert interface eth1 prefix 2001:db8:abcd:100::/64

set protocols bgp neighbor 2001:db8:abcd:100::a remote-as 65414
set protocols bgp neighbor 2001:db8:abcd:100::a address-family ipv6-unicast

set protocols bgp neighbor 192.0.2.10 remote-as 65414
set protocols bgp neighbor 192.0.2.10 address-family ipv4-unicast
```

Calico BGPPeer resource for the router:

```yaml
apiVersion: projectcalico.org/v3
kind: BGPPeer
metadata:
  name: router
spec:
  peerIP: "192.0.2.1"
  asNumber: 65000
  keepOriginalNextHop: true
```

`keepOriginalNextHop` is important. Windows RRAS re-advertises all mesh-learned routes to eBGP peers with itself as next-hop, which creates routing loops. This flag triggers a `DenyMeshEgress` routing policy that blocks re-advertisement of mesh-learned routes, so only the node's own pod blocks are advertised.

If the ISP reassigns the DHCPv6-PD prefix (e.g. after a WAN reconnect), the HNS network must be recreated. This requires restarting calico-node on affected Windows nodes. The IPAM blocks and BGP advertisements update automatically.


**Top-of-rack BGP variant**

In a datacenter with static IPv6 allocations, the same configuration applies. The ToR switch peers with each node. Each node advertises its pod blocks. The ToR aggregates and announces upstream:

```
set protocols bgp address-family ipv6-unicast aggregate-address 2001:db8:abcd:200::/56
```


**DaemonSet image**

CI builds on every push to `windows-dual-stack-v3.29.6`. The image is published to `ghcr.io/appmana/node-windows`. Build locally:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o node/dist/bin/calico-node.exe ./node/cmd/calico-node/main.go
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o node/dist/bin/calico.exe ./cni-plugin/cmd/calico/
cp node/dist/bin/calico.exe node/dist/bin/calico-ipam.exe
```

No init container is needed. The container installs CNI binaries at startup.


**Limitations**

HNS L2Bridge dual-stack requires both IPv4 and IPv6 subnets to be specified together when the network is first created. If the cluster was initially set up with IPv4 only, enabling IPv6 requires a node reboot so that calico-node recreates the HNS network with both address families. IPv6-only L2Bridge networks fail with "adapter not found"; dual-stack always needs an IPv4 subnet too.

Deleting the HNS network destroys all pod endpoints. This is why subnet changes are deferred to reboot rather than done live.

`Set-BgpRoutingPolicy` silently drops IPv6 prefixes from `MatchPrefix` when updating. The code works around this with remove and re-add. Combined IPv4 and IPv6 in a single HNS ACL `RemoteAddresses` field causes `ERROR_BUFFER_OVERFLOW` (0x6f), so Felix creates separate ACL rules per address family.

`Set-BgpRouter -IPv6Routing Enabled` hangs without `-Force` in non-interactive sessions. RRAS has no equivalent to BIRD's `next hop keep` or export filters; route control is done entirely through routing policies.

The Felix polling loop tracks only IPv4 addresses for HNS endpoint change detection. IPv6 addresses fluctuate when HNS creates vSwitch endpoints for pods. Including them would trigger full ACL reprograms on every pod creation, causing TCP RSTs on existing connections. IPv6 addresses for ACL rules are fetched on demand at rule-build time.


**Testing**

```bash
go test ./felix/dataplane/windows/... ./cni-plugin/pkg/dataplane/windows/... ./cni-plugin/pkg/ipamplugin/...

pwsh -Command "Import-Module Pester; Invoke-Pester -Path confd/windows-packaging/tests/ -Output Detailed"
```


Apache License 2.0, same as upstream Calico.
