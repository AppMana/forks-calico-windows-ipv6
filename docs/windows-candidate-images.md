# Windows candidate images

Branches under `cloud-provisioning/` publish candidate node and CNI images with
this tag format: `<base-version>-<sanitized-branch>-<12-character-commit>`.
The Windows architecture tag appends `-windows-ltsc2022`. The manifest uses that
Windows image and the matching Linux build. HostProcess uses the Windows host;
the workflow runs native networking contract tests on Server 2022 and 2025.

The candidate changes include:

- Exclude known workload IPv6 addresses from host-to-endpoint policy exemptions.
- Repair only existing exemptions affected by newly observed workload addresses.
- Verify opt-in VXLAN MTU before applying workload policy.
- Recheck one active endpoint per five-second tick to repair adapter-restart drift.

Set `FELIX_VXLANMTU` to the measured path budget. Zero preserves the platform
default. Configure the corresponding distro MTU and image pins together. Match
Calico CRDs and components to the branch's upstream release.

The MTU code derives from cloud-provisioning's Server 2022/2025 native experiments.
This combined branch still requires isolated VM CNI/policy, Service, fresh-worker,
and lifecycle tests. Image publication and unit coverage do not qualify a
production rollout. See [Windows isolation validation](windows-isolation-validation.md).
