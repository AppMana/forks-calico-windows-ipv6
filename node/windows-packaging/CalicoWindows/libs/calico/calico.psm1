# Copyright (c) 2018-2020 Tigera, Inc. All rights reserved.
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

# We require the 64-bit version of Powershell, which should live at the following path.
$powerShellPath = "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe"
$baseDir = "$PSScriptRoot\..\.."
$NSSMPath = "$baseDir\nssm\win64\nssm.exe"

function fileIsMissing($path)
{
    return (("$path" -EQ "") -OR (-NOT(Test-Path "$path")))
}

function Test-CalicoConfiguration()
{
    Write-Host "Validating configuration..."
    if (!$env:CNI_BIN_DIR)
    {
        throw "Config not loaded?."
    }
    if ($env:CALICO_NETWORKING_BACKEND -EQ "windows-bgp" -OR $env:CALICO_NETWORKING_BACKEND -EQ "vxlan") {
        if (fileIsMissing($env:CNI_BIN_DIR))
        {
            throw "CNI binary directory $env:CNI_BIN_DIR doesn't exist.  Please create it and ensure kubelet " +  `
                    "is configured with matching --cni-bin-dir."
        }
        if (fileIsMissing($env:CNI_CONF_DIR))
        {
            throw "CNI config directory $env:CNI_CONF_DIR doesn't exist.  Please create it and ensure kubelet " +  `
                    "is configured with matching --cni-conf-dir."
        }
    }
    if ($env:CALICO_NETWORKING_BACKEND -EQ "vxlan") {
        if (fileIsMissing($env:CNI_BIN_DIR))
        {
            throw "CNI binary directory $env:CNI_BIN_DIR doesn't exist.  Please create it and ensure kubelet " +  `
                    "is configured with matching --cni-bin-dir."
        }
        if (fileIsMissing($env:CNI_CONF_DIR))
        {
            throw "CNI config directory $env:CNI_CONF_DIR doesn't exist.  Please create it and ensure kubelet " +  `
                    "is configured with matching --cni-conf-dir."
        }
    }
    if ($env:CALICO_NETWORKING_BACKEND -EQ "vxlan" -AND $env:CNI_IPAM_TYPE -NE "calico-ipam") {
        throw "Calico VXLAN requires IPAM type calico-ipam, not $env:CNI_IPAM_TYPE."
    }
    if ($env:CALICO_DATASTORE_TYPE -EQ "kubernetes")
    {
        if (fileIsMissing($env:KUBECONFIG))
        {
            throw "kubeconfig file $env:KUBECONFIG doesn't exist.  Please update the configuration to match. " +  `
                    "the location of your kubeconfig file."
        }
    }
    elseif ($env:CALICO_DATASTORE_TYPE -EQ "etcdv3")
    {
        if (("$env:ETCD_ENDPOINTS" -EQ "") -OR ("$env:ETCD_ENDPOINTS" -EQ "<your etcd endpoints>"))
        {
            throw "Etcd endpoint not set, please update the configuration."
        }
        if (("$env:ETCD_KEY_FILE" -NE "") -OR ("$env:ETCD_CERT_FILE" -NE "") -OR ("$env:ETCD_CA_CERT_FILE" -NE ""))
        {
            if (fileIsMissing($env:ETCD_KEY_FILE))
            {
                throw "Some etcd TLS parameters are configured but etcd key file was not found."
            }
            if (fileIsMissing($env:ETCD_CERT_FILE))
            {
                throw "Some etcd TLS parameters are configured but etcd certificate file was not found."
            }
            if (fileIsMissing($env:ETCD_CA_CERT_FILE))
            {
                throw "Some etcd TLS parameters are configured but etcd CA certificate file was not found."
            }
        }
    }
    else
    {
        throw "Please set datastore type to 'etcdv3' or 'kubernetes'; current value: $env:CALICO_DATASTORE_TYPE."
    }
}

function Set-EnvVarIfNotSet {
    param(
        [parameter(Mandatory=$true)] $var,
        [parameter(Mandatory=$true)] $defaultValue
    )
    if (-not (Test-Path "env:$var"))
    {
        Write-Host ("Environment variable $var is not set. Setting it to the default value: {0}" -f $defaultValue)
        [Environment]::SetEnvironmentVariable($var, $defaultValue, 'Process')
    } else {
        Write-Host ("Environment variable $var is already set: {0}" -f (gci env:$var | select -expand Value))
    }
}

function Set-ConfigParameters {
    param(
        [parameter(Mandatory=$true)] $var,
        [parameter(Mandatory=$true)] $value
    )
    $OldString='Set-EnvVarIfNotSet -var "{0}".*$' -f $var
    $NewString='Set-EnvVarIfNotSet -var "{0}" -defaultValue "{1}"' -f $var, $value
    (Get-Content $baseDir\config.ps1) -replace $OldString, $NewString | Set-Content $baseDir\config.ps1 -Force
}

# Get-DSRSupport returns the string "true" or "false" for the
# __DSR_SUPPORT__ template placeholder.
#
# Order of precedence:
#   1. CALICO_DSR_DISABLE env var: if set to "true", returns "false"
#      (operator-disabled DSR, used in mixed Win/Linux clusters where the
#      Linux pod replies with its own pod IP as source instead of the
#      ClusterIP, which Windows TCP drops; kube-proxy must also be
#      configured with --enable-dsr=false).
#   2. Get-IsDSRSupported: OS-supports check (true on Server 2022).
function Get-DSRSupport()
{
    if ($env:CALICO_DSR_DISABLE -eq "true")
    {
        return "false"
    }
    if (Get-IsDSRSupported)
    {
        return "true"
    }
    return "false"
}

# Build-CNIConfigSubstitutions resolves all the __PLACEHOLDER__ values
# from environment variables and the supplied baseDir. Returns a hashtable
# suitable for Render-CNIConfigTemplate.
#
# Pure function modulo $env: and Get-IsDSRSupported / Get-IsContainerdRunning,
# which are mocked in tests via Pester's `Mock` against this module.
function Build-CNIConfigSubstitutions([string]$BaseDir)
{
    $dnsIPs = "$env:DNS_NAME_SERVERS".Split(",")
    $ipList = @()
    foreach ($ip in $dnsIPs) {
        $ipList += "`"$ip`""
    }
    $dnsIPList = ($ipList -join ",").TrimEnd(',')

    # HNS v1 and v2 have different string values for the ROUTE endpoint policy type.
    $routeType = "ROUTE"
    if (Get-IsContainerdRunning)
    {
        $routeType = "SDNROUTE"
    }

    $mode = ""
    if ($env:CALICO_NETWORKING_BACKEND -EQ "vxlan")
    {
        $mode = "vxlan"
    }

    # Datastore + IPAM type default to kubernetes/calico-ipam if unset.
    # The legacy installer hardcoded these via separately-set env vars;
    # for HostProcess containers without a configured installer step,
    # they may not be set. The CNI plugin rejects empty values with
    # "no plugin name provided" so we must always emit something valid.
    $datastoreType = "$env:CALICO_DATASTORE_TYPE"
    if (-not $datastoreType) { $datastoreType = "kubernetes" }
    $ipamType = "$env:CNI_IPAM_TYPE"
    if (-not $ipamType) { $ipamType = "calico-ipam" }

    # nodename_file: where calico-node.exe -startup writes the cluster
    # node name. The CNI plugin runs OUTSIDE the HostProcess container
    # (containerd invokes calico.exe in the host namespace), so the
    # path here must be host-visible. The legacy installer placed the
    # file at C:\CalicoWindows\nodename. CALICO_NODENAME_FILE_HOST_PATH
    # overrides this default for testing or relocation.
    $nodenameFile = "$env:CALICO_NODENAME_FILE_HOST_PATH"
    if (-not $nodenameFile) { $nodenameFile = "C:\CalicoWindows\nodename" }

    return @{
        NODENAME_FILE     = $nodenameFile.replace('\', '\\')
        KUBECONFIG        = "$env:KUBECONFIG".replace('\', '\\')
        K8S_SERVICE_CIDR  = "$env:K8S_SERVICE_CIDR"
        DNS_NAME_SERVERS  = $dnsIPList
        DATASTORE_TYPE    = $datastoreType
        DSR_SUPPORT       = (Get-DSRSupport)
        ETCD_ENDPOINTS    = "$env:ETCD_ENDPOINTS"
        ETCD_KEY_FILE     = "$env:ETCD_KEY_FILE".replace('\', '\\')
        ETCD_CERT_FILE    = "$env:ETCD_CERT_FILE".replace('\', '\\')
        ETCD_CA_CERT_FILE = "$env:ETCD_CA_CERT_FILE".replace('\', '\\')
        IPAM_TYPE         = $ipamType
        MODE              = $mode
        VNI               = "$env:VXLAN_VNI"
        MAC_PREFIX        = "$env:VXLAN_MAC_PREFIX"
        ROUTE_TYPE        = $routeType
    }
}

# Render-CNIConfigTemplate reads the CNI config template, performs
# placeholder substitution from $Subs, and returns the rendered text.
# Pure: no I/O other than reading $TemplatePath.
function Render-CNIConfigTemplate([string]$TemplatePath, [hashtable]$Subs)
{
    $rendered = (Get-Content $TemplatePath) | ForEach-Object {
        $line = $_
        foreach ($key in $Subs.Keys)
        {
            $line = $line.Replace("__${key}__", "$($Subs[$key])")
        }
        $line
    }
    return $rendered
}

# Write-CNIConfig regenerates the CNI config file from the template at
# $BaseDir\cni.conf.template, substituting current env-var-derived values.
# Idempotent: safe to call on every container start to pick up configmap
# changes (e.g., CALICO_DSR_DISABLE, K8S_SERVICE_CIDR, DNS_NAME_SERVERS).
#
# Does NOT install CNI binaries — that lives in node-service.ps1 for
# HostProcess containers (binaries are in the sandbox, not $BaseDir\cni).
# Install-CNIPlugin still does the binary copy for the legacy installer.
function Write-CNIConfig([string]$BaseDir = $baseDir)
{
    $cniConfFile = $env:CNI_CONF_DIR + "\" + $env:CNI_CONF_FILENAME
    Write-Host "Writing CNI configuration to $cniConfFile."
    $subs = Build-CNIConfigSubstitutions -BaseDir $BaseDir
    Render-CNIConfigTemplate -TemplatePath "$BaseDir\cni.conf.template" -Subs $subs |
        Set-Content $cniConfFile
    Write-Host "Wrote CNI configuration."
}

function Install-CNIPlugin()
{
    Write-Host "Copying CNI binaries to $env:CNI_BIN_DIR"
    cp "$baseDir\cni\*.exe" "$env:CNI_BIN_DIR"
    Write-CNIConfig -BaseDir $baseDir
}

function Remove-CNIPlugin()
{
    $cniConfFile = $env:CNI_CONF_DIR + "\" + $env:CNI_CONF_FILENAME
    if (Test-Path $cniConfFile) {
        Write-Host "Removing Calico CNI conf file at $cniConfFile ..."
        rm $cniConfFile
    }

    $cniBinPath = "$env:CNI_BIN_DIR/calico*.exe"
    if (Test-Path $cniBinPath) {
        Write-Host "Removing Calico CNI binaries at $cniBinPath ..."
        rm $cniBinPath
    }
}

function Install-NodeService()
{
    Write-Host "Installing node startup service..."

    ensureRegistryKey

    # Ensure our service file can run.
    Unblock-File $baseDir\node\node-service.ps1

    & $NSSMPath install CalicoNode $powerShellPath
    & $NSSMPath set CalicoNode AppParameters $baseDir\node\node-service.ps1
    & $NSSMPath set CalicoNode AppDirectory $baseDir
    & $NSSMPath set CalicoNode DisplayName "Calico Windows Startup"
    & $NSSMPath set CalicoNode Description "Calico Windows Startup, configures Calico datamodel resources for this node."

    # Configure it to auto-start by default.
    & $NSSMPath set CalicoNode Start SERVICE_AUTO_START
    & $NSSMPath set CalicoNode ObjectName LocalSystem
    & $NSSMPath set CalicoNode Type SERVICE_WIN32_OWN_PROCESS

    # Throttle process restarts if Felix restarts in under 1500ms.
    & $NSSMPath set CalicoNode AppThrottle 1500

    # Create the log directory if needed.
    if (-Not(Test-Path "$env:CALICO_LOG_DIR"))
    {
        write "Creating log directory."
        md -Path "$env:CALICO_LOG_DIR"
    }
    & $NSSMPath set CalicoNode AppStdout $env:CALICO_LOG_DIR\calico-node.log
    & $NSSMPath set CalicoNode AppStderr $env:CALICO_LOG_DIR\calico-node.err.log

    # Configure online file rotation.
    & $NSSMPath set CalicoNode AppRotateFiles 1
    & $NSSMPath set CalicoNode AppRotateOnline 1
    # Rotate once per day.
    & $NSSMPath set CalicoNode AppRotateSeconds 86400
    # Rotate after 10MB.
    & $NSSMPath set CalicoNode AppRotateBytes 10485760

    Write-Host "Done installing startup service."
}

function Remove-NodeService()
{
    & $NSSMPath remove CalicoNode confirm
}

function Install-FelixService()
{
    Write-Host "Installing Felix service..."

    # Ensure our service file can run.
    Unblock-File $baseDir\felix\felix-service.ps1

    # We run Felix via a wrapper script to make it easier to update env vars.
    & $NSSMPath install CalicoFelix $powerShellPath
    & $NSSMPath set CalicoFelix AppParameters $baseDir\felix\felix-service.ps1
    & $NSSMPath set CalicoFelix AppDirectory $baseDir
    & $NSSMPath set CalicoFelix DependOnService "CalicoNode"
    & $NSSMPath set CalicoFelix DisplayName "Calico Windows Agent"
    & $NSSMPath set CalicoFelix Description "Calico Windows Per-host Agent, Felix, provides network policy enforcement for Kubernetes."

    # Configure it to auto-start by default.
    & $NSSMPath set CalicoFelix Start SERVICE_AUTO_START
    & $NSSMPath set CalicoFelix ObjectName LocalSystem
    & $NSSMPath set CalicoFelix Type SERVICE_WIN32_OWN_PROCESS

    # Throttle process restarts if Felix restarts in under 1500ms.
    & $NSSMPath set CalicoFelix AppThrottle 1500

    # Create the log directory if needed.
    if (-Not(Test-Path "$env:CALICO_LOG_DIR"))
    {
        write "Creating log directory."
        md -Path "$env:CALICO_LOG_DIR"
    }
    & $NSSMPath set CalicoFelix AppStdout $env:CALICO_LOG_DIR\calico-felix.log
    & $NSSMPath set CalicoFelix AppStderr $env:CALICO_LOG_DIR\calico-felix.err.log

    # Configure online file rotation.
    & $NSSMPath set CalicoFelix AppRotateFiles 1
    & $NSSMPath set CalicoFelix AppRotateOnline 1
    # Rotate once per day.
    & $NSSMPath set CalicoFelix AppRotateSeconds 86400
    # Rotate after 10MB.
    & $NSSMPath set CalicoFelix AppRotateBytes 10485760

    Write-Host "Done installing Felix service."
}

function Remove-FelixService() {
    & $NSSMPath remove CalicoFelix confirm
}

function Install-ConfdService()
{
    Write-Host "Installing confd service..."

    # Ensure our service file can run.
    Unblock-File $baseDir\confd\confd-service.ps1

    # We run confd via a wrapper script to make it easier to update env vars.
    & $NSSMPath install CalicoConfd $powerShellPath
    & $NSSMPath set CalicoConfd AppParameters $baseDir\confd\confd-service.ps1
    & $NSSMPath set CalicoConfd AppDirectory $baseDir
    & $NSSMPath set CalicoConfd DependOnService "CalicoNode"
    & $NSSMPath set CalicoConfd DisplayName "Calico BGP Agent"
    & $NSSMPath set CalicoConfd Description "Calico BGP Agent, confd, configures BGP routing."

    # Configure it to auto-start by default.
    & $NSSMPath set CalicoConfd Start SERVICE_AUTO_START
    & $NSSMPath set CalicoConfd ObjectName LocalSystem
    & $NSSMPath set CalicoConfd Type SERVICE_WIN32_OWN_PROCESS

    # Throttle process restarts if confd restarts in under 1500ms.
    & $NSSMPath set CalicoConfd AppThrottle 1500

    # Create the log directory if needed.
    if (-Not(Test-Path "$env:CALICO_LOG_DIR"))
    {
        write "Creating log directory."
        md -Path "$env:CALICO_LOG_DIR"
    }
    & $NSSMPath set CalicoConfd AppStdout $env:CALICO_LOG_DIR\calico-confd.log
    & $NSSMPath set CalicoConfd AppStderr $env:CALICO_LOG_DIR\calico-confd.err.log

    # Configure online file rotation.
    & $NSSMPath set CalicoConfd AppRotateFiles 1
    & $NSSMPath set CalicoConfd AppRotateOnline 1
    # Rotate once per day.
    & $NSSMPath set CalicoConfd AppRotateSeconds 86400
    # Rotate after 10MB.
    & $NSSMPath set CalicoConfd AppRotateBytes 10485760

    Write-Host "Done installing confd service."
}

function Remove-ConfdService() {
    & $NSSMPath remove CalicoConfd confirm
}

function Install-UpgradeService()
{
    Write-Host "Installing Calico Upgrade startup service..."

    ensureRegistryKey

    # Ensure our service file can run.
    Unblock-File $baseDir\upgrade\upgrade-service.ps1

    & $NSSMPath install CalicoUpgrade $powerShellPath
    & $NSSMPath set CalicoUpgrade AppParameters $baseDir\upgrade\upgrade-service.ps1
    & $NSSMPath set CalicoUpgrade AppDirectory $baseDir
    & $NSSMPath set CalicoUpgrade DisplayName "Calico Windows Upgrade"
    & $NSSMPath set CalicoUpgrade Description "Calico Windows Upgrade monitors and manages upgrades"

    # Configure it to auto-start by default.
    & $NSSMPath set CalicoUpgrade Start SERVICE_AUTO_START
    & $NSSMPath set CalicoUpgrade ObjectName LocalSystem
    & $NSSMPath set CalicoUpgrade Type SERVICE_WIN32_OWN_PROCESS

    # Throttle process restarts if Felix restarts in under 1500ms.
    & $NSSMPath set CalicoUpgrade AppThrottle 1500

    # Create the log directory if needed.
    if (-Not(Test-Path "$env:CALICO_LOG_DIR"))
    {
        write "Creating log directory."
        md -Path "$env:CALICO_LOG_DIR"
    }
    & $NSSMPath set CalicoUpgrade AppStdout $env:CALICO_LOG_DIR\calico-upgrade.log
    & $NSSMPath set CalicoUpgrade AppStderr $env:CALICO_LOG_DIR\calico-upgrade.err.log

    # Configure online file rotation.
    & $NSSMPath set CalicoUpgrade AppRotateFiles 1
    & $NSSMPath set CalicoUpgrade AppRotateOnline 1
    # Rotate once per day.
    & $NSSMPath set CalicoUpgrade AppRotateSeconds 86400
    # Rotate after 10MB.
    & $NSSMPath set CalicoUpgrade AppRotateBytes 10485760

    Write-Host "Done installing upgrade service."
}

function Remove-UpgradeService()
{
    $svc = Get-Service | where Name -EQ 'CalicoUpgrade'
    if ($svc -NE $null)
    {
        if ($svc.Status -EQ 'Running')
        {
            Write-Host "CalicoUpgrade service is running, stopping it..."
            & $NSSMPath stop CalicoUpgrade confirm
        }
        Write-Host "Removing CalicoUpgrade service..."
        & $NSSMPath remove CalicoUpgrade confirm
    }
}

function Wait-ForManagementIP($NetworkName)
{
    while ((Get-HnsNetwork | ? Name -EQ $NetworkName).ManagementIP -EQ $null)
    {
        Write-Host "Waiting for management IP to appear on network $NetworkName..."
        Start-Sleep 1
    }
    return (Get-HnsNetwork | ? Name -EQ $NetworkName).ManagementIP
}

# Test-HnsMgmtIpHookMarker decides whether the hns-ipv6-hook needs to
# be re-injected, given the marker file's current state and the desired
# ManagementIP/ManagementIPv6 pair.
#
# Pure function — caller supplies all inputs. Returns one of:
#   'skip'              — marker valid; do NOT Restart-Service hns
#   'reinject-no-bridge'— marker exists but no Calico bridge yet; safe to re-inject
#   'reinject-mismatch' — marker records a different desired pair; re-inject
#   'reinject-stale'    — marker exists but predates the last boot
#   'inject-fresh'      — no marker file; first injection
#
# 'skip' is the only return that suppresses Restart-Service hns. All
# others indicate the caller should restart the hns service and run
# the injector again.
#
# Why a pure decider: the previous in-line check inside
# Inject-HnsMgmtIpHook silently kept stale hooks alive across desired-
# pair rotations (RandomizeIdentifiers off post-first-boot, DHCPv6-PD
# prefix rotation, etc.), causing HNS dual-stack create on the affected
# node to fail with HCN_E_ADAPTER_NOT_FOUND because the hook filtered
# GetAdaptersAddresses down to an IPv6 the NIC no longer carried.
function Test-HnsMgmtIpHookMarker
{
    [CmdletBinding()]
    param(
        [Parameter(Mandatory=$true)] [string]$MarkerPath,
        # Optional — caller may pass $null when the OS query failed.
        [DateTime]$BootTime,
        [Parameter(Mandatory=$true)] [bool]$HaveCalicoNetwork,
        [string]$DesiredV4,
        [string]$DesiredV6
    )

    if (-not (Test-Path $MarkerPath)) {
        return 'inject-fresh'
    }

    # Marker exists but no Calico bridge → safe to re-inject (no working
    # bridge for Restart-Service to destroy). Common on a fresh node
    # where a prior pod injected the hook with a stale desired pair and
    # crashed before the bridge came up.
    if (-not $HaveCalicoNetwork) {
        return 'reinject-no-bridge'
    }

    # Without a boot time the safest thing is to re-inject; can't
    # confirm the marker isn't from a previous boot.
    if (-not $BootTime) {
        return 'reinject-stale'
    }

    $markerTime = (Get-Item $MarkerPath).LastWriteTime
    if ($markerTime -le $BootTime) {
        return 'reinject-stale'
    }

    $expected = "$DesiredV4`t$DesiredV6"
    $actual = (Get-Content $MarkerPath -Raw -ErrorAction SilentlyContinue) -as [string]
    if ($actual) { $actual = $actual.TrimEnd("`r","`n") }
    if ($actual -ne $expected) {
        return 'reinject-mismatch'
    }

    return 'skip'
}

# Get-HnsMgmtIpHookMarkerLine produces the canonical marker file
# content for the desired pair. Single source of truth — both the
# writer in node-service.ps1 and the reader in
# Test-HnsMgmtIpHookMarker use the same format.
function Get-HnsMgmtIpHookMarkerLine
{
    [CmdletBinding()]
    param([string]$DesiredV4, [string]$DesiredV6)
    return "$DesiredV4`t$DesiredV6"
}

# Test-CalicoStartupCanSkip decides whether node-service.ps1 can skip
# calico-node.exe -startup for an already-created Calico L2Bridge.
#
# HNS's ManagementIPv6 field is deliberately not part of this decision:
# Server 2022 may silently ignore the requested ManagementIPv6 on create
# and later report the auto-picked value. Startup reconciliation remains
# the only path allowed to recreate the L2Bridge; once startup has created
# a dual-stack bridge with the correct IPv4 ManagementIP, pod CNI must be
# allowed to reuse it instead of deadlocking on non-authoritative IPv6
# metadata.
function Test-CalicoStartupCanSkip
{
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        $ExistingCalicoNetwork,
        [string]$ExpectedManagementIP
    )

    if (-not $ExistingCalicoNetwork) { return $false }
    if ([string]::IsNullOrEmpty($ExpectedManagementIP)) { return $false }
    if ($ExistingCalicoNetwork.Name -ne 'Calico') { return $false }
    if ($ExistingCalicoNetwork.Type -ne 'L2Bridge') { return $false }
    return ($ExistingCalicoNetwork.ManagementIP -eq $ExpectedManagementIP)
}

# Test-HnsManagementInterfaceAlias returns $true if the supplied
# InterfaceAlias is one we consider eligible to source the desired
# ManagementIP / ManagementIPv6 from. The lifecycle:
#   - "Ethernet"/"Ethernet N"  — bare physical NIC, fresh boot before
#     any Hyper-V vSwitch is attached.
#   - "vEthernet (Ethernet*"   — host vNIC after a vSwitch is created
#     on the management NIC.
#   - "vEthernet (Calico*"     — host vNIC of an existing Calico
#     L2Bridge (or a stale Transparent / Overlay leftover from an
#     older install), where the address can have migrated.
# PowerShell's "Ethernet*" wildcard does NOT match "vEthernet"; the
# branches are disjoint. Pure helper so the filter can be unit-tested.
function Test-HnsManagementInterfaceAlias
{
    [CmdletBinding()]
    [OutputType([bool])]
    param([Parameter(Mandatory=$true)] [string]$InterfaceAlias)
    return ($InterfaceAlias -like 'vEthernet (Ethernet*' -or
            $InterfaceAlias -like 'vEthernet (Calico*' -or
            $InterfaceAlias -like 'Ethernet*')
}

# Resolve-DesiredHnsManagementIPv6 picks the first matching IPv6 from
# a caller-supplied list of NetIPAddress-shaped objects. Encapsulates
# the filter so every call site in node-service.ps1 (and tests) uses
# the same logic. Inputs:
#   $Addresses   list of objects with InterfaceAlias + IPAddress
#                (typically Get-NetIPAddress -AddressFamily IPv6 output)
#   $Prefix      address prefix to match (e.g. "fd5a:8000:1:0:")
# Returns the first matching IPAddress string, or $null if none.
function Resolve-DesiredHnsManagementIPv6
{
    [CmdletBinding()]
    param(
        $Addresses,
        [Parameter(Mandatory=$true)] [string]$Prefix
    )
    $hit = $Addresses |
        Where-Object {
            (Test-HnsManagementInterfaceAlias $_.InterfaceAlias) -and
            ($_.IPAddress -like ($Prefix + '*')) -and
            ($_.IPAddress -notlike 'fe80*')
        } |
        Select-Object -First 1
    if ($hit) { return $hit.IPAddress } else { return $null }
}

# Resolve-DesiredHnsManagementIPv4 picks the first IPv4 from a
# caller-supplied list whose value falls within $NetworkCIDR. APIPA
# and loopback are excluded.
# Resolve-HnsManagementInterfaceAlias returns the InterfaceAlias of
# the first IPv4 address in the supplied list whose value falls within
# $NetworkCIDR and whose alias passes Test-HnsManagementInterfaceAlias.
# Used by node-service.ps1 to pick the InterfaceAlias to pass to
# New-HNSNetwork -AdapterName when bootstrapping the External
# placeholder L2Bridge: External must bind to the same NIC the host's
# IP_AUTODETECTION_METHOD chose, so the subsequent Calico L2Bridge
# create lands on the same adapter without HNS having to re-search.
# Test-IsCalicoManagedVMSwitch returns $true if a Hyper-V vSwitch
# name matches one of the Calico-managed identifiers ("External",
# "Calico", "Calico*"). Used by node-service.ps1 to decide whether
# to remove a vSwitch left behind by a deleted HNS network. Matching
# is intentionally narrow so an unrelated user-created Hyper-V
# external switch is never removed by the orphan-vSwitch cleanup.
# Get-CalicoHnsHookPaths returns the canonical set of paths that the
# hns-ipv6 hook + injector use, with env-var overrides applied. Pure:
# reads $env vars, does no I/O. Single source of truth — both
# node-service.ps1 (Inject-HnsMgmtIpHook + the install-step copy) and
# any future debugging tooling should call this rather than hardcoding
# the literals.
#
# Returns a hashtable with keys:
#   InstallDir   — host directory holding the DLL + injector + marker
#                  (CALICO_HNS_HOOK_INSTALL_DIR; default C:\opt\calico-hns-ipv6)
#   DllPath      — host path of the hook DLL.
#                  (CALICO_HNS_HOOK_DLL_PATH overrides; default <InstallDir>\hns-ipv6-hook.dll)
#   InjectorPath — host path of the injector executable.
#                  (CALICO_HNS_HOOK_INJECTOR_PATH overrides; default <InstallDir>\hns-ipv6-injector.exe)
#   MarkerPath   — Inject-HnsMgmtIpHook marker file recording the
#                  desired pair the current svchost-hns DLL was
#                  injected for.
#                  (CALICO_HNS_HOOK_MARKER_PATH overrides; default <InstallDir>\injected.flag)
#   CfgPath      — host path of the cfg file the injector writes for
#                  the DLL to read at DllMain.
#                  (CALICO_HNS_HOOK_CFG_PATH overrides; default C:\CalicoWindows\hns-ipv6-hook.cfg)
#   LogPath      — destination of the DLL's diagnostic log.
#                  (CALICO_HNS_HOOK_LOG_PATH overrides; default C:\var\log\calico\hook.log)
function Get-CalicoHnsHookPaths
{
    [CmdletBinding()]
    [OutputType([hashtable])]
    param()
    # Build paths via string concatenation rather than Join-Path so
    # the helper is exercisable from Linux pwsh (Join-Path on Linux
    # validates that the drive exists, which is undesirable for unit
    # tests that pass synthetic Windows-style C:\... defaults).
    function _join([string]$dir, [string]$leaf) {
        $sep = if ($dir.EndsWith('\') -or $dir.EndsWith('/')) { '' } else { '\' }
        return "$dir$sep$leaf"
    }
    $installDir = if ($env:CALICO_HNS_HOOK_INSTALL_DIR) { $env:CALICO_HNS_HOOK_INSTALL_DIR } else { 'C:\opt\calico-hns-ipv6' }
    $dllPath    = if ($env:CALICO_HNS_HOOK_DLL_PATH)    { $env:CALICO_HNS_HOOK_DLL_PATH }    else { _join $installDir 'hns-ipv6-hook.dll' }
    $injPath    = if ($env:CALICO_HNS_HOOK_INJECTOR_PATH) { $env:CALICO_HNS_HOOK_INJECTOR_PATH } else { _join $installDir 'hns-ipv6-injector.exe' }
    $markerPath = if ($env:CALICO_HNS_HOOK_MARKER_PATH) { $env:CALICO_HNS_HOOK_MARKER_PATH } else { _join $installDir 'injected.flag' }
    $cfgPath    = if ($env:CALICO_HNS_HOOK_CFG_PATH)    { $env:CALICO_HNS_HOOK_CFG_PATH }    else { 'C:\CalicoWindows\hns-ipv6-hook.cfg' }
    # Log path precedence:
    #   1. CALICO_HNS_HOOK_LOG_PATH — explicit override (highest)
    #   2. <CALICO_LOG_DIR>\hook.log — respect Calico's existing on-Windows
    #      log directory convention (legacy NSSM install sets
    #      CALICO_LOG_DIR to C:\CalicoWindows\logs; operators who set
    #      it explicitly typically want every Calico log under one root).
    #   3. C:\var\log\calico\hook.log — the new default, matches the
    #      Linux /var/log/calico convention and the kubelet pod-log
    #      tree at C:\var\log\pods\....
    if ($env:CALICO_HNS_HOOK_LOG_PATH) {
        $logPath = $env:CALICO_HNS_HOOK_LOG_PATH
    } elseif ($env:CALICO_LOG_DIR) {
        $logPath = (_join $env:CALICO_LOG_DIR 'hook.log')
    } else {
        $logPath = 'C:\var\log\calico\hook.log'
    }
    return @{
        InstallDir   = $installDir
        DllPath      = $dllPath
        InjectorPath = $injPath
        MarkerPath   = $markerPath
        CfgPath      = $cfgPath
        LogPath      = $logPath
    }
}

# Test-RRASNeedsBootstrap returns $true if the RemoteAccess service is
# in a state where confd's Add-BgpRouter / Add-BgpPeer will fail with
# 'LAN Routing not configured'. The bits are installed by the playbook
# (Routing + RemoteAccess Windows features), but the service flips from
# Disabled -> Manual only after Install-RemoteAccess -VpnType RoutingOnly.
# Pure helper — caller passes the Get-Service result so it's exercisable
# from Linux pwsh in tests. Pass $null when the RemoteAccess service
# doesn't exist (feature not installed); the caller should then refuse
# to proceed because nothing this code does will fix that.
#
# Returns:
#   $true  — service is missing the LAN routing role and needs Install-RemoteAccess
#   $false — service is already configured for LAN routing (no action needed)
function Test-RRASNeedsBootstrap
{
    [CmdletBinding()]
    [OutputType([bool])]
    param($Service)
    if (-not $Service) { return $true }
    if ($Service.StartType -eq 'Disabled') { return $true }
    if ($Service.Status -ne 'Running') { return $true }
    return $false
}

function Test-IsCalicoManagedVMSwitch
{
    [CmdletBinding()]
    [OutputType([bool])]
    param([Parameter(Mandatory=$true)] [string]$Name)
    return ($Name -eq 'External' -or
            $Name -eq 'Calico'   -or
            $Name -like 'Calico_*' -or
            $Name -like 'Calico-*')
}

# Test-IsBrokenCalicoVMSwitch returns $true if a Hyper-V vSwitch
# object (from Get-VMSwitch) is in a half-created state that will
# not actually carry pod traffic, even though an HNS L2Bridge
# network of the same name reports as healthy via Get-HnsNetwork.
#
# Symptoms collected from the field:
#   - vSwitch is missing entirely (HNS network exists, no
#     corresponding Hyper-V switch).
#   - SwitchType is not 'External'. A working Calico L2Bridge sits
#     on a SwitchType=External vSwitch bound to the management NIC;
#     SwitchType=Private / Internal indicates the bind step failed.
#   - NetAdapterInterfaceDescription is empty/whitespace. The
#     vSwitch has no underlying physical NIC, so packets cannot
#     leave the host.
#
# Pure helper so the predicate is unit-testable without a live
# Hyper-V instance. Pass $null for $Switch when Get-VMSwitch
# returned nothing for the queried name.
function Test-IsBrokenCalicoVMSwitch
{
    [CmdletBinding()]
    [OutputType([bool])]
    param($Switch)
    if (-not $Switch) { return $true }
    if ($Switch.SwitchType -ne 'External') { return $true }
    if ([string]::IsNullOrWhiteSpace($Switch.NetAdapterInterfaceDescription)) { return $true }
    return $false
}

# Remove-BrokenCalicoHnsNetwork wipes the named HNS network when its
# corresponding Hyper-V vSwitch is in the broken half-created state
# that Test-IsBrokenCalicoVMSwitch detects. Returns $true if a delete
# was performed, $false otherwise.
#
# Why this is needed: a previous calico-node startup that hit
# HCN_E_ADAPTER_NOT_FOUND (0x803b0006) mid-create can leave HNS in
# a state where the network record exists with the desired Subnets
# / ManagementIP / ManagementIPv6 (so networkNeedsRecreate sees
# everything matching and returns false), but the underlying
# vSwitch is Private with no NIC binding (so no traffic flows).
# calico-node never recreates the network on its own, the broken
# state persists forever, and pod CNI add fails because no host
# vNIC exists for endpoint attach.
function Remove-BrokenCalicoHnsNetwork
{
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [string]$NetworkName = 'Calico'
    )
    $hnsNet = Get-HnsNetwork -ErrorAction SilentlyContinue |
                Where-Object { $_.Name -eq $NetworkName -and $_.Type -eq 'L2Bridge' } |
                Select-Object -First 1
    if (-not $hnsNet) { return $false }
    $sw = Get-VMSwitch -Name $NetworkName -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not (Test-IsBrokenCalicoVMSwitch -Switch $sw)) { return $false }
    Write-Host ("Remove-BrokenCalicoHnsNetwork: '" + $NetworkName + "' HNS network is half-created (vSwitch=" + ($(if ($sw) { $sw.SwitchType } else { '<missing>' })) + ", NetAdapter='" + ($(if ($sw) { $sw.NetAdapterInterfaceDescription } else { '' })) + "'); deleting so calico-node can rebuild")
    # hnsdiag.exe is not in the HostProcess sandbox PATH; use the
    # Invoke-HNSRequest path the rest of the codebase uses.
    try {
        Invoke-HNSRequest -Method DELETE -Type networks -Id $hnsNet.Id -ErrorAction Stop | Out-Null
    } catch {
        Write-Host ("Remove-BrokenCalicoHnsNetwork: WARNING: Invoke-HNSRequest DELETE failed: " + $_.Exception.Message)
    }
    return $true
}

function Resolve-HnsManagementInterfaceAlias
{
    [CmdletBinding()]
    param(
        $Addresses,
        [Parameter(Mandatory=$true)] [string]$NetworkCIDR
    )
    $parts = $NetworkCIDR -split '/'
    if ($parts.Length -ne 2) { return $null }
    try {
        $netIP = [System.Net.IPAddress]::Parse($parts[0])
        $netLen = [int]$parts[1]
    } catch { return $null }
    $netBytes = $netIP.GetAddressBytes()
    $maskBits = 0xFFFFFFFFL -shl (32 - $netLen) -band 0xFFFFFFFFL
    $netInt = ([uint32]$netBytes[0] -shl 24) -bor ([uint32]$netBytes[1] -shl 16) -bor ([uint32]$netBytes[2] -shl 8) -bor [uint32]$netBytes[3]
    $netInt = $netInt -band $maskBits

    $hit = $Addresses |
        Where-Object {
            (Test-HnsManagementInterfaceAlias $_.InterfaceAlias) -and
            ($_.IPAddress -notlike '169.254.*') -and
            ($_.IPAddress -ne '127.0.0.1')
        } |
        ForEach-Object {
            try {
                $b = ([System.Net.IPAddress]::Parse($_.IPAddress)).GetAddressBytes()
                $i = ([uint32]$b[0] -shl 24) -bor ([uint32]$b[1] -shl 16) -bor ([uint32]$b[2] -shl 8) -bor [uint32]$b[3]
                if (($i -band $maskBits) -eq $netInt) { $_ }
            } catch {}
        } |
        Select-Object -First 1
    if ($hit) { return $hit.InterfaceAlias } else { return $null }
}

function Resolve-DesiredHnsManagementIPv4
{
    [CmdletBinding()]
    param(
        $Addresses,
        [Parameter(Mandatory=$true)] [string]$NetworkCIDR
    )
    $parts = $NetworkCIDR -split '/'
    if ($parts.Length -ne 2) { return $null }
    try {
        $netIP = [System.Net.IPAddress]::Parse($parts[0])
        $netLen = [int]$parts[1]
    } catch { return $null }
    $netBytes = $netIP.GetAddressBytes()
    $maskBits = 0xFFFFFFFFL -shl (32 - $netLen) -band 0xFFFFFFFFL
    $netInt = ([uint32]$netBytes[0] -shl 24) -bor ([uint32]$netBytes[1] -shl 16) -bor ([uint32]$netBytes[2] -shl 8) -bor [uint32]$netBytes[3]
    $netInt = $netInt -band $maskBits

    $hit = $Addresses |
        Where-Object {
            (Test-HnsManagementInterfaceAlias $_.InterfaceAlias) -and
            ($_.IPAddress -notlike '169.254.*') -and
            ($_.IPAddress -ne '127.0.0.1')
        } |
        ForEach-Object {
            try {
                $b = ([System.Net.IPAddress]::Parse($_.IPAddress)).GetAddressBytes()
                $i = ([uint32]$b[0] -shl 24) -bor ([uint32]$b[1] -shl 16) -bor ([uint32]$b[2] -shl 8) -bor [uint32]$b[3]
                if (($i -band $maskBits) -eq $netInt) { $_ }
            } catch {}
        } |
        Select-Object -First 1
    if ($hit) { return $hit.IPAddress } else { return $null }
}

function Get-LastBootTime()
{
    $bootTime = (Get-CimInstance win32_operatingsystem | select @{LABEL='LastBootUpTime';EXPRESSION={$_.lastbootuptime}}).LastBootUpTime
    if (($bootTime -EQ $null) -OR ($bootTime.length -EQ 0))
    {
        throw "Failed to get last boot time"
    }
 
    # This function is used in conjunction with Get-StoredLastBootTime, which
    # returns a string, so convert the datetime value to a string using the "general" standard format.
    return $bootTime.ToString("G")
}

$softwareRegistryKey = "HKLM:\Software\Tigera"
$calicoRegistryKey = $softwareRegistryKey + "\Calico"

function ensureRegistryKey()
{
    if (! (Test-Path $softwareRegistryKey))
    {
        New-Item $softwareRegistryKey
    }
    if (! (Test-Path $calicoRegistryKey))
    {
        New-Item $calicoRegistryKey
    }
}

function Get-StoredLastBootTime()
{
    try
    {
        return (Get-ItemProperty $calicoRegistryKey -ErrorAction Ignore).LastBootTime
    }
    catch
    {
        $PSItem.Exception.Message
    }
}

function Set-StoredLastBootTime($lastBootTime)
{
    ensureRegistryKey

    return Set-ItemProperty $calicoRegistryKey -Name LastBootTime -Value $lastBootTime
}

function Wait-ForCalicoInit()
{
    Write-Host "Waiting for Calico initialisation to finish..."
    $Stored=Get-StoredLastBootTime
    $Current=Get-LastBootTime
    while ($Stored -NE $Current) {
        Write-Host "Waiting for Calico initialisation to finish...StoredLastBootTime $Stored, CurrentLastBootTime $Current"
        Start-Sleep 1

        $Stored=Get-StoredLastBootTime
        $Current=Get-LastBootTime
    }
    Write-Host "Calico initialisation finished."
}

function Get-PlatformType()
{
    # AKS
    $hnsNetwork = Get-HnsNetwork | ? Name -EQ azure
    if ($hnsNetwork.name -EQ "azure") {
        return ("aks")
    }
    
    # EKS
    $hnsNetwork = Get-HnsNetwork | ? Name -like "vpcbr*"
    if ($hnsNetwork.name -like "vpcbr*") {
        return ("eks")
    }
    
    # EC2
    $restError = $null
    Try {
        $awsNodeName = Invoke-RestMethod -uri http://169.254.169.254/latest/meta-data/local-hostname -ErrorAction Ignore
    } Catch {
        if ($_.Exception.Response.StatusCode.value__ -eq 401) {
            # IMDSv2
            Try {
                $token = Invoke-RestMethod -Headers @{"X-aws-ec2-metadata-token-ttl-seconds" = "21600"} -Method PUT -Uri http://169.254.169.254/latest/api/token -ErrorAction Ignore
                $awsNodeName = Invoke-RestMethod -Headers @{"X-aws-ec2-metadata-token" = $token} -uri http://169.254.169.254/latest/meta-data/local-hostname -ErrorAction Ignore
            }
            Catch {
                $restError = $_
            }
        } else {
            $restError = $_
        }
    }
    if ($restError -eq $null) {
        return ("ec2")
    }
    
    # GCE
    $restError = $null
    Try {
        $gceNodeName = Invoke-RestMethod -UseBasicParsing -Headers @{"Metadata-Flavor"="Google"} "http://metadata.google.internal/computeMetadata/v1/instance/hostname"
    } Catch {
        $restError = $_
    }
    if ($restError -eq $null) {
        return ("gce")
    }

    return ("bare-metal")
}

function Set-MetaDataServerRoute($mgmtIP)
{
    $route = $null
    Try {
        $route=Get-NetRoute -DestinationPrefix 169.254.169.254/32 2>$null
    } Catch {
        Write-Host "Metadata server route not found."
    }
    if ($route -eq $null) {
        Write-Host "Restore metadata server route."
    
        $routePrefix= $mgmtIP + "/32"
        Try {
            $ifIndex=Get-NetRoute -DestinationPrefix $routePrefix | Select-Object -ExpandProperty ifIndex
            New-NetRoute -DestinationPrefix 169.254.169.254/32 -InterfaceIndex $ifIndex
        } Catch {
            Write-Host "Warning! Failed to restore metadata server route."
        }
    }
}

function Get-UpgradeService()
{
    # Don't use get-wmiobject since that is not available in Powershell 7.
    return Get-CimInstance -Query "SELECT * from Win32_Service WHERE name = 'CalicoUpgrade'"
}

# Assume same relative path for containerd CNI bin/conf dir
# By default, containerd is installed in c:\Program Files\containerd, and CNI bin/conf is in
# c:\Program Files\containerd\cni\bin and c:\Program Files\containerd\cni\conf.
function Get-ContainerdCniBinDir()
{
    $path = getContainerdPath
    return "$path\cni\bin"
}
function Get-ContainerdCniConfDir()
{
    $path = getContainerdPath
    return "$path\cni\conf"
}

function getContainerdService()
{
    # Don't use get-wmiobject since that is not available in Powershell 7.
    return Get-CimInstance -Query "SELECT * from Win32_Service WHERE name = 'containerd'"
}

function getContainerdPath()
{
    # Get the containerd service pathname.
    $containerdPathName = getContainerdService | Select-Object -ExpandProperty PathName

    # Get the path only, and remove any extra quotes left over.
    return (Split-Path -Path $containerdPathname) -replace '"', ""
}

function Get-IsContainerdRunning()
{
    return (getContainerdService | Select-Object -ExpandProperty State) -EQ "Running"
}

function Get-IsDSRSupported()
{
    # Determine the windows version and build number for DSR support.
    # OsHardwareAbstractionLayer is a version string like 10.0.17763.1432
    $OSInfo = (Get-ComputerInfo  | select WindowsVersion, OsBuildNumber, OsHardwareAbstractionLayer)

    # Windows supports DSR if
    # - it is 1809 build 1432
    # - it is 1903 or later
    $min1809BuildSupportingDSR = (($OSInfo.OsHardwareAbstractionLayer.Split(".") | select-object -Last 1) -as [int]) -GE 1432
    $windows1809 = (($OSInfo.WindowsVersion -as [int]) -EQ 1809 -And ($OSInfo.OsBuildNumber -as [int]) -GE 17763)
    $windows1903OrNewer = (($OSInfo.WindowsVersion -as [int]) -GE 1903 -And ($OSInfo.OsBuildNumber -as [int]) -GE 18317)

    return ($windows1809 -And $min1809BuildSupportingDSR) -Or $windows1903OrNewer
}

Export-ModuleMember -Function 'Test-*'
Export-ModuleMember -Function 'Install-*'
Export-ModuleMember -Function 'Remove-*'
Export-ModuleMember -Function 'Wait-*'
Export-ModuleMember -Function 'Get-*'
Export-ModuleMember -Function 'Set-*'
Export-ModuleMember -Function 'Build-*'
Export-ModuleMember -Function 'Render-*'
Export-ModuleMember -Function 'Write-*'
Export-ModuleMember -Function 'Resolve-*'
