Dual-Stack Internals: Mock-able Behaviors and Known Constraints

This document captures the live behaviors that the fork's tests mock, the
real-world constraints they encode, and the trade-offs the code makes. It
is intended for someone modifying the fork: read this before changing how
HNS networks are managed, how Felix programs ACLs, or how RRAS BGP policies
are emitted.

For the user-facing how-to, see windows-dual-stack.md.

---

# 1. CNI plugin path: dataplane_windows.go + hns_types.go

## Cross-platform abstraction

`hns_types.go` defines `HNSSubnet`, `HNSNetworkInfo`, `HNSEndpointInfo` as
mirror types (no hcsshim dependency). Two interfaces let the production
code call into hcsshim while tests inject mocks:

- `HNSNetworkAPI`: `GetByName`, `Delete`, `Create(jsonRequest)` — wraps
  `hcsshim.GetHNSNetworkByName`, `(*HNSNetwork).Delete`,
  `hcsshim.HNSNetworkRequest("POST", "", json)`.
- `HNSEndpointAPI`: `GetByName`, `Delete`, `Create(jsonRequest)`,
  `HostAttach(ep, compartmentID)`.

Production wiring: `realHNS{}`, `realHNSEndpoint{}` (both in
`dataplane_windows.go`). Tests use `mockHNS`, `mockHNSEndpoint` in
`ensure_network_test.go` / `host_endpoint_test.go`.

`hcsshimNetworkToInfo(n *hcsshim.HNSNetwork) *HNSNetworkInfo` lossily
projects to the cross-platform info. Only `Id`, `Name`, `Type`, `Subnets[].
{AddressPrefix,GatewayAddress}` are kept; `IPv6` flag, `Policies`,
`MacPools`, `ManagementIP` are dropped. Re-fetching via
`hcsshim.GetHNSNetworkByName(name)` is required to recover those fields
for the consumer code.

## getNthIP carry bug

```go
func getNthIP(PodCIDR *net.IPNet, n int) net.IP {
    ...
    buf[3] += byte(n)  // IPv4
    ...
    buf[15] += byte(n) // IPv6
}
```

Only the last byte is incremented; there is no carry into earlier bytes.
Safe for `n` in the small constants used today (gateway = 1, host endpoint
= 2). Breaks silently for any `n >= 256` or for any /CIDR where the last
byte of the network address is non-zero (e.g., `10.3.48.192/26` with
`n=64` would wrap to `.0` instead of `.0` of the next /26 block).

Mock-able behavior: a test could verify carry by passing a /24 with
`n=300` and asserting the result. **Today no such test exists.**

## networkNeedsRecreate (current intent: recreate on any mismatch)

```go
wantCount := 1
if subNetV6 != nil { wantCount = 2 }
if len(existingSubnets) != wantCount { return true }
```

The intent has flipped three times in the commit history:

1. `ffbd7ec4c4` (2026-03-24) — recreate on mismatch (initial dual-stack
   transitions).
2. `01c91bf1a3` (2026-03-24) — DO NOT delete on mismatch; warn and rely
   on reboot. Rationale: deleting an L2Bridge tears down the vSwitch and
   destroys all pod endpoints with "General failure" errors.
3. `13f3d4cfb4` (2026-03-27) — recreate on mismatch.
4. `338e61c43d` (2026-03-27) — DO NOT delete on rollout, rely on reboot.
   Same rationale as #2.
5. `597b173e74` (2026-04-07) — recreate on mismatch. Rationale: handle
   IPv6 prefix change via DHCPv6-PD.

The current code is at state #5. It conflicts with state #4's design and
with `docs/windows-dual-stack.md` lines 88, 132, which still describe
state #4 ("On rollout (DaemonSet restart, same boot): EnsureNetworkExists
finds the existing network. If subnets don't match...it logs a warning
and keeps the existing network").

**The conflict has a real cause:** CNI is invoked per-pod, and pods do
not all request the same address families. A pod without
`cni.projectcalico.org/ipv6pools` annotation calls
`SetupL2bridgeNetwork(subV4, nil)` (per `DoNetworking`'s parsing of
IPAM result). When the existing network is dual-stack, that call hits
`len(existingSubnets) != wantCount` and triggers recreate as IPv4-only.

State #5 silently breaks any cluster where IPv4-only pods coexist with
dual-stack pods. State #4 silently leaves stale IPv6 prefixes on the HNS
network across DHCPv6-PD rotations. **Neither extreme is right.**

Mock-able test cases that capture the intent:
- `TestNetworkNeedsRecreate_DualStack_Match` — exact-count match → no
  recreate.
- `TestNetworkNeedsRecreate_IPv4ToDualStack` — adding v6 to v4-only →
  recreate (this scenario only happens at calico-node startup, not from
  per-pod CNI).
- `TestNetworkNeedsRecreate_DualStack_WrongV6Subnet` — v6 prefix
  rotated → recreate (this is the DHCPv6-PD case state #5 cared about).
- `TestNetworkNeedsRecreate_DualStackToIPv4Only` — v4-only pod arrives
  on dual-stack network. Currently passes the test that asserts recreate;
  in production this is the "downgrade dual-stack to v4-only" path that
  blows away running pods.

The right fix is to make CNI pass `subNetV6` from the **node-level**
state (not the per-pod IPAM result), so per-pod calls don't accidentally
strip IPv6. Calico-node startup already knows whether v6 is enabled
(`FELIX_IPV6SUPPORT=true` and v6 IPAM block was reserved). CNI plugin
could read the same node-level signal instead of trusting per-pod IPAM.

## DSR (windows_loopback_DSR)

```go
if d.conf.WindowsLoopbackDSR {
    // adds OutBoundNAT loopback rules to v1pols and v2pols
}
```

`d.conf.WindowsLoopbackDSR` comes from CNI config field
`"windows_loopback_DSR"` which `calico.psm1::Install-CNIPlugin`
substitutes from `Get-IsDSRSupported` (true on Server 2022 build 18317+).

DSR is stateful-load-balancer reuse on Windows. For mixed Win/Linux
clusters where Windows pods talk to ClusterIPs backed by Linux pods, DSR
on the Linux side replies with the Linux pod IP as source instead of the
ClusterIP, which the Windows TCP stack drops. The cluster runs
kube-proxy with `--enable-dsr=false` in `kube-proxy-windows`'s
`start-patched.ps1`, but the **CNI plugin's loopback DSR is independent**
and is currently `true`. To match cluster intent, the CNI config must
also have `windows_loopback_DSR: false`.

Mock-able test would verify that `(d.conf.WindowsLoopbackDSR == false)`
prevents the OutBoundNAT loopback policies from being added. Today no
test covers this branch (only the `WindowsLoopbackDSR == true` path is
implicitly exercised when test pods are created).

The fix: introduce a `CALICO_DSR_DISABLE` env var and have
`Install-CNIPlugin` (calico.psm1) override `$dsrSupport` to "false" if
set. Or simpler: hardcode `$dsrSupport = "false"` in calico.psm1
unconditionally and document the trade-off (no loopback DSR; pod-to-its-
own-ClusterIP traffic does the full NAT path).

## V1 vs V2 endpoint creation

`createAndAttachContainerEP` branches on `cri.IsDockershimV1(args.Netns)`:

- **V1 path (Dockershim, netns="none" or "container:..."):** uses
  `hcsshim.HNSEndpoint` struct. **Does NOT add IPv6 to the endpoint.**
  Only `IPAddress`, `GatewayAddress`, `MacAddress`, `Policies`. So
  Dockershim+IPv6 pods would have no v6 connectivity. Comment explains
  containerd 2.x sets a hex netns so this path is not used for our
  cluster.
- **V2 path (containerd, hex netns):** uses `hcn.HostComputeEndpoint`.
  Adds IPv6 to `IpConfigurations` and adds `::/0` route to `hcnRoutes`
  when `podIPv6 != nil && subNetV6 != nil`.

Mock-able: `cri.IsDockershimV1` is package-level; tests would need to
shim it. No existing test covers this branch.

## V2 endpoint policy: NeedEncap to mgmt IP

```go
v2pols = append(v2pols, hcn.EndpointPolicy{
    Type: hcn.SDNRoute,
    Settings: ...DestinationPrefix=mgmtIP/32, NeedEncap=true,
})
```

This pushes traffic to the management IP through the encap path. The
fork's docs (`windows-dual-stack.md`) describe this as "required for
node ports". For dual-stack nodes, the `mgmtIP` is the IPv4 management
IP only — there is no equivalent v6 rule. If a pod sends to the host's
own IPv6 (e.g., via SLAAC), it does not get this NeedEncap behavior.
**Today no test covers what happens when an IPv6 pod talks to its
host's IPv6 address.**

## OutBoundNAT exclusion list

`filterIPAMPools(pools, podIP)`:

- Skips IPv6 pools entirely (HNS rejects IPv6 CIDRs in OutBoundNAT
  exclusions).
- Builds the IPv4 cidrs list from the v4 pools.
- Returns `natOutgoing` from the **v4 pool that contains the podIP**.

Implication for dual-stack: when a dual-stack pod is created, the
returned `natOutgoing` reflects the v4 pool's setting. If the
`pod-ipv6-pool` has `natOutgoing: false` (correct for globally-routable
v6) but the v4 pool has `natOutgoing: true` (typical), the v4 path is
NAT'd while the v6 path is not. That is the desired behavior.

Test coverage: `ipam_pools_test.go` covers this filter.

---

# 2. IPAM plugin path: ipam_plugin.go

## calculateAssignCounts

```go
num4 = 1
if assignIpv4 != nil && *assignIpv4 == "false" { num4 = 0 }
num6 = 0
if assignIpv6 != nil && *assignIpv6 == "true" {
    num6 = 1
} else if len(ipv6Pools) > 0 {
    num6 = 1  // pod annotation set
}
```

Pods without the `cni.projectcalico.org/ipv6pools` annotation get IPv4
only (when CNI config doesn't set `assign_ipv6: true`). System pods
(CSI, GPU plugin, ohmgraphite, etc.) generally do not annotate. So they
flow through the per-pod CNI mismatch path described above.

Test: `ipam_assign_counts_test.go` covers all four annotation × CNI-flag
combinations.

## Windows host block reservation

Lines 267-278: on Windows, IPAM reserves the first 3 + last 1 of each
block via `HostReservedAttrIPv4s` (and v6 if `num6 > 0`). This is so
calico-node can use the 2nd IP of the block for `Calico_ep`.

`startup_windows.go::ensureNetworkForOS` does the same reservation when
EnsureBlock is called.

Test: `ipam_windows_reserved_test.go`.

## Asymmetric assignment cleanup

If v4 partial-fulfillment fails but v6 succeeds, the v6 IPs are released
(and vice versa). Atomic from the pod's perspective.

---

# 3. Felix endpoint manager: endpoint_mgr.go

## Cache key format

`m.addressToEndpointId` keys are `<ip>/<32 or 128>`. RefreshHnsEndpointCache
adds two entries per dual-stack endpoint: `<v4>/32` → ID, `<v6>/128` → ID.

Lookups try v4 first then v6. The `<32 or 128>` suffix is a stylistic
inconsistency: line 220 always appends `ipv4AddrSuffix = "/32"` even if
`endpoint.IPAddress` is an IPv6 address. In practice
`endpoint.IPAddress` is always v4 on Calico-Windows endpoints
(IPv4-only or dual-stack), so this doesn't bite. **An IPv6-only HNS
endpoint would key as `<v6>/32` here, which would never match a v6
lookup.** Test
`TestRefreshCache_IPv6OnlyEndpoint` documents this limitation: it asserts
the v6 key is reachable, but it relies on `IPAddress = net.IPv4zero`
being treated as the v4 entry (not the v6 endpoint's address).

## Stale-endpoint filter

```go
if len(endpoint.SharedContainers) == 0 { skip with warning }
```

This is the source of the persistent "Calico_ep stale endpoint" warnings
in production logs. `Calico_ep` is the host endpoint; it has no
SharedContainers (it's not a pod sandbox). The warning is a false
positive — Felix correctly skips it because it doesn't represent a
workload — but the log spam is misleading.

## host-to-endpoint ACL split

`nodeToEndpointRules` returns up to 2 ACL rules:

1. IPv4 host addresses joined into `RemoteAddresses` (priority
   `HostToEndpointRulePriority`).
2. IPv6 host addresses (fetched on-demand via `getIPv6Addrs`) into a
   second rule with the same priority.

Reason for the split: combining v4 and v6 in a single
`RemoteAddresses` field hits HNS's `ERROR_BUFFER_OVERFLOW` (0x6f).

`getIPv6Addrs` is overridable (`mgr.getIPv6Addrs = ...`) for testing.
Default returns all global unicast v6 addresses on the host (excluding
loopback and link-local). **This includes the ULA**, so host-to-pod
traffic from the ULA is allowed.

## Why IPv6 isn't tracked in the polling loop

`loopPollingForInterfaceAddrs` polls only IPv4. The doc comment explains:
HNS creates new vSwitch endpoints for pods, which causes IPv6 SLAAC
churn on the host. If polling tracked v6, every pod creation would
trigger `markAllEndpointForRefresh()` → reprogram every pod's HNS ACL →
TCP RSTs.

Trade-off: real IPv6 changes (SLAAC prefix rotation) don't trigger a
refresh. The host-to-endpoint ACL is rebuilt at next workload update
because it's regenerated by `nodeToEndpointRules` each time
`applyRules` is called.

---

# 4. RRAS BGP control: confd/windows-packaging/config-bgp.psm1

## ProcessBgpRouter, ProcessBgpRouterIPv6

Standalone — set router-id/ASN, enable IPv6 routing with
`-LocalIPv6Address $LocalIPv6`. Stub: `bgp-stubs.psm1::Add-BgpRouter,
Set-BgpRouter`.

## ProcessBgpBlocks(Blocks, BlocksV6)

Adds new custom routes for entries in `$allBlocks = $Blocks + $BlocksV6`.
Removes routes that exist in BgpCustomRoute but aren't in the input.

**Stale-block bug surfaced live:** `blocks.ps1.template` reads
`/host/<NODE>/ipv6/block/*` from etcd and emits all entries with
`state == "confirmed"`. When a DHCPv6-PD prefix rotation happens,
calico-node reserves a new block from the new pool but does not release
old block affinities. The old confirmed entries persist. Result:
`$blocks_v6` has 19+ stale `/122` entries in addition to the live one,
and ProcessBgpBlocks adds `Add-BgpCustomRoute` for all of them. The
SetNH6_ policies are added for all 19. eBGP egress advertises 19 stale
prefixes to VyOS.

The test in `config-bgp.tests.ps1` covers ProcessBgpBlocks with the
correct input list — it does NOT exercise "old confirmed blocks should
have been cleaned up before reaching this function". The cleanup belongs
upstream in calico-node startup IPAM (release affinity for blocks whose
pool no longer exists).

## DenyMeshEgress

Builds `$meshNextHops` from peerings with `AS == LocalAsn` and any
`peering.IP`. **`peering.IP` is always IPv4** because `peerings.ps1.
template` only emits `/host/<NODE>/ip_addr_v4` for mesh peers. So the
DenyMeshEgress policy filters by IPv4 next-hops only.

Implication: when Windows RRAS receives an IPv6 NLRI from a mesh peer
with the peer's IPv6 next-hop, the route's next-hop is **not** in the
DenyMeshEgress MatchNextHop list (which contains only IPv4 addresses).
So Windows would re-advertise the route to eBGP without filtering.
However, the SetNH6_ policy applies before egress and rewrites the
next-hop to LocalIPv6 — so the re-advertised route would point at the
local Windows node's IPv6 (wrong for foreign-originated routes).

This may explain the route propagation issue we saw live: VyOS receives
correct routes from Windows, but Linux nodes never see Windows-originated
v6 routes because mesh peers don't have any policy attached at all (lines
296: `if ($peering.AS -eq $LocalAsn) { continue }` — only attaches to
eBGP peers).

## SetNH4_ / SetNH6_

Per-block ModifyAttribute policies that rewrite next-hop on egress.
Attached **only to eBGP peers** (lines 295-297 / 360-362). Mesh peers
get no policy, so RRAS uses default behavior, which for IPv6 NLRI on
an IPv4 BGP session is `Set-BgpRouter -LocalIPv6Address` (the local
node's IPv6). After the ULA cutover this is the ULA — correct in
theory, since all mesh peers are on the same ULA /64.

Mock-able: `bgp-stubs.psm1::Add-BgpRoutingPolicy` records `MatchPrefix`,
`MatchNextHop`, `NewNextHop`, `PolicyType`. Tests in `config-bgp.tests.ps1`
verify the policies are created and MatchNextHop is the correct list.

---

# 5. node-service.ps1 — container startup

Runs every time the calico-node container (re)starts. Key actions in
order:

1. Reads `Get-LastBootTime` and `Get-StoredLastBootTime`. If they differ,
   considers it a "first run since boot" and:
   - Removes all non-NAT HNS networks.
   - Waits up to STARTUP_VALID_IP_TIMEOUT for a non-link-local, non-loopback IP.
2. Installs CNI binaries `calico.exe`, `calico-ipam.exe` from
   `<sandbox>/opt/cni/bin` to `C:\opt\cni\bin`.
3. **Does NOT install/regenerate the CNI config (`10-calico.conf`).**
   That's `Install-CNIPlugin` in `calico.psm1`, which is only called by
   `install-calico.ps1` during initial installation. So changes to the
   CNI config template don't propagate without a fresh installation.
4. Creates "External" placeholder L2Bridge **only if neither Calico nor
   External exists** (post-fix). This was a fragile sequence; see
   `cfad96ef0b`.
5. Sets `WeakHostReceive=Enabled` and `WeakHostSend=Enabled` on
   `vEthernet (Ethernet*)` for both IPv4 and IPv6. Required to make
   strong-host-model Windows accept packets routed via the Calico_ep
   adapter when destined for a local pod. **Live observation: this
   sometimes does not take effect on first boot.** Cause unknown;
   working theory is that the adapter doesn't yet have a stable name
   when Set-NetIPInterface runs.
6. Disables IPv6 RandomizeIdentifiers so SLAAC is stable EUI-64.
7. Sets `Tcpip6\IPEnableRouter=1` (IPv6 forwarding enable).

   **BUG:** does NOT set `Tcpip\IPEnableRouter=1` (IPv4 forwarding
   enable). On Server 2022 fresh installs, IPv4 IPEnableRouter is 0 by
   default. Per-interface forwarding is enabled separately, but the
   global flag controls some behaviors (notably forwarding between
   interfaces). Live: had to set this manually on appmana-003.

8. `Restart-Service RemoteAccess` if backend is windows-bgp. Per docs:
   "RRAS BGP sessions may not exchange routes after a service restart
   until Restart-Service RemoteAccess is called."
9. Main loop: when kubelet starts/restarts, runs `calico-node.exe -startup`.
10. Runs the token refresher (`calico-node.exe -monitor-token`).

---

# 6. CNI config generation: calico.psm1::Install-CNIPlugin

Called once during installation. Reads `cni.conf.template`, substitutes
all `__VAR__` placeholders. Writes to `$env:CNI_CONF_DIR\$env:CNI_CONF_FILENAME`.

`__DSR_SUPPORT__` is from `Get-IsDSRSupported` (true on Server 2022).
`__K8S_SERVICE_CIDR__` is from `$env:K8S_SERVICE_CIDR`. The template has
**no IPv6-specific fields** — no IPv6 service CIDR, no IPv6 ipam pool
override. The IPAM plugin reads pools from Calico's IPPool resources;
the CNI config doesn't need to enumerate them.

Note: `Install-CNIPlugin` is NOT called from `node-service.ps1`. The
CNI config persists from the initial installation. To pick up template
changes, the host needs a reinstall, which doesn't happen automatically
on the fork's HostProcess deployment.

---

# 7. blocks.ps1.template, peerings.ps1.template

confd templates that emit `$blocks`, `$blocks_v6`, `$peerings`,
`$local_ip`, `$local_ipv6`. Read from etcd keys
`/host/<NODENAME>/{ipv4,ipv6}/block/*` and `/host/<NODENAME>/ip_addr_{v4,v6}`.

`$peerings` mesh entries only include IPv4 (`$onode_ip` from
`/host/<host>/ip_addr_v4`). There is no equivalent emission of the peer's
IPv6 address. Because `Add-BgpPeer -PeerIPAddress` requires an IPv4 (the
TCP session is IPv4 in this fork), this is correct — but it limits
DenyMeshEgress to filtering by IPv4 next-hops only. See above.

---

# 8. Test pattern summary

| Layer | Mock | Tests |
|---|---|---|
| HNS network creation | `mockHNS` (cni-plugin/pkg/dataplane/windows) | `ensure_network_test.go`, `network_needs_recreate_test.go` |
| HNS host endpoint | `mockHNSEndpoint` (same pkg) | `host_endpoint_test.go` |
| IPAM pool filtering | none (pure inputs) | `ipam_pools_test.go` |
| getNthIP | none | `getnthip_test.go` |
| IPAM assign counts | none | `ipam_assign_counts_test.go` |
| Windows IPAM block reservation | none | `ipam_windows_reserved_test.go` |
| Felix endpoint manager | `hns.MockAPI`, `getIPv6Addrs` override | `endpoint_mgr_test.go`, `endpoint_mgr_deferred_test.go` |
| Felix policysets | inline `hns.HNSAPI` shim | `policysets_test.go`, `dual_stack_test.go` |
| Felix v4/v6 ACL split | inline | `flattener_test.go` |
| RRAS BGP cmdlets | `bgp-stubs.psm1` | `config-bgp.tests.ps1` |

Tests run on Linux (`go test ./...` and `pwsh -Command "Invoke-Pester"`)
because all Windows-specific dependencies are abstracted behind mocks.

---

# 9. Bugs identified during this read

1. **getNthIP carry**: silently truncates when n+lastByte >= 256.
   Affects future correctness if larger blocks are used. No live impact
   today.
2. **networkNeedsRecreate state-flip**: per-pod CNI calls with
   subNetV6=nil triggers dual-stack network recreation, blowing away
   running pods. Conflict between code (state #5) and docs (state #4).
3. **DSR enabled in CNI config**: `windows_loopback_DSR: true` is set
   automatically on Server 2022. The cluster intent is to disable DSR
   end-to-end (kube-proxy already has `--enable-dsr=false`).
4. **Tcpip\IPEnableRouter not set**: only Tcpip6 is set. Both are
   needed for full IPv4 forwarding on Server 2022 fresh installs.
5. **Stale `$blocks_v6` entries**: blocks template emits all etcd
   `confirmed` entries; calico-node startup detects "no pool" but
   doesn't release block affinity. RRAS ends up advertising 19 stale
   prefixes via `Add-BgpCustomRoute`.
6. **DenyMeshEgress is IPv4-next-hop only**: `peerings.ps1.template`
   doesn't emit per-peer IPv6 addresses, so `$meshNextHops` has only
   IPv4 entries. IPv6 NLRI from mesh peers isn't filtered.
7. **No SetNH6_/DenyMeshEgress policies attached to mesh peers**: only
   eBGP peers get the policies. RRAS default behavior for iBGP IPv6
   advertisements relies on `Set-BgpRouter -LocalIPv6Address` being
   set. May explain why Linux nodes don't receive Windows-originated
   IPv6 routes (live observation; needs further verification).
8. **`Install-CNIPlugin` is one-shot**: changes to `cni.conf.template`
   don't propagate without a host reinstall. `node-service.ps1`
   should regenerate the CNI config on each container start.
9. **WeakHost setting sometimes doesn't take effect on first boot**:
   live observation; cause unknown.
10. **Calico_ep filtered as stale**: false-positive log spam due to
    `len(SharedContainers) == 0` filter. Cosmetic.

---

# 10. Recommended approach for a fix

For each bug in section 9, before touching production code:

1. Write a Go or Pester test in the existing pattern that captures the
   current (buggy) behavior. Confirm it passes against the live code.
2. Modify the test to capture the desired behavior. Confirm it now
   fails.
3. Fix the production code so the test passes. Confirm related tests
   still pass.
4. Build the image; deploy by digest; live-test on appmana-003 with
   `hack/appmana/ipv6-health-check.sh`.
5. Roll back by digest if the live test regresses.

DO NOT change production code without a corresponding mock test. The
fork's value is that it can be tested on Linux without a Windows host.
