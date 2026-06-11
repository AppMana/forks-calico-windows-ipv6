# kube-proxy on Windows with Calico

Our kube-proxy HostProcess image is now its own fork at
[AppMana/forks-kube-proxy-calico-hostprocess-ipv6](https://github.com/AppMana/forks-kube-proxy-calico-hostprocess-ipv6).
It's the upstream `kubernetes-sigs/sig-windows-tools` image with a patched
`kube-proxy.exe` (stale ELB reconciliation, see that repo's `patches/`) plus a
`start.ps1` diff adding source-vip discovery for L2Bridge networks with DSR
disabled (mixed Windows/Linux clusters where DSR breaks cross-node ClusterIP
routing). See that repo's `hostprocess/calico/kube-proxy/kube-proxy.yml` for
the reference DaemonSet and `KUBEPROXY_DISABLE_DSR=true` env var to opt out of
DSR. Build runs in GitHub Actions on a windows-2022 runner and pushes to
`ghcr.io/appmana/kube-proxy:<k8sVersion>-calico-hostprocess`.

When Windows pods can reach a Linux service endpoint IP directly but fail
against the service ClusterIP, debug kube-proxy/HNS ELB state before changing
Calico BGP or IPPools. The live AppMana failure mode (May 8 and June 9, 2026)
was kube-dns / apiserver ClusterIP timeouts while direct endpoint IPs worked
and kube-proxy logged `Policy already applied`: the service exists at the
Kubernetes level, but Windows VFP is not enforcing the ClusterIP load
balancer.

Minimal Windows check — functional, per VIP:

```powershell
Test-NetConnection <endpoint-pod-or-node-ip> -Port <port>   # expect True
Test-NetConnection <service-cluster-ip> -Port <port>        # broken when False
```

Do NOT use `Get-HnsPolicyList`'s `IsApplied` field as the health signal: it
reads `false` for every PolicyList on healthy, fully-working Windows Server
2022 nodes (verified 2026-06-10 across three production nodes). Earlier
versions of this document keyed remediation on `IsApplied=false`; that
diagnostic is unreliable through the PowerShell module.

If direct endpoint traffic works but the ClusterIP does not, the fix belongs
in the kube-proxy fork (HNS load-balancer reconciliation and/or its stale-ELB
liveness probe, which restarts the container so `start.ps1` wipes and rebuilds
all ELB PolicyLists), not in Calico route advertisement. Use the newest
`v1.34.6-appmana.post.N-calico-hostprocess` tag for Kubernetes 1.34 / Calico
3.29 and the newest `v1.35.5-appmana.post.N` tag for Kubernetes 1.35 / Calico
3.31. Never use `v1.34.6-appmana.post.3` or `v1.35.5-appmana.post.4`: those
were published with the reconciliation patch silently not applied.
