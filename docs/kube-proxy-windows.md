# kube-proxy on Windows with Calico

Our kube-proxy HostProcess image is now its own fork at
[AppMana/forks-kube-proxy-calico-hostprocess-ipv6](https://github.com/AppMana/forks-kube-proxy-calico-hostprocess-ipv6).
It's the upstream `kubernetes-sigs/sig-windows-tools` image with a 27-line
diff in `hostprocess/calico/kube-proxy/start.ps1` adding source-vip
discovery for L2Bridge networks with DSR disabled (mixed Windows/Linux
clusters where DSR breaks cross-node ClusterIP routing). See that repo's
`hostprocess/calico/kube-proxy/kube-proxy.yml` for the reference DaemonSet
and `KUBEPROXY_DISABLE_DSR=true` env var to opt out of DSR. Build runs in
GitHub Actions on a windows-2022 runner and pushes to
`ghcr.io/appmana/kube-proxy:<k8sVersion>-calico-hostprocess`.

When Windows pods can reach a Linux service endpoint IP directly but fail
against the service ClusterIP, debug kube-proxy/HNS ELB state before changing
Calico BGP or IPPools. The live AppMana failure mode was kube-dns ClusterIP
timeouts with HNS ELB PolicyLists present but `IsApplied=false`, while
kube-proxy logged `Policy already applied`. That means the service route exists
at the Kubernetes level, but Windows has not applied the VFP load-balancer
policy for the ClusterIP.

Minimal Windows check:

```powershell
$vip = "10.152.184.10"
Get-HnsPolicyList |
  Where-Object { $_.Policies | Where-Object { $_.Type -eq "ELB" -and $_.VIP -eq $vip } } |
  Select-Object ID, IsApplied, @{n="Policies";e={$_.Policies | ConvertTo-Json -Compress}}
```

If direct endpoint traffic works and the ClusterIP ELB is `IsApplied=false`,
the fix belongs in the kube-proxy fork's HNS load-balancer reconciliation, not
in Calico route advertisement. Use
`ghcr.io/appmana/kube-proxy:v1.34.6-appmana.post.3-calico-hostprocess` for
Kubernetes 1.34 / Calico 3.29, or
`ghcr.io/appmana/kube-proxy:v1.35.5-appmana.post.4-calico-hostprocess` for
Kubernetes 1.35 / Calico 3.31.
