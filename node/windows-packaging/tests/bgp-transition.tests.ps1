BeforeAll {
    . "$PSScriptRoot/../../../libcalico-go/lib/winutils/bgp_transition.ps1"
    function Get-Service {
        if ($script:serviceQueryFails) { throw 'service query failed' }
        [pscustomobject]@{Name='RemoteAccess';Status=$script:serviceStatus}
    }
    function Get-NetIPAddress { [pscustomobject]@{IPAddress='fd00:10::20';InterfaceIndex=$script:interfaceIndex;AddressState='Preferred'} }
    function Get-CimInstance { [pscustomobject]@{LastBootUpTime=$script:bootTime} }
    function Get-CalicoBGPBinding { @{NetworkID='bridge-2';ManagementIP='fd00:10::20';EpochPath=$script:epochPath} }
    function Get-BgpPeer {
        param($Name)
        if ($script:queryFails) { throw 'query failed' }
        if ($Name) { $script:peers | Where-Object PeerName -eq $Name } else { $script:peers }
    }
    function Stop-BgpPeer {
        param($Name, [switch]$Force)
        if (!(Test-Path $script:checkpoint)) { throw 'stop before checkpoint' }
        if ($script:stopFails) { throw 'stop failed' }
        $script:stops++
        ($script:peers | Where-Object PeerName -eq $Name).ConnectivityStatus='Disconnected'
    }
    function Start-BgpPeer {
        param($Name)
        if ($script:startFails) { throw 'start failed' }
        $script:starts++
        $script:bindingsAtResume += $script:rrasBinding
        ($script:peers | Where-Object PeerName -eq $Name).ConnectivityStatus='Connecting'
    }
    function Restart-Service {
        param($Name, [switch]$Force)
        if ($Name -ne 'RemoteAccess') { throw 'unexpected service restart' }
        $script:rebinds++
        if ($script:rebindFails) { throw 'RRAS rebind failed' }
        $script:rrasBinding = $script:managementBinding
    }
}

Describe 'Planned HNS BGP session transition' {
    BeforeEach {
        $script:checkpoint=Join-Path $TestDrive 'bgp.json'
        $script:epochPath=Join-Path $TestDrive 'bridge-epoch.flag'
        Remove-Item $script:epochPath -ErrorAction SilentlyContinue
        $script:interfaceIndex=6; $script:bootTime=[datetime]'2026-10-01T00:00:00Z'
        Remove-Item $script:checkpoint -ErrorAction SilentlyContinue
        $script:serviceStatus='Running'
        $script:serviceQueryFails=$false
        $script:queryFails=$false; $script:stopFails=$false; $script:startFails=$false
        $script:stops=0; $script:starts=0
        $script:managementBinding='old-interface'; $script:rrasBinding='old-interface'
        $script:bindingsAtResume=@(); $script:rebinds=0; $script:rebindFails=$false
        $script:peers=@([pscustomobject]@{
            PeerName='Mesh6_fd00_10__10'; LocalIPAddress='fd00:10::20';
            PeerIPAddress='fd00:10::10'; PeerASN=64512; PeeringMode='Automatic'; ConnectivityStatus='Connected'
        })
    }

    It 'checkpoints before closing and resumes only after completion' {
        Begin-CalicoBGPSessionTransition $script:checkpoint
        $script:stops | Should -Be 1
        $script:starts | Should -Be 0
        Complete-CalicoBGPSessionTransition $script:checkpoint
        $script:starts | Should -Be 1
        Test-Path $script:checkpoint | Should -BeFalse
    }

    It 'retains intent across an interrupted replacement and another process invocation' {
        Begin-CalicoBGPSessionTransition $script:checkpoint
        Begin-CalicoBGPSessionTransition $script:checkpoint
        $script:stops | Should -Be 1
        Complete-CalicoBGPSessionTransition $script:checkpoint
        $script:starts | Should -Be 1
    }

    It 'rebinds RRAS to the replacement interface before resuming interrupted peers' {
        Begin-CalicoBGPSessionTransition $script:checkpoint
        # HNS has replaced the adapter while the CNI mutex is held. A later
        # node-service restart must not be needed after ADD reports success.
        $script:managementBinding='replacement-interface'
        Complete-CalicoBGPSessionTransition $script:checkpoint
        $script:bindingsAtResume.Count | Should -Be 1
        $script:bindingsAtResume[0] | Should -Be 'replacement-interface'
        $script:rebinds | Should -Be 1
        $record=Get-Content $script:epochPath -Raw | ConvertFrom-Json
        $record.NetworkID | Should -Be 'bridge-2'
        $record.ManagementIP | Should -Be 'fd00:10::20'
        $record.InterfaceIndex | Should -Be 6
        $record.BootTimeTicks | Should -Be $script:bootTime.ToUniversalTime().Ticks.ToString()
    }

    It 'does not resume peers or discard recovery intent when RRAS rebind fails' {
        Begin-CalicoBGPSessionTransition $script:checkpoint
        $script:managementBinding='replacement-interface'
        $script:rebindFails=$true
        { Complete-CalicoBGPSessionTransition $script:checkpoint } | Should -Throw '*RRAS rebind failed*'
        $script:starts | Should -Be 0
        Test-Path $script:checkpoint | Should -BeTrue
        Test-Path $script:epochPath | Should -BeFalse
        $script:rebindFails=$false
        Complete-CalicoBGPSessionTransition $script:checkpoint
        $script:bindingsAtResume[0] | Should -Be 'replacement-interface'
        Test-Path $script:checkpoint | Should -BeFalse
    }

    It 'fails closed and preserves intent when stop fails' {
        $script:stopFails=$true
        { Begin-CalicoBGPSessionTransition $script:checkpoint } | Should -Throw '*stop failed*'
        Test-Path $script:checkpoint | Should -BeTrue
        $script:stopFails=$false
        Begin-CalicoBGPSessionTransition $script:checkpoint
        Complete-CalicoBGPSessionTransition $script:checkpoint
        $script:starts | Should -Be 1
    }

    It 'retains recovery intent when start fails' {
        Begin-CalicoBGPSessionTransition $script:checkpoint
        $script:startFails=$true
        { Complete-CalicoBGPSessionTransition $script:checkpoint } | Should -Throw '*start failed*'
        Test-Path $script:checkpoint | Should -BeTrue
        Test-Path $script:epochPath | Should -BeFalse
        $script:rebinds | Should -Be 1
        $script:startFails=$false
        Complete-CalicoBGPSessionTransition $script:checkpoint
        $script:rebinds | Should -Be 1
        Test-Path $script:checkpoint | Should -BeFalse
    }

    It 'rebinds again if the interface identity changed during a failed recovery' {
        Begin-CalicoBGPSessionTransition $script:checkpoint
        $script:startFails=$true
        { Complete-CalicoBGPSessionTransition $script:checkpoint } | Should -Throw '*start failed*'
        $script:interfaceIndex=7
        $script:startFails=$false
        Complete-CalicoBGPSessionTransition $script:checkpoint
        $script:rebinds | Should -Be 2
    }

    It 'does not reuse a completed rebind from an earlier boot' {
        Begin-CalicoBGPSessionTransition $script:checkpoint
        $script:startFails=$true
        { Complete-CalicoBGPSessionTransition $script:checkpoint } | Should -Throw '*start failed*'
        $script:bootTime=$script:bootTime.AddDays(1)
        $script:startFails=$false
        Complete-CalicoBGPSessionTransition $script:checkpoint
        $script:rebinds | Should -Be 2
    }

    It 'keeps recovery intent when publishing the completed binding fails' {
        Begin-CalicoBGPSessionTransition $script:checkpoint
        $parentFile=Join-Path $TestDrive 'not-a-directory'
        Set-Content $parentFile 'file'
        $validEpoch=$script:epochPath
        $script:epochPath=Join-Path $parentFile 'epoch'
        { Complete-CalicoBGPSessionTransition $script:checkpoint } | Should -Throw
        Test-Path $script:checkpoint | Should -BeTrue
        $script:epochPath=$validEpoch
        Complete-CalicoBGPSessionTransition $script:checkpoint
        $script:rebinds | Should -Be 1
        Test-Path $script:checkpoint | Should -BeFalse
    }

    It 'does not start a reconfigured or deleted peer' {
        Begin-CalicoBGPSessionTransition $script:checkpoint
        $script:peers[0].PeerIPAddress='fd00:10::99'
        Complete-CalicoBGPSessionTransition $script:checkpoint
        $script:starts | Should -Be 0
    }

    It 'does not stop unrelated or already stopped peers' {
        $script:peers[0].PeerName='administrator-peer'
        Begin-CalicoBGPSessionTransition $script:checkpoint
        $script:peers[0].PeerName='Mesh6_fd00_10__10'
        $script:peers[0].ConnectivityStatus='Disconnected'
        Begin-CalicoBGPSessionTransition $script:checkpoint
        $script:stops | Should -Be 0
        Test-Path $script:checkpoint | Should -BeFalse
    }

    It 'does not query BGP when RRAS is stopped' {
        $script:serviceStatus='Stopped'; $script:queryFails=$true
        { Begin-CalicoBGPSessionTransition $script:checkpoint } | Should -Not -Throw
    }

    It 'fails before mutation when an active RRAS query fails' {
        $script:queryFails=$true
        { Begin-CalicoBGPSessionTransition $script:checkpoint } | Should -Throw '*query failed*'
        $script:stops | Should -Be 0
    }

    It 'does not mistake a failed service query for absent RRAS' {
        $script:serviceQueryFails=$true
        { Begin-CalicoBGPSessionTransition $script:checkpoint } | Should -Throw '*service query failed*'
        $script:stops | Should -Be 0
    }

    It 'rejects corrupt recovery intent before interrupting BGP' {
        Set-Content $script:checkpoint '{"Version":99}'
        { Begin-CalicoBGPSessionTransition $script:checkpoint } | Should -Throw '*invalid BGP*'
        $script:stops | Should -Be 0
        Test-Path $script:checkpoint | Should -BeTrue
    }

    It 'leaves ordinary ADD without a checkpoint alone' {
        $script:queryFails=$true
        { Complete-CalicoBGPSessionTransition $script:checkpoint } | Should -Not -Throw
        $script:starts | Should -Be 0
    }
}
