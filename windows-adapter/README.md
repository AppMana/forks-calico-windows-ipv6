# Calico adapter for stock k0s

Generates aligned image settings and Windows BGP resources for k0s.
Use it to maintain k0sctl and GitOps without a k0s fork or admission controller.

## Use

Obtain the adapter from the Calico release assets, or build it from this directory:

```sh
go build -o calico-windows-adapter ./cmd/calico-windows-adapter
```

Set RELEASE_LOCK to the [qualified release lock](releases/k0s-1.36.4-calico-3.32.2-26615db8.json).

```sh
./calico-windows-adapter --mode k0s-images --release-lock "$RELEASE_LOCK"
```

Merge the returned image settings into spec.k0s.config.spec.images in k0sctl.yaml.
Render Windows resources using your cluster's addresses:

```sh
./calico-windows-adapter --mode render --release-lock "$RELEASE_LOCK" \
  --api-host 192.0.2.10 --api-port 6443 \
  --service-cidr 10.96.0.0/12 --dns-address 10.96.0.10 \
  --autodetection-method cidr=192.0.2.0/24 \
  --ipv6-autodetection-method cidr=fd00:1::/64
```

Commit the output to GitOps. For direct installation, use plan then apply with an
explicit kubeconfig and the approval value returned by plan. Run after the API
and Calico prerequisites are available, before waiting for Windows readiness.
Choose one resource owner: GitOps or direct apply.

The adapter does not configure pools, ToR routing, containerd or host restarts.
See [Calico compatibility](../README.md) and the command's --help.

| Configuration | Result |
| --- | --- |
| Rendering, release-lock and approval contracts | [tested](https://github.com/AppMana/forks-calico-windows-ipv6/actions/runs/37166214321) |
| Stock k0s 1.36.4, Linux amd64 + Windows 2022 BGP | [tested](https://github.com/AppMana/k0s-containerd-calico-windows-integration/tree/ec4c527520826a7ab3555fd16ff94453fd508094) |
| Linux arm64 runtime / Windows 2025 full deployment | unknown |
