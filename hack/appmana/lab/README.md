# AppMana Calico Labcontainers runner

This runner uses upstream Containerlab Go objects. It has no topology YAML to
maintain and no management network. Windows guest control uses QGA over serial;
the declared Ethernet link is the only simulated network connection.

The runner consumes prebuilt artifacts; it does not download or build Calico.
Use this fork's aligned branch, not upstream vanilla Calico. The migration was
tested against fork commit `54046893d4` (based on Calico 3.32.2).

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
satisfy this contract. The fork's image workflow now builds a separate
`ghcr.io/appmana/cni-windows:<image-tag>-windows-ltsc2022` with this fork's
installer, Calico/IPAM plugins, and IPv6 helper binaries, using the existing
`cni-plugin/Dockerfile-windows`. This package is for Calico networking; it does
not include the optional Flannel plugin. Both Windows images must be built from
the same source revision, resolved to digests, and staged before isolated tests.

Registry audit on 2026-09-22 found the aligned node image at
`ghcr.io/appmana/node@sha256:483bc6de68f86a94c1221716a4c35e320f07c2cbdd90fb555954b051a8a2da73`:
its version label identifies fork revision `54046893d40b`, Calico 3.32.2, and
its Windows image version is `10.0.20348.5622`. This is metadata evidence, not
runtime qualification. The older cloud-provisioning candidate digest starting
`4d771e55` identifies Calico 3.32.1 and its accompanying `docker.io/calico/cni-windows`
is not the fork. Do not use that candidate list for the aligned scenario.

The installer, node, and kube-controllers images were subsequently published
from fork commit `338d8a0c46d2`; image workflow run `35815057847` passed all 26
jobs. Runtime testing found a missing Linux bandwidth plugin, fixed in
`758ae7a9d868` and `b55378edd776`. Do not substitute vanilla images to make the
networking qualification pass.

The Linux workflow also builds and publishes kube-controllers using its native
Makefile target. A push of `feature/labcontainers-native-sdk` runs the existing
test gates before publishing branch-and-commit-specific image tags; it does not
update the stable `appmana-v3.32.2` tag. Publishing this consumer branch and
running that image pipeline require separate approval from publishing the SDK.

## Isolated k0s networking qualification

`TestLiveK0sWindowsNetwork` is an opt-in product scenario using native
Containerlab, k0s, Pod, and Service objects. Set `LABCONTAINERS_CALICO_MEDIA`
to an absolute path to a prepared read-only ISO, its SHA-256 in
`LABCONTAINERS_CALICO_MEDIA_SHA256`, and explicitly select `LABCONTAINERS_VM_IMAGE`,
`LABCONTAINERS_WINDOWS_IMAGE`, `LABCONTAINERS_LABD`, and `LABCONTAINERS_CONTAINERLAB`.
Run `GOWORK=off go test -v -count=1 -run '^TestLiveK0sWindowsNetwork$' -timeout 30m .`.

The ISO label must be `LCQUAL`, with Joliet filenames for Windows. Include the
verified Linux `k0s` and Windows `k0s.exe` binaries at its root, plus per-platform
image archives named `linux-*.tar` and `windows-*.tar`. OCI archives must retain
the exact configured image-reference annotations, including `:pinned@sha256:...`
where the native k0s image object uses that form. A digest-only import does not
satisfy containerd's exact sandbox image lookup. `crane pull --format oci
--annotate-ref` normalizes combined references to digest-only. After pulling,
run `go run ./cmd/oci-ref LAYOUT 'REPOSITORY:pinned@sha256:DIGEST'` before
archiving the layout. This uses the upstream OCI Index type, verifies the
descriptor digest, and preserves the exact requested runtime name. Inspect the
resulting `index.json` rather than assuming a pull option retained the tag.
The exact fork and supporting image references are explicit in the test's
native `ClusterImages` object. The locally built Linux proxy archive must expose
`docker.io/labcontainers/kube-proxy:a2c4329d5a8-linux`; its source and recipe are
in the Kubernetes migration worktree. The Linux CNI archive must expose
`docker.io/labcontainers/calico-cni:b55378edd776-linux`, built with this fork's
native `make -C cni-plugin image ARCH=amd64 CNI_PLUGIN_IMAGE=labcontainers/calico-cni`
target, including the bandwidth packaging correction. Verify the image's
`/opt/cni/bin/bandwidth` with `CNI_COMMAND=VERSION` before preparing the archive.
The ISO hash pins these local artifacts too; this is not qualification of a
published CNI image.
No downloads are attempted by the test or enabled in the guests.

This initial scenario checks bidirectional ordinary pod, ClusterIP, and DNS
reachability, cuts the only simulated data link, requires Linux-to-Windows pod-IP and service
requests to fail, and verifies recovery. QGA remains the out-of-band control
channel, including HNS/CNI failure diagnostics. It is not the complete dual-stack
or storage matrix.
An opted-out test is a skip, not qualification evidence. The 2026-09-23 runs
failed: single-node mode initially rejected joins; after correcting that,
image-name aliases and a Service-CIDR-only route were needed. Both nodes then
reached Ready, but Linux workloads failed on the missing `bandwidth` CNI binary
and Windows reported an HNS endpoint failure. The fixture now fixes the startup
mode, scopes the Linux Service-CIDR route to its data NIC, synchronizes Windows'
UTC clock before startup, and opens kubelet TCP only to the declared controller.
The bandwidth packaging fix is in the fork workflow. A fresh workload pass is
still required; diagnostic repairs to a running lab do not count as that pass.

The next diagnostic run confirmed two more no-WAN prerequisites: Calico's
Linux proxy-ARP gateway needs an explicit `169.254.1.1/32` route when there is
no default route; adding only that route changed the pod neighbor from
INCOMPLETE to REACHABLE and restored kube-controllers' API connectivity.
CoreDNS's default forwarder exits with no nameservers when the guest has no
upstream resolver. The fixture explicitly patches its native ConfigMap through
k0s's `CoreDNS.Patches` API to serve cluster DNS without forwarding. Neither
change enables WAN, adds a NIC, or belongs in the generic VM SDK defaults.

Windows startup then exposed a k0s template error: it renders the autodetection
method into `IP`, which Calico expects to be an address or `autodetect`. The
fixture patches both Windows DaemonSet containers using native Kubernetes
objects and `Calico.Patches`. In the diagnostic lab, running startup with
`IP=autodetect` and the normal service configuration created the Calico HNS
network and host endpoint successfully. This is not yet workload proof.
The fork also fixes startup stdout contaminating PowerShell's Boolean return:
logs plus `$false` previously became a truthy array, hiding the failed startup.
All 199 packaging Pester tests pass, including a new behavioral regression.
