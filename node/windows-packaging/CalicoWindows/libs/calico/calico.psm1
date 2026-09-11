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
#   1. CALICO_DSR_DISABLE env var: if set to anything other than "false",
#      returns "false".  DSR is disabled by default for mixed Win/Linux
#      clusters where Linux pod replies can use their own pod IP as source
#      instead of the ClusterIP, which Windows TCP drops; kube-proxy must
#      also be configured with --enable-dsr=false.
#   2. CALICO_DSR_DISABLE=false opt-in: enable DSR only if the OS supports it.
function Get-DSRSupport()
{
    if ($env:CALICO_DSR_DISABLE -ne "false")
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

# Resolve-CalicoConfdDirectory returns the directory confd should use for
# templates, generated peerings/blocks, and the reload command working
# directory. In HostProcess mode, $PSScriptRoot points into the container
# sandbox. Use the host package directory instead so generated BGP config
# is durable and config-bgp.ps1 runs beside its template outputs.
function Resolve-CalicoConfdDirectory([string]$ScriptRoot)
{
    if ($env:CALICO_CONFD_HOST_PATH) {
        return $env:CALICO_CONFD_HOST_PATH
    }
    if ($env:CONTAINER_SANDBOX_MOUNT_POINT -or $ScriptRoot -match '^[A-Za-z]:[\\/]hpc[\\/]CalicoWindows[\\/]confd[\\/]?$') {
        return "C:\CalicoWindows\confd"
    }
    return $ScriptRoot
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
    $cniConfFile = Join-Path $env:CNI_CONF_DIR $env:CNI_CONF_FILENAME
    Write-Host "Writing CNI configuration to $cniConfFile."
    $subs = Build-CNIConfigSubstitutions -BaseDir $BaseDir
    $rendered = (Render-CNIConfigTemplate -TemplatePath "$BaseDir\cni.conf.template" -Subs $subs) -join [Environment]::NewLine
    if ([string]::IsNullOrWhiteSpace($rendered)) {
        throw "Rendered CNI configuration is empty."
    }
    if ($rendered -match '__[A-Z_]+__') {
        throw "Rendered CNI configuration contains unsubstituted placeholders."
    }
    $null = $rendered | ConvertFrom-Json

    $tmpFile = "$cniConfFile.$PID.tmp"
    try {
        [System.IO.File]::WriteAllText($tmpFile, $rendered + [Environment]::NewLine, [System.Text.Encoding]::ASCII)
        $tmpInfo = Get-Item -LiteralPath $tmpFile
        if ($tmpInfo.Length -le 0) {
            throw "Rendered CNI configuration temp file is empty."
        }
        Move-Item -LiteralPath $tmpFile -Destination $cniConfFile -Force
    } catch {
        Remove-Item -LiteralPath $tmpFile -Force -ErrorAction SilentlyContinue
        throw
    }

    $cniInfo = Get-Item -LiteralPath $cniConfFile
    if ($cniInfo.Length -le 0) {
        throw "Wrote empty CNI configuration to $cniConfFile."
    }
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
# be re-injected, given the marker file's current state, the current HNS
# service PID, and the desired ManagementIP/ManagementIPv6 pair.
#
# Pure function — caller supplies all inputs. Returns one of:
#   'skip'              — marker valid; do NOT Restart-Service hns
#   'reinject-no-bridge'— marker exists but no Calico bridge yet and does not
#                         prove the current HNS process already has the hook
#   'reinject-mismatch' — marker records a different desired pair; re-inject
#   'reinject-stale'    — marker exists but predates the last boot, or is
#                         a legacy marker that does not prove the HNS PID
#   'reinject-pid'      — marker was for a different HNS PID
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
        [string]$DesiredV6,
        [int]$CurrentHnsPid = 0
    )

    if (-not (Test-Path $MarkerPath)) {
        return 'inject-fresh'
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

    $actual = (Get-Content $MarkerPath -Raw -ErrorAction SilentlyContinue) -as [string]
    if ($actual) { $actual = $actual.TrimEnd("`r","`n") }

    $parts = @($actual -split "`t", 4)
    if ($parts.Count -ge 4 -and $parts[0] -eq 'v2') {
        $markerPid = 0
        [void][int]::TryParse($parts[1], [ref]$markerPid)
        if ($CurrentHnsPid -le 0 -or $markerPid -ne $CurrentHnsPid) {
            return 'reinject-pid'
        }
        if ($parts[2] -ne $DesiredV4 -or $parts[3] -ne $DesiredV6) {
            return 'reinject-mismatch'
        }
        return 'skip'
    }

    # Marker exists but no Calico bridge, and it did not prove that the
    # current HNS process already has the desired v2 hook. Safe to re-inject:
    # no working bridge is present for Restart-Service to destroy.
    if (-not $HaveCalicoNetwork) {
        return 'reinject-no-bridge'
    }

    # Legacy marker: it records only desired addresses, not which
    # svchost-hns process was injected. Treat it as stale when the caller
    # can provide a PID, otherwise keep the old address-only behaviour for
    # unit tests and older call sites.
    if ($CurrentHnsPid -gt 0) {
        return 'reinject-stale'
    }

    $expected = "$DesiredV4`t$DesiredV6"
    if ($actual -ne $expected) {
        return 'reinject-mismatch'
    }

    return 'skip'
}

function Invoke-HnsHookServiceRestart
{
    [CmdletBinding()]
    param(
        [int]$Attempts = 12,
        [int]$DelaySeconds = 5,
        [int]$RunningTimeoutSeconds = 30
    )

    $lastError = $null
    for ($attempt = 1; $attempt -le $Attempts; $attempt++) {
        try {
            Restart-HnsService
        } catch {
            $lastError = $_.Exception.Message
            try {
                Stop-RemoteAccessService
                Stop-HnsService
                Start-HnsService
            } catch {
                $lastError = $_.Exception.Message
                if ($attempt -lt $Attempts) {
                    Write-Host ("Invoke-HnsHookServiceRestart: attempt " + $attempt + "/" + $Attempts + " failed: " + $lastError + "; retrying")
                    Start-Sleep -Seconds $DelaySeconds
                    continue
                }
                throw ("Invoke-HnsHookServiceRestart: hns did not restart after " + $Attempts + " attempts: " + $lastError)
            }
        }

        $deadline = (Get-Date).AddSeconds($RunningTimeoutSeconds)
        while ((Get-Date) -lt $deadline) {
            $svc = Get-HnsService
            if ($svc -and $svc.Status -eq 'Running') {
                return
            }
            Start-Sleep -Milliseconds 500
        }

        $lastError = "service did not report Running within " + $RunningTimeoutSeconds + "s"
        if ($attempt -lt $Attempts) {
            Write-Host ("Invoke-HnsHookServiceRestart: attempt " + $attempt + "/" + $Attempts + " timed out; retrying")
            Start-Sleep -Seconds $DelaySeconds
            continue
        }
    }

    throw ("Invoke-HnsHookServiceRestart: hns did not restart after " + $Attempts + " attempts: " + $lastError)
}

function Restart-HnsService
{
    Restart-Service hns -Force -ErrorAction Stop
}

function Stop-RemoteAccessService
{
    Stop-Service RemoteAccess -Force -ErrorAction SilentlyContinue
}

function Stop-HnsService
{
    Stop-Service hns -Force -ErrorAction Stop
}

function Start-HnsService
{
    Start-Service hns -ErrorAction Stop
}

function Get-HnsService
{
    Get-Service hns -ErrorAction SilentlyContinue
}

# Get-HnsMgmtIpHookMarkerLine produces the canonical marker file
# content for the desired pair. Single source of truth — both the
# writer in node-service.ps1 and the reader in
# Test-HnsMgmtIpHookMarker use the same format.
function Get-HnsMgmtIpHookMarkerLine
{
    [CmdletBinding()]
    param([string]$DesiredV4, [string]$DesiredV6, [int]$HnsPid = 0)
    if ($HnsPid -gt 0) {
        return "v2`t$HnsPid`t$DesiredV4`t$DesiredV6"
    }
    return "$DesiredV4`t$DesiredV6"
}

# Test-CalicoStartupCanSkip decides whether node-service.ps1 can skip
# calico-node.exe -startup for an already-created Calico L2Bridge.
#
# In dual-stack mode this must default to false. The startup binary is the
# only path allowed to reconcile stale HNS L2Bridge subnets after a DHCPv6-PD /
# IPPool rotation. If node-service skips startup based only on IPv4
# ManagementIP, pod CNI ADD later detects the stale IPv6 subnet and correctly
# refuses to delete a live L2Bridge from the per-pod path.
function Test-CalicoStartupCanSkip
{
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        $ExistingCalicoNetwork,
        [string]$ExpectedManagementIP,
        [bool]$IPv6SupportEnabled = ($env:FELIX_IPV6SUPPORT -eq 'true'),
        [bool]$AllowDualStackStartupSkip = ($env:CALICO_ALLOW_DUALSTACK_STARTUP_SKIP -eq 'true'),
        [bool]$BridgeFromCurrentBoot = $true
    )

    if (-not $ExistingCalicoNetwork) { return $false }
    if ([string]::IsNullOrEmpty($ExpectedManagementIP)) { return $false }
    if ($ExistingCalicoNetwork.Name -ne 'Calico') { return $false }
    if ($ExistingCalicoNetwork.Type -ne 'L2Bridge') { return $false }
    if ($IPv6SupportEnabled -and -not $AllowDualStackStartupSkip) { return $false }
    # Restore upstream recreate-on-boot semantics: the skip optimisation
    # exists to avoid repeated in-boot bridge recreates (qemu HNS-restart
    # loops); it must not extend across a host reboot, where the bridge is
    # restored from HNS persistence rather than created by -startup. Post-
    # reboot ClusterIP failures with otherwise-consistent HNS state were
    # observed on the qemu lab and on appmana-026 (Jun 9 2026); recreating
    # on the first run of each boot epoch removes the persisted-bridge
    # variable from that class of incident.
    if (-not $BridgeFromCurrentBoot) { return $false }
    return ($ExistingCalicoNetwork.ManagementIP -eq $ExpectedManagementIP)
}

# Test-CalicoBridgeEpochMarkerFresh: $true when the bridge-epoch marker file
# exists and was written after the last boot — i.e. the Calico L2Bridge was
# (re)created by calico-node -startup in THIS boot epoch, not restored from
# HNS persistence across a reboot. Pure helper: caller supplies boot time.
function Test-CalicoBridgeEpochMarkerFresh
{
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [Parameter(Mandatory=$true)] [string]$MarkerPath,
        [Parameter(Mandatory=$true)] [datetime]$BootTime
    )
    if (-not (Test-Path $MarkerPath)) { return $false }
    try {
        return ((Get-Item $MarkerPath).LastWriteTime -gt $BootTime)
    } catch {
        return $false
    }
}

function Test-CalicoHnsNetworkNeedsStartupRecreate
{
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        $ExistingCalicoNetwork,
        [string]$ExpectedManagementIP,
        [string]$ExpectedManagementIPv6,
        [bool]$IPv6SupportEnabled = ($env:FELIX_IPV6SUPPORT -eq 'true')
    )

    if (-not $ExistingCalicoNetwork) { return $false }
    if ($ExistingCalicoNetwork.Name -ne 'Calico') { return $false }
    if ($ExistingCalicoNetwork.Type -ne 'L2Bridge') { return $false }

    if (-not [string]::IsNullOrEmpty($ExpectedManagementIP) -and
        $ExistingCalicoNetwork.ManagementIP -ne $ExpectedManagementIP) {
        return $true
    }

    if ($IPv6SupportEnabled -and
        -not [string]::IsNullOrEmpty($ExpectedManagementIPv6) -and
        $ExistingCalicoNetwork.ManagementIPv6 -ne $ExpectedManagementIPv6) {
        return $true
    }

    return $false
}

# Get-CalicoHnsNetworkType: the HNS network type calico-node.exe -startup
# owns for the configured backend. windows-bgp creates the Calico L2Bridge
# (SetupL2bridgeNetwork); vxlan creates the Calico Overlay
# (SetupVxlanNetwork). Any other backend owns no HNS network, so every
# L2Bridge-only repair in node-service.ps1 keys off this instead of assuming
# the bridge.
function Get-CalicoHnsNetworkType
{
    [CmdletBinding()]
    [OutputType([string])]
    param(
        [string]$Backend = $env:CALICO_NETWORKING_BACKEND
    )
    switch ($Backend) {
        'windows-bgp' { return 'L2Bridge' }
        'vxlan' { return 'Overlay' }
        default { return $null }
    }
}

function Test-CalicoBackendUsesL2Bridge
{
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [string]$Backend = $env:CALICO_NETWORKING_BACKEND
    )
    return ((Get-CalicoHnsNetworkType -Backend $Backend) -eq 'L2Bridge')
}

# Select-CalicoHnsNetwork picks the network calico-node.exe -startup owns out
# of a Get-HnsNetwork snapshot: named Calico, with the backend's type. A
# Calico network of another type is a leftover from a different backend and
# never the live datapath, and the External placeholder is never it either.
function Select-CalicoHnsNetwork
{
    [CmdletBinding()]
    param(
        $Networks,
        [string]$Backend = $env:CALICO_NETWORKING_BACKEND,
        [string]$NetworkName = 'Calico'
    )
    $type = Get-CalicoHnsNetworkType -Backend $Backend
    if ([string]::IsNullOrEmpty($type)) { return $null }
    return ($Networks | Where-Object { $_.Name -eq $NetworkName -and $_.Type -eq $type } | Select-Object -First 1)
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

function Get-HnsManagementInterfaceRank
{
    [CmdletBinding()]
    [OutputType([int])]
    param([Parameter(Mandatory=$true)] [string]$InterfaceAlias)
    if ($InterfaceAlias -like 'Ethernet*') { return 0 }
    if ($InterfaceAlias -like 'vEthernet (Ethernet*') { return 1 }
    if ($InterfaceAlias -like 'vEthernet (Calico*') { return 2 }
    return 99
}

function Get-HnsManagementAddressRank
{
    [CmdletBinding()]
    [OutputType([int])]
    param([Parameter(Mandatory=$true)] [string]$InterfaceAlias)
    $preference = $env:CALICO_HNS_MGMT_ADDRESS_INTERFACE_PREFERENCE
    if ([string]::IsNullOrWhiteSpace($preference)) {
        $preference = 'vEthernet (Ethernet),Ethernet,vEthernet (Ethernet*),Ethernet*,vEthernet (Calico*)'
    }

    $rank = 0
    foreach ($pattern in ($preference -split ',')) {
        $pattern = $pattern.Trim()
        if ([string]::IsNullOrWhiteSpace($pattern)) { continue }
        if ($InterfaceAlias -like $pattern) { return $rank }
        $rank++
    }
    return 9999
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
        Sort-Object @{ Expression = { Get-HnsManagementAddressRank $_.InterfaceAlias } } |
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
    param(
        $Service,
        [Nullable[bool]]$RoutingConfigured = $null
    )
    if (-not $Service) { return $true }
    if ($Service.StartType -eq 'Disabled') { return $true }
    if ($Service.Status -ne 'Running') { return $true }
    if ($null -ne $RoutingConfigured -and -not $RoutingConfigured) { return $true }
    return $false
}

function Test-RRASRoutingConfigured
{
    [CmdletBinding()]
    [OutputType([Nullable[bool]])]
    param()
    try {
        $remoteAccess = Get-RemoteAccess -ErrorAction Stop
    } catch {
        Write-Host ("WARNING: unable to query RRAS routing status: " + $_.Exception.Message)
        return $null
    }

    foreach ($propertyName in @('RoutingStatus', 'LanRoutingStatus')) {
        $property = $remoteAccess.PSObject.Properties[$propertyName]
        if ($property) {
            $value = "$($property.Value)"
            if ($value -eq 'Installed' -or $value -eq 'Enabled') { return $true }
            if ($value -eq 'Uninstalled' -or $value -eq 'Disabled' -or $value -eq 'NotInstalled') { return $false }
        }
    }

    return $null
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
        Sort-Object @{ Expression = { Get-HnsManagementInterfaceRank $_.InterfaceAlias } } |
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
        Sort-Object @{ Expression = { Get-HnsManagementAddressRank $_.InterfaceAlias } } |
        Select-Object -First 1
    if ($hit) { return $hit.IPAddress } else { return $null }
}

function Test-HnsManagementIPAddressMatchesAutodetection
{
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [Parameter(Mandatory=$true)] [string]$IPAddress,
        [Parameter(Mandatory=$true)] [string]$AutodetectionMethod,
        [ValidateSet('IPv4','IPv6')] [string]$AddressFamily = 'IPv4'
    )
    if ([string]::IsNullOrWhiteSpace($IPAddress)) { return $false }
    if ($AutodetectionMethod -notlike 'cidr=*') { return $true }

    $cidr = $AutodetectionMethod.Substring(5).Split(',')[0].Trim()
    if ([string]::IsNullOrWhiteSpace($cidr)) { return $true }

    if ($AddressFamily -eq 'IPv6') {
        $prefix = ($cidr -split '/')[0] -replace '::$', ':'
        return ($IPAddress -like ($prefix + '*'))
    }

    $parts = $cidr -split '/'
    if ($parts.Length -ne 2) { return $true }
    try {
        $addrBytes = ([System.Net.IPAddress]::Parse($IPAddress)).GetAddressBytes()
        $netBytes = ([System.Net.IPAddress]::Parse($parts[0])).GetAddressBytes()
        if ($addrBytes.Length -ne 4 -or $netBytes.Length -ne 4) { return $false }
        $netLen = [int]$parts[1]
        $maskBits = 0xFFFFFFFFL -shl (32 - $netLen) -band 0xFFFFFFFFL
        $addrInt = ([uint32]$addrBytes[0] -shl 24) -bor ([uint32]$addrBytes[1] -shl 16) -bor ([uint32]$addrBytes[2] -shl 8) -bor [uint32]$addrBytes[3]
        $netInt = ([uint32]$netBytes[0] -shl 24) -bor ([uint32]$netBytes[1] -shl 16) -bor ([uint32]$netBytes[2] -shl 8) -bor [uint32]$netBytes[3]
        return (($addrInt -band $maskBits) -eq ($netInt -band $maskBits))
    } catch {
        return $false
    }
}

function Test-HnsManagementIPAddressIsAssigned
{
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [Parameter(Mandatory=$true)] [string]$IPAddress,
        [Parameter(Mandatory=$true)] $Addresses,
        [ValidateSet('IPv4','IPv6')] [string]$AddressFamily = 'IPv4'
    )

    if ([string]::IsNullOrWhiteSpace($IPAddress)) { return $false }
    $expected = $IPAddress.Trim()

    foreach ($addr in @($Addresses)) {
        if ($null -eq $addr) { continue }
        if (-not (Test-HnsManagementInterfaceAlias $addr.InterfaceAlias)) { continue }
        $candidate = ([string]$addr.IPAddress).Trim()
        if ([string]::IsNullOrWhiteSpace($candidate)) { continue }

        try {
            $parsed = [System.Net.IPAddress]::Parse($candidate)
            if ($AddressFamily -eq 'IPv4' -and $parsed.AddressFamily -ne [System.Net.Sockets.AddressFamily]::InterNetwork) { continue }
            if ($AddressFamily -eq 'IPv6' -and $parsed.AddressFamily -ne [System.Net.Sockets.AddressFamily]::InterNetworkV6) { continue }
        } catch {
            continue
        }

        if ($candidate -eq $expected) {
            return $true
        }
    }
    return $false
}

# Read-PersistedManagementPair reads the desired-management-pair.env file
# written by Inject-HnsMgmtIpHook. Format: "<v4>`t<v6>" on one line; either
# side may be empty. Returns @{ V4 = <string|$null>; V6 = <string|$null> }.
function Read-PersistedManagementPair
{
    [CmdletBinding()]
    param([Parameter(Mandatory=$true)] [string]$Path)
    $pair = @{ V4 = $null; V6 = $null }
    if (-not (Test-Path $Path)) { return $pair }
    try {
        $parts = ((Get-Content -Path $Path -Raw -ErrorAction Stop).TrimEnd("`r", "`n") -split "`t", 2)
        if ($parts.Count -gt 0 -and -not [string]::IsNullOrWhiteSpace($parts[0])) { $pair.V4 = $parts[0].Trim() }
        if ($parts.Count -gt 1 -and -not [string]::IsNullOrWhiteSpace($parts[1])) { $pair.V6 = $parts[1].Trim() }
    } catch {
        Write-Host ("Read-PersistedManagementPair: WARNING: could not read " + $Path + ": " + $_.Exception.Message)
    }
    return $pair
}

# Test-IPAddressFamily returns $true when $IPAddress parses as the given
# address family.
function Test-IPAddressFamily
{
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [string]$IPAddress,
        [ValidateSet('IPv4','IPv6')] [string]$AddressFamily = 'IPv4'
    )
    if ([string]::IsNullOrWhiteSpace($IPAddress)) { return $false }
    try {
        $parsed = [System.Net.IPAddress]::Parse($IPAddress.Trim())
    } catch { return $false }
    if ($AddressFamily -eq 'IPv4') { return $parsed.AddressFamily -eq [System.Net.Sockets.AddressFamily]::InterNetwork }
    return $parsed.AddressFamily -eq [System.Net.Sockets.AddressFamily]::InterNetworkV6
}

# Resolve-DesiredHnsManagementAddress resolves one address family's desired
# HNS management address, with wait/retry before falling back.
#
# Priority order:
#   1. $ExplicitDesired (CALICO_DESIRED_HNS_MGMT_IPV4/6) — authoritative,
#      returned without assignment checks.
#   2. $NodeIP — the kubelet-registered node IP (NODE_IP env from
#      status.hostIP). On hosts with several NICs in the management subnet
#      this is the only unambiguous choice.
#   3. $PersistedDesired — the pair persisted by a previous run.
#   4. cidr autodetection over the last address snapshot.
#
# Candidates 2 and 3 must match $AutodetectionMethod AND be currently
# assigned. When no candidate is assigned, the snapshot is re-taken every
# $PollSeconds up to $DeadlineSeconds before cidr fallback runs: addresses
# on the management NIC vanish briefly right after boot or an HNS restart,
# and falling back instantly latches onto another NIC in the same subnet
# (the appmana-003 USB-NIC brick: persisted 10.2.0.3 rejected as "no longer
# assigned" while the Intel NIC re-bound, cidr=10.2.0.0/24 picked the
# Realtek USB NIC's 10.2.0.24, and the injected hook then hid the real
# management adapter from HNS — 0x803b0006 on every CNI ADD).
#
# When no waitable candidate exists at all (first boot: no NodeIP, no
# persisted pair), the deadline is skipped and fallback runs immediately.
#
# Returns [pscustomobject] @{ Address; Source ('explicit'|'node-ip'|
# 'persisted'|'cidr'|$null); WaitedSeconds; Snapshot }.
function Resolve-DesiredHnsManagementAddress
{
    [CmdletBinding()]
    param(
        [ValidateSet('IPv4','IPv6')] [string]$AddressFamily = 'IPv4',
        [string]$ExplicitDesired,
        [string]$NodeIP,
        [string]$PersistedDesired,
        [string]$AutodetectionMethod,
        [Parameter(Mandatory=$true)] [scriptblock]$SnapshotProvider,
        [int]$DeadlineSeconds = 120,
        [int]$PollSeconds = 3,
        [scriptblock]$SleepFunction = { param($s) Start-Sleep -Seconds $s }
    )

    if (-not [string]::IsNullOrWhiteSpace($ExplicitDesired)) {
        return [pscustomobject]@{ Address = $ExplicitDesired.Trim(); Source = 'explicit'; WaitedSeconds = 0; Snapshot = (& $SnapshotProvider) }
    }

    # Build the ordered waitable candidate list.
    $candidates = @()
    if ((Test-IPAddressFamily -IPAddress $NodeIP -AddressFamily $AddressFamily) -and
        (Test-HnsManagementIPAddressMatchesAutodetection -IPAddress $NodeIP -AutodetectionMethod $AutodetectionMethod -AddressFamily $AddressFamily)) {
        $candidates += [pscustomobject]@{ Address = $NodeIP.Trim(); Source = 'node-ip' }
    }
    if ((Test-IPAddressFamily -IPAddress $PersistedDesired -AddressFamily $AddressFamily) -and
        (Test-HnsManagementIPAddressMatchesAutodetection -IPAddress $PersistedDesired -AutodetectionMethod $AutodetectionMethod -AddressFamily $AddressFamily)) {
        if (-not ($candidates | Where-Object { $_.Address -eq $PersistedDesired.Trim() })) {
            $candidates += [pscustomobject]@{ Address = $PersistedDesired.Trim(); Source = 'persisted' }
        }
    }

    $waited = 0
    $snapshot = & $SnapshotProvider
    if ($candidates.Count -gt 0) {
        while ($true) {
            foreach ($c in $candidates) {
                if (Test-HnsManagementIPAddressIsAssigned -IPAddress $c.Address -Addresses $snapshot -AddressFamily $AddressFamily) {
                    return [pscustomobject]@{ Address = $c.Address; Source = $c.Source; WaitedSeconds = $waited; Snapshot = $snapshot }
                }
            }
            if ($waited -ge $DeadlineSeconds) { break }
            $step = [Math]::Min($PollSeconds, $DeadlineSeconds - $waited)
            if ($step -le 0) { break }
            & $SleepFunction $step
            $waited += $step
            $snapshot = & $SnapshotProvider
        }
        Write-Host ("Resolve-DesiredHnsManagementAddress: " + $AddressFamily + " candidates (" + (($candidates | ForEach-Object { $_.Source + "=" + $_.Address }) -join ", ") + ") not assigned after " + $waited + "s; falling back to autodetection")
    }

    # cidr fallback over the freshest snapshot.
    $fallback = $null
    if ($AutodetectionMethod -like 'cidr=*') {
        $cidr = $AutodetectionMethod.Substring(5).Split(',')[0].Trim()
        if ($AddressFamily -eq 'IPv6') {
            $prefix = ($cidr -split '/')[0] -replace '::$', ':'
            $fallback = Resolve-DesiredHnsManagementIPv6 -Addresses $snapshot -Prefix $prefix
        } else {
            try {
                $fallback = Resolve-DesiredHnsManagementIPv4 -Addresses $snapshot -NetworkCIDR $cidr
            } catch {
                Write-Host ("Resolve-DesiredHnsManagementAddress: WARNING: cannot parse autodetection method '" + $AutodetectionMethod + "': " + $_.Exception.Message)
            }
        }
    }
    if (-not [string]::IsNullOrWhiteSpace($fallback)) {
        return [pscustomobject]@{ Address = $fallback; Source = 'cidr'; WaitedSeconds = $waited; Snapshot = $snapshot }
    }
    return [pscustomobject]@{ Address = $null; Source = $null; WaitedSeconds = $waited; Snapshot = $snapshot }
}

# Get-RenderedBgpPeerNames parses a confd-rendered peerings.ps1 and returns
# the peer names it declares (Mesh_*, Mesh6_*, Global_*, Node_*). Parsing is
# textual (no dot-sourcing) so the function is pure and unit-testable.
function Get-RenderedBgpPeerNames
{
    [CmdletBinding()]
    param([Parameter(Mandatory=$true)] [string]$PeeringsPath)
    if (-not (Test-Path $PeeringsPath)) { return @() }
    $names = @()
    try {
        $content = Get-Content -Raw -Path $PeeringsPath -ErrorAction Stop
        foreach ($m in [regex]::Matches($content, '@\{\s*Name\s*=\s*"([^"]+)"')) {
            $names += $m.Groups[1].Value
        }
    } catch {
        Write-Host ("Get-RenderedBgpPeerNames: WARNING: could not parse " + $PeeringsPath + ": " + $_.Exception.Message)
    }
    return @($names)
}

# Get-BgpPeerDrift compares the confd-rendered desired peer set with the
# peers actually present in RRAS. Only confd-managed peer name prefixes are
# considered for the Extra set so operator-added peers are left alone.
# RRAS persists peers across reboots but loses them on upgrades/reinstalls,
# and confd only re-applies when its rendered output CHANGES — observed on
# appmana-005, which sat with zero BGP peers (pod block unroutable from the
# rest of the cluster) for two days while confd considered everything in
# sync. Returns @{ Missing = @(); Extra = @() }.
function Get-BgpPeerDrift
{
    [CmdletBinding()]
    param(
        $RenderedPeerNames,
        $ActualPeerNames
    )
    $rendered = @($RenderedPeerNames | Where-Object { $_ })
    $actual = @($ActualPeerNames | Where-Object { $_ })
    $managedPrefixes = @('Mesh_*', 'Mesh6_*', 'Global_*', 'Node_*')
    $missing = @($rendered | Where-Object { $actual -notcontains $_ })
    $extra = @($actual | Where-Object {
        $name = $_
        ($rendered -notcontains $name) -and
        (($managedPrefixes | Where-Object { $name -like $_ }).Count -gt 0)
    })
    return @{ Missing = $missing; Extra = $extra }
}

# Get-BgpEmptyRibDecision detects the connected-but-route-less RRAS state
# seen on appmana-026 and appmana-003 after reboots (2026-07-09): every peer
# reports ConnectivityStatus=Connected yet Get-BgpRouteInformation returns
# NOTHING, and stays that way until RemoteAccess is restarted. Peer-set
# drift repair cannot see this (the peer sets match) and re-running
# config-bgp.ps1 does not clear it. The function is pure: the caller feeds
# the observed counts plus its prior consecutive-stuck counter, and acts on
# RestartNeeded. MinConnectedPeers guards genuinely isolated nodes (a node
# with one or two sessions may legitimately have nothing to learn yet), and
# StuckObservationsBeforeRestart makes the caller ride out normal BGP
# convergence instead of restarting on a transient.
function Get-BgpEmptyRibDecision
{
    [CmdletBinding()]
    param(
        [Parameter(Mandatory=$true)] [int]$ConnectedPeerCount,
        [Parameter(Mandatory=$true)] [int]$RibRouteCount,
        [Parameter(Mandatory=$true)] [int]$ConsecutiveStuckObservations,
        [int]$MinConnectedPeers = 3,
        [int]$StuckObservationsBeforeRestart = 3
    )
    $stuck = ($ConnectedPeerCount -ge $MinConnectedPeers) -and ($RibRouteCount -eq 0)
    if (-not $stuck) {
        return @{ Stuck = $false; NewConsecutive = 0; RestartNeeded = $false }
    }
    $n = $ConsecutiveStuckObservations + 1
    return @{
        Stuck = $true
        NewConsecutive = $n
        RestartNeeded = ($n -ge $StuckObservationsBeforeRestart)
    }
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
Export-ModuleMember -Function 'Invoke-*'
Export-ModuleMember -Function 'Read-*'
Export-ModuleMember -Function 'Select-*'
