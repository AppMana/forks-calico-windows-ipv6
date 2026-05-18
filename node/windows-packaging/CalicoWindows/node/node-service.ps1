# Copyright (c) 2018-2021 Tigera, Inc. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http:#www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# This script is run from the main Calico folder.
. .\config.ps1

ipmo .\libs\calico\calico.psm1 -Force
ipmo .\libs\hns\hns.psm1 -Force -DisableNameChecking

function Get-TokenRefresherPid()
{
    return $(Get-WmiObject Win32_Process -Filter "name = 'calico-node.exe'" | Select-Object CommandLine, ProcessId | Where-Object -Property CommandLine -match ".*calico-node.exe.*-monitor-token.*").ProcessId
}

function Start-TokenRefresher()
{
    Write-Host "Starting Calico token refresher..."
    Start-Process -NoNewWindow .\calico-node.exe -ArgumentList "-monitor-token"
    Write-Host "Calico token refresher running on PID" $(Get-TokenRefresherPid)
}

function Ensure-TokenRefresher()
{
    if (-not $(Get-TokenRefresherPid))
    {
        Write-Host "Calico token refresher is not running, restarting it"
        Start-TokenRefresher
    }
}

function Restart-TokenRefresher()
{
    $tokenRefresherPid = Get-TokenRefresherPid
    if ($tokenRefresherPid)
    {
        Write-Host "Restarting Calico token refresher"
        Stop-Process -force -Id $tokenRefresherPid
    }
    Start-TokenRefresher
}

function Get-HnsServicePid()
{
    try {
        $svc = Get-CimInstance Win32_Service -Filter "Name = 'hns'" -ErrorAction Stop
        if ($svc -and $svc.ProcessId) { return [int]$svc.ProcessId }
    } catch {
        Write-Host ("WARNING: Get-HnsServicePid failed: " + $_.Exception.Message)
    }
    return 0
}

# Apply-WeakHost: enable Weak Host model on the management interface
# (vEthernet (Ethernet)) for both IPv4 and IPv6 address families.
#
# Without WeakHost, Windows enforces the strong host model: a packet
# arriving on vEthernet (Ethernet) destined for a local pod IP (whose
# route points at vEthernet (Calico_ep)) is rejected as "not for this
# interface". With WeakHost, Windows accepts the packet and forwards
# it via the matching route on the other vEthernet adapter. Required
# for cross-node Linux->Windows pod traffic, which arrives on the
# management adapter and must be forwarded to a pod endpoint.
#
# Idempotent. HNS network creation/recreation re-binds vEthernet
# (Ethernet) and resets these settings to Disabled, so this must be
# called after every calico-node.exe -startup, not just once at
# container start.
function Apply-WeakHost()
{
    # Don't use Get-NetAdapter: it fails with "Provider load failure"
    # in HostProcess containers (StandardCimv2 WMI provider missing).
    # Use Set-NetIPInterface -InterfaceAlias directly, which goes
    # through a different provider and works.
    $alias = 'vEthernet (Ethernet)'
    foreach ($af in @("IPv4","IPv6")) {
        try {
            Set-NetIPInterface -InterfaceAlias $alias -WeakHostReceive Enabled -WeakHostSend Enabled -AddressFamily $af -ErrorAction Stop
        } catch {
            Write-Host ("WARNING: Apply-WeakHost: Set-NetIPInterface " + $af + " failed: " + $_.Exception.Message)
        }
    }
    try {
        $state = Get-NetIPInterface -InterfaceAlias $alias -ErrorAction Stop | Select-Object AddressFamily,WeakHostReceive,WeakHostSend
        foreach ($s in $state) {
            Write-Host ("WeakHost on " + $alias + " " + $s.AddressFamily + ": Receive=" + $s.WeakHostReceive + " Send=" + $s.WeakHostSend)
        }
    } catch {
        Write-Host ("WARNING: Apply-WeakHost: Get-NetIPInterface failed: " + $_.Exception.Message)
    }
}

# Inject-HnsMgmtIpHook: pre-inject the hns-ipv6-hook DLL into svchost-hns
# BEFORE the first L2Bridge is created. This is the only correct point —
# if we wait until after kubelet is detected (the kubelet-restart loop
# below), the External placeholder L2Bridge has already been created and
# HNS has already picked the wrong ManagementIP/ManagementIPv6, which
# silently locks ARP/NS at the vSwitch and bricks the host.
#
# Idempotent: the DLL's DllMain detects an existing FF 25 trampoline at
# iphlpapi!GetAdaptersAddresses and refuses to re-patch, so calling this
# function multiple times (once pre-create, again in the kubelet-restart
# loop) is safe.
#
# Returns the (possibly-empty) desired pair as a 2-tuple
# @($desiredV6, $desiredV4) so callers can log the values.
function Inject-HnsMgmtIpHook()
{
    $hookPaths = Get-CalicoHnsHookPaths
    $injector  = $hookPaths.InjectorPath
    $dll       = $hookPaths.DllPath
    if (-not ((Test-Path $injector) -and (Test-Path $dll))) {
        Write-Host ("Inject-HnsMgmtIpHook: artifacts not staged at " + $hookPaths.InstallDir + "; skipping")
        return @($null, $null)
    }
    if ($env:CALICO_HNS_IPV6_HOOK -eq 'false') {
        Write-Host "Inject-HnsMgmtIpHook: disabled by CALICO_HNS_IPV6_HOOK=false"
        return @($null, $null)
    }

    # Derive the desired IPv6 + IPv4. Pure helpers in calico.psm1
    # (Resolve-DesiredHnsManagement{IPv4,IPv6}) hold the InterfaceAlias
    # filter so they can be unit-tested without a live host.
    $desiredV6 = $env:CALICO_DESIRED_HNS_MGMT_IPV6
    if ([string]::IsNullOrEmpty($desiredV6) -and $env:IP6_AUTODETECTION_METHOD -like 'cidr=*') {
        $cidr = $env:IP6_AUTODETECTION_METHOD.Substring(5).Split(',')[0].Trim()
        $prefix = ($cidr -split '/')[0] -replace '::$',':'
        $addrs = Get-NetIPAddress -AddressFamily IPv6 -ErrorAction SilentlyContinue
        $desiredV6 = Resolve-DesiredHnsManagementIPv6 -Addresses $addrs -Prefix $prefix
    }

    # The brick mechanism for IPv4 mirrors IPv6: HNS L2Bridge installs
    # EnableOverrideReceiveRoutingForLocalAddressesIpv4, which delivers
    # ARP only for the registered ManagementIP. If HNS picks the wrong
    # IPv4 (transient DHCP renewal, APIPA, or one mid-transition
    # between the physical NIC and vEthernet (Calico)), ARP for the
    # host's actual management IPv4 is silently dropped at the vSwitch.
    $desiredV4 = $env:CALICO_DESIRED_HNS_MGMT_IPV4
    if ([string]::IsNullOrEmpty($desiredV4) -and $env:IP_AUTODETECTION_METHOD -like 'cidr=*') {
        $cidr4 = $env:IP_AUTODETECTION_METHOD.Substring(5).Split(',')[0].Trim()
        try {
            $addrs4 = Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue
            $desiredV4 = Resolve-DesiredHnsManagementIPv4 -Addresses $addrs4 -NetworkCIDR $cidr4
        } catch {
            Write-Host ("Inject-HnsMgmtIpHook: WARNING: cannot parse IP_AUTODETECTION_METHOD=" + $env:IP_AUTODETECTION_METHOD + ": " + $_.Exception.Message)
        }
    }

    if ([string]::IsNullOrEmpty($desiredV6) -and [string]::IsNullOrEmpty($desiredV4)) {
        Write-Host "Inject-HnsMgmtIpHook: no desired ManagementIP/ManagementIPv6 derived; skipping"
        return @($null, $null)
    }

    # SKIP Restart-Service hns if the hook is already installed in the
    # CURRENT svchost-hns process. Each Restart-Service hns destroys all
    # HNS networks (including a working Calico L2Bridge), forcing
    # calico-node.exe -startup to recreate it. The bridge recreation
    # involves an HNS NIC rebind that briefly drops ARP/WinRM, and
    # kubelet's liveness probe interprets this as a container failure,
    # killing calico-node and triggering another full container restart.
    # On restart, this function runs again, restarts hns again, the cycle
    # repeats, and the cluster oscillates indefinitely.
    #
    # The hook persists for the lifetime of svchost-hns. We use a marker
    # file in C:\opt\calico-hns-ipv6\injected.flag to indicate "already
    # injected for the current boot." If the file exists and is newer than
    # the system's last boot time, skip Restart-Service hns (which would
    # destroy the working Calico bridge and trigger a brick→pod-restart
    # oscillation).
    #
    # The file MUST be written BEFORE Restart-Service hns, because the
    # restart kills this calico-node container (it shares HNS state with
    # the host) and we never reach any post-Restart-Service code.
    # Decide whether to re-inject. Test-HnsMgmtIpHookMarker is a pure
    # function in calico.psm1 — see its comment for the rationale. The
    # only outcome that skips Restart-Service is 'skip'.
    $markerPath = $hookPaths.MarkerPath
    $bootTime = (Get-CimInstance Win32_OperatingSystem -ErrorAction SilentlyContinue).LastBootUpTime
    $haveCalicoNetwork = $false
    try {
        $haveCalicoNetwork = [bool](Get-HnsNetwork -ErrorAction SilentlyContinue | Where-Object { $_.Name -eq 'Calico' -and $_.Type -eq 'L2Bridge' })
    } catch {}
    $currentHnsPid = Get-HnsServicePid
    $markerLine = Get-HnsMgmtIpHookMarkerLine -DesiredV4 $desiredV4 -DesiredV6 $desiredV6 -HnsPid $currentHnsPid
    $decision = Test-HnsMgmtIpHookMarker -MarkerPath $markerPath -BootTime $bootTime -HaveCalicoNetwork $haveCalicoNetwork -DesiredV4 $desiredV4 -DesiredV6 $desiredV6 -CurrentHnsPid $currentHnsPid
    Write-Host ("Inject-HnsMgmtIpHook: marker decision=" + $decision + " desiredPair='" + $markerLine + "' haveCalicoNetwork=" + $haveCalicoNetwork + " hnsPid=" + $currentHnsPid)
    if ($decision -eq 'skip') {
        return @($desiredV6, $desiredV4)
    }

    if (-not $haveCalicoNetwork) {
        # Restart hns to evict any stale hook from a previous boot before
        # the first L2Bridge is created. When a Calico bridge already
        # exists, do not restart hns here: that destroys the bridge. In
        # that case inject into the current HNS process below.
        try {
            Write-Host "Inject-HnsMgmtIpHook: restarting hns service to evict any stale hook before first L2Bridge"
            Restart-Service hns -Force -ErrorAction Stop
            $deadline = (Get-Date).AddSeconds(30)
            while ((Get-Date) -lt $deadline) {
                $svc = Get-Service hns -ErrorAction SilentlyContinue
                if ($svc -and $svc.Status -eq 'Running') { break }
                Start-Sleep -Milliseconds 500
            }
            $currentHnsPid = Get-HnsServicePid
            $markerLine = Get-HnsMgmtIpHookMarkerLine -DesiredV4 $desiredV4 -DesiredV6 $desiredV6 -HnsPid $currentHnsPid
        } catch {
            Write-Host ("Inject-HnsMgmtIpHook: WARNING: could not restart hns service: " + $_.Exception.Message)
        }
    } else {
        Write-Host "Inject-HnsMgmtIpHook: Calico bridge exists; injecting into current hns process without Restart-Service"
    }

    Write-Host ("Injecting hns-ipv6-hook for desired ManagementIPv6=" + $desiredV6 + " ManagementIP=" + $desiredV4)
    $injArgs = @('-dll', $dll)
    if (-not [string]::IsNullOrEmpty($desiredV6)) { $injArgs += @('-desired-mgmt-ipv6', $desiredV6) }
    if (-not [string]::IsNullOrEmpty($desiredV4)) { $injArgs += @('-desired-mgmt-ipv4', $desiredV4) }
    & $injector @injArgs
    if ($LastExitCode -eq 0) {
        New-Item -ItemType Directory -Force -Path (Split-Path $markerPath -Parent) | Out-Null
        Set-Content -Path $markerPath -Value $markerLine -Force -Encoding ASCII
        Write-Host ("Inject-HnsMgmtIpHook: wrote installed marker " + $markerPath + " (" + $markerLine + ")")
    } else {
        Write-Host ("Inject-HnsMgmtIpHook: WARNING: injector exited " + $LastExitCode + "; not writing installed marker")
    }

    return @($desiredV6, $desiredV4)
}

# Strip-NonClusterIPv6: temporarily remove host IPv6 addresses that
# don't match the desired management IPv6, so HNS picks the desired
# address when it scans the NIC at L2Bridge create/refresh time.
#
# Selection precedence:
#   1. CALICO_DESIRED_HNS_MGMT_IPV6 (exact IPv6, no /prefix) — keep
#      ONLY this address (and link-local). Removes everything else,
#      including same-prefix neighbours. Most explicit; supports any
#      operator choice (ULA, GUA, manual).
#   2. IP6_AUTODETECTION_METHOD = "cidr=<prefix>" — keep RA-derived
#      IPv6s inside that prefix, remove RA-derived IPv6s outside.
#      Works for both ULA-as-mgmt and GUA-as-mgmt setups; the choice
#      is the operator's via the configmap.
#   3. Anything else — no-op + warning.
#
# Removed addresses are RA-derived; SLAAC re-adds them shortly after
# from the next RA. The window between strip and SLAAC re-add is the
# only time HNS can observe the filtered NIC state, which is exactly
# the time we WANT it to observe (it scans then). This is the simpler
# alternative to the hns-ipv6-hook DLL injection — no kernel-mode
# trampolines, no svchost LoadLibrary, no per-Windows-build prologue
# verification. The trade-off is a brief (RA-period) window where the
# stripped addresses are missing from the host NIC.
#
# === Why this exists ===
#
# HNS L2Bridge installs a VFP rule (internally named
# EnableOverrideReceiveRoutingForLocalAddressesIpv4 / Ipv6 in
# HostNetSvc.dll) that delivers Neighbor Solicitations to the
# management OS only for the network's registered ManagementIP and
# ManagementIPv6. NS for any other host IPv6 is silently dropped by
# the vSwitch. Confirmed by HostNetSvc.dll PDB symbols:
#   HNS::Service::Network::SDNLayer::UpdateManagementIp
#   HNS::Service::Core::NetworkEntityManager::EnableOverrideReceiveRoutingForLocalAddressesIpv6
#
# HNS picks ManagementIPv6 by scanning the underlying NIC at network
# create/refresh time. There is no input field for it: hcsshim's
# typed HNSNetwork struct (v0.11.x, v0.14.x, current main) declares
# only ManagementIP, and HNS's POST /networks silently drops any
# ManagementIPv6 in the input JSON. The HCN schema in microsoft/hnslib
# doesn't have it either. AKS's Azure/AgentBaker/.../windowsnodereset
# .ps1 confirms there is no setter — they Restart-Service hns to
# retrigger the auto-pick.
#
# So the only lever is what's on the NIC at scan time. When the BGP
# source is a stable ULA but a SLAAC-derived GUA is also present, HNS
# picks the GUA and Linux peers can never resolve the ULA via NDP.
# Stripping the GUA forces HNS to pin the ULA.
#
# === Behaviour by configuration ===
#
# Triggered by IP6_AUTODETECTION_METHOD = "cidr=<prefix>":
#   - Prefix in fc00::/7 (ULA): strip RA-derived IPv6 addresses outside
#     the prefix. Pods are unaffected (they get GUAs from Calico IPAM
#     blocks, not host SLAAC).
#   - Prefix in 2000::/3 (GUA): no-op + warning. Operator has chosen
#     a rotating-prefix BGP source; we don't second-guess it.
#
# Any other IP6_AUTODETECTION_METHOD value (first-found, interface=,
# can-reach=, kubernetes-internal-ip): no-op + warning. We can't
# determine the chosen prefix without running the autodetect, and the
# operator can switch to "cidr=" form to opt in.
#
# === Idempotent / safe ===
#
# Touches only RA-derived addresses; manual / DHCP-assigned ones are
# left alone. Skips fe80:: link-local. Logs every kept and every
# removed address. Safe to call on every loop iteration. Failures
# anywhere are warnings, never fatal — this hook must never block
# calico-node startup.
function Strip-NonClusterIPv6()
{
    # Mode 1: explicit desired address. Keep ONLY that exact IPv6 (and
    # link-local); remove every other RA-derived host IPv6.
    $desired = $env:CALICO_DESIRED_HNS_MGMT_IPV6
    if (-not [string]::IsNullOrEmpty($desired)) {
        $desiredIP = $null
        if (-not [System.Net.IPAddress]::TryParse($desired, [ref]$desiredIP)) {
            Write-Host ("Strip-NonClusterIPv6: WARNING: cannot parse CALICO_DESIRED_HNS_MGMT_IPV6='" + $desired + "'; skipping")
            return
        }
        Strip-IPv6 -mode 'exact' -targetIP $desiredIP -label $desired
        return
    }

    # Mode 2: cidr= autodetect. Keep IPv6s inside the configured /prefix;
    # remove RA-derived IPv6s outside it. Works for any prefix (ULA or
    # GUA) — the operator picks via IP6_AUTODETECTION_METHOD.
    $method = $env:IP6_AUTODETECTION_METHOD
    if ([string]::IsNullOrEmpty($method)) {
        Write-Host "Strip-NonClusterIPv6: IP6_AUTODETECTION_METHOD is empty; IPv6 is disabled, skipping"
        return
    }
    if ($method -notlike 'cidr=*') {
        Write-Host ("Strip-NonClusterIPv6: WARNING: IP6_AUTODETECTION_METHOD='" + $method + "' is not 'cidr=...'; skipping.")
        Write-Host "Strip-NonClusterIPv6: WARNING: set IP6_AUTODETECTION_METHOD=cidr=<prefix>/64 OR set CALICO_DESIRED_HNS_MGMT_IPV6=<exact-ipv6> to enable strip."
        return
    }
    $cidr = $method.Substring(5).Split(',')[0].Trim()
    $parts = $cidr -split '/'
    if ($parts.Count -ne 2) {
        Write-Host ("Strip-NonClusterIPv6: WARNING: malformed cidr '" + $cidr + "'; skipping")
        return
    }
    $prefixIP = $null
    if (-not [System.Net.IPAddress]::TryParse($parts[0], [ref]$prefixIP)) {
        Write-Host ("Strip-NonClusterIPv6: WARNING: cannot parse '" + $parts[0] + "' as IPv6; skipping")
        return
    }
    $prefixLen = 0
    if (-not [int]::TryParse($parts[1], [ref]$prefixLen)) {
        Write-Host ("Strip-NonClusterIPv6: WARNING: cannot parse prefix length '" + $parts[1] + "'; skipping")
        return
    }
    Strip-IPv6 -mode 'prefix' -prefixIP $prefixIP -prefixLen $prefixLen -label $cidr
}

# Internal worker: walks vEthernet (Ethernet*) and unlinks IPv6
# addresses that don't match the configured filter.
#
#   mode='exact'   targetIP must be set; only $targetIP and link-local
#                  survive.
#   mode='prefix'  prefixIP+prefixLen must be set; addresses inside
#                  that /N and link-local survive.
#
# Touches only RA-derived (PrefixOrigin=RouterAdvertisement) addresses
# in 'prefix' mode and same in 'exact' mode (manual / DHCP addresses
# stay put). Failures are warnings.
function Strip-IPv6([string]$mode, $targetIP = $null, $prefixIP = $null, [int]$prefixLen = 0, [string]$label = '')
{
    $inPrefix = {
        param($addr, $base, $bits)
        $ab = $addr.GetAddressBytes()
        $bb = $base.GetAddressBytes()
        if ($ab.Length -ne 16 -or $bb.Length -ne 16) { return $false }
        $whole = [int][Math]::Floor($bits / 8)
        $partial = $bits - ($whole * 8)
        for ($i = 0; $i -lt $whole; $i++) {
            if ($ab[$i] -ne $bb[$i]) { return $false }
        }
        if ($partial -gt 0) {
            $mask = [byte](0xFF -shl (8 - $partial) -band 0xFF)
            if (($ab[$whole] -band $mask) -ne ($bb[$whole] -band $mask)) {
                return $false
            }
        }
        return $true
    }

    # Use InterfaceAlias filtering directly. Get-NetAdapter relies on
    # the StandardCimv2 WMI provider which fails with "Provider load
    # failure" inside HostProcess containers (verified on Server 2022
    # build 20348). Get-NetIPAddress / Get-NetIPInterface use a
    # different provider and DO work, so go through them.
    $candidates = Get-NetIPAddress -AddressFamily IPv6 -ErrorAction SilentlyContinue |
                    Where-Object { ($_.InterfaceAlias -like 'vEthernet (Ethernet*' -or
                                    $_.InterfaceAlias -like 'Ethernet*') -and
                                   $_.IPAddress -notlike 'fe80*' -and
                                   $_.PrefixOrigin -eq 'RouterAdvertisement' }
    if (-not $candidates) {
        Write-Host "Strip-IPv6: no RA-derived IPv6 addresses on management vNIC; nothing to do"
        return
    }
    foreach ($c in $candidates) {
        $ipObj = $null
        if (-not [System.Net.IPAddress]::TryParse($c.IPAddress, [ref]$ipObj)) { continue }
        $keep = $false
        if ($mode -eq 'exact') {
            $keep = $ipObj.Equals($targetIP)
        } else {
            $keep = (& $inPrefix $ipObj $prefixIP $prefixLen)
        }
        if ($keep) {
            Write-Host ("Strip-IPv6: keeping " + $c.IPAddress + " (matches " + $label + ")")
            continue
        }
        try {
            Remove-NetIPAddress -InterfaceIndex $c.InterfaceIndex -IPAddress $c.IPAddress -Confirm:$false -ErrorAction Stop
            Write-Host ("Strip-IPv6: removed " + $c.IPAddress + " (does not match " + $label + ")")
        } catch {
            Write-Host ("Strip-IPv6: WARNING: failed to remove " + $c.IPAddress + ": " + $_.Exception.Message)
        }
    }
}

# Clean up junk IPv6 NDP cache entries. Specifically: a self-referential
# entry for one of the host's own IPv6 addresses with all-zero MAC, which
# is junk Windows leaves behind from failed self-NDP-resolution attempts
# and which keeps the address in "Unreachable" state. Clearing it lets
# subsequent NDP exchanges populate the cache cleanly.
#
# Idempotent. Logs every removal.
function Clear-JunkNDP()
{
    $ownAddrs = (Get-NetIPAddress -AddressFamily IPv6 -ErrorAction SilentlyContinue |
                  Where-Object { $_.IPAddress -notlike 'fe80*' -and $_.IPAddress -ne '::1' } |
                  Select-Object -ExpandProperty IPAddress)
    $junk = Get-NetNeighbor -AddressFamily IPv6 -ErrorAction SilentlyContinue |
              Where-Object { $_.LinkLayerAddress -eq '00-00-00-00-00-00' -and $_.State -eq 'Unreachable' }
    foreach ($n in $junk) {
        if ($ownAddrs -contains $n.IPAddress) {
            try {
                Remove-NetNeighbor -InterfaceIndex $n.InterfaceIndex -IPAddress $n.IPAddress -Confirm:$false -ErrorAction Stop
                Write-Host ("Cleared junk self-NDP entry " + $n.IPAddress + " on ifIndex " + $n.InterfaceIndex)
            } catch {
                Write-Host ("WARNING: Clear-JunkNDP: failed to remove " + $n.IPAddress + ": " + $_.Exception.Message)
            }
        }
    }
}

$lastBootTime = Get-LastBootTime
$Stored = Get-StoredLastBootTime
Write-Host "StoredLastBootTime $Stored, CurrentLastBootTime $lastBootTime"

$timeout = $env:STARTUP_VALID_IP_TIMEOUT
$vxlanAdapter = $env:VXLAN_ADAPTER

# Autoconfigure the IPAM block mode.
if ($env:CNI_IPAM_TYPE -EQ "host-local") {
    $env:USE_POD_CIDR = "true"
} else {
    $env:USE_POD_CIDR = "false"
}

$platform = Get-PlatformType

# Install CNI binaries to the host. Containerd calls calico.exe from
# C:\opt\cni\bin, which is outside the container sandbox.
$cniDst = "C:\opt\cni\bin"
$cniSrc = Join-Path $PSScriptRoot "..\opt\cni\bin"
if (-not (Test-Path $cniSrc)) {
    # HostProcess container: resolve from sandbox mount point.
    $sb = [Environment]::GetEnvironmentVariable('CONTAINER_SANDBOX_MOUNT_POINT','Process')
    if ($sb) { $cniSrc = Join-Path $sb "opt\cni\bin" }
}
if ((Test-Path $cniSrc) -and ($cniSrc -ne $cniDst)) {
    New-Item -ItemType Directory -Force -Path $cniDst | Out-Null
    Copy-Item (Join-Path $cniSrc "calico.exe") (Join-Path $cniDst "calico.exe") -Force
    Copy-Item (Join-Path $cniSrc "calico-ipam.exe") (Join-Path $cniDst "calico-ipam.exe") -Force
    Write-Host "Installed CNI binaries to $cniDst"
}

# Install hns-ipv6 hook artifacts on the host filesystem so svchost-hns
# (running outside our HostProcess sandbox) can LoadLibrary the DLL.
# CalicoWindows under the sandbox mount is invisible to other host
# processes — only paths in C:\opt\cni\bin (or anywhere on the real
# host filesystem) are readable from outside.
$hookSrcDir = Split-Path $cniSrc -Parent | Join-Path -ChildPath "..\CalicoWindows"
$_hookPaths = Get-CalicoHnsHookPaths
$hookDstDir = $_hookPaths.InstallDir
$hookSrc = $null
foreach ($p in @(
    (Join-Path $PSScriptRoot "..\hns-ipv6-hook.dll"),
    (Join-Path $PSScriptRoot "..\hns-ipv6-injector.exe")
)) { if (Test-Path $p) { } }
$sb = [Environment]::GetEnvironmentVariable('CONTAINER_SANDBOX_MOUNT_POINT','Process')
if ($sb) {
    $dllSrc = Join-Path $sb "CalicoWindows\hns-ipv6-hook.dll"
    $injSrc = Join-Path $sb "CalicoWindows\hns-ipv6-injector.exe"
} else {
    $dllSrc = Join-Path $PSScriptRoot "..\hns-ipv6-hook.dll"
    $injSrc = Join-Path $PSScriptRoot "..\hns-ipv6-injector.exe"
}
if ((Test-Path $dllSrc) -and (Test-Path $injSrc)) {
    New-Item -ItemType Directory -Force -Path $hookDstDir | Out-Null
    Copy-Item $dllSrc $_hookPaths.DllPath -Force
    Copy-Item $injSrc $_hookPaths.InjectorPath -Force
    Write-Host "Installed hns-ipv6 hook artifacts to $hookDstDir"
}
# Pre-create the log directory so the DLL's first hlog() inside
# svchost-hns doesn't fail silently if the parent dir is missing.
# The DLL also tries to create it on first write, but doing it here
# means the directory is owned by SYSTEM and visible to debugging
# tools immediately after node-service.ps1 returns.
try {
    New-Item -ItemType Directory -Force -Path (Split-Path $_hookPaths.LogPath -Parent) -ErrorAction Stop | Out-Null
} catch {
    Write-Host ("WARNING: could not pre-create hook log dir " + (Split-Path $_hookPaths.LogPath -Parent) + ": " + $_.Exception.Message)
}

# Mirror the Tigera operator's host-process-install.ps1 install step:
# unpack the CalicoWindows package from the HostProcess sandbox onto the
# host filesystem at C:\CalicoWindows so anything that reads host-relative
# paths (calico-kube-config, libs/calico/calico.psm1, libs/hns/hns.psm1,
# config.ps1, the .template files, calico-node.exe for the upgrade flow)
# finds them where it expects. Also render calico-kube-config from the
# projected ServiceAccount token + ca.crt that Kubernetes mounts into
# the pod, matching install-calico-windows.ps1's GetCalicoKubeConfig.
if ($sb) {
    # CALICO_HOST_INSTALL_DIR (configmap key) overrides the historical
    # legacy install location. Anything that runs on the host and
    # expects the legacy non-HPC layout (e.g. start-calico.ps1,
    # uninstall-calico.ps1, debugging tools) reads from $hostRoot.
    $hostRoot = $env:CALICO_HOST_INSTALL_DIR
    if ([string]::IsNullOrEmpty($hostRoot)) { $hostRoot = "C:\CalicoWindows" }
    $sandboxRoot = Join-Path $sb "CalicoWindows"
    if (Test-Path $sandboxRoot) {
        New-Item -ItemType Directory -Force -Path $hostRoot | Out-Null
        # Robocopy is robust against in-use files and skips files that
        # haven't changed.  /XJ avoids reparse-point loops, /NFL/NDL/NJH/NJS
        # silence the per-file output but keep the summary.  Exit codes
        # 0..7 are success in robocopy semantics.
        $rc = (Start-Process -FilePath robocopy.exe -ArgumentList @($sandboxRoot, $hostRoot, "/MIR", "/XJ", "/NFL", "/NDL", "/NJH", "/NJS", "/R:1", "/W:1") -NoNewWindow -Wait -PassThru).ExitCode
        if ($rc -lt 8) {
            Write-Host "Mirrored sandbox CalicoWindows -> $hostRoot (robocopy exit $rc)"
        } else {
            Write-Host "WARNING: robocopy CalicoWindows mirror failed with exit $rc"
        }
    } else {
        Write-Host "WARNING: sandbox $sandboxRoot does not exist; cannot mirror to host"
    }

    # Render calico-kube-config from the projected SA token + ca.crt.
    # Mirrors install-calico-windows.ps1::GetCalicoKubeConfig HPC branch.
    $caPath = Join-Path $sb "var\run\secrets\kubernetes.io\serviceaccount\ca.crt"
    $tokenPath = Join-Path $sb "var\run\secrets\kubernetes.io\serviceaccount\token"
    $tplPath = Join-Path $hostRoot "calico-kube-config.template"
    $kubeCfgPath = Join-Path $hostRoot "calico-kube-config"
    if ((Test-Path $caPath) -and (Test-Path $tokenPath) -and (Test-Path $tplPath)) {
        $caRaw = Get-Content -Raw -Path $caPath
        $caB64 = [System.Convert]::ToBase64String([System.Text.Encoding]::UTF8.GetBytes($caRaw))
        $token = (Get-Content -Path $tokenPath -Raw).TrimEnd("`r","`n")
        $k8sHost = $env:KUBERNETES_SERVICE_HOST
        $k8sPort = $env:KUBERNETES_SERVICE_PORT
        if ($k8sHost -and $k8sPort) {
            $serverLine = "server: https://{0}:{1}" -f $k8sHost, $k8sPort
            (Get-Content $tplPath) `
                -replace '<ca>', $caB64 `
                -replace '<server>', $serverLine `
                -replace '<token>', $token | Set-Content $kubeCfgPath -Force -Encoding ASCII
            Write-Host "Rendered $kubeCfgPath from projected SA token (server=$serverLine)"
        } else {
            Write-Host "WARNING: KUBERNETES_SERVICE_HOST/PORT not set; calico-kube-config not rendered"
        }
    } else {
        Write-Host "WARNING: missing token / ca.crt / template; skipping calico-kube-config render"
    }
}

# Regenerate the CNI config from the template every container start.
# The legacy host-installer Install-CNIPlugin only runs once at install
# time; without this, ConfigMap changes (CALICO_DSR_DISABLE,
# K8S_SERVICE_CIDR, DNS_NAME_SERVERS, etc.) never reach the live CNI
# config until someone reinstalls the host. Idempotent.
try {
    if ($env:CNI_CONF_DIR) {
        if (-not $env:CNI_CONF_FILENAME) {
            $env:CNI_CONF_FILENAME = "10-calico.conf"
        }
        New-Item -ItemType Directory -Force -Path $env:CNI_CONF_DIR | Out-Null
        Write-CNIConfig
    } else {
        Write-Host "CNI_CONF_DIR not set; skipping CNI config regeneration"
    }
} catch {
    Write-Host "WARNING: Write-CNIConfig failed: $($_.Exception.Message)"
    throw
}

if ($env:CALICO_NETWORKING_BACKEND -EQ "windows-bgp" -OR $env:CALICO_NETWORKING_BACKEND -EQ "vxlan")
{
    Write-Host "Calico $env:CALICO_NETWORKING_BACKEND networking enabled."

    # Start the CNI kubeconfig token refresher up-front, BEFORE any
    # HNS network bootstrap. The refresher (calico-node.exe -monitor-token)
    # writes c:\etc\cni\net.d\calico-kubeconfig from the projected SA
    # token; the calico CNI plugin reads that file to authenticate to
    # the apiserver during pod-sandbox setup. The refresher is
    # independent of HNS state — it only needs the in-cluster token —
    # and gating it on calico-node initialisation success means a node
    # whose dual-stack create is stuck in retry never gets a valid
    # kubeconfig, so every pod CNI add fails with "Unauthorized" even
    # when the underlying L2Bridge eventually comes up.
    if ($env:CONTAINER_SANDBOX_MOUNT_POINT) {
        Ensure-TokenRefresher
    }

    # Wipe any half-created Calico HNS network whose underlying
    # Hyper-V vSwitch is in the broken Private/no-NIC-binding state
    # described by Test-IsBrokenCalicoVMSwitch. networkNeedsRecreate
    # only inspects HNS-level fields (Subnets, ManagementIP, etc.)
    # and would otherwise see this network as healthy and refuse to
    # rebuild it, leaving the node permanently unable to attach pod
    # endpoints.
    Remove-BrokenCalicoHnsNetwork -NetworkName 'Calico' | Out-Null

    # Check if the node has been rebooted.  If so, the HNS networks will be in unknown state so we need to
    # clean them up and recreate them.
    $prevLastBootTime = Get-StoredLastBootTime
    if ($prevLastBootTime -NE $lastBootTime)
    {
        if ((Get-HNSNetwork | ? Type -NE nat))
        {
            Write-Host "First time Calico has run since boot up, cleaning out any old network state."
            Get-HNSNetwork | ? Type -NE nat | Remove-HNSNetwork
            do
            {
                Write-Host "Waiting for network deletion to complete."
                Start-Sleep 1
            } while ((Get-HNSNetwork | ? Type -NE nat))
        }

        # After deletion of all hns networks, wait for an interface to have an IP that is not a 169.254.0.0/16 (or 127.0.0.0/8) address,
        # before creation of External network.
        $isValidIP = $false
        $IPRegEx1='(^127\.0\.0\.)'
        $IPRegEx2='(^169\.254\.)'
        while(!($isValidIP) -AND ($timeout -gt 0))
        {
            $IPAddress = (Get-NetIPAddress -AddressFamily IPv4).IPAddress
            Write-Host "`nTimeout Remaining: $timeout sec"
            Write-Host "List of IP Address before initialising Calico: $IPAddress"
            Foreach ($ip in $IPAddress)
            {
                if (($ip -NotMatch $IPRegEx1) -AND ($ip -NotMatch $IPRegEx2))
                {
                    $isValidIP = $true
                    Write-Host "`nFound valid IP: $ip"
                    break
                }
            }
            if (!($isValidIP))
            {
                Start-Sleep -s 5
                $timeout = $timeout - 5
            }
        }
    }

    # Create a placeholder L2Bridge to trigger vSwitch creation, but ONLY if no
    # L2Bridge network exists yet. If the "Calico" network already exists (from a
    # previous calico-node run), skip External creation to avoid the dual-L2Bridge
    # conflict that breaks pod networking.
    $existingCalico = Get-HnsNetwork | Where-Object { $_.Name -eq "Calico" -and $_.Type -eq "L2Bridge" }
    $existingExternal = Get-HnsNetwork | Where-Object { $_.Name -eq "External" -and $_.Type -eq "L2Bridge" }

    $expectedMgmtV4 = $env:CALICO_DESIRED_HNS_MGMT_IPV4
    if ([string]::IsNullOrEmpty($expectedMgmtV4) -and $env:IP_AUTODETECTION_METHOD -like 'cidr=*') {
        $cidr4 = $env:IP_AUTODETECTION_METHOD.Substring(5).Split(',')[0].Trim()
        $expectedMgmtV4 = Resolve-DesiredHnsManagementIPv4 -Addresses (Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue) -NetworkCIDR $cidr4
    }

    $expectedMgmtV6 = $env:CALICO_DESIRED_HNS_MGMT_IPV6
    if ([string]::IsNullOrEmpty($expectedMgmtV6) -and $env:IP6_AUTODETECTION_METHOD -like 'cidr=*') {
        $cidr6 = $env:IP6_AUTODETECTION_METHOD.Substring(5).Split(',')[0].Trim()
        $prefix6 = ($cidr6 -split '/')[0] -replace '::$',':'
        $expectedMgmtV6 = Resolve-DesiredHnsManagementIPv6 -Addresses (Get-NetIPAddress -AddressFamily IPv6 -ErrorAction SilentlyContinue) -Prefix $prefix6
    }

    if (Test-CalicoHnsNetworkNeedsStartupRecreate -ExistingCalicoNetwork $existingCalico -ExpectedManagementIP $expectedMgmtV4 -ExpectedManagementIPv6 $expectedMgmtV6) {
        Write-Host ("Calico L2Bridge has stale HNS management addresses (current ManagementIP=" + $existingCalico.ManagementIP + ", ManagementIPv6=" + $existingCalico.ManagementIPv6 + "; desired ManagementIP=" + $expectedMgmtV4 + ", ManagementIPv6=" + $expectedMgmtV6 + "); deleting so calico-node can rebuild")
        try {
            Invoke-HNSRequest -Method DELETE -Type networks -Id $existingCalico.Id -ErrorAction Stop | Out-Null
        } catch {
            Write-Host ("WARNING: failed to delete stale Calico HNS network before startup: " + $_.Exception.Message)
        }
        do {
            Start-Sleep 1
            $existingCalico = Get-HnsNetwork | Where-Object { $_.Name -eq "Calico" -and $_.Type -eq "L2Bridge" }
        } while ($existingCalico)
        $existingExternal = Get-HnsNetwork | Where-Object { $_.Name -eq "External" -and $_.Type -eq "L2Bridge" }
    }

    if ($existingCalico) {
        Write-Host "Calico L2Bridge network already exists, skipping External creation."
        $mgmtIP = Wait-ForManagementIP "Calico"
    } elseif ($existingExternal) {
        Write-Host "External L2Bridge network already exists."
        $mgmtIP = Wait-ForManagementIP "External"
    } else {
        # CRITICAL: pre-inject the hns-ipv6-hook BEFORE any L2Bridge is
        # created. HNS picks ManagementIP/ManagementIPv6 by scanning the
        # NIC at network create time; without the hook in place, HNS picks
        # whatever it sees first — which in practice is whichever the OS
        # reports during the rebind transition. The wrong pick installs a
        # VFP rule that silently drops ARP/NS for the host's actual
        # management addresses, bricking the host. The hook makes HNS see
        # only the operator-chosen addresses, so the pick is deterministic.
        Inject-HnsMgmtIpHook | Out-Null

        # Create a placeholder "External" L2Bridge to trigger vSwitch
        # creation on the management NIC. This is critical on FRESH
        # nodes: HNS L2Bridge create with ManagementIP fails with
        # "An adapter was not found (0x803b0006)" when the vms_pp
        # binding is disabled on the target NIC, and Windows leaves
        # vms_pp disabled until SOME vSwitch is bound to the NIC.
        # The placeholder New-HNSNetwork call (no ManagementIP, no
        # AdapterName specified) lets HNS auto-pick an external NIC
        # and create the vSwitch as a side effect — which enables
        # vms_pp persistently while ANY HNS network exists on the NIC.
        # calico-node.exe -startup then sees existingExternal, deletes
        # it ("Removing L2Bridge network 'External' to free the physical
        # adapter"), and creates the real "Calico" L2Bridge with the
        # correct ManagementIP — by which point vms_pp stays enabled
        # because Calico will hold it once created.
        # The hook is in place so HNS picks the operator-chosen
        # ManagementIP/ManagementIPv6 during the Calico create.
        Write-Host "Creating External placeholder L2Bridge to trigger vSwitch creation"

        # Clean up any orphan Calico-managed Hyper-V vSwitches before
        # bootstrapping External. An older flannel-era install (HNS
        # Transparent network) can leave a Hyper-V vSwitch with the
        # name 'Calico' in place after its HNS network is deleted; the
        # host's IPv4 then lives on the 'vEthernet (Calico)' vNIC of
        # that orphan, NOT on the bare physical NIC. New-HNSNetwork
        # -AdapterName cannot bind to a vNIC and rejects with "The
        # parameter is incorrect". Remove only switches we ourselves
        # would have named (External / Calico / Calico_* / Calico-*)
        # so an unrelated user-created Hyper-V external switch on the
        # same host is never collected. Test-IsCalicoManagedVMSwitch
        # in calico.psm1 holds the predicate.
        try {
            $hnsNames = @(Get-HnsNetwork -ErrorAction SilentlyContinue | ForEach-Object { $_.Name })
            Get-VMSwitch -ErrorAction SilentlyContinue |
                Where-Object { $_.SwitchType -eq 'External' -and
                               (Test-IsCalicoManagedVMSwitch -Name $_.Name) -and
                               ($hnsNames -notcontains $_.Name) } |
                ForEach-Object {
                    Write-Host ("Removing orphan Hyper-V vSwitch '" + $_.Name + "' (Calico-managed name, no matching HNS network)")
                    Remove-VMSwitch -Name $_.Name -Force -ErrorAction Stop
                }
        } catch {
            Write-Host ("WARNING: orphan vSwitch cleanup failed: " + $_.Exception.Message)
        }

        # Resolve the management interface alias from IP_AUTODETECTION_METHOD
        # so the External placeholder binds to the same NIC calico-node.exe
        # -startup will subsequently use. Without -AdapterName, HNS auto-picks
        # any external NIC, which on multi-NIC hosts (e.g. an out-of-band NIC
        # on a virtualization host, or a dual-port LOM) may bind the wrong
        # one — calico's later Calico create then needs to bind a different
        # NIC where vms_pp is still disabled and fails with
        # "adapter not found (0x803b0006)".
        $extAdapter = $null
        if ($env:IP_AUTODETECTION_METHOD -like 'cidr=*') {
            $cidr4 = $env:IP_AUTODETECTION_METHOD.Substring(5).Split(',')[0].Trim()
            try {
                $addrs4 = Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue
                $extAdapter = Resolve-HnsManagementInterfaceAlias -Addresses $addrs4 -NetworkCIDR $cidr4
            } catch {
                Write-Host "WARNING: cannot derive External AdapterName from IP_AUTODETECTION_METHOD: $($_.Exception.Message)"
            }
        }
        if ($extAdapter) {
            Write-Host "External will bind to AdapterName='$extAdapter'"
            # Export to calico-node.exe -startup so its own HNS Calico create
            # request includes NetworkAdapterName, bypassing the binding-aware
            # ManagementIP-based adapter search that fails on fresh nodes after
            # External is deleted (see hns_types.go ensureNetworkExistsWithAPI).
            $env:CALICO_HNS_ADAPTER_NAME = $extAdapter
        }
        $deadline = (Get-Date).AddSeconds(60)
        while (-not (Get-HnsNetwork | Where-Object { $_.Name -eq "External" -and $_.Type -eq "L2Bridge" }) -and (Get-Date) -lt $deadline) {
            try {
                if ($extAdapter) {
                    New-HNSNetwork -Type L2Bridge -AddressPrefix "192.168.255.0/30" -Gateway "192.168.255.1" -Name "External" -AdapterName $extAdapter -Verbose -ErrorAction Stop | Out-Null
                } else {
                    New-HNSNetwork -Type L2Bridge -AddressPrefix "192.168.255.0/30" -Gateway "192.168.255.1" -Name "External" -Verbose -ErrorAction Stop | Out-Null
                }
            } catch {
                Write-Host "External L2Bridge create attempt failed: $($_.Exception.Message)"
                Start-Sleep 5
            }
        }
        if (Get-HnsNetwork | Where-Object { $_.Name -eq "External" -and $_.Type -eq "L2Bridge" }) {
            Write-Host "External placeholder created; vSwitch bootstrap complete"
            $mgmtIP = Wait-ForManagementIP "External"
        } else {
            Write-Host "WARNING: External placeholder L2Bridge create timed out; calico-node.exe -startup may fail with 'adapter not found'"
            $mgmtIP = (Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
                        Where-Object { $_.InterfaceAlias -like 'Ethernet*' -or
                                       $_.InterfaceAlias -like 'vEthernet (Ethernet*' } |
                        Select-Object -First 1).IPAddress
            if ([string]::IsNullOrEmpty($mgmtIP)) { $mgmtIP = "0.0.0.0" }
        }
    }
    Write-Host "Management IP detected on vSwitch: $mgmtIP."

    # Enable WeakHost on the management interface — see Apply-WeakHost
    # below for the rationale. This early call covers the External
    # placeholder period; the function is called again after every
    # calico-node.exe -startup, since HNS network (re)creation re-binds
    # vEthernet and resets WeakHost to default (Disabled).
    Apply-WeakHost

    # Disable randomized IPv6 interface identifiers so that the SLAAC address
    # is stable (EUI-64 derived from MAC). Without this, RRAS advertises a
    # stale BGP next-hop after each vSwitch recreation.
    Set-NetIPv6Protocol -RandomizeIdentifiers Disabled -ErrorAction SilentlyContinue

    # Enable IPv4 + IPv6 routing in the TCP/IP stack. Without these, the
    # host won't forward packets between the management interface and pod
    # endpoints despite per-interface Forwarding=Enabled. Both keys require
    # a reboot to take effect; we set them on every startup so new nodes
    # get them on their first reboot after Calico is installed.
    foreach ($af in @("Tcpip","Tcpip6")) {
        $regPath = "HKLM:\SYSTEM\CurrentControlSet\Services\$af\Parameters"
        $current = (Get-ItemProperty -Path $regPath -Name IPEnableRouter -ErrorAction SilentlyContinue).IPEnableRouter
        if ($current -ne 1) {
            New-ItemProperty -Path $regPath -Name IPEnableRouter -Value 1 -PropertyType DWord -Force | Out-Null
            Write-Host "Set $af IPEnableRouter=1 (takes effect after reboot)"
        }
    }

    Start-Sleep 10

    if (($platform -EQ "ec2") -or ($platform -EQ "gce")) {
        Set-MetaDataServerRoute -mgmtIP $mgmtIP
    }

    if ($env:CALICO_NETWORKING_BACKEND -EQ "windows-bgp") {
        # Bootstrap RRAS LAN routing if it's not configured yet. The
        # Windows feature install (Routing + RemoteAccess + RSAT) gives
        # us the bits, but Install-RemoteAccess -VpnType RoutingOnly is
        # the step that actually flips the RemoteAccess service from
        # Disabled -> Manual + enables LAN routing. Without it,
        # confd's Add-BgpRouter / Add-BgpPeer fail silently with
        # 'LAN Routing not configured' and the node never advertises
        # its pod /26 over BGP — Linux backends can't return SYN-ACK
        # to Windows pods, and every Windows-pod -> ClusterIP TCP
        # times out (the SYN reaches the backend, the SYN-ACK is
        # black-holed). Idempotent: returns instantly if already
        # configured. Safe to run on every container start.
        try {
            $svc = Get-Service -Name RemoteAccess -ErrorAction Stop
            if (Test-RRASNeedsBootstrap -Service $svc) {
                Write-Host "RRAS LAN routing not configured; running Install-RemoteAccess -VpnType RoutingOnly"
                Install-RemoteAccess -VpnType RoutingOnly -PassThru -ErrorAction Stop | Out-Null
            }
            if ((Get-Service -Name RemoteAccess).StartType -ne 'Automatic') {
                Set-Service -Name RemoteAccess -StartupType Automatic -ErrorAction SilentlyContinue
            }
            if ((Get-Service -Name RemoteAccess).Status -ne 'Running') {
                Start-Service RemoteAccess -ErrorAction Stop
            }
        } catch {
            Write-Host ("WARNING: RRAS bootstrap failed: " + $_.Exception.Message + ". confd's Add-BgpRouter will fail; pod->ClusterIP TCP from this node's pods will time out.")
        }

        Write-Host "Restarting BGP service to pick up any interface renumbering..."
        Restart-Service RemoteAccess
    }
}

# nodename file path. The CNI plugin (calico.exe) runs OUTSIDE the
# HostProcess sandbox — containerd invokes it in the host namespace —
# so it reads this file at the host's literal C:\CalicoWindows\nodename
# (see Build-CNIConfigSubstitutions in libs/calico/calico.psm1, which
# defaults nodename_file to that path when CALICO_NODENAME_FILE_HOST_PATH
# is unset). Setting CALICO_NODENAME_FILE to a relative ".\nodename"
# would make calico-node.exe -startup write the file inside the sandbox
# at $CONTAINER_SANDBOX_MOUNT_POINT\CalicoWindows\nodename, which is
# invisible to the host-side CNI plugin → every pod sandbox-create
# fails with "CreateFile C:\CalicoWindows\nodename: file not found".
# Use the absolute host path so the writer (calico-node.exe -startup
# inside the pod) and the reader (calico.exe outside the pod) agree.
# The legacy installer didn't need this because it ran on the host
# directly with $RootDir = C:\CalicoWindows.
if ($env:CONTAINER_SANDBOX_MOUNT_POINT) {
    New-Item -ItemType Directory -Force -Path "C:\CalicoWindows" | Out-Null
    $env:CALICO_NODENAME_FILE = "C:\CalicoWindows\nodename"
} else {
    $env:CALICO_NODENAME_FILE = ".\nodename"
}

# We use this setting as a trigger for the other scripts to proceed.
Set-StoredLastBootTime $lastBootTime
$Stored = Get-StoredLastBootTime
Write-Host "Stored new lastBootTime $Stored"

# The old version of Calico upgrade service may still be running
# so try to remove it while it exists.
#
# Upgrade service is not needed if node is running in a hostprocess container.
if (-not $env:CONTAINER_SANDBOX_MOUNT_POINT) {
    while (Get-UpgradeService)
    {

        Remove-UpgradeService
        if ($LastExitCode -EQ 0) {
            Write-Host "CalicoUpgrade service removed"
            break
        }
        Start-Sleep 5
        Write-Host "Failed to clean up old CalicoUpgrade service, retrying..."
    }
}

# Run the startup script whenever kubelet (re)starts. This makes sure that we refresh our Node annotations if
# kubelet recreates the Node resource.
$kubeletPid = -1
while ($True)
{
    try
    {
        # Run calico-node.exe if kubelet starts/restarts
        $currentKubeletPid = (Get-Process -Name kubelet -ErrorAction Stop).id
        if ($currentKubeletPid -NE $kubeletPid)
        {
            Write-Host "Kubelet has (re)started, (re)initialising the node..."
            $kubeletPid = $currentKubeletPid
            while ($true)
            {
                # Pin HNS ManagementIPv6 via the iphlpapi!GetAdaptersAddresses
                # hook. The DLL filters HNS's view of the NIC so HNS pins
                # the operator-chosen ManagementIPv6 even when SLAAC re-
                # adds the GUA milliseconds after we strip it. This is
                # the ONLY reliable solution — every PowerShell-only
                # alternative (Strip + retry, RouterDiscovery toggle,
                # post-create poll, Restart-Service hns) loses the
                # SLAAC race in HostProcess containers because:
                #   1. HNS scans the NIC asynchronously over a multi-
                #      second window after Create returns.
                #   2. Set-NetIPInterface for IPv6 is intermittently
                #      unavailable inside HostProcess (StandardCimv2
                #      WMI provider load failure during HNS create).
                #   3. SLAAC re-adds RA-derived addresses in <1s.
                # The hook is invariant to all three: HNS only sees what
                # the hook lets it see, regardless of NIC state.
                #
                # Lifecycle (the bit that bit us before):
                # The DLL stays loaded in svchost-hns forever once
                # injected. Earlier deploys stacked stale hook code
                # across pod restarts, which is what caused the HCS/HNS
                # failures the user observed. Fixed here by:
                #   1. Restart-Service hns at startup -> kills the old
                #      svchost-hns process (and the loaded DLL with it).
                #   2. Wait for hns to come back with a fresh PID.
                #   3. Inject the current-build DLL into the fresh PID.
                # Result: the hook is always the current-build version,
                # no stale code from previous releases.
                #
                # Disable via CALICO_HNS_IPV6_HOOK=false (only useful
                # for debugging — the strip-only fallback loses the
                # race).
                # Ensure the hook is installed in the current svchost-hns
                # before calico-node.exe -startup. Startup may delete and
                # recreate the Calico L2Bridge (subnet rotation, stale HNS
                # state, image upgrade), even when a Calico bridge existed at
                # container start. A historical log file is not enough proof:
                # it does not identify the current HNS PID.
                $hookPair = Inject-HnsMgmtIpHook
                if (-not $hookPair -or ([string]::IsNullOrEmpty($hookPair[0]) -and [string]::IsNullOrEmpty($hookPair[1]))) {
                    Strip-NonClusterIPv6
                }

                # Skip calico-node.exe -startup if a Calico L2Bridge with our
                # desired ManagementIP already exists. The startup binary
                # (re)creates the bridge each time it runs, and the bridge
                # create triggers an HNS NIC rebind that bricks the host on
                # qemu (cumulative state corruption). On real hardware the
                # rebind is brief and recovers; on qemu it eventually fails.
                # Once the bridge is in the right state, there is no work to
                # do — skip and let the kubelet-restart loop continue
                # monitoring without re-creating the bridge.
                $skipStartup = $false
                $expectedV4 = $env:CALICO_DESIRED_HNS_MGMT_IPV4
                if ([string]::IsNullOrEmpty($expectedV4) -and $env:IP_AUTODETECTION_METHOD -like 'cidr=*') {
                    # Match Inject-HnsMgmtIpHook's derivation; same logic.
                    $cidr4 = $env:IP_AUTODETECTION_METHOD.Substring(5).Split(',')[0].Trim()
                    $expectedV4 = Resolve-DesiredHnsManagementIPv4 -Addresses (Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue) -NetworkCIDR $cidr4
                }
                $existingCalicoNet = Get-HnsNetwork | Where-Object { $_.Name -eq 'Calico' -and $_.Type -eq 'L2Bridge' } | Select-Object -First 1
                if (Test-CalicoStartupCanSkip -ExistingCalicoNetwork $existingCalicoNet -ExpectedManagementIP $expectedV4) {
                    Write-Host ("Calico L2Bridge already configured with correct ManagementIP=" + $expectedV4 + "; skipping calico-node.exe -startup to avoid bridge recreate")
                    $skipStartup = $true
                }

                if ($skipStartup) {
                    Write-Host "Calico node initialisation skipped (idempotent); monitoring kubelet for restarts..."
                    Apply-WeakHost
                    Clear-JunkNDP
                    if ($env:CONTAINER_SANDBOX_MOUNT_POINT) {
                        Restart-TokenRefresher
                    }
                    break
                }

                .\calico-node.exe -startup
                if ($LastExitCode -EQ 0)
                {
                    Write-Host "Calico node initialisation succeeded; monitoring kubelet for restarts..."
                    # Clean up the bootstrap External L2Bridge if it's still
                    # around. The Go code (ensureNetworkExistsWithAPI) no longer
                    # deletes it pre-create — keeping External alive until
                    # Calico is up keeps the Hyper-V vSwitch (and therefore
                    # vms_pp on the management NIC) enabled across the create
                    # window, which avoids the HCN_E_ADAPTER_NOT_FOUND failure
                    # mode on fresh nodes. Now that Calico is bound, External
                    # is unneeded — and on multi-bridge hosts having both can
                    # confuse pod IPv4 routing (see Issue 3 in
                    # docs/calico-windows-issues.md).
                    Get-HnsNetwork |
                        Where-Object { $_.Name -eq "External" -and $_.Type -eq "L2Bridge" } |
                        ForEach-Object {
                            Write-Host ("Removing leftover External L2Bridge " + $_.Id + " (Calico is up)")
                            try { hnsdiag delete networks $_.Id 2>$null | Out-Null } catch {}
                        }
                    # HNS network (re)creation by calico-node -startup re-binds the
                    # management vEthernet adapter and resets WeakHost to Disabled.
                    # Re-apply now that the network is up.
                    Apply-WeakHost
                    Clear-JunkNDP
                    # Token refresher only needs to run in hostprocess containers
                    if ($env:CONTAINER_SANDBOX_MOUNT_POINT -AND ("$env:CNI_PLUGIN_TYPE" -eq "Calico")) {
                        Restart-TokenRefresher
                    }
                    break
                }

                Write-Host "Calico node initialisation failed, will retry..."
                Start-Sleep 1
            }
        }
    }
    catch
    {
        Write-Host "Kubelet not running, waiting for Kubelet to start..."
        $kubeletPid = -1
    }

    # Token refresher only needs to run in hostprocess containers
    if ($env:CONTAINER_SANDBOX_MOUNT_POINT -AND ("$env:CNI_PLUGIN_TYPE" -eq "Calico")) {
        Ensure-TokenRefresher
    }

    Start-Sleep 10
}
