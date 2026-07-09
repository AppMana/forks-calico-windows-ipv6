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

## IPv6 Service VIPs from Windows pods: routing fall-through (solved 2026-07-09)

Windows VFP never enforces IPv6 ILB DNAT on WS2022: kube-proxy programs a
correctly-shaped v6 ELB (right SourceVIP, right endpoints, rule visible in
VFP) and the flow still times out. This was previously classified as an
unfixable platform limitation (the two historical windows->v6-svc health
matrix cells). The correct model is: **the broken v6 ELB is inert, not a
blackhole** — VIP-destined v6 traffic falls through to the host routing
table. That makes v6 ClusterIPs from Windows pods workable with two pieces of
pure routing/NAT configuration and no code changes:

1. **Route the v6 service CIDR to a Linux node.** Windows RRAS already learns
   it over BGP when `BGPConfiguration.spec.serviceClusterIPs` advertises the
   v6 service CIDR (production advertises `fd98::/108`; the Windows nodes'
   route table shows it via a Linux node ULA). The receiving Linux node's
   kube-proxy performs the DNAT that Windows could not.
2. **Masquerade the detoured flows on every Linux node** so the reply returns
   through the DNAT node even when the chosen backend is on a different node
   (including Windows-hosted backends, which otherwise produce an asymmetric
   return path and RSTs):

   ```
   ip6tables -t nat -I POSTROUTING -s <pod-cidr-v6> \
     -m conntrack --ctorigdst <service-cidr-v6> -j MASQUERADE
   ```

   The conntrack original-destination match scopes the rule to exactly the
   flows that were DNAT'd from a v6 service VIP; direct pod-to-pod traffic is
   untouched. Trade-off: the backend sees the DNAT node's address instead of
   the Windows pod address for these flows, so pod-identity-based ingress
   NetworkPolicies on v6 service backends would not match Windows clients.

The kind/QEMU lab programs both pieces in
`hack/appmana/apply-kind-qemu-forwarding.sh` (host route stands in for RRAS in
the lab topology); with them the health matrix passes 26/26. Production needs
only the masquerade rule on Linux nodes (the RRAS route already exists);
candidate delivery mechanisms are the calico-node Linux startup (fork patch,
rolls with the DaemonSet) or felix-managed NAT rules.
