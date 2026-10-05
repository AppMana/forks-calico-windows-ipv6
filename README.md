# AppMana Calico

Calico 3.32.2 with Windows BGP/IPv6, HNS recovery and mixed Linux/Windows
service fixes. Runs with **stock k0s 1.36.4**; no k0s fork is needed.

## Use

Use the [Calico adapter](windows-adapter/README.md) and its
[qualified release lock](windows-adapter/releases/k0s-1.36.4-calico-3.32.2-26615db8.json)
to configure k0sctl and generate Windows BGP manifests for GitOps.

| Image | Platforms |
| --- | --- |
| ghcr.io/appmana/node | Linux amd64/arm64; Windows Server 2022 amd64 |
| ghcr.io/appmana/cni | Linux amd64/arm64; Windows Server 2022 amd64 |
| docker.io/calico/kube-controllers | Unmodified upstream release selected by the lock |

The lock also selects matching Linux and Windows kube-proxy images.
Use its image references rather than guessing tags. k0s owns its generated
DaemonSets; configure image overrides in k0sctl, not by patching live workloads.

The qualified topology uses BGP/L2Bridge with DSR disabled. Windows IPv6 services
need [Linux routing fall-through](docs/kube-proxy-windows.md). A dual-stack ToR
needs Linux IPv4 and IPv6 BGP peers. Configure router import policy to prevent
Windows RRAS re-advertisements from hijacking another node's pod routes.

## Compatibility

“Tested” applies to the linked scope; “fails” is an observed failure; “unknown”
means qualification has not been established.

| Configuration | Result |
| --- | --- |
| k0s 1.36.4, Linux amd64 + Windows 2022, BGP/dual-stack, real VyOS WAN | [tested](https://github.com/AppMana/k0s-containerd-calico-windows-integration/tree/ec4c527520826a7ab3555fd16ff94453fd508094) |
| Linux components and Windows 2022/2025 unit/package checks | [tested](https://github.com/AppMana/forks-calico-windows-ipv6/actions/runs/37166214321) |
| Linux arm64 workload networking | unknown |
| Windows 2025 full mixed-cluster lifecycle | unknown |
| Windows IPv6 ClusterIP without Linux fall-through | fails |
| Other version/CNI combinations | unknown |

[Builds](.github/workflows/build-images.yml) · [Lab](hack/appmana/lab) ·
[Upstream](https://github.com/projectcalico/calico)
