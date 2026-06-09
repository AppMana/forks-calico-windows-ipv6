# AppMana Calico v3.29 for k0s Windows/Linux clusters

This branch builds AppMana's Calico v3.29 image for mixed Linux and Windows
k0s clusters. The published image is a multi-platform manifest:

```text
ghcr.io/appmana/node:v3.29.6-appmana.post.2
```

Use it with the matching kube-proxy image:

```text
ghcr.io/appmana/kube-proxy:v1.34.6-appmana.post.1-calico-hostprocess
```

Version matrix:

```text
k0s / Kubernetes: 1.34.x
Calico:           3.29.6 + AppMana Windows IPv6/BGP/HNS fixes
kube-proxy:       1.34.6 + AppMana Windows winkernel fixes
Windows base:     Server 2022 / ltsc2022
Networking mode:  Calico windows-bgp / HNS L2Bridge
```

The Calico image manifest contains Linux `amd64` and Windows `amd64/ltsc2022`
variants. Linux nodes pull the Linux Calico image from the same tag; Windows
nodes pull the HostProcess-compatible Windows image from that tag.

## What this branch fixes

- Windows `windows-bgp` dual-stack operation with HNS L2Bridge.
- Stable HNS management IPv4/IPv6 selection and recovery across HNS restarts.
- Windows CNI configuration copied to `C:\CalicoWindows` so CNI can read it
  outside the HostProcess sandbox.
- Windows BGP route rendering for IPv4 and IPv6 pod blocks.
- DSR support controlled by configuration for mixed Linux/Windows clusters.

The matching kube-proxy image carries the Windows winkernel fixes required for
Calico L2Bridge with DSR disabled, including `--source-vip` behavior for
L2Bridge and IPv4-only fallback when the HNS network does not support IPv6.

## k0s usage

In k0s, use Calico as the cluster CNI and pin the Calico images to this branch's
manifest tag. The exact k0sctl field names vary by k0sctl version, but the
intent is:

```yaml
spec:
  k0s:
    version: v1.34.x+k0s.x
    config:
      spec:
        network:
          provider: calico
          calico:
            mode: bird
            overlay: Never
            envVars:
              IP_AUTODETECTION_METHOD: "cidr=10.2.0.0/24"
              IP6_AUTODETECTION_METHOD: "cidr=fd5a:8000:1::/64"
        dualStack:
          enabled: true
          IPv6podCIDR: 2001:db8:10:244::/64
          IPv6serviceCIDR: fd98::/108
```

k0s renders the Linux Calico dual-stack pool variables from the `dualStack`
section. Do not use the node-interface IPv6 prefix as `CALICO_IPV6POOL_CIDR`.
`IP6_AUTODETECTION_METHOD` is for the node's stable management or LAN IPv6
address, not the pod IPv6 pool. AppMana production uses the stable ULA node
prefix for autodetection and keeps the pod IPv6 pool/routing policy in the
Calico IPPool and BGP manifests.

After k0s installs the baseline manifests, patch the Calico Linux and Windows
DaemonSets to use the multi-platform image:

```bash
kubectl -n kube-system set image ds/calico-node \
  calico-node=ghcr.io/appmana/node:v3.29.6-appmana.post.2

kubectl -n kube-system set image ds/calico-node-windows \
  node=ghcr.io/appmana/node:v3.29.6-appmana.post.2 \
  felix=ghcr.io/appmana/node:v3.29.6-appmana.post.2 \
  confd=ghcr.io/appmana/node:v3.29.6-appmana.post.2
```

Use the matching kube-proxy HostProcess image on Windows nodes:

```bash
kubectl -n kube-system set image ds/kube-proxy-windows \
  kube-proxy=ghcr.io/appmana/kube-proxy:v1.34.6-appmana.post.1-calico-hostprocess
```

The Windows kube-proxy DaemonSet must set:

```yaml
env:
- name: KUBEPROXY_DISABLE_DSR
  value: "true"
```

DSR must stay disabled in mixed Linux/Windows clusters. With DSR enabled,
Windows pods can open ClusterIP connections to Linux-backed services and then
drop real TCP data because replies arrive directly from the Linux pod IP rather
than the ClusterIP.

## Calico configuration

The Linux CNI config must allocate both address families. The checked-in
`manifests/calico.yaml` has the required `calico-config` IPAM block:

```json
"ipam": {
  "type": "calico-ipam",
  "assign_ipv4": "true",
  "assign_ipv6": "true"
}
```

Without `assign_ipv6`, Linux pods can miss IPv6 addresses even when Felix and
BIRD6 are enabled. In k0s, verify the rendered `calico-config` ConfigMap after
bootstrap instead of adding duplicate pool env vars by hand.

Use BGP mode for Windows:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: calico-windows-config
  namespace: kube-system
data:
  CALICO_NETWORKING_BACKEND: "windows-bgp"
  FELIX_IPV6SUPPORT: "true"
  CALICO_DSR_DISABLE: "true"
  IP: "autodetect"
  IP_AUTODETECTION_METHOD: "cidr=10.2.0.0/24"
  IP6: "autodetect"
  IP6_AUTODETECTION_METHOD: "cidr=fd5a:8000:1::/64"
```

For IPv4 pod networking in the validated kind/QEMU lab:

```yaml
apiVersion: crd.projectcalico.org/v1
kind: IPPool
metadata:
  name: kind-ipv4-pool
spec:
  cidr: 10.244.0.0/16
  blockSize: 26
  ipipMode: Never
  vxlanMode: Never
  natOutgoing: true
  nodeSelector: all()
```

For routable Windows pod IPv6, add a separate pool and select it explicitly:

```yaml
apiVersion: crd.projectcalico.org/v1
kind: IPPool
metadata:
  name: public-ipv6
spec:
  cidr: 2001:db8:10:244::/64
  blockSize: 122
  natOutgoing: false
  nodeSelector: "kubernetes.io/os == 'windows'"
```

Pods can request that pool with:

```yaml
metadata:
  annotations:
    cni.projectcalico.org/ipv6pools: '["public-ipv6"]'
```

## BGP settings

Windows uses RRAS BGP. For node-to-node mesh, keep Calico's normal node mesh
enabled. For an upstream router or ToR, keep the IPv4 peer and add a separate
IPv6 peer. An IPv4-only BGPPeer renders the external peer in BIRD4 only; it does
not negotiate IPv6 unicast in BIRD6. The failure mode is an upstream router with
zero accepted IPv6 prefixes, or `NoNeg` for IPv6, while pod/service IPv6 traffic
falls through to the router's default/WAN route.

```yaml
apiVersion: crd.projectcalico.org/v1
kind: BGPPeer
metadata:
  name: upstream-router
spec:
  peerIP: 10.2.0.1
  asNumber: 65000
  keepOriginalNextHop: true
---
apiVersion: crd.projectcalico.org/v1
kind: BGPPeer
metadata:
  name: upstream-router-ipv6
spec:
  peerIP: fd5a:8000:1::1
  asNumber: 65000
  keepOriginalNextHop: true
  nodeSelector: kubernetes.io/os == 'linux'
---
apiVersion: crd.projectcalico.org/v1
kind: BGPConfiguration
metadata:
  name: default
spec:
  serviceClusterIPs:
  - cidr: 10.96.0.0/16
  - cidr: fd98::/108
```

`keepOriginalNextHop: true` matters when Windows learns mesh routes and peers
with an external router. It prevents Windows from re-advertising mesh-learned
routes as if Windows were the next-hop for every pod block.

The IPv6 upstream BGPPeer is scoped to Linux nodes because Linux BIRD6 exports
the mesh-learned IPv6 pod blocks and the IPv6 service CIDR reliably. Windows
still participates in node mesh and RRAS transit, but the external IPv6 route
export should not depend on Windows HostProcess BGP startup.

Expected Windows checks:

```powershell
Get-BgpRouter | Select BgpIdentifier,LocalASN,TransitRouting
Get-BgpPeer | Select PeerName,PeerIPAddress,PeeringState
Get-BgpRouteInformation -Type All | ? Best -eq $true
```

`TransitRouting` should be `Enabled`, peers should be `Connected`, and pod
blocks should appear as best routes.

## Validated manifests

The validated Windows deployment is the Calico Windows HostProcess DaemonSet
using the three Windows containers from the same multi-platform Calico tag:

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: calico-node-windows
  namespace: kube-system
spec:
  selector:
    matchLabels:
      k8s-app: calico-node-windows
  template:
    metadata:
      labels:
        k8s-app: calico-node-windows
    spec:
      hostNetwork: true
      nodeSelector:
        kubernetes.io/os: windows
      tolerations:
      - operator: Exists
      securityContext:
        windowsOptions:
          hostProcess: true
          runAsUserName: "NT AUTHORITY\\system"
      containers:
      - name: node
        image: ghcr.io/appmana/node:v3.29.6-appmana.post.2
      - name: felix
        image: ghcr.io/appmana/node:v3.29.6-appmana.post.2
      - name: confd
        image: ghcr.io/appmana/node:v3.29.6-appmana.post.2
```

The validated Linux deployment is the normal Calico Linux DaemonSet using the
same manifest tag:

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: calico-node
  namespace: kube-system
spec:
  template:
    spec:
      nodeSelector:
        kubernetes.io/os: linux
      containers:
      - name: calico-node
        image: ghcr.io/appmana/node:v3.29.6-appmana.post.2
```

The validated Windows kube-proxy deployment is:

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: kube-proxy-windows
  namespace: kube-system
spec:
  template:
    spec:
      hostNetwork: true
      nodeSelector:
        kubernetes.io/os: windows
      securityContext:
        windowsOptions:
          hostProcess: true
          runAsUserName: "NT AUTHORITY\\system"
      containers:
      - name: kube-proxy
        image: ghcr.io/appmana/kube-proxy:v1.34.6-appmana.post.1-calico-hostprocess
        env:
        - name: KUBEPROXY_DISABLE_DSR
          value: "true"
```

## kind/QEMU validation demo

The local validation uses Linux kind workers plus one Windows Server 2022 QEMU
worker on `br0`. The runbook is in
`docs/kind-qemu-calico-validation.md`.

After the runbook has started kind and joined the QEMU Windows worker, roll the
branch images and run the wrapper:

```bash
kubectl -n kube-system set image ds/calico-node-windows \
  node=ghcr.io/appmana/node:v3.29.6-appmana.post.2 \
  felix=ghcr.io/appmana/node:v3.29.6-appmana.post.2 \
  confd=ghcr.io/appmana/node:v3.29.6-appmana.post.2

kubectl -n kube-system set image ds/kube-proxy-windows \
  kube-proxy=ghcr.io/appmana/kube-proxy:v1.34.6-appmana.post.1-calico-hostprocess

hack/appmana/run-kind-qemu-health.sh
```

The wrapper applies the kind/QEMU forwarding rules, creates the test namespace,
and runs `hack/appmana/ipv6-health-check.sh`. The health script owns the test
matrix: Linux pod and Windows pod sources to Linux pod, Windows pod, Linux
service, Windows service, kube-dns UDP, WAN, plus host-to-pod reachability. The
kube-dns check sends a raw UDP DNS query from the Windows pod through the
kube-dns ClusterIP and validates the returned A record for the Linux-backed test
service. It creates separate IPv4 and IPv6 SingleStack services so IPv6
ClusterIP routing failures are visible independently from IPv4.

The external-router case was validated on June 7, 2026 with a disposable FRR
router attached to the kind Docker network while testing the shared AppMana
Calico lab flow. The same BGP configuration applies to v3.29: with only the
IPv4 BGPPeer, FRR learned IPv4 pod routes and learned zero IPv6 prefixes; after
adding the Linux-scoped IPv6 BGPPeer and the dual-stack `BGPConfiguration`
service CIDRs, FRR learned the Linux IPv6 pod block and `fd98::/108`, and ping
from the router host to a Linux pod IPv6 address passed 3/3.

## Build and publish

GitHub Actions builds and tests the branch on every push to
`windows-dual-stack-v3.29.6`. The workflow publishes:

```text
ghcr.io/appmana/node:v3.29.6-appmana.post.2-linux-amd64
ghcr.io/appmana/node:v3.29.6-appmana.post.2-windows-ltsc2022
ghcr.io/appmana/node:v3.29.6-appmana.post.2
```

The final tag is the multi-platform manifest used by k0s.
