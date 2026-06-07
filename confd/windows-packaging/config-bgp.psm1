# Copyright (c) 2018-2020 Tigera, Inc. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


# Function module to config BGP

# Return Null if no action is taken. Otherwise return action logs.
FUNCTION ProcessBgpRouter ($BgpId, $LocalAsn)
{
    # Look for existing BGP router with the correct ID.
    $found = $True
    try
    {
        $router = Get-BgpRouter| Where-Object BgpIdentifier -eq $BgpId
    }
    catch
    {
        $ErrorMessage = $_.Exception.Message
        Write-Output "Get-BgpRouter error:", $ErrorMessage

        $found = $False
    }
    if ($found)
    {
        if ($router.LocalASN -ne $localAsn) {
            # An existing BGP router with the wrong ASN; remove it.
            Remove-BgpRouter -Force
            Write-Output "Remove existing BGP router"
        }
        else
        {
            if ($router.TransitRouting -ne "Enabled") {
                Set-BgpRouter -TransitRouting Enabled -Force
                Write-Output "Enable BGP transit routing"
            }
            # No action is taken. Nothing returned.
            return
        }
    }

    # Add BGP router with the desired ID and AS number.
    Add-BgpRouter -BgpIdentifier $BgpId -LocalASN $localAsn
    Set-BgpRouter -TransitRouting Enabled -Force
    Write-Output "Add BGP router"
}

# Enable IPv6 routing on the BGP router if the node has an IPv6 address.
FUNCTION ProcessBgpRouterIPv6 ($LocalIPv6)
{
    if (-not $LocalIPv6 -or $LocalIPv6 -eq "")
    {
        return
    }

    try
    {
        $router = Get-BgpRouter
        if ($router.IPv6Routing -ne "Enabled" -or $router.LocalIPv6Address -ne $LocalIPv6)
        {
            Set-BgpRouter -IPv6Routing Enabled -LocalIPv6Address $LocalIPv6 -Force
            Write-Output "Enabled IPv6 routing with local address $LocalIPv6"
        }
    }
    catch
    {
        Write-Output "Failed to enable IPv6 routing: $($_.Exception.Message)"
    }
}

# Return Null if no action is taken. Otherwise return action logs.
FUNCTION ProcessBgpBlocks ($Blocks, $BlocksV6)
{
    $allBlocks = @()
    if ($Blocks) { $allBlocks += $Blocks }
    if ($BlocksV6) { $allBlocks += $BlocksV6 }

    $current_blocks = (Get-BgpCustomRoute).Network
    $unused_blocks = [System.Collections.ArrayList]$current_blocks

    foreach ($block in $allBlocks)
    {
        if ($current_blocks -contains $block)
        {
            $unused_blocks.Remove($block)
            continue
        }
        if ($block -ne "")
        {
            Add-BgpCustomRoute -Network $block
            Write-Output "Add custom route", $block
        }
    }

    # Remove unused blocks
    foreach ($unused_block in $unused_blocks)
    {
        Remove-BgpCustomRoute -Network $unused_block -Force

        Write-Output "Remove unused block ", $unused_block
    }
}

# Return Null if no action is taken. Otherwise return action logs.
#
# A $peering hash may carry a LocalIP override (used for IPv6 mesh peers
# where the local socket bind address is the host's ULA). When LocalIP
# is empty, fall back to the function-level $LocalIp parameter (the
# IPv4 management IP, used for IPv4 peers and eBGP-to-VyOS).
FUNCTION ProcessBgpPeers ($Peerings, $LocalIp)
{
    $current_peers = @(Get-BgpPeer)
    $unused_peers = [System.Collections.ArrayList]$current_peers
    $new_peers = New-Object System.Collections.ArrayList

    # Add peerings. We try to minimize calling to BGP daemon.
    foreach ($peering in $Peerings)
    {
        if (-not $peering.Name)
        {
            continue
        }

        $effLocalIp = $LocalIp
        if ($peering.LocalIP) { $effLocalIp = $peering.LocalIP }

        $done = $False

        foreach ($current_peer in $current_peers)
        {
            if ($current_peer.PeerName -eq $peering.Name)
            {

                if (($current_peer.LocalIPAddress -eq $effLocalIp) -And ($current_peer.PeerIPAddress -eq $peering.IP) -And ($current_peer.PeerASN -eq $peering.AS))
                {
                    # Peer exists and identical
                    # Do nothing
                }
                else
                {
                    # Peer exists but differ
                    Remove-BgpPeer -Name $current_peer.PeerName -Force
                    # Defer the Add-BgpPeer call since it may conflict with another peering that we're about to
                    # delete.  For example if it is being renamed.
                    $new_peers.Add($peering)
                    Write-Output "Peering updated: ", $current_peer.PeerName
                }

                $done = $True

                # Remove this peer from unused.
                $unused_peers.Remove($current_peer)

                break
            }
        }

        if (-not $done)
        {
            Write-Output "New peering detected: ", $peering.Name
            # Defer the Add-BgpPeer call since it may conflict with another peering that we're about to
            # delete.  For example if it is being renamed.
            $new_peers.Add($peering)
        }
    }

    # Remove unused peerings first, in case a peering has been renamed.
    foreach ($unused_peer in $unused_peers)
    {
        Write-Output "Removing unused peer ", $unused_peer.PeerName
        Remove-BgpPeer -Name $unused_peer.PeerName -Force
    }

    foreach ($peering in $new_peers)
    {
        $effLocalIp = $LocalIp
        if ($peering.LocalIP) { $effLocalIp = $peering.LocalIP }
        Write-Output "Adding peer ", $peering.Name
        Add-BgpPeer -Name $peering.Name -LocalIPAddress $effLocalIp -PeerIPAddress $peering.IP -PeerASN $peering.AS
    }
}

# Prevent RRAS from re-advertising mesh-learned routes to eBGP peers.
#
# RRAS re-advertises all learned routes (including iBGP mesh routes) to eBGP
# peers with itself as next-hop. This creates routing loops because VyOS
# receives the same prefix from multiple nodes with wrong next-hops.
#
# Fix: Deny routes whose next-hop matches an iBGP mesh peer. Mesh-learned
# routes carry the originating node's IP as next-hop. Locally-originated
# custom routes (the node's own IPAM blocks) have no remote next-hop and
# pass through, getting correct next-hop via SetNH4_/SetNH6_ policies.
#
# Note: MatchPrefix "0.0.0.0/0" does NOT work as a wildcard in RRAS.
# A Deny with no MatchPrefix blocks everything including local blocks.
# Using -MatchNextHop with mesh peer IPs selectively blocks only
# mesh-learned routes.
FUNCTION ProcessBgpNextHopPolicies ($Peerings, $LocalAsn)
{
    # Find eBGP peers with keepOriginalNextHop set.
    $ebgpPeers = @()
    foreach ($peering in $Peerings)
    {
        if (-not $peering.Name) { continue }
        if ($peering.AS -eq $LocalAsn) { continue }
        if ($peering.KeepOriginalNextHop -eq $true)
        {
            $ebgpPeers += $peering.Name
        }
    }

    # Collect all iBGP mesh peer IPs (these are next-hops on mesh-learned
    # routes). For IPv6 mesh peers, the peer's ULA is the next-hop on
    # IPv6 NLRI received from it, so include those too — otherwise Win
    # would re-advertise mesh-learned IPv6 routes to VyOS with the
    # original ULA next-hop, causing routing loops the same way IPv4
    # mesh routes did.
    $meshNextHops = @()
    foreach ($peering in $Peerings)
    {
        if (-not $peering.Name) { continue }
        if ($peering.AS -ne $LocalAsn) { continue }
        if ($peering.IP) { $meshNextHops += $peering.IP }
    }

    # Clean up old KeepNH_ policies (replaced by DenyMeshEgress).
    Get-BgpRoutingPolicy -ErrorAction SilentlyContinue | Where-Object { $_.PolicyName -like "KeepNH_*" } | ForEach-Object {
        Remove-BgpRoutingPolicy -Name $_.PolicyName -Force
        Write-Output "Removed legacy policy $($_.PolicyName)"
    }

    if ($ebgpPeers.Count -eq 0 -or $meshNextHops.Count -eq 0)
    {
        $existing = Get-BgpRoutingPolicy -Name "DenyMeshEgress" -ErrorAction SilentlyContinue
        if ($existing) {
            Remove-BgpRoutingPolicy -Name "DenyMeshEgress" -Force
            Write-Output "Removed DenyMeshEgress (no eBGP peers or no mesh peers)"
        }
        return
    }

    # Check if existing policy matches current mesh peer list.
    $existing = Get-BgpRoutingPolicy -Name "DenyMeshEgress" -ErrorAction SilentlyContinue
    $needsRecreate = $false
    if (-not $existing)
    {
        $needsRecreate = $true
    }
    else
    {
        # Recreate if MatchNextHop list changed or if using old MatchPrefix style.
        $currentNH = @()
        if ($existing.MatchNextHop) { $currentNH = @($existing.MatchNextHop | ForEach-Object { $_.ToString() }) }
        $desiredNH = @($meshNextHops | Sort-Object)
        $currentNH = @($currentNH | Sort-Object)
        if ($existing.MatchPrefix -and $existing.MatchPrefix.Count -gt 0)
        {
            $needsRecreate = $true
            Write-Output "Removing old MatchPrefix-based DenyMeshEgress"
        }
        elseif (-not $existing.MatchNextHop -or $existing.MatchNextHop.Count -eq 0)
        {
            # Old true-deny-all style (no match criteria). Recreate with MatchNextHop.
            $needsRecreate = $true
            Write-Output "Removing old deny-all DenyMeshEgress (blocked local blocks)"
        }
        elseif (Compare-Object $currentNH $desiredNH)
        {
            $needsRecreate = $true
            Write-Output "Mesh peer list changed, updating DenyMeshEgress"
        }
    }

    if ($needsRecreate)
    {
        # Remove old policy first.
        if ($existing) {
            Remove-BgpRoutingPolicy -Name "DenyMeshEgress" -Force
        }
        Add-BgpRoutingPolicy -Name "DenyMeshEgress" -PolicyType Deny -MatchNextHop $meshNextHops -Force
        foreach ($peerName in $ebgpPeers)
        {
            Add-BgpRoutingPolicyForPeer -PeerName $peerName -PolicyName "DenyMeshEgress" -Direction Egress -Force
        }
        Write-Output "Added DenyMeshEgress (deny mesh next-hops: $($meshNextHops.Count) peers)"
    }
}

# Set the BGP next-hop for locally-originated IPv6 custom routes to the
# node's SLAAC address.  Without this, RRAS uses the Calico_ep interface
# address (from the pod subnet) as the next-hop, which is unreachable from
# the VyOS router.
FUNCTION ProcessBgpIPv6NextHopPolicies ($Peerings, $LocalAsn, $LocalIPv6, $BlocksV6)
{
    if (-not $LocalIPv6 -or $LocalIPv6 -eq "")
    {
        return
    }

    # Find eBGP peers.
    $ebgpPeers = @()
    foreach ($peering in $Peerings)
    {
        if (-not $peering.Name) { continue }
        if ($peering.AS -eq $LocalAsn) { continue }
        $ebgpPeers += $peering.Name
    }

    if ($ebgpPeers.Count -eq 0) { return }

    $existingPolicies = @{}
    Get-BgpRoutingPolicy -ErrorAction SilentlyContinue | Where-Object { $_.PolicyName -like "SetNH6_*" } | ForEach-Object {
        $existingPolicies[$_.PolicyName] = $_
    }

    $desiredPolicies = @{}
    foreach ($block in $BlocksV6)
    {
        if (-not $block -or $block -eq "") { continue }
        $safeName = $block -replace "[/.:]+", "_"
        $policyName = "SetNH6_$safeName"
        $desiredPolicies[$policyName] = @{ Prefix = $block; NextHop = $LocalIPv6 }

        if ($existingPolicies.ContainsKey($policyName))
        {
            $existing = $existingPolicies[$policyName]
            if ($existing.NewNextHop -ne $LocalIPv6)
            {
                Set-BgpRoutingPolicy -Name $policyName -NewNextHop $LocalIPv6 -Force
                Write-Output "Updated $policyName -> $LocalIPv6"
            }
        }
        else
        {
            Add-BgpRoutingPolicy -Name $policyName -PolicyType ModifyAttribute -MatchPrefix $block -NewNextHop $LocalIPv6
            foreach ($peerName in $ebgpPeers)
            {
                Add-BgpRoutingPolicyForPeer -PeerName $peerName -PolicyName $policyName -Direction Egress -Force
            }
            Write-Output "Added $policyName ($block -> $LocalIPv6)"
        }
    }

    # Remove stale policies.
    foreach ($name in @($existingPolicies.Keys))
    {
        if (-not $desiredPolicies.ContainsKey($name))
        {
            Remove-BgpRoutingPolicy -Name $name -Force
            Write-Output "Removed stale $name"
        }
    }
}

# Set the BGP next-hop for locally-originated IPv4 custom routes to the
# node's management IP.  Without this, RRAS rewrites next-hop to self for
# eBGP advertisements, and with multipath-relax on VyOS, re-advertised
# mesh routes create ECMP to the wrong node.
FUNCTION ProcessBgpIPv4NextHopPolicies ($Peerings, $LocalAsn, $LocalIp, $Blocks)
{
    if (-not $LocalIp -or $LocalIp -eq "")
    {
        return
    }

    $ebgpPeers = @()
    foreach ($peering in $Peerings)
    {
        if (-not $peering.Name) { continue }
        if ($peering.AS -eq $LocalAsn) { continue }
        $ebgpPeers += $peering.Name
    }

    if ($ebgpPeers.Count -eq 0) { return }

    $existingPolicies = @{}
    Get-BgpRoutingPolicy -ErrorAction SilentlyContinue | Where-Object { $_.PolicyName -like "SetNH4_*" } | ForEach-Object {
        $existingPolicies[$_.PolicyName] = $_
    }

    $desiredPolicies = @{}
    foreach ($block in $Blocks)
    {
        if (-not $block -or $block -eq "") { continue }
        $safeName = $block -replace "[/.:]+", "_"
        $policyName = "SetNH4_$safeName"
        $desiredPolicies[$policyName] = @{ Prefix = $block; NextHop = $LocalIp }

        if ($existingPolicies.ContainsKey($policyName))
        {
            $existing = $existingPolicies[$policyName]
            if ($existing.NewNextHop -ne $LocalIp)
            {
                Set-BgpRoutingPolicy -Name $policyName -NewNextHop $LocalIp -Force
                Write-Output "Updated $policyName -> $LocalIp"
            }
        }
        else
        {
            Add-BgpRoutingPolicy -Name $policyName -PolicyType ModifyAttribute -MatchPrefix $block -NewNextHop $LocalIp
            foreach ($peerName in $ebgpPeers)
            {
                Add-BgpRoutingPolicyForPeer -PeerName $peerName -PolicyName $policyName -Direction Egress -Force
            }
            Write-Output "Added $policyName ($block -> $LocalIp)"
        }
    }

    foreach ($name in @($existingPolicies.Keys))
    {
        if (-not $desiredPolicies.ContainsKey($name))
        {
            Remove-BgpRoutingPolicy -Name $name -Force
            Write-Output "Removed stale $name"
        }
    }
}

Export-ModuleMember -Function ProcessBGPRouter
Export-ModuleMember -Function ProcessBGPRouterIPv6
Export-ModuleMember -Function ProcessBGPBlocks
Export-ModuleMember -Function ProcessBGPPeers
Export-ModuleMember -Function ProcessBGPNextHopPolicies
Export-ModuleMember -Function ProcessBGPIPv4NextHopPolicies
Export-ModuleMember -Function ProcessBGPIPv6NextHopPolicies
