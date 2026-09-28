param([ValidateSet('Prepare','Verify')][string]$Mode = 'Verify')
$ErrorActionPreference = 'Stop'

function Invoke-RRASPrerequisites {
    param([ValidateSet('Prepare','Verify')][string]$Mode)
    $ErrorActionPreference = 'Stop'
    $os = Get-CimInstance Win32_OperatingSystem
    if ($os.BuildNumber -ne '20348') { throw 'RRAS prerequisites require Windows Server 2022 build 20348' }
    Import-Module ServerManager -ErrorAction Stop
    $names = @('RemoteAccess','Routing','RSAT-RemoteAccess-PowerShell')
    $features = @(Get-WindowsFeature -Name $names -ErrorAction Stop)
    if ($features.Count -ne $names.Count) { throw 'Missing required RRAS feature metadata' }
    foreach ($name in $names) {
        if (@($features | Where-Object Name -eq $name).Count -ne 1) { throw "Missing or duplicate feature $name" }
    }
    $missing = @($features | Where-Object { $_.InstallState -ne 'Installed' })
    if ($missing.Count) {
        if ($Mode -ne 'Prepare') { throw 'RRAS Windows features are not installed' }
        Import-Module Dism -ErrorAction Stop
        # Use this image's ServerManager->DISM mapping, not guessed feature IDs.
        # Refuse removed payloads; LimitAccess prevents Windows Update fallback.
        $installNames = @()
        foreach ($feature in $missing) {
            if ($feature.InstallState -ne 'Available') { throw "Offline payload unavailable for $($feature.Name)" }
            $mapping = @($feature.AdditionalInfo.InstallName)
            if ($mapping.Count -ne 1) { throw "Ambiguous DISM mapping for $($feature.Name)" }
            $installName = [string]$mapping[0]
            if ([string]::IsNullOrWhiteSpace($installName)) { throw "Missing DISM mapping for $($feature.Name)" }
            $optional = @(Get-WindowsOptionalFeature -Online -FeatureName $installName -ErrorAction Stop)
            if ($optional.Count -ne 1 -or $optional[0].FeatureName -ne $installName -or $optional[0].State -notin @('Disabled','Enabled')) {
                throw "Unusable local DISM feature $installName"
            }
            $installNames += $installName
        }
        $result = Enable-WindowsOptionalFeature -Online -FeatureName $installNames -All -LimitAccess -NoRestart -ErrorAction Stop
        if ($null -eq $result) { throw 'Missing DISM installation result' }
        if ($result.RestartNeeded) { return 'reboot' }
        $features = @(Get-WindowsFeature -Name $names -ErrorAction Stop)
        if ($features.Count -ne 3 -or @($features | Where-Object InstallState -ne 'Installed').Count) { throw 'RRAS features remain uninstalled' }
    }
    Import-Module RemoteAccess -ErrorAction Stop
    foreach ($command in @('Get-RemoteAccess','Install-RemoteAccess','Get-BgpRouter','Add-BgpRouter','Set-BgpRouter','Get-BgpPeer','Add-BgpPeer','Get-BgpCustomRoute','Add-BgpCustomRoute')) {
        Get-Command $command -ErrorAction Stop | Out-Null
    }
    $reboot = $false
    foreach ($family in @('Tcpip','Tcpip6')) {
        $path = "HKLM:\SYSTEM\CurrentControlSet\Services\$family\Parameters"
        $property = Get-ItemProperty -Path $path -Name IPEnableRouter -ErrorAction SilentlyContinue
        if ($null -eq $property -or $property.IPEnableRouter -ne 1) {
            if ($Mode -ne 'Prepare') { throw "$family forwarding requires preparation and reboot" }
            New-ItemProperty -Path $path -Name IPEnableRouter -Value 1 -PropertyType DWord -Force -ErrorAction Stop | Out-Null
            $reboot = $true
        }
    }
    if ($reboot) { return 'reboot' }
    $routing = Get-RemoteAccess -ErrorAction Stop
    if ($routing.RoutingStatus -ne 'Installed' -and $routing.LanRoutingStatus -ne 'Enabled') {
        if ($Mode -ne 'Prepare') { throw 'RRAS LAN routing is not configured' }
        Install-RemoteAccess -VpnType RoutingOnly -PassThru -ErrorAction Stop | Out-Null
        $routing = Get-RemoteAccess -ErrorAction Stop
        if ($routing.RoutingStatus -ne 'Installed' -and $routing.LanRoutingStatus -ne 'Enabled') { throw 'RoutingOnly configuration failed' }
    }
    $service = Get-Service RemoteAccess -ErrorAction Stop
    if ($service.StartType -ne 'Automatic' -or $service.Status -ne 'Running') {
        if ($Mode -ne 'Prepare') { throw 'RRAS service is not running automatically' }
        Set-Service RemoteAccess -StartupType Automatic -ErrorAction Stop
        Start-Service RemoteAccess -ErrorAction Stop
        $service = Get-Service RemoteAccess -ErrorAction Stop
        if ($service.StartType -ne 'Automatic' -or $service.Status -ne 'Running') { throw 'RRAS service failed to start' }
    }
    return 'ready'
}

if ($MyInvocation.InvocationName -ne '.') {
    Invoke-RRASPrerequisites -Mode $Mode
}
