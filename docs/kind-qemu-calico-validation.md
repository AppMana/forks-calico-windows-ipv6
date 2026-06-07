# Kind + QEMU Calico Validation

This is the local validation loop for AppMana Calico images. It uses a Linux
kind cluster plus the Windows QEMU worker and must not target the live cluster.

The lab validates two situations:

- Linux Calico on kind workers.
- Windows Calico on the QEMU worker, joined to the same kind API server.

The important Windows contract is that HostProcess startup mirrors the image's
`CalicoWindows` tree to `C:\CalicoWindows`. Host-side CNI configuration and
helpers read that path outside the container sandbox.

## Quick Start

From a clean workstation state, the normal loop is:

```bash
cd /home/administrator/Documents/forks-calico-windows-ipv6
export KUBECONFIG=/tmp/appmana-calico-kind/kubeconfig
mkdir -p "$(dirname "$KUBECONFIG")"

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

Start the Windows QEMU worker from the management repo:

```bash
cd /home/administrator/Documents/appmana/appmana-management/src/appmana_management
USE_BASELINE=1 bash autoinstall/windows/vm-test.sh
```

Return to the Calico fork, roll the image under test, and run the full matrix:

```bash
cd /home/administrator/Documents/forks-calico-windows-ipv6

kubectl -n kube-system set image ds/calico-node-windows \
  node=harbor.appmana.com/appmana-shared/node-windows:${TAG} \
  felix=harbor.appmana.com/appmana-shared/node-windows:${TAG} \
  confd=harbor.appmana.com/appmana-shared/node-windows:${TAG}
kubectl -n kube-system rollout status ds/calico-node-windows --timeout=10m

hack/appmana/run-kind-qemu-health.sh
```

`run-kind-qemu-health.sh` applies the local kind/QEMU forwarding rules, creates
the `calico-qemu-test` namespace, and runs the health matrix with
`--windows-exec hcsdiag`.

## Start the Linux kind cluster

Create or reuse the isolated kubeconfig:

```bash
export KUBECONFIG=/tmp/appmana-calico-kind/kubeconfig
mkdir -p /tmp/appmana-calico-kind
```

Start a three-node Linux kind cluster:

```bash
kind create cluster \
  --name appmana-calico \
  --config hack/test/kind/kind.config \
  --kubeconfig "$KUBECONFIG"
```

Confirm the Linux nodes:

```bash
kubectl get nodes -o wide
```

Expected Linux node names:

```text
kind-control-plane
kind-worker
kind-worker2
```

Install the Calico CRDs and manifests normally for the lab. Do not apply these
commands to the production kubeconfig:

```bash
kubectl create -f libcalico-go/config/crd
kubectl apply -f manifests/calico.yaml
```

For Windows BGP mode in this mixed kind/QEMU lab, the IPv4 pool must not use
IPIP. Linux routes to the Windows pod block must go over `eth0`, not `tunl0`;
Windows BGP mode does not decapsulate Linux IPIP traffic.

```bash
kubectl patch ippool.crd.projectcalico.org kind-ipv4-pool \
  --type merge \
  -p '{"spec":{"ipipMode":"Never","vxlanMode":"Never"}}'

kubectl get ippools.crd.projectcalico.org \
  -o custom-columns=NAME:.metadata.name,CIDR:.spec.cidr,IPIP:.spec.ipipMode,VXLAN:.spec.vxlanMode
```

## Start the Windows QEMU worker

The QEMU harness lives in the AppMana management repo:

```bash
cd /home/administrator/Documents/appmana/appmana-management/src/appmana_management
```

One-time baseline creation, when the baseline disk does not exist:

```bash
bash autoinstall/windows/run-lab-test.sh
bash autoinstall/windows/snapshot-baseline.sh
```

Fast per-iteration boot from the baseline:

```bash
USE_BASELINE=1 bash autoinstall/windows/vm-test.sh
```

The VM must have the two NICs from `vm-test.sh`:

- bridge NIC on `br0`, MAC `52:54:00:01:23:45`, used by Kubernetes and Calico.
- QEMU user-mode NAT NIC with OOB WinRM/SSH forwards, `127.0.0.1:16985` and `127.0.0.1:12222`.

The bridge NIC is the cluster NIC and must remain a normal WAN-capable host
interface. Do not boot it on the kind Docker bridge. The OOB NIC is only a
break-glass path for WinRM/SSH if Calico or HNS breaks the cluster NIC.
Kubernetes, Calico BGP, host SSH, and host WAN traffic should all use
`10.2.0.180` on `br0`.

Verify the VM is attached to `br0` from the host:

```bash
bridge link show | grep tap0
ip neigh show dev br0 | grep 10.2.0.180
ssh -o StrictHostKeyChecking=no administrator@10.2.0.180 hostname
```

Inside Windows, verify the HNS L2Bridge is bound to the cluster NIC and that
the default route prefers the cluster `vEthernet` adapter over the OOB NAT NIC:

```bash
ssh -o StrictHostKeyChecking=no administrator@10.2.0.180 \
  'powershell -NoProfile -Command "
    Get-HnsNetwork | Select Name,Type,NetworkAdapterName,ManagementIP,ManagementIPv6;
    Get-NetRoute -DestinationPrefix 0.0.0.0/0 |
      Select InterfaceAlias,NextHop,RouteMetric,InterfaceMetric,
        @{n=\"TotalMetric\";e={\$_.RouteMetric+\$_.InterfaceMetric}} |
      Sort TotalMetric;
    Test-NetConnection 1.1.1.1 -Port 443 |
      Select SourceAddress,InterfaceAlias,RemoteAddress,TcpTestSucceeded
  "'
```

Expected shape:

```text
Calico L2Bridge: NetworkAdapterName=<cluster NIC>, ManagementIP=10.2.0.180
Default route: vEthernet (<cluster NIC>) via 10.2.0.1 has the lowest total metric
WAN TCP: SourceAddress=10.2.0.180, InterfaceAlias=vEthernet (<cluster NIC>), TcpTestSucceeded=True
```

After boot, start kubelet only when the lab DaemonSet is pointing at the image
under test:

```bash
ssh -o StrictHostKeyChecking=no administrator@10.2.0.180 'nssm start kubelet'
```

Confirm the Windows node joined the kind cluster:

```bash
kubectl get nodes -o wide
```

Expected Windows node name in this lab:

```text
appmana-000
```

## Build iteration images

Use post-style tags for candidates. Do not use `latest` for this critical image.
For local Harbor iteration, append the commit so every test points at an
immutable candidate tag:

```bash
TAG=v3.29.6-appmana.post.2-kind.$(git rev-parse --short=12 HEAD)
```

Build and push the Windows image. This helper is AppMana-specific build glue,
not a general Calico build script:

```bash
./hack/appmana/build-and-push-windows-image.sh --tag "$TAG"
```

The helper:

1. Cross-compiles `calico-node.exe`, `calico.exe`, `calico-ipam.exe`,
   `hns-ipv6-injector.exe`, and `hns-ipv6-hook.dll`.
2. Stamps `calico-node.exe` with `pkg/buildinfo.Version`,
   `pkg/buildinfo.GitRevision`, and `pkg/buildinfo.BuildDate`.
3. Verifies the Windows binary contains the requested tag and full git revision.
4. Copies the Windows BGP `confd` templates into `node/windows-packaging`.
5. Caches `nssm.exe` and `hns.psm1`.
6. Fetches the remote Windows BuildKit mTLS certs.
7. Builds and pushes `harbor.appmana.com/appmana-shared/node-windows:$TAG`.

Build and push the Linux image from the same commit:

```bash
make -C node image ARCH=amd64 NODE_IMAGE=node
docker tag node:latest-amd64 harbor.appmana.com/appmana-shared/node:${TAG}-linux-amd64
docker push harbor.appmana.com/appmana-shared/node:${TAG}-linux-amd64
```

## Roll only the lab DaemonSets

```bash
kubectl -n kube-system set image ds/calico-node \
  calico-node=harbor.appmana.com/appmana-shared/node:${TAG}-linux-amd64
kubectl -n kube-system rollout status ds/calico-node --timeout=10m
```

```bash
kubectl -n kube-system set image ds/calico-node-windows \
  node=harbor.appmana.com/appmana-shared/node-windows:${TAG} \
  felix=harbor.appmana.com/appmana-shared/node-windows:${TAG} \
  confd=harbor.appmana.com/appmana-shared/node-windows:${TAG}
kubectl -n kube-system rollout status ds/calico-node-windows --timeout=10m
```

Check the image IDs before trusting the test:

```bash
kubectl -n kube-system get pod -l k8s-app=calico-node \
  -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.containerStatuses[0].imageID}{"\n"}{end}'

kubectl -n kube-system get pod -l k8s-app=calico-node-windows \
  -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.containerStatuses[*].imageID}{"\n"}{end}'
```

## Apply kind/QEMU lab forwarding

The kind workers live behind Docker bridge `br-64a19c4dd412`, while the Windows
QEMU node lives on `br0`. Docker drops traffic into container addresses from
non-Docker interfaces unless the lab host explicitly permits it. Without these
rules, BGP can stay half-open, Windows pod-to-Linux-pod traffic can disappear at
the host, and Windows kube-proxy non-DSR service traffic can be dropped after
HNS SNATs it to the Windows node IP.

Apply these rules only on the local kind/QEMU host:

```bash
hack/appmana/apply-kind-qemu-forwarding.sh
```

The script discovers the kind bridge, kind subnet, Windows node IP, Calico block
affinities, and host `br0` address from the lab. The equivalent manual commands
are below for debugging:

```bash
WIN_NODE_IP=10.2.0.180
HOST_BR0_IP=10.2.0.55
KIND_BR=br-64a19c4dd412

sudo nft insert rule ip raw PREROUTING ip saddr "$WIN_NODE_IP" ip daddr 172.21.0.0/16 accept 2>/dev/null || true
sudo nft insert rule ip raw PREROUTING ip saddr 172.21.0.0/16 ip daddr "$WIN_NODE_IP" accept 2>/dev/null || true

sudo iptables -C DOCKER-USER -s "$WIN_NODE_IP/32" -d 172.21.0.0/16 -j ACCEPT 2>/dev/null || \
  sudo iptables -I DOCKER-USER 1 -s "$WIN_NODE_IP/32" -d 172.21.0.0/16 -j ACCEPT
sudo iptables -C DOCKER-USER -s 172.21.0.0/16 -d "$WIN_NODE_IP/32" -j ACCEPT 2>/dev/null || \
  sudo iptables -I DOCKER-USER 2 -s 172.21.0.0/16 -d "$WIN_NODE_IP/32" -j ACCEPT

sudo iptables -C DOCKER-USER -s 10.244.85.192/26 -d 10.244.0.0/16 -j ACCEPT 2>/dev/null || \
  sudo iptables -I DOCKER-USER 1 -s 10.244.85.192/26 -d 10.244.0.0/16 -j ACCEPT
sudo iptables -C DOCKER-USER -s 10.244.0.0/16 -d 10.244.85.192/26 -j ACCEPT 2>/dev/null || \
  sudo iptables -I DOCKER-USER 2 -s 10.244.0.0/16 -d 10.244.85.192/26 -j ACCEPT
sudo iptables -C DOCKER-USER -s "$WIN_NODE_IP/32" -d 10.244.0.0/16 -j ACCEPT 2>/dev/null || \
  sudo iptables -I DOCKER-USER 1 -s "$WIN_NODE_IP/32" -d 10.244.0.0/16 -j ACCEPT
sudo iptables -C DOCKER-USER -s 10.244.0.0/16 -d "$WIN_NODE_IP/32" -j ACCEPT 2>/dev/null || \
  sudo iptables -I DOCKER-USER 2 -s 10.244.0.0/16 -d "$WIN_NODE_IP/32" -j ACCEPT

sudo iptables -t nat -C POSTROUTING -s 172.21.0.0/16 -d "$WIN_NODE_IP/32" -j RETURN 2>/dev/null || \
  sudo iptables -t nat -I POSTROUTING 1 -s 172.21.0.0/16 -d "$WIN_NODE_IP/32" -j RETURN
sudo iptables -t nat -C POSTROUTING -s 10.244.0.0/16 -d 10.244.85.192/26 -j RETURN 2>/dev/null || \
  sudo iptables -t nat -I POSTROUTING 1 -s 10.244.0.0/16 -d 10.244.85.192/26 -j RETURN
sudo iptables -t nat -C POSTROUTING -s 10.244.85.192/26 -d 10.244.0.0/16 -j RETURN 2>/dev/null || \
  sudo iptables -t nat -I POSTROUTING 1 -s 10.244.85.192/26 -d 10.244.0.0/16 -j RETURN
sudo iptables -t nat -C POSTROUTING -s 10.244.0.0/16 -d "$WIN_NODE_IP/32" -j RETURN 2>/dev/null || \
  sudo iptables -t nat -I POSTROUTING 1 -s 10.244.0.0/16 -d "$WIN_NODE_IP/32" -j RETURN
sudo iptables -t nat -C POSTROUTING -s "$WIN_NODE_IP/32" -d 10.244.0.0/16 -j RETURN 2>/dev/null || \
  sudo iptables -t nat -I POSTROUTING 1 -s "$WIN_NODE_IP/32" -d 10.244.0.0/16 -j RETURN

sudo ip route replace 10.244.85.192/26 via "$WIN_NODE_IP" dev br0
sudo ip route replace 10.244.110.128/26 via 172.21.0.2 dev "$KIND_BR"
sudo ip route replace 10.244.162.128/26 via 172.21.0.3 dev "$KIND_BR"
sudo ip route replace 10.244.82.0/26 via 172.21.0.4 dev "$KIND_BR"
```

Windows also needs lab routes back to kind and Linux pod blocks through the
host `br0` address:

```bash
ssh -o StrictHostKeyChecking=no administrator@"$WIN_NODE_IP" 'powershell -NoProfile -Command "
  $if = (Get-NetIPInterface -InterfaceAlias \"vEthernet (Ethernet 2)\" -AddressFamily IPv4 -ErrorAction SilentlyContinue).InterfaceIndex
  if (-not $if) { $if = (Get-NetIPInterface -InterfaceAlias \"Ethernet 2\" -AddressFamily IPv4).InterfaceIndex }
  foreach ($prefix in @(\"172.21.0.0/16\", \"10.244.110.128/26\", \"10.244.162.128/26\", \"10.244.82.0/26\")) {
    Remove-NetRoute -DestinationPrefix $prefix -Confirm:$false -ErrorAction SilentlyContinue
    New-NetRoute -DestinationPrefix $prefix -NextHop \"10.2.0.55\" -InterfaceIndex $if -PolicyStore ActiveStore -RouteMetric 5 | Out-Null
  }
"'
```

If BGP peers are connected but Windows shows Linux pod routes as
`Unresolvable`, that is expected in this lab before the Windows static routes
above. The Linux BGP next-hop is a Docker address (`172.21.0.x`), which is not
directly connected to Windows.

## Run the copied health check

The copied script is `hack/appmana/ipv6-health-check.sh`. It creates one test
pod plus separate IPv4 and IPv6 ClusterIP services per node. For a Linux node
and a Windows node, it tests this matrix:

- Linux pod -> Linux pod, Windows pod, Linux service, Windows service, WAN.
- Windows pod -> Linux pod, Windows pod, Linux service, Windows service, WAN.
- Host -> Linux pod and Windows pod, unless `--skip-inbound` is set.

The script creates IPv4 and IPv6 SingleStack services separately. A dual-stack
cluster can pass pod-to-pod IPv6 while IPv6 ClusterIP fails because the service
CIDR is not advertised or because the upstream router is missing the relevant
IPv6 route. Treat IPv6 service failures as route/BGP failures until proven
otherwise; do not collapse them into the IPv4 service result.

The Windows backend image must be able to run PowerShell so the script can
start a tiny HTTP listener for the Windows service check.

For QEMU Windows, use `--windows-exec hcsdiag`. The Windows kubelet exec/log
path can fail with `remote error: tls: internal error`, which makes
Windows-origin checks look failed even when the dataplane works. The hcsdiag
mode gets the Windows container ID from pod status, SSHes to the Windows node,
and runs probes with `hcsdiag exec`.

Create a namespace for lab probes:

```bash
kubectl create namespace calico-qemu-test --dry-run=client -o yaml | kubectl apply -f -
```

Run it against the Linux and Windows nodes:

```bash
hack/appmana/run-kind-qemu-health.sh
```

The wrapper is equivalent to:

```bash
hack/appmana/apply-kind-qemu-forwarding.sh

bash hack/appmana/ipv6-health-check.sh \
  --namespace calico-qemu-test \
  --ipv4-pool kind-ipv4-pool \
  --ipv4-only \
  --windows-exec hcsdiag \
  --linux-image nicolaka/netshoot:latest \
  --win-image mcr.microsoft.com/windows/servercore:ltsc2022 \
  kind-worker2 appmana-000
```

Use `--ipv4-only` for the current local kind/QEMU test. Do not skip external or
inbound reachability when validating the full lab; those checks verify that the
host routes and Docker forwarding exceptions above are present.

Expected IPv4-only kind/QEMU result shape after rolling a branch candidate and
the matching Windows kube-proxy image:

```text
=== External Reachability ===
kind-worker2 -> https://1.1.1.1 (IPv4 WAN TCP): PASS
appmana-000 -> https://1.1.1.1 (IPv4 WAN TCP): PASS

=== Inbound Reachability (from this host) ===
-> kind-worker2 IPv4: PASS
-> appmana-000 IPv4: PASS

=== Service Reachability (2 nodes, all services) ===
kind-worker2(linux) -> kind-worker2(linux) Service IPv4: PASS
kind-worker2(linux) -> appmana-000(windows) Service IPv4: PASS
appmana-000(windows) -> kind-worker2(linux) Service IPv4: PASS
appmana-000(windows) -> appmana-000(windows) Service IPv4: PASS

=== Pod-to-Pod Reachability (2 nodes, all pairs) ===
kind-worker2(linux) -> kind-worker2 IPv4: PASS
kind-worker2(linux) -> appmana-000 IPv4: PASS
appmana-000(windows) -> kind-worker2 IPv4: PASS
appmana-000(windows) -> appmana-000 IPv4: PASS

Total: 12  Pass: 12  Fail: 0
```

Windows host SSH and WAN, Windows pod WAN, Windows/Linux pod-to-pod, and
Linux/Windows ClusterIP service traffic pass with the cluster NIC on `br0`.
Earlier failures were caused by missing kind/QEMU lab forwarding rules and by
using `kubectl exec` against a Windows kubelet whose exec path returned TLS
internal errors. RRAS BGP peers were `Connected`, `TransitRouting` was
`Enabled`, and Linux pod routes learned by RRAS were `Best`.

## Check Windows DSR and outbound NAT

Windows loopback DSR must be disabled in mixed Linux/Windows clusters unless a
specific all-Windows service path is being tested. The rendered CNI file on the
Windows host should show:

```bash
kubectl -n kube-system exec ds/calico-node-windows -c node -- \
  powershell -NoProfile -Command \
  '(Get-Content C:\etc\cni\net.d\10-calico.conf -Raw | ConvertFrom-Json).windows_loopback_DSR'
```

Expected:

```text
False
```

Validate `natOutgoing` by creating Windows pods after changing the pool. Endpoint
policies are applied during CNI ADD, so create a fresh pod for each pool setting.
The Windows pod must tolerate the node taints:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: win-nat-true
spec:
  nodeSelector:
    kubernetes.io/hostname: appmana-000
  tolerations:
  - operator: Exists
  containers:
  - name: pause
    image: mcr.microsoft.com/windows/servercore:ltsc2022
    command: ["cmd", "/c", "ping -t localhost"]
```

For `natOutgoing: false`, no normal `OutBoundNAT` endpoint policy should be
present:

```bash
kubectl patch ippool kind-ipv4-pool --type=merge \
  -p '{"spec":{"natOutgoing":false}}'
kubectl -n calico-qemu-test apply -f win-nat-false.yaml
kubectl -n calico-qemu-test wait --for=condition=Ready pod/win-nat-false --timeout=240s
POD_IP=$(kubectl -n calico-qemu-test get pod win-nat-false -o jsonpath='{.status.podIP}')
kubectl -n kube-system exec ds/calico-node-windows -c node -- \
  powershell -NoProfile -Command \
  "\$ep=Get-HnsEndpoint | Where-Object { \$_.IPAddress -eq '$POD_IP' }; \$ep.Policies | Where-Object { \$_.Type -like 'OutBound*' } | ConvertTo-Json -Depth 12"
```

For `natOutgoing: true`, the normal `OutBoundNAT` endpoint policy should be
present with service and Calico pool exceptions:

```bash
kubectl patch ippool kind-ipv4-pool --type=merge \
  -p '{"spec":{"natOutgoing":true}}'
kubectl -n calico-qemu-test apply -f win-nat-true.yaml
kubectl -n calico-qemu-test wait --for=condition=Ready pod/win-nat-true --timeout=240s
POD_IP=$(kubectl -n calico-qemu-test get pod win-nat-true -o jsonpath='{.status.podIP}')
kubectl -n kube-system exec ds/calico-node-windows -c node -- \
  powershell -NoProfile -Command \
  "\$ep=Get-HnsEndpoint | Where-Object { \$_.IPAddress -eq '$POD_IP' }; \$ep.Policies | Where-Object { \$_.Type -like 'OutBound*' } | ConvertTo-Json -Depth 12"
```

Always restore the lab pool after experiments:

```bash
kubectl patch ippool kind-ipv4-pool --type=merge \
  -p '{"spec":{"natOutgoing":true}}'
```

Observed in the QEMU lab when testing the AppMana Windows Calico and kube-proxy
fixes:

- `natOutgoing: false` rendered no `OutBoundNAT` policy.
- `natOutgoing: true` rendered `OutBoundNAT` with `10.96.0.0/16` and
  `10.244.0.0/16` exceptions.
- With no `CALICO_DSR_DISABLE` ConfigMap key, the rendered CNI still had
  `windows_loopback_DSR: false`; DSR was disabled by the image default.
- The Windows host could reach `1.1.1.1:443` via
  the cluster `vEthernet` adapter, source `10.2.0.180`.
- Windows pod WAN egress passed when the cluster NIC was WAN-capable and the
  pod check used `curl.exe --ssl-no-revoke` for Windows Schannel.
- Windows pod -> Linux-backed ClusterIP service passed through kube-proxy
  non-DSR HNS load balancing once the host allowed the SNATed
  `10.2.0.180 -> 10.244.0.0/16` lab path.

When the lab has a working dual-stack pod pool and host/WAN routes are in
scope, pass the IPv6 pool explicitly and omit the skip flags:

```bash
bash hack/appmana/ipv6-health-check.sh \
  --namespace calico-qemu-test \
  --ipv4-pool kind-ipv4-pool \
  --ipv6-pool kind-ipv6-pool \
  kind-worker kind-worker2 appmana-000
```

The health check is not enough by itself. Keep the route, BGP, HNS, and service
checks below because they catch the Windows failure mode where pods run but
Linux routes, Windows BGP rendering, or `C:\CalicoWindows` mirroring are wrong.

## Script Tests

The lab scripts have mocked tests so changes to command construction are caught
without needing QEMU or kind:

```bash
bash -n hack/appmana/apply-kind-qemu-forwarding.sh \
  hack/appmana/run-kind-qemu-health.sh \
  hack/appmana/ipv6-health-check.sh \
  hack/appmana/tests/run-appmana-script-tests.sh

hack/appmana/tests/run-appmana-script-tests.sh
```

These tests mock `kubectl`, `docker`, `ip`, `iptables`, `nft`, `ssh`, `scp`,
`iconv`, and `base64`. They verify:

- The forwarding script adds the Docker raw/filter/NAT exceptions and routes
  for the Windows node, Windows pod block, and Linux pod blocks.
- Docker network inspection can return an IPv6 subnet before IPv4; the script
  must still use the IPv4 kind subnet for host and Windows routes.
- The generated Windows PowerShell route payload sends kind and Linux pod-block
  routes through the host `br0` address.
- The health script uses `hcsdiag exec <container-id>` for Windows-origin
  probes when `--windows-exec hcsdiag` is set.
- The mocked health matrix includes Linux pod, Windows pod, Linux service,
  Windows service, separate IPv4/IPv6 ClusterIP checks, and WAN checks from
  both Linux and Windows sources.
- The wrapper applies forwarding and passes the expected default health-check
  arguments.

## Validate Linux kind networking

Create two Linux smoke pods and a ClusterIP service:

```bash
kubectl create namespace calico-smoke --dry-run=client -o yaml | kubectl apply -f -
kubectl -n calico-smoke run smoke-a --image=nicolaka/netshoot:latest \
  --restart=Never --overrides='{"spec":{"nodeSelector":{"kubernetes.io/hostname":"kind-worker"},"containers":[{"name":"smoke-a","image":"nicolaka/netshoot:latest","command":["sh","-c","sleep infinity"]}]}}'
kubectl -n calico-smoke run smoke-b --image=hashicorp/http-echo:1.0 \
  --restart=Never --overrides='{"spec":{"nodeSelector":{"kubernetes.io/hostname":"kind-worker2"},"containers":[{"name":"smoke-b","image":"hashicorp/http-echo:1.0","args":["-text=ok"],"ports":[{"containerPort":5678}]}]}}'
kubectl -n calico-smoke expose pod smoke-b --port=5678 --target-port=5678
kubectl -n calico-smoke wait --for=condition=Ready pod/smoke-a pod/smoke-b --timeout=180s
```

Run Linux pod-to-pod and service probes:

```bash
SMOKE_B_IP=$(kubectl -n calico-smoke get pod smoke-b -o jsonpath='{.status.podIP}')
SMOKE_SVC_IP=$(kubectl -n calico-smoke get svc smoke-b -o jsonpath='{.spec.clusterIP}')

kubectl -n calico-smoke exec smoke-a -- ping -c 3 -W 2 "$SMOKE_B_IP"
kubectl -n calico-smoke exec smoke-a -- curl -fsS "http://${SMOKE_SVC_IP}:5678"
```

## Validate Windows QEMU networking

Create or reuse one Linux client and one Windows client:

```bash
kubectl -n calico-qemu-test run linux-client --image=nicolaka/netshoot:latest \
  --restart=Never --overrides='{"spec":{"nodeSelector":{"kubernetes.io/hostname":"kind-worker2"},"containers":[{"name":"linux-client","image":"nicolaka/netshoot:latest","command":["sh","-c","sleep infinity"]}]}}'

kubectl -n calico-qemu-test run windows-client --image=harbor.appmana.com/appmana-shared/busybox:latest \
  --restart=Never --overrides='{"spec":{"nodeSelector":{"kubernetes.io/hostname":"appmana-000"},"imagePullSecrets":[{"name":"harbor"}],"containers":[{"name":"windows-client","image":"harbor.appmana.com/appmana-shared/busybox:latest","command":["cmd","/c","ping -t localhost"]}]}}'

kubectl -n calico-qemu-test wait --for=condition=Ready pod/linux-client pod/windows-client --timeout=600s
```

Run traffic both ways:

```bash
LINUX_IP=$(kubectl -n calico-qemu-test get pod linux-client -o jsonpath='{.status.podIP}')
WIN_IP=$(kubectl -n calico-qemu-test get pod windows-client -o jsonpath='{.status.podIP}')

kubectl -n calico-qemu-test exec linux-client -- ping -c 3 -W 2 "$WIN_IP"
kubectl -n calico-qemu-test exec windows-client -- cmd /c "ping -n 3 $LINUX_IP"
```

Check Linux routes to the Windows pod block are direct:

```bash
for n in kind-control-plane kind-worker kind-worker2; do
  docker exec "$n" ip route get "$WIN_IP"
done
```

Expected route shape:

```text
10.244.x.y via 172.21.0.1 dev eth0
```

Check BIRD has the Windows pod block via the Windows node:

```bash
WIN_BLOCK=$(kubectl get blockaffinities.crd.projectcalico.org \
  -o jsonpath='{range .items[?(@.spec.node=="appmana-000")]}{.spec.cidr}{"\n"}{end}' \
  | grep -v ':' | head -1)

for pod in $(kubectl -n kube-system get pod -l k8s-app=calico-node -o name); do
  kubectl -n kube-system exec "$pod" -c calico-node -- birdcl show route "$WIN_BLOCK" all
done
```

Check Windows BGP and rendered config:

```bash
kubectl -n kube-system exec ds/calico-node-windows -c confd -- powershell -NoProfile -Command \
  'Get-BgpPeer | Select PeerName,PeerIPAddress,PeerASN,ConnectivityStatus'

kubectl -n kube-system exec ds/calico-node-windows -c confd -- powershell -NoProfile -Command \
  'Get-BgpCustomRoute | Select Network,PolicyStore'

kubectl -n kube-system exec ds/calico-node-windows -c confd -- powershell -NoProfile -Command \
  'Get-Item C:\CalicoWindows\confd\peerings.ps1,C:\CalicoWindows\confd\blocks.ps1 | Select FullName,Length,LastWriteTime'
```

Check the host mirror contains the same binary as the HostProcess sandbox:

```bash
kubectl -n kube-system exec ds/calico-node-windows -c confd -- powershell -NoProfile -Command \
  '$sandbox="C:\hpc\CalicoWindows\calico-node.exe"; $hostPath="C:\CalicoWindows\calico-node.exe"; Get-FileHash $sandbox,$hostPath -Algorithm SHA256 | Select Path,Hash'

kubectl -n kube-system exec ds/calico-node-windows -c confd -- powershell -NoProfile -Command \
  'C:\CalicoWindows\calico-node.exe -v'
```

Check HNS state:

```bash
kubectl -n kube-system exec ds/calico-node-windows -c node -- powershell -NoProfile -Command \
  'Get-HnsNetwork | ? Name -eq Calico | Select Name,Type,ManagementIP,ManagementIPv6'

kubectl -n kube-system exec ds/calico-node-windows -c node -- powershell -NoProfile -Command \
  'Get-HnsEndpoint | Select Name,IPAddress,IPv6Address,VirtualNetworkName'
```

The pass condition for the current kind/QEMU lab is:

- Linux DaemonSet rolled and all Linux Calico pods are ready.
- Windows DaemonSet rolled and the QEMU pod is `3/3 Running`.
- `C:\CalicoWindows\calico-node.exe` hash matches `C:\hpc\CalicoWindows\calico-node.exe`.
- Windows `confd` rendered `peerings.ps1` and `blocks.ps1`.
- Windows BGP peers are `Connected`.
- Linux BIRD has the Windows pod block via the QEMU node.
- Linux kernel routes to the Windows pod IP go via `eth0`, not `tunl0`.
- Linux-to-Linux service smoke passes.
- Linux-to-Windows and Windows-to-Linux pod pings pass.
