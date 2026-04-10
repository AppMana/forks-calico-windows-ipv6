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
        ProcessBgpRouter -BgpId "192.0.2.1" -LocalAsn 64512
        $r = Get-BgpRouter
        $r.BgpIdentifier | Should -Be "192.0.2.1"
        $r.LocalASN | Should -Be 64512
    }

    It "replaces router with wrong ASN" {
        Add-BgpRouter -BgpIdentifier "192.0.2.1" -LocalASN 64501
        ProcessBgpRouter -BgpId "192.0.2.1" -LocalAsn 64512
        $r = Get-BgpRouter
        $r.LocalASN | Should -Be 64512
    }

    It "does nothing when router is correct" {
        Add-BgpRouter -BgpIdentifier "192.0.2.1" -LocalASN 64512
        $result = ProcessBgpRouter -BgpId "192.0.2.1" -LocalAsn 64512
        $result | Should -BeNullOrEmpty
    }
}

Describe "ProcessBgpRouterIPv6" {
    BeforeEach {
        Reset-BgpStubs
        Add-BgpRouter -BgpIdentifier "192.0.2.1" -LocalASN 64512
    }

    It "enables IPv6 routing with an address" {
        ProcessBgpRouterIPv6 -LocalIPv6 "2001:db8:2::3"
        $r = Get-BgpRouter
        $r.IPv6Routing | Should -Be "Enabled"
        $r.LocalIPv6Address | Should -Be "2001:db8:2::3"
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
        ProcessBgpBlocks -Blocks @("198.51.100.192/26", "") -BlocksV6 @("")
        (Get-BgpCustomRoute).Network | Should -Contain "198.51.100.192/26"
        (Get-BgpCustomRoute).Network | Should -Not -Contain ""
    }

    It "adds both IPv4 and IPv6 blocks" {
        ProcessBgpBlocks -Blocks @("198.51.100.192/26", "") -BlocksV6 @("2001:db8:3::/64", "")
        $routes = (Get-BgpCustomRoute).Network
        $routes | Should -Contain "198.51.100.192/26"
        $routes | Should -Contain "2001:db8:3::/64"
    }

    It "removes stale blocks" {
        Add-BgpCustomRoute -Network "203.0.113.0/26"
        ProcessBgpBlocks -Blocks @("198.51.100.192/26", "") -BlocksV6 @("")
        $routes = (Get-BgpCustomRoute).Network
        $routes | Should -Contain "198.51.100.192/26"
        $routes | Should -Not -Contain "203.0.113.0/26"
    }

    It "handles IPv6-only blocks" {
        ProcessBgpBlocks -Blocks @("") -BlocksV6 @("2001:db8:3::/64", "")
        (Get-BgpCustomRoute).Network | Should -Contain "2001:db8:3::/64"
    }

    It "keeps existing block that is still desired" {
        Add-BgpCustomRoute -Network "198.51.100.192/26"
        ProcessBgpBlocks -Blocks @("198.51.100.192/26", "2001:db8:3::/64", "") -BlocksV6 @("")
        $routes = (Get-BgpCustomRoute).Network
        $routes | Should -Contain "198.51.100.192/26"
        $routes | Should -Contain "2001:db8:3::/64"
    }

    It "handles null BlocksV6" {
        ProcessBgpBlocks -Blocks @("198.51.100.192/26", "") -BlocksV6 $null
        (Get-BgpCustomRoute).Network | Should -Contain "198.51.100.192/26"
    }
}

Describe "ProcessBgpPeers" {
    BeforeEach { Reset-BgpStubs }

    It "adds new peers" {
        $peerings = @(
            @{ Name = "Mesh_10_2_0_4"; IP = "192.0.2.4"; AS = 64512 },
            @{ Name = "Global_10_2_0_1"; IP = "198.51.100.1"; AS = 64501 },
            @{}
        )
        ProcessBgpPeers -Peerings $peerings -LocalIp "192.0.2.1"
        $peers = Get-BgpPeer
        $peers.Count | Should -Be 2
        ($peers | Where-Object PeerName -eq "Mesh_10_2_0_4").PeerIPAddress | Should -Be "192.0.2.4"
    }

    It "removes unused peers" {
        Add-BgpPeer -Name "Old_Peer" -LocalIPAddress "192.0.2.1" -PeerIPAddress "192.0.2.99" -PeerASN 64512
        ProcessBgpPeers -Peerings @(@{}) -LocalIp "192.0.2.1"
        (Get-BgpPeer).Count | Should -Be 0
    }

    It "updates changed peers" {
        Add-BgpPeer -Name "Mesh_10_2_0_4" -LocalIPAddress "192.0.2.1" -PeerIPAddress "192.0.2.4" -PeerASN 64501
        $peerings = @(
            @{ Name = "Mesh_10_2_0_4"; IP = "192.0.2.4"; AS = 64512 },
            @{}
        )
        ProcessBgpPeers -Peerings $peerings -LocalIp "192.0.2.1"
        $peer = Get-BgpPeer | Where-Object PeerName -eq "Mesh_10_2_0_4"
        $peer.PeerASN | Should -Be 64512
    }
}

Describe "ProcessBgpNextHopPolicies" {
    BeforeEach { Reset-BgpStubs }

    It "does nothing with no eBGP peers" {
        $peerings = @(
            @{ Name = "Mesh_10_2_0_4"; IP = "192.0.2.4"; AS = 64512 },
            @{}
        )
        ProcessBgpNextHopPolicies -Peerings $peerings -LocalAsn 64512
        (Get-BgpRoutingPolicy).Count | Should -Be 0
    }

    It "creates DenyMeshEgress matching mesh peer next-hops" {
        $peerings = @(
            @{ Name = "Mesh_10_2_0_4"; IP = "192.0.2.4"; AS = 64512 },
            @{ Name = "Mesh_10_2_0_60"; IP = "192.0.2.60"; AS = 64512 },
            @{ Name = "Global_10_2_0_1"; IP = "198.51.100.1"; AS = 64501; KeepOriginalNextHop = $true },
            @{}
        )
        ProcessBgpNextHopPolicies -Peerings $peerings -LocalAsn 64512

        $policies = Get-BgpRoutingPolicy
        $policies.Count | Should -Be 1
        $policies[0].PolicyName | Should -Be "DenyMeshEgress"
        $policies[0].PolicyType | Should -Be "Deny"
        $policies[0].MatchNextHop | Should -Contain "192.0.2.4"
        $policies[0].MatchNextHop | Should -Contain "192.0.2.60"
    }

    It "removes legacy KeepNH_ policies" {
        Add-BgpRoutingPolicy -Name "KeepNH_203_0_113_0_26" -PolicyType "ModifyAttribute" -MatchPrefix "203.0.113.0/26" -NewNextHop "192.0.2.99"

        $peerings = @(
            @{ Name = "Mesh_10_2_0_60"; IP = "192.0.2.60"; AS = 64512 },
            @{ Name = "Global_10_2_0_1"; IP = "198.51.100.1"; AS = 64501; KeepOriginalNextHop = $true },
            @{}
        )
        ProcessBgpNextHopPolicies -Peerings $peerings -LocalAsn 64512

        $policies = Get-BgpRoutingPolicy
        ($policies | Where-Object PolicyName -like "KeepNH_*").Count | Should -Be 0
        ($policies | Where-Object PolicyName -eq "DenyMeshEgress").Count | Should -Be 1
    }

    It "cleans up when no eBGP peers have KeepOriginalNextHop" {
        Add-BgpRoutingPolicy -Name "KeepNH_203_0_113_0_26" -PolicyType "ModifyAttribute" -MatchPrefix "203.0.113.0/26" -NewNextHop "192.0.2.99"
        Add-BgpRoutingPolicy -Name "DenyMeshEgress" -PolicyType "Deny" -MatchNextHop @("192.0.2.4")

        $peerings = @(
            @{ Name = "Mesh_10_2_0_4"; IP = "192.0.2.4"; AS = 64512 },
            @{ Name = "Global_10_2_0_1"; IP = "198.51.100.1"; AS = 64501; KeepOriginalNextHop = $false },
            @{}
        )
        ProcessBgpNextHopPolicies -Peerings $peerings -LocalAsn 64512

        (Get-BgpRoutingPolicy).Count | Should -Be 0
    }

    It "removes DenyMeshEgress when no mesh peers exist" {
        Add-BgpRoutingPolicy -Name "DenyMeshEgress" -PolicyType "Deny" -MatchNextHop @("192.0.2.4")

        $peerings = @(
            @{ Name = "Global_10_2_0_1"; IP = "198.51.100.1"; AS = 64501; KeepOriginalNextHop = $true },
            @{}
        )
        ProcessBgpNextHopPolicies -Peerings $peerings -LocalAsn 64512

        (Get-BgpRoutingPolicy).Count | Should -Be 0
    }

    It "updates DenyMeshEgress when mesh peer list changes" {
        Add-BgpRoutingPolicy -Name "DenyMeshEgress" -PolicyType "Deny" -MatchNextHop @("192.0.2.4")

        $peerings = @(
            @{ Name = "Mesh_10_2_0_4"; IP = "192.0.2.4"; AS = 64512 },
            @{ Name = "Mesh_10_2_0_60"; IP = "192.0.2.60"; AS = 64512 },
            @{ Name = "Global_10_2_0_1"; IP = "198.51.100.1"; AS = 64501; KeepOriginalNextHop = $true },
            @{}
        )
        ProcessBgpNextHopPolicies -Peerings $peerings -LocalAsn 64512

        $pol = Get-BgpRoutingPolicy | Where-Object PolicyName -eq "DenyMeshEgress"
        $pol | Should -Not -BeNullOrEmpty
        $pol.MatchNextHop | Should -Contain "192.0.2.4"
        $pol.MatchNextHop | Should -Contain "192.0.2.60"
    }
}

Describe "ProcessBgpIPv4NextHopPolicies" {
    BeforeEach { Reset-BgpStubs }

    It "creates policies for IPv4 blocks with node IP as next-hop" {
        $peerings = @(
            @{ Name = "Global_10_2_0_1"; IP = "198.51.100.1"; AS = 64501 },
            @{}
        )
        ProcessBgpIPv4NextHopPolicies -Peerings $peerings -LocalAsn 64512 -LocalIp "192.0.2.1" -Blocks @("198.51.100.192/26", "")
        $policies = Get-BgpRoutingPolicy | Where-Object { $_.PolicyName -like "SetNH4_*" }
        $policies.Count | Should -Be 1
        $policies[0].NewNextHop | Should -Be "192.0.2.1"
    }

    It "does nothing when no eBGP peers" {
        $peerings = @(
            @{ Name = "Mesh_10_2_0_4"; IP = "192.0.2.4"; AS = 64512 },
            @{}
        )
        ProcessBgpIPv4NextHopPolicies -Peerings $peerings -LocalAsn 64512 -LocalIp "192.0.2.1" -Blocks @("198.51.100.192/26")
        (Get-BgpRoutingPolicy).Count | Should -Be 0
    }

    It "removes stale policies" {
        Add-BgpRoutingPolicy -Name "SetNH4_old" -PolicyType "ModifyAttribute" -MatchPrefix "203.0.113.0/26" -NewNextHop "192.0.2.99"
        $peerings = @(
            @{ Name = "Global_10_2_0_1"; IP = "198.51.100.1"; AS = 64501 },
            @{}
        )
        ProcessBgpIPv4NextHopPolicies -Peerings $peerings -LocalAsn 64512 -LocalIp "192.0.2.1" -Blocks @("198.51.100.192/26", "")
        $policies = Get-BgpRoutingPolicy | Where-Object { $_.PolicyName -like "SetNH4_*" }
        $policies.Count | Should -Be 1
        $policies[0].PolicyName | Should -BeLike "SetNH4_198*"
    }
}

Describe "ProcessBgpIPv6NextHopPolicies" {
    BeforeEach { Reset-BgpStubs }

    It "creates policies for IPv6 blocks with SLAAC next-hop" {
        $peerings = @(
            @{ Name = "Global_10_2_0_1"; IP = "198.51.100.1"; AS = 64501 },
            @{}
        )
        ProcessBgpIPv6NextHopPolicies -Peerings $peerings -LocalAsn 64512 -LocalIPv6 "2001:db8:2::3" -BlocksV6 @("2001:db8:1::/122", "")
        $policies = Get-BgpRoutingPolicy | Where-Object { $_.PolicyName -like "SetNH6_*" }
        $policies.Count | Should -Be 1
        $policies[0].NewNextHop | Should -Be "2001:db8:2::3"
    }

    It "does nothing when LocalIPv6 is empty" {
        $peerings = @(
            @{ Name = "Global_10_2_0_1"; IP = "198.51.100.1"; AS = 64501 },
            @{}
        )
        ProcessBgpIPv6NextHopPolicies -Peerings $peerings -LocalAsn 64512 -LocalIPv6 "" -BlocksV6 @("2001:db8:1::/122")
        (Get-BgpRoutingPolicy).Count | Should -Be 0
    }

    It "does nothing when no eBGP peers" {
        $peerings = @(
            @{ Name = "Mesh_10_2_0_4"; IP = "192.0.2.4"; AS = 64512 },
            @{}
        )
        ProcessBgpIPv6NextHopPolicies -Peerings $peerings -LocalAsn 64512 -LocalIPv6 "2001:db8:2::3" -BlocksV6 @("2001:db8:1::/122")
        (Get-BgpRoutingPolicy).Count | Should -Be 0
    }

    It "removes stale policies" {
        Add-BgpRoutingPolicy -Name "SetNH6_old_block" -PolicyType "ModifyAttribute" -MatchPrefix "2001:db8:99::/122" -NewNextHop "2001:db8:2::99"
        $peerings = @(
            @{ Name = "Global_10_2_0_1"; IP = "198.51.100.1"; AS = 64501 },
            @{}
        )
        ProcessBgpIPv6NextHopPolicies -Peerings $peerings -LocalAsn 64512 -LocalIPv6 "2001:db8:2::3" -BlocksV6 @("2001:db8:1::/122", "")
        $policies = Get-BgpRoutingPolicy | Where-Object { $_.PolicyName -like "SetNH6_*" }
        $policies.Count | Should -Be 1
        $policies[0].PolicyName | Should -BeLike "SetNH6_2001*"
    }

    It "updates policy when SLAAC address changes" {
        Add-BgpRoutingPolicy -Name "SetNH6_2001_db8_1__122" -PolicyType "ModifyAttribute" -MatchPrefix "2001:db8:1::/122" -NewNextHop "2001:db8:2::old"
        $peerings = @(
            @{ Name = "Global_10_2_0_1"; IP = "198.51.100.1"; AS = 64501 },
            @{}
        )
        ProcessBgpIPv6NextHopPolicies -Peerings $peerings -LocalAsn 64512 -LocalIPv6 "2001:db8:2::new" -BlocksV6 @("2001:db8:1::/122", "")
        $pol = Get-BgpRoutingPolicy | Where-Object { $_.PolicyName -like "SetNH6_*" }
        $pol.NewNextHop | Should -Be "2001:db8:2::new"
    }
}
