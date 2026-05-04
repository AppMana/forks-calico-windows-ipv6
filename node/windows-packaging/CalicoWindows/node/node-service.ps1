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
    $mgmtAdapter = Get-NetAdapter | Where-Object { $_.Name -like 'vEthernet (Ethernet*' }
    if (-not $mgmtAdapter) {
        Write-Host "WARNING: Apply-WeakHost: no adapter matches 'vEthernet (Ethernet*' (HNS network may not yet exist)"
        return
    }
    foreach ($af in @("IPv4","IPv6")) {
        try {
            Set-NetIPInterface -InterfaceIndex $mgmtAdapter.ifIndex -WeakHostReceive Enabled -WeakHostSend Enabled -AddressFamily $af -ErrorAction Stop
        } catch {
            Write-Host ("WARNING: Apply-WeakHost: Set-NetIPInterface " + $af + " failed: " + $_.Exception.Message)
        }
    }
    $state = Get-NetIPInterface -InterfaceIndex $mgmtAdapter.ifIndex | Select-Object AddressFamily,WeakHostReceive,WeakHostSend
    foreach ($s in $state) {
        Write-Host ("WeakHost on " + $mgmtAdapter.Name + " " + $s.AddressFamily + ": Receive=" + $s.WeakHostReceive + " Send=" + $s.WeakHostSend)
    }
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

    $mgmtAdapter = Get-NetAdapter -ErrorAction SilentlyContinue |
                     Where-Object { $_.Name -like 'vEthernet (Ethernet*' }
    if (-not $mgmtAdapter) {
        Write-Host "Strip-IPv6: WARNING: no adapter matches 'vEthernet (Ethernet*'; skipping"
        return
    }

    $candidates = Get-NetIPAddress -InterfaceIndex $mgmtAdapter.ifIndex -AddressFamily IPv6 -ErrorAction SilentlyContinue |
                    Where-Object { $_.IPAddress -notlike 'fe80*' -and $_.PrefixOrigin -eq 'RouterAdvertisement' }
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
$hookDstDir = "C:\opt\calico-hns-ipv6"
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
    Copy-Item $dllSrc (Join-Path $hookDstDir "hns-ipv6-hook.dll") -Force
    Copy-Item $injSrc (Join-Path $hookDstDir "hns-ipv6-injector.exe") -Force
    Write-Host "Installed hns-ipv6 hook artifacts to $hookDstDir"
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
}

if ($env:CALICO_NETWORKING_BACKEND -EQ "windows-bgp" -OR $env:CALICO_NETWORKING_BACKEND -EQ "vxlan")
{
    Write-Host "Calico $env:CALICO_NETWORKING_BACKEND networking enabled."

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
    if ($existingCalico) {
        Write-Host "Calico L2Bridge network already exists, skipping External creation."
        $mgmtIP = Wait-ForManagementIP "Calico"
    } elseif ($existingExternal) {
        Write-Host "External L2Bridge network already exists."
        $mgmtIP = Wait-ForManagementIP "External"
    } else {
        Write-Host "`nStart creating vSwitch. Note: Connection may get lost for RDP, please reconnect...`n"
        while (!(Get-HnsNetwork | ? Name -EQ "External"))
        {
            if ($env:CALICO_NETWORKING_BACKEND -EQ "vxlan") {
                New-NetFirewallRule -Name OverlayTraffic4789UDP -Description "Overlay network traffic UDP" -Action Allow -LocalPort 4789 -Enabled True -DisplayName "Overlay Traffic 4789 UDP" -Protocol UDP -ErrorAction SilentlyContinue
                $result = New-HNSNetwork -Type Overlay -AddressPrefix "192.168.255.0/30" -Gateway "192.168.255.1" -Name "External" -SubnetPolicies @(@{Type = "VSID"; VSID = 9999; }) -AdapterName $vxlanAdapter -Verbose
            }
            else
            {
                $result = New-HNSNetwork -Type L2Bridge -AddressPrefix "192.168.255.0/30" -Gateway "192.168.255.1" -Name "External" -Verbose
            }
            if ($result.Error -OR (!$result.Success)) {
                Write-Host "Failed to create network, retrying..."
                Start-Sleep 1
            } else {
                break
            }
        }
        $mgmtIP = Wait-ForManagementIP "External"
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
        Write-Host "Restarting BGP service to pick up any interface renumbering..."
        Restart-Service RemoteAccess
    }
}

# For Windows, we expect the nodename file to exist in the root directory of the
# Calico for Windows installation. The CNI config field 'nodename_file' will
# always be $RootDir\nodename
$env:CALICO_NODENAME_FILE = ".\nodename"

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
                # Pin HNS ManagementIPv6 to the operator's chosen address.
                #
                # Default: Strip-NonClusterIPv6 (PowerShell-only, no
                # native code). It transiently removes RA-derived IPv6
                # addresses from vEthernet (Ethernet*) that don't match
                # CALICO_DESIRED_HNS_MGMT_IPV6 or IP6_AUTODETECTION_METHOD.
                # SLAAC re-adds them from the next RA, but by then HNS
                # has already pinned the desired address at create time.
                # No DLL injection, no svchost LoadLibrary, no per-Win-
                # build prologue verification.
                #
                # Opt-in: CALICO_HNS_IPV6_HOOK=true keeps the host NIC
                # untouched and instead injects hns-ipv6-hook.dll into
                # svchost-hns to filter iphlpapi!GetAdaptersAddresses.
                # Default off because the hook has caused HCS/HNS
                # failures in the field (svchost crashes / sticky stale
                # state) and the strip path achieves the same end state
                # without those risks.
                if ($env:CALICO_HNS_IPV6_HOOK -eq 'true') {
                    $hostHookDir = "C:\opt\calico-hns-ipv6"
                    $hostInjector = Join-Path $hostHookDir "hns-ipv6-injector.exe"
                    $hostDll = Join-Path $hostHookDir "hns-ipv6-hook.dll"
                    $desired = $env:CALICO_DESIRED_HNS_MGMT_IPV6
                    if ([string]::IsNullOrEmpty($desired) -and $env:IP6_AUTODETECTION_METHOD -like 'cidr=*') {
                        $cidr = $env:IP6_AUTODETECTION_METHOD.Substring(5).Split(',')[0].Trim()
                        $prefix = ($cidr -split '/')[0] -replace '::$',':'
                        $mgmtAdapter = Get-NetAdapter -ErrorAction SilentlyContinue |
                                          Where-Object { $_.Name -like 'vEthernet (Ethernet*' }
                        if ($mgmtAdapter) {
                            $existing = Get-NetIPAddress -InterfaceIndex $mgmtAdapter.ifIndex -AddressFamily IPv6 -ErrorAction SilentlyContinue |
                                          Where-Object { $_.IPAddress -like ($prefix + '*') -and $_.IPAddress -notlike 'fe80*' } |
                                          Select-Object -First 1
                            if ($existing) { $desired = $existing.IPAddress }
                        }
                    }
                    if ((-not [string]::IsNullOrEmpty($desired)) -and (Test-Path $hostInjector) -and (Test-Path $hostDll)) {
                        Write-Host ("Injecting hns-ipv6-hook for desired ManagementIPv6=" + $desired)
                        & $hostInjector -desired-mgmt-ipv6 $desired -dll $hostDll
                    } else {
                        Write-Host "hns-ipv6 hook enabled but no desired ManagementIPv6 resolvable; skipping"
                    }
                } else {
                    Strip-NonClusterIPv6
                }

                .\calico-node.exe -startup
                if ($LastExitCode -EQ 0)
                {
                    Write-Host "Calico node initialisation succeeded; monitoring kubelet for restarts..."
                    # HNS network (re)creation by calico-node -startup re-binds the
                    # management vEthernet adapter and resets WeakHost to Disabled.
                    # Re-apply now that the network is up.
                    Apply-WeakHost
                    Clear-JunkNDP
                    # Token refresher only needs to run in hostprocess containers
                    if ($env:CONTAINER_SANDBOX_MOUNT_POINT) {
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
    if ($env:CONTAINER_SANDBOX_MOUNT_POINT) {
        Ensure-TokenRefresher
    }

    Start-Sleep 10
}
