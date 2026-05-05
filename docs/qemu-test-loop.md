# qemu test loop for calico-node-windows HNS L2Bridge changes

When iterating on `node-service.ps1` (or anything in this fork that runs
inside the calico-node-windows container), the question is always
"does this change brick the host's IPv4 management IP when HNS creates
the L2Bridge?". Bricking real hardware costs a console session and a
power cycle; bricking a qemu VM costs ~30 seconds.

This doc describes the qemu-VM testing loop that lets you iterate on
fork changes in ~5 minutes per cycle.

## Where the loop lives

The harness lives in the `appmana-management` repo (not this fork):

- `appmana-management/src/appmana_management/autoinstall/windows/vm-test.sh`
  — boots a qemu VM, supports `USE_BASELINE=1` to restore from the
  baseline qcow2 in ~5 seconds instead of running a fresh Windows
  install.
- `appmana-management/src/appmana_management/autoinstall/windows/run-lab-test.sh`
  — runs `playbook_kubernetes_containerd.yaml` end-to-end against the
  VM (used to *create* the baseline; not used per-iteration).
- `appmana-management/src/appmana_management/autoinstall/windows/snapshot-baseline.sh`
  — graceful-shutdown the VM, copy `disk.qcow2` →
  `disk-baseline.qcow2`. Run once.
- `appmana-management/src/appmana_management/autoinstall/windows/test-calico-iteration.sh`
  — restores baseline, boots VM, ssh's in, `nssm start kubelet`, watches
  for ARP/ping/winrm changes for 3 minutes, exits 0 (PASS) if the host
  stayed reachable through the L2Bridge rebind, exit 2 (BRICK) otherwise.

## Network requirements (one-time host setup)

The qemu VM bridges directly onto the LAN so it can reach the real
cluster control plane and so the operator's host can talk to it:

- `/etc/netplan/00-vm-bridge.yaml` migrates `enp119s0` into a Linux
  bridge `br0`, host's DHCP lease moves to `br0`. (See task #34 to
  codify this in `playbook_worker.yaml`.)
- `/etc/qemu/bridge.conf` contains `allow br0`; qemu's bridge-helper
  binary is setuid root so unprivileged qemu can attach.
- VyOS has a DHCP static-mapping for the VM's MAC
  `52:54:00:01:23:45` → `10.2.0.180`, plus a `protocols bgp neighbor
  10.2.0.180 peer-group calico` entry. (Task #35.)

## One-time baseline creation

```bash
cd appmana-management/src/appmana_management
ansible-galaxy collection install ansible.windows  # if not already
bash autoinstall/windows/run-lab-test.sh
# wait ~10 min: Windows installs, ansible provisions through the main
# play (containerd/envoy/kubelet registered+stopped via first-run gate)
# and the Windows Updates plays SKIP because skip_windows_updates=true
# in the qemu_lab_test inventory entry.

bash autoinstall/windows/snapshot-baseline.sh
# qemu shuts down gracefully, disk.qcow2 → disk-baseline.qcow2
```

After this, `/var/tmp/appmana-winauto-vm/disk-baseline.qcow2` is the
"ansible-provisioned, kubelet registered+stopped, no L2Bridge" state.

## Per-iteration loop

```bash
# 1. Edit something in this fork (e.g. node-service.ps1, the hook DLL,
#    Apply-WeakHost logic, etc.)

# 2. Rebuild + push the calico-node-windows container image to harbor
docker buildx build --platform windows/amd64 \
  --build-context hooksrc=cni-plugin/cmd/hns-ipv6-hook \
  -t harbor.appmana.com/appmana-shared/node-windows:test-X \
  -f node/Dockerfile-windows --push .

# 3. Update the cluster's calico-windows DaemonSet to point at the new
#    image SHA, force-roll if needed (calico-windows-3.29.6.yaml in
#    appmana-cluster pins by sha256 — bump it, commit, flux reconciles)

# 4. Run one iteration
cd appmana-management/src/appmana_management
bash autoinstall/windows/test-calico-iteration.sh
```

Output:
```
[10:23:01] Restoring baseline → starting VM
[10:23:06] Polling for WinRM (max 120s)
[10:23:21] Pre-start: 10.2.0.180 ARP=REACHABLE
[10:23:21] Pre-start: ping=OK
[10:23:21] Starting kubelet on the VM
[10:23:24] arp=REACHABLE ping=OK winrm=OK
[10:24:34] arp=REACHABLE ping=OK winrm=OK     ← survived calico-node start
[10:25:24] arp=INCOMPLETE ping=no winrm=no    ← bricked at L2Bridge rebind
[10:26:24] arp=INCOMPLETE ping=no winrm=no
BRICK: host management IP was lost
```

Each cycle: ~5 min wall-clock.

## Known failure mode the loop reproduces

As of 2026-05-04, *every* fresh Windows worker bricks at the calico-
node-windows L2Bridge moment (verified on appmana-026 *and* in qemu).
The IPv6 ManagementIP hook DLL we built (see
`hns-managementipv6-history.md`) successfully pins the IPv6 management
address, but the IPv4 address gets eaten when HNS rebinds the physical
NIC to the L2Bridge transparent layer. The host loses ARP-reachability
on `10.2.0.x` (or whichever subnet the management IP lives on) until
the calico-node-windows pod is removed and HNS state is reset (which
needs console access).

The qemu loop reproduces this in ~3 minutes from VM boot, so we can
iterate on fork changes without burning real-hardware reboot cycles.

## Resolution (2026-05-05)

**Root cause**: Windows kernel BSOD (`BugCheck 0x3B SYSTEM_SERVICE_EXCEPTION`,
`STATUS_ACCESS_VIOLATION` at `+0xb36` in an HNS/vmswitch driver) triggered by
the FIRST L2Bridge create on Windows Server 2022 build `20348.587` (RTM CU).
The host crashes, reboots, BSODs again on the next calico-node-windows pod
schedule, hits the 2-failed-boots Recovery threshold, and lands at the
"It looks like Windows didn't load correctly" screen. From the outside, this
manifests as the host going ARP-INCOMPLETE permanently — what we kept
calling "the brick" was actually Windows itself crashing in a reboot loop.

**Fix**: install Windows updates that take the build past `20348.4773`. Any
modern cumulative update (we used `KB5082137` + `KB5082142` + `KB5082427`,
landing at `20348.5020`) contains the kernel-driver fix. Verified end-to-end
on the qemu lab VM:

- Pre-patch (`20348.587`): every fresh L2Bridge create → BSOD → reboot loop
- Post-patch (`20348.5020`): pod 3/3 Running, node Ready, Calico L2Bridge
  with correct ManagementIP=10.2.0.180 / ManagementIPv6 set, stable for 35+
  minutes with zero new restarts.

**Diagnostic process that found it**:

1. Added a second qemu NIC (slirp/NAT) so we kept WinRM/SSH access via
   `127.0.0.1:16985` / `127.0.0.1:12222` even when the LAN-side primary NIC
   went unreachable.
2. With OOB intact, used `qemu monitor screendump` to grab the console while
   the LAN was down — saw the Windows Recovery screen.
3. Booted into Recovery → Command Prompt, used `reg load` to mount the
   offline `SYSTEM` hive and set `kubelet` service `Start=4` (Disabled) so
   Windows could finish booting without scheduling the calico-node pod.
4. After the disabled kubelet let Windows stay up, ran the playbook with
   `skip_windows_updates: false` to install all pending updates.
5. Re-enabled kubelet (`Set-Service kubelet -StartupType Automatic`) and
   rebooted. Pod onboarded cleanly on the patched build.

**Operational impact**: the install-time `autoinstall` payload still ships
build `20348.587`. Slipstream of a current CU into `install.wim` (task #36)
is blocked by the Microsoft SSU-chain dependency change post-2023, so the
autoinstall-then-WindowsUpdate two-step is the production onboarding path.
The patched baseline qcow2 is at
`/var/tmp/appmana-winauto-vm/disk-baseline-patched.qcow2` for fast lab
iteration; restore from it instead of `disk-baseline.qcow2` to skip the
~30-minute Windows Update step.

The `hns-ipv6-hook` fork changes (IPv4 hook extension, pre-create injection,
PID-skip marker, `calico-node.exe -startup` skip-if-bridge-exists) are still
correct and useful for the IPv6 ManagementIPv6 pinning, but they are
secondary — without the Windows Update fix, the BSOD wins regardless.

## Status (2026-05-05) — pre-resolution notes below

**Pre-create injection of the IPv4+IPv6 hook works.** External L2Bridge
is created with the correct ManagementIP=10.2.0.180 / ManagementIPv6=
fd5a:8000:1:0:.../64. Verified end-to-end on the qemu lab VM: host's
ARP/WinRM survives External creation, node joins as Ready.

Hook log evidence (qemu lab, build 20348.587):
```
hns-ipv6-hook: installed at 0x..., desired ManagementIPv6=fd5a:8000:1:0:bce6:dce1:5dd2:77ce ManagementIP=10.2.0.180
```

`Get-HnsNetwork` post-create:
```
Name     Type     ManagementIP ManagementIPv6 AdapterName
----     ----     ------------ -------------- -----------
External L2Bridge 10.2.0.180
```

**Remaining issue: External→Calico transition still bricks transiently
(~1min outage).** When `calico-node.exe -startup` converts External to
the production Calico L2Bridge, the host briefly loses ARP/WinRM, then
recovers. The hook IS installed and the IPv4 cfg pin is correct, but
something during the bridge swap evades the filter or causes an
unrelated outage. Suspects:
- `calico-node.exe -startup` may call `Restart-Service hns` itself,
  destroying the working bridge.
- The Calico L2Bridge create may scan the NIC at a moment when 10.2.0.180
  is mid-transition between physical Ethernet and a vEthernet, and
  HNS picks something briefly other than 10.2.0.180 — though the hook
  should filter that.
- A separate kernel/HNS race during vEthernet swap.

Workaround until diagnosed: appmana-003 (which had Calico L2Bridge
already in place when the new image landed) skips External creation
entirely via the `$existingCalico` branch — no transition, no brick,
3/3 Running with 0 restarts. So nodes already onboarded under the old
image do not regress; only fresh first-onboard cycles oscillate.

## What needs to change in the fork (working hypotheses)

The IPv6 hook injection works because we patch
`iphlpapi!GetAdaptersAddresses` inside `svchost-hns` so HNS's
ManagementIPv6 picker sees only the desired ULA. The IPv4 picker uses
a different code path that the hook doesn't intercept.

Candidate fixes to test in this loop:

1. **Extend the hook** to also filter the IPv4 unicast addresses
   returned to HNS, pinning the desired IPv4. Mirrors the IPv6
   approach exactly. Most surgical.
2. **Pre-strip GUA-equivalent IPv4** before kubelet starts (a
   `Strip-NonClusterIPv4` companion to `Strip-NonClusterIPv6`). Less
   surgical but doesn't require the DLL hook for IPv4.
3. **Post-create rebind** — after L2Bridge creation, explicitly
   `Add-NetIPAddress` the host IP onto `vEthernet (Calico)` and set
   the route. Most aggressive; may fight HNS ownership.

Whichever we land on, the iteration is "edit fork → rebuild image →
roll DaemonSet → `test-calico-iteration.sh` → BRICK or PASS".
