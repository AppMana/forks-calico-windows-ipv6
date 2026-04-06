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

    # Enable weak host model on the management interface so that Windows
    # considers routes on ALL interfaces when forwarding packets, not just
    # routes on the receiving interface. Without this, packets arriving on
    # vEthernet (Ethernet) destined for a local pod (routed via Calico_ep)
    # hit the default route back to VyOS instead of the specific pod route,
    # causing a routing loop.
    $mgmtAdapter = Get-NetAdapter | Where-Object { $_.Name -like 'vEthernet (Ethernet*' }
    if ($mgmtAdapter) {
        Set-NetIPInterface -InterfaceIndex $mgmtAdapter.ifIndex -WeakHostReceive Enabled -WeakHostSend Enabled -AddressFamily IPv4
        Set-NetIPInterface -InterfaceIndex $mgmtAdapter.ifIndex -WeakHostReceive Enabled -WeakHostSend Enabled -AddressFamily IPv6
        Write-Host "Enabled WeakHostReceive/WeakHostSend on $($mgmtAdapter.Name) for IPv4 and IPv6"
    } else {
        Write-Host "WARNING: Could not find management adapter matching 'vEthernet (Ethernet*'"
    }

    # Disable randomized IPv6 interface identifiers so that the SLAAC address
    # is stable (EUI-64 derived from MAC). Without this, RRAS advertises a
    # stale BGP next-hop after each vSwitch recreation.
    Set-NetIPv6Protocol -RandomizeIdentifiers Disabled -ErrorAction SilentlyContinue

    # Enable IPv6 routing in the TCP/IP stack. Without this, the host won't
    # forward IPv6 packets between the management interface and pod endpoints.
    # This requires a reboot to take effect, but we set it every startup so
    # new nodes get it on their first reboot after Calico is installed.
    $regPath = "HKLM:\SYSTEM\CurrentControlSet\Services\Tcpip6\Parameters"
    $current = (Get-ItemProperty -Path $regPath -Name IPEnableRouter -ErrorAction SilentlyContinue).IPEnableRouter
    if ($current -ne 1) {
        New-ItemProperty -Path $regPath -Name IPEnableRouter -Value 1 -PropertyType DWord -Force | Out-Null
        Write-Host "Set Tcpip6 IPEnableRouter=1 (takes effect after reboot)"
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
                .\calico-node.exe -startup
                if ($LastExitCode -EQ 0)
                {
                    Write-Host "Calico node initialisation succeeded; monitoring kubelet for restarts..."
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
