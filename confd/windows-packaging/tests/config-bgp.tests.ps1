# Pester tests for config-bgp.psm1 dual-stack and keepOriginalNextHop functionality.
# Runs on Linux (pwsh) or Windows using stub BGP cmdlets.

BeforeAll {
    # Import stubs first so they override any real cmdlets.
    Import-Module "$PSScriptRoot/bgp-stubs.psm1" -Force
    # Import the module under test.
    Import-Module "$PSScriptRoot/../config-bgp.psm1" -Force
}

Describe "ProcessBgpRouter" {
    BeforeEach { Reset-BgpStubs }

    It "creates a new BGP router" {
        ProcessBgpRouter -BgpId "10.2.0.3" -LocalAsn 65414
        $r = Get-BgpRouter
        $r.BgpIdentifier | Should -Be "10.2.0.3"
        $r.LocalASN | Should -Be 65414
    }

    It "replaces router with wrong ASN" {
        Add-BgpRouter -BgpIdentifier "10.2.0.3" -LocalASN 65000
        ProcessBgpRouter -BgpId "10.2.0.3" -LocalAsn 65414
        $r = Get-BgpRouter
        $r.LocalASN | Should -Be 65414
    }

    It "does nothing when router is correct" {
        Add-BgpRouter -BgpIdentifier "10.2.0.3" -LocalASN 65414
        $result = ProcessBgpRouter -BgpId "10.2.0.3" -LocalAsn 65414
        $result | Should -BeNullOrEmpty
    }
}

Describe "ProcessBgpRouterIPv6" {
    BeforeEach {
        Reset-BgpStubs
        Add-BgpRouter -BgpIdentifier "10.2.0.3" -LocalASN 65414
    }

    It "enables IPv6 routing with an address" {
        ProcessBgpRouterIPv6 -LocalIPv6 "fd00:10:2::3"
        $r = Get-BgpRouter
        $r.IPv6Routing | Should -Be "Enabled"
        $r.LocalIPv6Address | Should -Be "fd00:10:2::3"
    }

    It "does nothing when LocalIPv6 is empty" {
        ProcessBgpRouterIPv6 -LocalIPv6 ""
        $r = Get-BgpRouter
        $r.IPv6Routing | Should -Be "Disabled"
    }

    It "does nothing when LocalIPv6 is null" {
        ProcessBgpRouterIPv6 -LocalIPv6 $null
        $r = Get-BgpRouter
        $r.IPv6Routing | Should -Be "Disabled"
    }
}

Describe "ProcessBgpBlocks" {
    BeforeEach { Reset-BgpStubs }

    It "adds IPv4 blocks" {
        ProcessBgpBlocks -Blocks @("10.3.48.192/26", "") -BlocksV6 @("")
        (Get-BgpCustomRoute).Network | Should -Contain "10.3.48.192/26"
        (Get-BgpCustomRoute).Network | Should -Not -Contain ""
    }

    It "adds both IPv4 and IPv6 blocks" {
        ProcessBgpBlocks -Blocks @("10.3.48.192/26", "") -BlocksV6 @("fd00:10:3::/64", "")
        $routes = (Get-BgpCustomRoute).Network
        $routes | Should -Contain "10.3.48.192/26"
        $routes | Should -Contain "fd00:10:3::/64"
    }

    It "removes stale blocks" {
        Add-BgpCustomRoute -Network "10.99.0.0/26"
        ProcessBgpBlocks -Blocks @("10.3.48.192/26", "") -BlocksV6 @("")
        $routes = (Get-BgpCustomRoute).Network
        $routes | Should -Contain "10.3.48.192/26"
        $routes | Should -Not -Contain "10.99.0.0/26"
    }

    It "handles IPv6-only blocks" {
        ProcessBgpBlocks -Blocks @("") -BlocksV6 @("fd00:10:3::/64", "")
        (Get-BgpCustomRoute).Network | Should -Contain "fd00:10:3::/64"
    }

    It "keeps existing block that is still desired" {
        Add-BgpCustomRoute -Network "10.3.48.192/26"
        ProcessBgpBlocks -Blocks @("10.3.48.192/26", "fd00:10:3::/64", "") -BlocksV6 @("")
        $routes = (Get-BgpCustomRoute).Network
        $routes | Should -Contain "10.3.48.192/26"
        $routes | Should -Contain "fd00:10:3::/64"
    }

    It "handles null BlocksV6" {
        ProcessBgpBlocks -Blocks @("10.3.48.192/26", "") -BlocksV6 $null
        (Get-BgpCustomRoute).Network | Should -Contain "10.3.48.192/26"
    }
}

Describe "ProcessBgpPeers" {
    BeforeEach { Reset-BgpStubs }

    It "adds new peers" {
        $peerings = @(
            @{ Name = "Mesh_10_2_0_4"; IP = "10.2.0.4"; AS = 65414 },
            @{ Name = "Global_10_2_0_1"; IP = "10.2.0.1"; AS = 65000 },
            @{}
        )
        ProcessBgpPeers -Peerings $peerings -LocalIp "10.2.0.3"
        $peers = Get-BgpPeer
        $peers.Count | Should -Be 2
        ($peers | Where-Object PeerName -eq "Mesh_10_2_0_4").PeerIPAddress | Should -Be "10.2.0.4"
    }

    It "removes unused peers" {
        Add-BgpPeer -Name "Old_Peer" -LocalIPAddress "10.2.0.3" -PeerIPAddress "10.2.0.99" -PeerASN 65414
        ProcessBgpPeers -Peerings @(@{}) -LocalIp "10.2.0.3"
        (Get-BgpPeer).Count | Should -Be 0
    }

    It "updates changed peers" {
        Add-BgpPeer -Name "Mesh_10_2_0_4" -LocalIPAddress "10.2.0.3" -PeerIPAddress "10.2.0.4" -PeerASN 65000
        $peerings = @(
            @{ Name = "Mesh_10_2_0_4"; IP = "10.2.0.4"; AS = 65414 },
            @{}
        )
        ProcessBgpPeers -Peerings $peerings -LocalIp "10.2.0.3"
        $peer = Get-BgpPeer | Where-Object PeerName -eq "Mesh_10_2_0_4"
        $peer.PeerASN | Should -Be 65414
    }
}

Describe "ProcessBgpNextHopPolicies" {
    BeforeEach { Reset-BgpStubs }

    It "does nothing with no eBGP peers" {
        $peerings = @(
            @{ Name = "Mesh_10_2_0_4"; IP = "10.2.0.4"; AS = 65414 },
            @{}
        )
        ProcessBgpNextHopPolicies -Peerings $peerings -LocalAsn 65414
        (Get-BgpRoutingPolicy).Count | Should -Be 0
    }

    It "creates policies for eBGP peers with KeepOriginalNextHop" {
        # Add route info (simulating iBGP learned routes)
        Add-BgpRouteInformation -Network "10.3.5.192/26" -NextHop "10.2.0.60" -LearnedFromPeer "Mesh_10_2_0_60"
        Add-BgpRouteInformation -Network "10.3.9.128/26" -NextHop "10.2.0.4" -LearnedFromPeer "Mesh_10_2_0_4"

        $peerings = @(
            @{ Name = "Mesh_10_2_0_4"; IP = "10.2.0.4"; AS = 65414 },
            @{ Name = "Global_10_2_0_1"; IP = "10.2.0.1"; AS = 65000; KeepOriginalNextHop = $true },
            @{}
        )
        ProcessBgpNextHopPolicies -Peerings $peerings -LocalAsn 65414

        $policies = Get-BgpRoutingPolicy
        $policies.Count | Should -Be 2
        ($policies | Where-Object PolicyName -eq "KeepNH_10_3_5_192_26").NewNextHop | Should -Be "10.2.0.60"
        ($policies | Where-Object PolicyName -eq "KeepNH_10_3_9_128_26").NewNextHop | Should -Be "10.2.0.4"
    }

    It "handles IPv6 routes in policy names" {
        Add-BgpRouteInformation -Network "fd00:10:3::/64" -NextHop "fd00:10:2::4" -LearnedFromPeer "Mesh_10_2_0_4"

        $peerings = @(
            @{ Name = "Global_10_2_0_1"; IP = "10.2.0.1"; AS = 65000; KeepOriginalNextHop = $true },
            @{}
        )
        ProcessBgpNextHopPolicies -Peerings $peerings -LocalAsn 65414

        $policies = Get-BgpRoutingPolicy
        $policies.Count | Should -Be 1
        # Colons and slashes in IPv6 are replaced with underscores.
        $policies[0].PolicyName | Should -BeLike "KeepNH_fd00_10_3*"
    }

    It "removes stale policies" {
        Add-BgpRoutingPolicy -Name "KeepNH_10_99_0_0_26" -PolicyType "ModifyAttribute" -MatchPrefix "10.99.0.0/26" -NewNextHop "10.2.0.99"

        $peerings = @(
            @{ Name = "Global_10_2_0_1"; IP = "10.2.0.1"; AS = 65000; KeepOriginalNextHop = $true },
            @{}
        )
        # No routes from mesh peers, so no desired policies.
        ProcessBgpNextHopPolicies -Peerings $peerings -LocalAsn 65414

        (Get-BgpRoutingPolicy).Count | Should -Be 0
    }

    It "cleans up all policies when no eBGP peers have KeepOriginalNextHop" {
        Add-BgpRoutingPolicy -Name "KeepNH_10_99_0_0_26" -PolicyType "ModifyAttribute" -MatchPrefix "10.99.0.0/26" -NewNextHop "10.2.0.99"

        $peerings = @(
            @{ Name = "Global_10_2_0_1"; IP = "10.2.0.1"; AS = 65000; KeepOriginalNextHop = $false },
            @{}
        )
        ProcessBgpNextHopPolicies -Peerings $peerings -LocalAsn 65414

        (Get-BgpRoutingPolicy).Count | Should -Be 0
    }

    It "updates existing policy when next-hop changes" {
        # Pre-create a policy with old next-hop.
        Add-BgpRoutingPolicy -Name "KeepNH_10_3_5_192_26" -PolicyType "ModifyAttribute" -MatchPrefix "10.3.5.192/26" -NewNextHop "10.2.0.99"
        # Route now has a different next-hop.
        Add-BgpRouteInformation -Network "10.3.5.192/26" -NextHop "10.2.0.60" -LearnedFromPeer "Mesh_10_2_0_60"

        $peerings = @(
            @{ Name = "Global_10_2_0_1"; IP = "10.2.0.1"; AS = 65000; KeepOriginalNextHop = $true },
            @{}
        )
        ProcessBgpNextHopPolicies -Peerings $peerings -LocalAsn 65414

        $pol = Get-BgpRoutingPolicy | Where-Object PolicyName -eq "KeepNH_10_3_5_192_26"
        $pol | Should -Not -BeNullOrEmpty
        $pol.NewNextHop | Should -Be "10.2.0.60"
    }

    It "deduplicates routes by prefix" {
        # Same prefix from two different mesh peers.
        Add-BgpRouteInformation -Network "10.152.0.0/16" -NextHop "10.2.0.4" -LearnedFromPeer "Mesh_10_2_0_4"
        Add-BgpRouteInformation -Network "10.152.0.0/16" -NextHop "10.2.0.60" -LearnedFromPeer "Mesh_10_2_0_60"

        $peerings = @(
            @{ Name = "Global_10_2_0_1"; IP = "10.2.0.1"; AS = 65000; KeepOriginalNextHop = $true },
            @{}
        )
        ProcessBgpNextHopPolicies -Peerings $peerings -LocalAsn 65414

        # Only one policy for the prefix (first route wins).
        (Get-BgpRoutingPolicy).Count | Should -Be 1
    }
}
