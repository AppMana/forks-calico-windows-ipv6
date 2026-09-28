BeforeAll {
    . "$PSScriptRoot/rras-prerequisites.ps1"
    function Get-CimInstance { param($ClassName) }
    function Get-WindowsFeature { param($Name) }
    function Get-WindowsOptionalFeature { param([switch]$Online,$FeatureName) }
    function Enable-WindowsOptionalFeature { param([switch]$Online,$FeatureName,[switch]$All,[switch]$LimitAccess,[switch]$NoRestart) }
    function Get-RemoteAccess {}
    function Install-RemoteAccess { param($VpnType,[switch]$PassThru) }
    function Get-Service { param($Name) }
    function Set-Service { param($Name,$StartupType) }
    function Start-Service { param($Name) }
}
Describe 'RRAS explicit offline prerequisites' {
    BeforeEach {
        Mock Get-CimInstance { @{BuildNumber='20348'} }
        Mock Import-Module {}
        Mock Get-WindowsFeature { @('RemoteAccess','Routing','RSAT-RemoteAccess-PowerShell') | ForEach-Object { [pscustomobject]@{Name=$_;InstallState='Installed';AdditionalInfo=@{InstallName="dism-$_"}} } }
        Mock Get-Command { @{Name=$Name} }
        Mock Get-ItemProperty { @{IPEnableRouter=1} }
        Mock Get-RemoteAccess { @{RoutingStatus='Installed';LanRoutingStatus='Enabled'} }
        Mock Get-Service { @{StartType='Automatic';Status='Running'} }
        Mock Enable-WindowsOptionalFeature { throw 'unexpected installation' }
        Mock Install-RemoteAccess { throw 'unexpected configuration' }
        Mock New-ItemProperty { throw 'unexpected registry mutation' }
        Mock Set-Service { throw 'unexpected service mutation' }
        Mock Start-Service { throw 'unexpected service mutation' }
    }
    It 'verifies prepared prerequisites without mutation' {
        Invoke-RRASPrerequisites -Mode Verify | Should -BeExactly ready
        Should -Invoke Enable-WindowsOptionalFeature -Times 0
        Should -Invoke Install-RemoteAccess -Times 0
        Should -Invoke New-ItemProperty -Times 0
        Should -Invoke Set-Service -Times 0
    }
    It 'refuses missing features in verify mode' {
        Mock Get-WindowsFeature { @() }
        { Invoke-RRASPrerequisites -Mode Verify } | Should -Throw '*metadata*'
    }
    It 'refuses removed offline payloads before installation' {
        Mock Get-WindowsFeature { @('RemoteAccess','Routing','RSAT-RemoteAccess-PowerShell') | ForEach-Object { [pscustomobject]@{Name=$_;InstallState='Removed';AdditionalInfo=@{InstallName="dism-$_"}} } }
        { Invoke-RRASPrerequisites -Mode Prepare } | Should -Throw '*Offline payload unavailable*'
        Should -Invoke Enable-WindowsOptionalFeature -Times 0
    }
    It 'uses local validated mappings and requires caller reboot when DISM requests it' {
        Mock Get-WindowsFeature { @('RemoteAccess','Routing','RSAT-RemoteAccess-PowerShell') | ForEach-Object { [pscustomobject]@{Name=$_;InstallState='Available';AdditionalInfo=@{InstallName="dism-$_"}} } }
        Mock Get-WindowsOptionalFeature { [pscustomobject]@{FeatureName=$FeatureName;State='Disabled'} }
        Mock Enable-WindowsOptionalFeature { @{RestartNeeded=$true} }
        Invoke-RRASPrerequisites -Mode Prepare | Should -BeExactly reboot
        Should -Invoke Enable-WindowsOptionalFeature -Times 1 -ParameterFilter { $Online -and $All -and $LimitAccess -and $NoRestart -and $FeatureName.Count -eq 3 }
        Should -Invoke Install-RemoteAccess -Times 0
    }
    It 'does not swallow servicing errors' {
        Mock Get-WindowsFeature { @('RemoteAccess','Routing','RSAT-RemoteAccess-PowerShell') | ForEach-Object { [pscustomobject]@{Name=$_;InstallState='Available';AdditionalInfo=@{InstallName="dism-$_"}} } }
        Mock Get-WindowsOptionalFeature { throw 'metadata unavailable' }
        { Invoke-RRASPrerequisites -Mode Prepare } | Should -Throw '*metadata unavailable*'
    }
    It 'refuses unconfigured routing in verify mode' {
        Mock Get-RemoteAccess { @{RoutingStatus='Uninstalled';LanRoutingStatus='Disabled'} }
        { Invoke-RRASPrerequisites -Mode Verify } | Should -Throw '*not configured*'
    }
    It 'refuses stopped RRAS in verify mode' {
        Mock Get-Service { @{StartType='Disabled';Status='Stopped'} }
        { Invoke-RRASPrerequisites -Mode Verify } | Should -Throw '*not running*'
    }
    It 'requests reboot after setting forwarding without claiming readiness' {
        Mock Get-ItemProperty { @{IPEnableRouter=0} }
        Mock New-ItemProperty {}
        Invoke-RRASPrerequisites -Mode Prepare | Should -BeExactly reboot
        Should -Invoke New-ItemProperty -Times 2
        Should -Invoke Install-RemoteAccess -Times 0
    }
    It 'prepares RoutingOnly and verifies the resulting service' {
        $script:configured = $false
        $script:started = $false
        Mock Get-RemoteAccess { if ($script:configured) { @{RoutingStatus='Installed'} } else { @{RoutingStatus='Uninstalled'} } }
        Mock Install-RemoteAccess { $script:configured = $true }
        Mock Get-Service { if ($script:started) { @{StartType='Automatic';Status='Running'} } else { @{StartType='Disabled';Status='Stopped'} } }
        Mock Set-Service {}
        Mock Start-Service { $script:started = $true }
        Invoke-RRASPrerequisites -Mode Prepare | Should -BeExactly ready
        Should -Invoke Install-RemoteAccess -Times 1 -ParameterFilter { $VpnType -eq 'RoutingOnly' }
        Invoke-RRASPrerequisites -Mode Verify | Should -BeExactly ready
        Should -Invoke Install-RemoteAccess -Times 1
    }
}
