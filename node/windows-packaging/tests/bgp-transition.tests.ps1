BeforeAll {
    . "$PSScriptRoot/../../../libcalico-go/lib/winutils/bgp_transition.ps1"
    function Get-Service {
        if ($script:serviceQueryFails) { throw 'service query failed' }
        [pscustomobject]@{Name='RemoteAccess';Status=$script:serviceStatus}
    }
    function Get-NetIPAddress { [pscustomobject]@{IPAddress='fd00:10::20'} }
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
        ($script:peers | Where-Object PeerName -eq $Name).ConnectivityStatus='Connecting'
    }
}

Describe 'Planned HNS BGP session transition' {
    BeforeEach {
        $script:checkpoint=Join-Path $TestDrive 'bgp.json'
        Remove-Item $script:checkpoint -ErrorAction SilentlyContinue
        $script:serviceStatus='Running'
        $script:serviceQueryFails=$false
        $script:queryFails=$false; $script:stopFails=$false; $script:startFails=$false
        $script:stops=0; $script:starts=0
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
        $script:startFails=$false
        Complete-CalicoBGPSessionTransition $script:checkpoint
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
