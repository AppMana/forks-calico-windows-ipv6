# AppMana Calico Labcontainers runner

This runner uses upstream Containerlab Go objects. It has no topology YAML to
maintain and no management network. Windows guest control uses QGA over serial;
the declared Ethernet link is the only simulated network connection.

The runner consumes prebuilt artifacts; it does not download or build Calico.
Use this fork's release branch, not upstream vanilla Calico.

This module pins published Labcontainers commit `43833b0979f7` using Go's
pseudo-version. No local SDK workspace is required: `GOWORK=off go test ./...`
tests the published dependency. Build the matching daemon with
`go install github.com/appmana/labcontainers/cmd/labd@v0.2.0-alpha.2.0.20260922225920-43833b0979f7`
and set `LABCONTAINERS_LABD` to its absolute path.

Run existing project script tests using a preloaded image containing their tools:

```sh
go run . -case script-tests -image YOUR_PRELOADED_IMAGE -artifacts /absolute/evidence/scripts
```

Build the Windows tests from the fork root (outside the integration workspace):

```sh
GOWORK=off GOTOOLCHAIN=go1.26.0 GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go test -c -o /absolute/artifacts/calico-windows.test.exe ./cni-plugin/pkg/dataplane/windows
```

Then, from this runner module with `GOWORK=off`:

```sh
go run . -case windows-tests -image YOUR_PRELOADED_WINDOWS_IMAGE \
  -peer-image YOUR_PRELOADED_PEER_IMAGE \
  -binary /absolute/artifacts/calico-windows.test.exe -binary-sha256 EXPECTED_SHA256 \
  -artifacts /absolute/evidence/windows
```

Both VM and peer images must be selected explicitly; there is no default peer
image. Images use native `image-pull-policy: Never`. Use a repository tag or repository
digest, not a bare Docker image ID (Containerlab interprets it as an image name).
The expected executable SHA-256 is required and checked before launching the
daemon or creating a VM; the verified bytes are the bytes uploaded. Record this
pin when preparing the artifact from the aligned fork. A matching digest proves
content identity, not fork provenance or version alignment. The verified
executable's SHA-256 is printed. Guests are destroyed when the runner
exits; the explicit artifact directory is retained. Windows unit-test success is
not evidence of working Calico pod networking: that requires a separate aligned
Kubernetes/k0s scenario and workload reachability assertions.

## Windows k0s image contract

k0s uses separate node and CNI installer images. Its `install-cni` init container
runs `/opt/cni/bin/install.exe`; the node image's plugin binaries alone do not
satisfy this contract. The `windows/amd64` member of the fork's
`ghcr.io/appmana/cni:<image-tag>` manifest list is that installer image, with
this fork's Calico/IPAM plugins and IPv6 helper binaries, built from
`cni-plugin/Dockerfile-windows`. It does not include the optional Flannel
plugin. Node and CNI images come from the same release tag; the test pins their
per-platform manifest-list members by digest.

kube-controllers is upstream `calico/kube-controllers` at the same Calico
version. Linux kube-proxy, pause and CoreDNS are the k0s release's defaults,
and Windows kube-proxy is the fork's HostProcess image, so the qualified set is
the set a k0s cluster pins.

## Isolated k0s networking qualification

`TestLiveK0sWindowsNetwork` is an opt-in product scenario using native
Containerlab, k0s, Pod, and Service objects. Set `LABCONTAINERS_CALICO_MEDIA`
to an absolute path to a prepared read-only ISO, its SHA-256 in
`LABCONTAINERS_CALICO_MEDIA_SHA256`, and explicitly select `LABCONTAINERS_VM_IMAGE`,
`LABCONTAINERS_WINDOWS_IMAGE`, `LABCONTAINERS_LABD`, and `LABCONTAINERS_CONTAINERLAB`.
Run `GOWORK=off go test -v -count=1 -run '^TestLiveK0sWindowsNetwork$' -timeout 45m .`.

The ISO label must be `LCQUAL`, with Joliet filenames for Windows. Include the
verified Linux `k0s` and Windows `k0s.exe` binaries at its root, plus per-platform
image archives named `linux-*.tar` and `windows-*.tar`. OCI archives must retain
the exact configured image-reference annotations, including `:pinned@sha256:...`
where the native k0s image object uses that form. A digest-only import does not
satisfy containerd's exact sandbox image lookup. `crane pull --format oci
--annotate-ref` normalizes combined references to digest-only. After pulling,
run `go run ./cmd/oci-ref LAYOUT 'REPOSITORY:pinned@sha256:DIGEST'` before
archiving the layout. This uses the upstream OCI Index type, verifies the
descriptor digest, and retains both the exact requested runtime name and its
digest-only alias. Containerd's sandbox lookup requires the first while CRI's
normalized lookup also needs the second; either alone failed in live tests.
Inspect the resulting `index.json` rather than assuming a pull option retained
both names.
The exact fork and supporting image references are explicit in the test's
native `ClusterImages` object; each archive is a published image pulled by
that digest (`crane pull --format oci REPOSITORY@sha256:DIGEST`) and annotated
with `cmd/oci-ref`. The workload images are
`docker.io/nicolaka/netshoot@sha256:34eeca87...` (`linux-workload.tar`) and
`mcr.microsoft.com/windows/servercore@sha256:e10503b9...`
(`windows-workload.tar`), annotated with their digest-only names.
No downloads are attempted by the test or enabled in the guests.

The scenario invokes `ipv6-health-check.sh --existing` against native Go-created
pods and services: all-pairs HTTP over PodIP and ClusterIP, plus UDP cluster DNS,
before and after cutting the sole data link. WAN and host-inbound probes are
explicitly disabled. During the outage, serial-controlled local runtime exec
checks both directions and verifies local positive controls. Transport errors
do not count as evidence of a network outage. Windows layer preparation has a
separate deadline from workload readiness and bounded HTTP probes.

This is an IPv4 scenario, not the complete dual-stack or storage matrix.
An opted-out test is a skip, not qualification evidence. Investigation history
and run results belong in commit messages.
