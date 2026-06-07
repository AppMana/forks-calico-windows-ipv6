# AppMana Calico v3.29 for k0s Windows/Linux clusters

This branch builds AppMana's Calico v3.29 image for mixed Linux and Windows
k0s clusters. The published image is a multi-platform manifest:

```text
ghcr.io/appmana/node:v3.29.6-appmana.post.1
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
            envVars:
              FELIX_IPV6SUPPORT: "true"
              CALICO_NETWORKING_BACKEND: "windows-bgp"
              CALICO_DSR_DISABLE: "true"
```

After k0s installs the baseline manifests, patch the Calico Linux and Windows
DaemonSets to use the multi-platform image:

```bash
kubectl -n kube-system set image ds/calico-node \
  calico-node=ghcr.io/appmana/node:v3.29.6-appmana.post.1

kubectl -n kube-system set image ds/calico-node-windows \
  node=ghcr.io/appmana/node:v3.29.6-appmana.post.1 \
  felix=ghcr.io/appmana/node:v3.29.6-appmana.post.1 \
  confd=ghcr.io/appmana/node:v3.29.6-appmana.post.1
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
  IP6_AUTODETECTION_METHOD: "cidr=2001:db8:10:2::/64"
```

For IPv4 pod networking in the validated kind/QEMU lab:

```yaml
apiVersion: projectcalico.org/v3
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
apiVersion: projectcalico.org/v3
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
enabled. For an upstream router or ToR, use a BGPPeer like:

```yaml
apiVersion: projectcalico.org/v3
kind: BGPPeer
metadata:
  name: upstream-router
spec:
  peerIP: 10.2.0.1
  asNumber: 65000
  keepOriginalNextHop: true
```

`keepOriginalNextHop: true` matters when Windows learns mesh routes and peers
with an external router. It prevents Windows from re-advertising mesh-learned
routes as if Windows were the next-hop for every pod block.

Expected Windows checks:

```powershell
Get-BgpRouter | Select BgpIdentifier,LocalASN
Get-BgpPeer | Select PeerName,PeerIPAddress,PeeringState
Get-BgpRouteInformation -Type All | ? Best -eq $true
```

Peers should be `Connected`, and pod blocks should appear as best routes.

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
        image: ghcr.io/appmana/node:v3.29.6-appmana.post.1
      - name: felix
        image: ghcr.io/appmana/node:v3.29.6-appmana.post.1
      - name: confd
        image: ghcr.io/appmana/node:v3.29.6-appmana.post.1
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
        image: ghcr.io/appmana/node:v3.29.6-appmana.post.1
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

Use Linux kind workers plus one Windows Server 2022 QEMU worker on `br0`.

Start the Linux kind cluster:

```bash
export KUBECONFIG=/tmp/appmana-calico-kind/kubeconfig
kind create cluster \
  --name appmana-calico \
  --config hack/test/kind/kind.config \
  --kubeconfig "$KUBECONFIG"
kubectl create -f libcalico-go/config/crd
kubectl apply -f manifests/calico.yaml
kubectl patch ippool.crd.projectcalico.org kind-ipv4-pool \
  --type merge \
  -p '{"spec":{"ipipMode":"Never","vxlanMode":"Never","natOutgoing":true}}'
```

Start the Windows QEMU worker from the AppMana management repo:

```bash
USE_BASELINE=1 bash autoinstall/windows/vm-test.sh
```

Roll the validated images and run the health matrix:

```bash
kubectl -n kube-system set image ds/calico-node-windows \
  node=ghcr.io/appmana/node:v3.29.6-appmana.post.1 \
  felix=ghcr.io/appmana/node:v3.29.6-appmana.post.1 \
  confd=ghcr.io/appmana/node:v3.29.6-appmana.post.1

kubectl -n kube-system set image ds/kube-proxy-windows \
  kube-proxy=ghcr.io/appmana/kube-proxy:v1.34.6-appmana.post.1-calico-hostprocess
```

Validate these paths:

```text
Linux pod -> Windows pod
Windows pod -> Linux pod
Linux pod -> Windows service
Windows pod -> Linux service
Linux pod -> WAN
Windows pod -> WAN
Host -> Linux pod
Host -> Windows pod
```

The same health-check structure is documented on the v3.31 branch in
`docs/kind-qemu-calico-validation.md`; the image tags above are the v3.29/v1.34
equivalents.

## Build and publish

GitHub Actions builds and tests the branch on every push to
`windows-dual-stack-v3.29.6`. The workflow publishes:

```text
ghcr.io/appmana/node:v3.29.6-appmana.post.1-linux-amd64
ghcr.io/appmana/node:v3.29.6-appmana.post.1-windows-ltsc2022
ghcr.io/appmana/node:v3.29.6-appmana.post.1
```

The final tag is the multi-platform manifest used by k0s.
