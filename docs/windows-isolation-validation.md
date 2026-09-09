# Windows pod isolation in disposable VM clusters

Run `hack/appmana/windows-isolation-check.py` after the single-NIC VM harness
has joined its Linux and Windows workers. This complements
`hack/appmana/ipv6-health-check.sh` with source-identity and NetworkPolicy
checks. It does not prove that Windows native IPv6 Service translation works
until the checks pass on the actual Windows build.

The checker requires a dedicated kubeconfig, its context, the expected API
server, and the disposable cluster's `kube-system` namespace UID recorded by
the harness. It verifies those identities and the Windows build before
creating anything. It creates its own uniquely named namespace, uses only
namespaced test resources, and deletes that namespace after checking its UID.
It never changes the kubeconfig's current context, CNI configuration, routing,
Felix configuration, existing workload policy, or component images.

## Images and invocation

Supply digest-pinned test images already cached in the VM image. The Linux
image must contain `python3`; the Windows image must contain Windows
PowerShell and .NET `System.Net.Http`, with the correct container base for
the host OS. Nothing is installed by the test pods.

```bash
python3 hack/appmana/windows-isolation-check.py \
  --kubeconfig "$TEST_KUBECONFIG" \
  --context "$TEST_CONTEXT" \
  --server "$TEST_API_SERVER" \
  --cluster-uid "$TEST_CLUSTER_UID" \
  --linux-node "$LINUX_NODE" \
  --windows-node "$WINDOWS_NODE" \
  --windows-build 10.0.20348 \
  --linux-image "$LINUX_TEST_IMAGE_DIGEST" \
  --windows-image "$WINDOWS_2022_TEST_IMAGE_DIGEST" \
  --output "$RUN_OUTPUT/windows-2022-isolation.json"
```

Repeat the row with `--windows-build 10.0.26100`, the Windows Server 2025
node, and its matching test image. Obtain cluster identity from the harness's
recorded assets; do not derive the expected identity from whichever current
context happens to be selected. The output directory must already exist.

The existing health script remains useful for its UDP DNS and broader
connectivity checks. Invoke it with the same dedicated kubeconfig, whose
selected context must be the validated test context, and explicit test pools,
test namespace, and pinned images. The legacy `run-kind-qemu-health.sh`
wrapper changes kind-specific forwarding and is not part of this VM workflow.

## What the result means

The checker creates three ordinary pods per OS: an allowed client, a denied
client, and an HTTP backend. Both Windows clients are pinned to the same
Windows node. It creates separate IPv4 and IPv6 SingleStack Services for
each backend and requires both addresses on every pod.

Each phase contains 32 cases: four clients, two backends, two address families,
and direct Pod IP versus Service access. The phases are baseline, selective
ingress, and selective egress. Both allowed connectivity and forbidden
connectivity must match in two consecutive complete rounds before a phase
passes. An unreachable allowed client fails the test. A failed `kubectl exec`
or malformed probe response is an error, never evidence of policy enforcement.
Baseline connectivity must succeed before policy denial can be interpreted.

Every successful request reports the client's address as seen by the backend.
It must equal the source pod's address, including requests sent through a
Service. Consequently, successful traffic that was masqueraded to a gateway
or node address fails qualification. IPv4-mapped IPv6 representations are
normalized before comparison. JSON output contains the final observations and
the number of consecutive matching rounds for each phase; transient rounds
are used for convergence, not counted as additional test cases.

This is a TCP and same-node isolation regression check. It does not replace
the broader VM matrix for UDP, cross-namespace policy, Windows-to-Windows
traffic across nodes, self/hairpin Services, node deletion/replacement,
WireGuard reconnects, or reboot recovery. It also does not establish physical
NIC count; that is the VM harness's responsibility.

## IPv6 Service blocker

`ipv6ServiceFallthroughMasqCIDR` restores some IPv6 Service connectivity by
masquerading the original pod address on a Linux node. It does not preserve
pod-identity NetworkPolicy semantics. Leave production's existing setting and
image pins unchanged while testing candidate images in disposable clusters.
The checker deliberately does not enable this workaround or accept a
gateway-address allow rule as proof of isolation.

The endpoint-manager regression fix excludes known active and pending
workload IPv6 addresses from the host-to-pod exemption. If a newly observed
workload address was already present in an installed host exemption, only
the affected endpoints are queued for repair. This fixes a policy compiler
invariant; it does not supply native IPv6 Service DNAT or resolve the Service
fallthrough limitation. Actual Windows 2022 and 2025 VM validation remains
required before declaring support.

## Local regression checks

```bash
go test -mod=readonly ./felix/dataplane/windows/... -count=1
python3 -m unittest discover -s hack/appmana/tests -p 'test_windows_isolation_check.py' -v
bash hack/appmana/tests/run-appmana-script-tests.sh
```

The Go tests use the Linux HNS mock. The Python tests exercise context/cluster
guards, cleanup ownership, same-node fixtures, source rewriting, failed exec,
and policy convergence. When `pwsh` is installed, a localhost HTTP fixture
also checks PowerShell probe success and refused connections. These are
controller/probe tests, not live Windows dataplane evidence.
