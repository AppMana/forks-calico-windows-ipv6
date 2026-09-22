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

The new installer-image workflow has been linted and its Windows installer
cross-compiled locally; its Windows image build and publication still need to
run. No matching published `appmana/cni-windows` or `appmana/kube-controllers`
repository was available in that audit. Do not substitute vanilla images to
make the networking qualification pass.

The Linux workflow also builds and publishes kube-controllers using its native
Makefile target. A push of `feature/labcontainers-native-sdk` runs the existing
test gates before publishing branch-and-commit-specific image tags; it does not
update the stable `appmana-v3.32.2` tag. Publishing this consumer branch and
running that image pipeline require separate approval from publishing the SDK.
