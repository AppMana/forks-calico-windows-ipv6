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
