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
