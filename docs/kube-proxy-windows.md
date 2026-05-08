# kube-proxy on Windows with Calico (HostProcess + L2Bridge)

This document captures the **correct** kube-proxy DaemonSet configuration
for Windows worker nodes joined to a Calico-on-Windows cluster running in
BGP / L2Bridge mode (no overlay). It exists because three independent
defects stacked together produce a single failure mode that is easy to
misdiagnose: **pod -> ClusterIP TCP times out for every service**, while
pod -> pod-IP and pod -> external IP both work.

This is *not* a kube-proxy fork. It is a wrapped invocation of the
upstream HostProcess image (`sigwindowstools/kube-proxy:v1.34.6-calico-hostprocess`)
through a PowerShell start script mounted in via ConfigMap.

## Required preconditions on the node

These are all responsibilities of `calico-node-windows` and the host
provisioning playbook, not of kube-proxy. They are listed here because
kube-proxy *appears* healthy (logs show "Syncing Policies complete") even
when these are missing, so you have to verify them independently.

1. **`RemoteAccess` Windows feature in LAN-routing mode**:
   `Get-Service RemoteAccess` returns `Status=Running, StartType=Automatic`.
   Without it, calico-node's confd renders `Add-BgpRouter` /
   `Add-BgpPeer` calls that silently no-op, no IPv4 BGP session is
   established, and Linux backends have no return route to this node's
   pod /26. SYN-ACK from a Linux backend is black-holed and pod ->
   ClusterIP times out *even though* kube-proxy did its job.
   The fork's `node-service.ps1` runs `Install-RemoteAccess -VpnType
   RoutingOnly` on first boot if needed; the matching ansible task in
   `playbook_kubernetes_containerd.yaml` does the same outside the
   container.
2. **`Calico_ep` HNS endpoint exists** with an `IPAddress` (gateway+1
   of this node's pod CIDR — e.g. `10.3.48.194` if the pod CIDR is
   `10.3.48.192/26`). This IP is the **per-node source VIP** kube-proxy
   must use; see below.
3. **`Calico` HNS network exists with a `ManagementIP`** (the host's
   underlay IP).
4. **VFP switch extension is enabled** on the `Calico` vSwitch
   (default for L2Bridge networks; `Get-VMSwitchExtension -VMSwitchName
   Calico` should show `Microsoft Azure VFP Switch Extension` with
   `Enabled=True`).

## Required kube-proxy CLI flags

For `kernelspace` proxy mode on a Calico L2Bridge network with DSR
disabled, the **non-negotiable** flag set is:

| Flag | Required value | Why |
|---|---|---|
| `--proxy-mode` | `kernelspace` | Only mode that talks to HNS / VFP |
| `--enable-dsr` | `false` | DSR has its own bug class on L2Bridge; keep it off unless you have a specific reason |
| `--source-vip` | `Calico_ep` HNS endpoint IP | **Required.** Without this, every HCN ELB PolicyList that kube-proxy registers is `IsApplied=false` because VFP cannot resolve a source NIC. Pod -> ClusterIP TCP silently black-holes for every service. |
| `--hostname-override` | `$env:NODE_NAME` | Match the kubelet node name |
| `--kubeconfig` | path inside HostProcess sandbox | The kubeconfig delivered by the `kube-proxy` ConfigMap, after path rewriting (see start script) |

`--source-vip` discovery is dynamic — the IP is per-node and is only
known after `calico-node-windows` has created the `Calico_ep`
endpoint. The start script polls `Get-HnsEndpoint -Name 'Calico_ep'`
and pulls `IPAddress`.

## The "Policy already applied" gotcha

kube-proxy v1.34's reconcile loop logs `Policy already applied` and
**skips re-creating** an HCN ELB PolicyList when one already exists for
the (VIP, port) tuple. So if kube-proxy ran once without
`--source-vip`, then is restarted *with* `--source-vip`, the existing
PolicyLists keep the **stale empty source-vip** from the first run, HNS
still refuses to apply them into per-port VFP rules, and pod ->
ClusterIP stays broken until something forces re-creation.

Two ways to force re-creation:

1. `Restart-Service hns` — re-applies all PolicyLists from scratch.
   Disruptive (kills all pod networking briefly), and stale source-vip
   will resurface on the next kube-proxy roll.
2. **Wipe ELB-type PolicyLists at kube-proxy startup** so the next
   reconcile rebuilds them with the current `--source-vip`. Safe: only
   PolicyLists are wiped — `OutBoundNAT` policies live on
   `HnsEndpoint.Policies`, not in PolicyLists, so CNI per-pod state is
   untouched.

The example manifest below uses approach (2).

## Old pods need a one-time recycle

Pods that were running with the broken configuration keep their broken
HNS endpoint state until the pod is replaced. Once kube-proxy is fixed,
**new** pods get correct VFP LB rules immediately, but pre-existing
pods do not. To recover a broken deployment, roll its controller
(`kubectl rollout restart deploy/<name>`) — never delete pods directly.

## Verification on a node

```powershell
# 1. RemoteAccess up
Get-Service RemoteAccess
# Expect: Status=Running, StartType=Automatic

# 2. kube-proxy launched with --source-vip set to Calico_ep IP
Get-CimInstance Win32_Process -Filter "Name = 'kube-proxy.exe'" |
    Select-Object -ExpandProperty CommandLine
$cep = (Get-HnsEndpoint | Where-Object Name -eq 'Calico_ep').IPAddress
"Calico_ep IP: $cep"
# Expect: command line contains --source-vip=<the same IP>

# 3. Every ELB PolicyList is applied
$lbs = Get-HnsPolicyList | Where-Object {
    $_.Policies | Where-Object { $_.Type -eq 'ELB' }
}
"$(($lbs | Measure).Count) total, $(($lbs | Where IsApplied -eq $true | Measure).Count) applied"
# Expect: "<N> total, <N> applied"  (counts equal)

# 4. TCP from a fresh Windows pod to a ClusterIP
kubectl --context=local exec -n default <fresh-windows-pod> -- powershell -c `
    "Test-NetConnection 10.152.184.99 -Port 443"
# Expect: TcpTestSucceeded : True
```

## Example manifest

Drop this into `kube-system`. Adjust the image tag to your kube-proxy
version and the `KUBE_NETWORK` env var to your HNS network name if it
isn't `Calico`.

```yaml
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: kube-proxy-start
  namespace: kube-system
data:
  start-patched.ps1: |
    $NetworkName = "Calico"
    $sb = [Environment]::GetEnvironmentVariable('CONTAINER_SANDBOX_MOUNT_POINT','Process')
    if (-not $sb) { $sb = $PSScriptRoot + '/..' }
    $kproxy = "$sb/kube-proxy/kube-proxy.exe"
    ipmo -Force "$sb/kube-proxy/hns.psm1"

    Write-Host "Waiting for HNS network $NetworkName with ManagementIP..."
    while (-not (Get-HnsNetwork | Where-Object { $_.Name -eq $NetworkName -and $_.ManagementIP })) {
        Start-Sleep 2
    }
    Write-Host "HNS network $NetworkName ready."

    # Wait for Calico_ep host endpoint (created by calico-node). Its
    # IPAddress is the per-node source VIP that kube-proxy must use for
    # ELB SNAT in kernelspace+L2Bridge mode. Without --source-vip
    # kube-proxy creates HCN ELB policies but does not program
    # source-pod VFP rules, so pod->ClusterIP traffic bypasses the
    # load-balancer layer and times out.
    Write-Host "Waiting for Calico_ep endpoint..."
    while (-not ($cep = Get-HnsEndpoint | Where-Object { $_.Name -eq 'Calico_ep' -and $_.IPAddress })) {
        Start-Sleep 2
    }
    $sourceVip = $cep.IPAddress
    Write-Host "Calico_ep ready. Source VIP: $sourceVip"

    mkdir -force /var/lib/kube-proxy/ -ErrorAction SilentlyContinue | Out-Null
    $kubeconfig = "$sb/var/lib/kube-proxy/kubeconfig.conf"
    $winconfig = "$sb/var/lib/kube-proxy/kubeconfig-win.conf"
    if (Test-Path $kubeconfig) {
        $content = (Get-Content -Raw $kubeconfig) -replace '/var',"$sb/var"
        Set-Content -Path $winconfig -Value $content
        Copy-Item $winconfig /var/lib/kube-proxy/kubeconfig.conf -Force -ErrorAction SilentlyContinue
    }

    # Wipe stale ELB PolicyLists. kube-proxy v1.34's "Policy already
    # applied" path skips re-creation when matching policies exist, so
    # on a restart with changed flags (e.g. --source-vip after first
    # boot) the existing PolicyLists keep stale source-vip and HNS
    # never re-applies them into per-port VFP rules. Result: HCN ELB
    # exists, but pod->ClusterIP TCP times out because no LB DNAT runs
    # on the source pod's vSwitch port.
    #
    # Wiping ELB-type PolicyLists is safe: OutBoundNAT (per-endpoint
    # policies) lives on HnsEndpoint.Policies, not in PolicyLists.
    try {
        $stale = Get-HnsPolicyList | Where-Object {
            $_.Policies | Where-Object { $_.Type -eq 'ELB' }
        }
        if ($stale) {
            Write-Host "Removing $(($stale | Measure-Object).Count) stale ELB PolicyLists..."
            $stale | Remove-HnsPolicyList
        }
    } catch {
        Write-Host "WARNING: failed to wipe stale ELB PolicyLists: $($_.Exception.Message)"
    }

    $env:KUBE_NETWORK = $NetworkName

    $argList = @(
        "--hostname-override=$env:NODE_NAME",
        "--v=4",
        "--proxy-mode=kernelspace",
        "--kubeconfig=$winconfig",
        "--enable-dsr=false",
        "--source-vip=$sourceVip"
    )

    Write-Host "Starting kube-proxy: $kproxy $argList"
    & $kproxy @argList
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  labels:
    k8s-app: kube-proxy
  name: kube-proxy-windows
  namespace: kube-system
spec:
  selector:
    matchLabels:
      k8s-app: kube-proxy-windows
  template:
    metadata:
      labels:
        k8s-app: kube-proxy-windows
    spec:
      serviceAccountName: kube-proxy
      securityContext:
        windowsOptions:
          hostProcess: true
          runAsUserName: "NT AUTHORITY\\system"
      hostNetwork: true
      containers:
      - image: sigwindowstools/kube-proxy:v1.34.6-calico-hostprocess
        args: ["C:/var/lib/kube-proxy-start/start-patched.ps1"]
        workingDir: "$env:CONTAINER_SANDBOX_MOUNT_POINT/kube-proxy/"
        name: kube-proxy
        imagePullPolicy: Always
        env:
        - name: NODE_NAME
          valueFrom:
            fieldRef:
              apiVersion: v1
              fieldPath: spec.nodeName
        - name: KUBE_NETWORK
          value: "Calico"
        volumeMounts:
        - mountPath: /var/lib/kube-proxy
          name: kube-proxy
        - mountPath: /var/lib/kube-proxy-start
          name: kube-proxy-start
      nodeSelector:
        kubernetes.io/os: windows
      tolerations:
      - key: CriticalAddonsOnly
        operator: Exists
      - operator: Exists
      volumes:
      - configMap:
          name: kube-proxy
        name: kube-proxy
      - configMap:
          name: kube-proxy-start
        name: kube-proxy-start
  updateStrategy:
    type: RollingUpdate
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kube-proxy
  namespace: kube-system
```

## Companion ConfigMap from kubeadm/k0s

The `kube-proxy` ConfigMap (mounted at `/var/lib/kube-proxy`) is the
standard one written by kubeadm or the k0s control plane. It contains
`config.conf` (the `KubeProxyConfiguration`) and `kubeconfig.conf`. The
start script above rewrites `/var` paths inside the kubeconfig to the
HostProcess sandbox mount point and copies the result to
`/var/lib/kube-proxy/kubeconfig.conf` for kube-proxy to read. No
modifications to the upstream kubeadm/k0s ConfigMap are required.

## Related references

- `docs/windows-dual-stack.md` — IPv4/IPv6 dual-stack details for
  Calico-Windows.
- `node/windows-packaging/CalicoWindows/node/node-service.ps1` —
  RRAS bootstrap implementation in calico-node startup.
- `node/windows-packaging/CalicoWindows/libs/calico/calico.psm1` —
  `Test-RRASNeedsBootstrap` predicate.
- `node/windows-packaging/tests/calico.tests.ps1` — Pester coverage
  for the RRAS bootstrap predicate.
