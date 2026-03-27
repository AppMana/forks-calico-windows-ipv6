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
            # No action is taken. Nothing returned.
            return
        }
    }

    # Add BGP router with the desired ID and AS number.
    Add-BgpRouter -BgpIdentifier $BgpId -LocalASN $localAsn
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

        $done = $False

        foreach ($current_peer in $current_peers)
        {
            if ($current_peer.PeerName -eq $peering.Name)
            {

                if (($current_peer.LocalIPAddress -eq $LocalIp) -And ($current_peer.PeerIPAddress -eq $peering.IP) -And ($current_peer.PeerASN -eq $peering.AS))
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
        Write-Output "Adding peer ", $peering.Name
        Add-BgpPeer -Name $peering.Name -LocalIPAddress $LocalIp -PeerIPAddress $peering.IP -PeerASN $peering.AS
    }
}

# Prevent RRAS from re-advertising mesh-learned routes to eBGP peers.
#
# RRAS re-advertises all learned routes (including iBGP mesh routes) to eBGP
# peers with itself as next-hop. This creates routing loops because VyOS
# receives the same prefix from multiple nodes with wrong next-hops.
#
# Fix: add a Deny egress policy that blocks all mesh-learned routes from
# being advertised to eBGP peers. Only locally-originated routes (the
# node's own IPAM blocks, handled by SetNH4_/SetNH6_ policies) are
# advertised.
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

    # Clean up old KeepNH_ policies (replaced by DenyMeshEgress).
    Get-BgpRoutingPolicy -ErrorAction SilentlyContinue | Where-Object { $_.PolicyName -like "KeepNH_*" } | ForEach-Object {
        Remove-BgpRoutingPolicy -Name $_.PolicyName -Force
        Write-Output "Removed legacy policy $($_.PolicyName)"
    }

    if ($ebgpPeers.Count -eq 0)
    {
        # No eBGP peers with keepOriginalNextHop. Clean up DenyMeshEgress if it exists.
        $existing = Get-BgpRoutingPolicy -Name "DenyMeshEgress" -ErrorAction SilentlyContinue
        if ($existing) {
            Remove-BgpRoutingPolicy -Name "DenyMeshEgress" -Force
            Write-Output "Removed DenyMeshEgress (keepOriginalNextHop not set)"
        }
        return
    }

    # Collect all mesh-learned prefixes to deny on egress.
    $meshRoutes = Get-BgpRouteInformation -ErrorAction SilentlyContinue | Where-Object { $_.LearnedFromPeer -like "Mesh_*" }
    $meshPrefixes = @()
    $seen = @{}
    foreach ($route in $meshRoutes)
    {
        if (-not $seen.ContainsKey($route.Network))
        {
            $seen[$route.Network] = $true
            $meshPrefixes += $route.Network
        }
    }

    if ($meshPrefixes.Count -eq 0)
    {
        # No mesh routes yet (BGP still converging). Remove policy if exists.
        $existing = Get-BgpRoutingPolicy -Name "DenyMeshEgress" -ErrorAction SilentlyContinue
        if ($existing) {
            Remove-BgpRoutingPolicy -Name "DenyMeshEgress" -Force
            Write-Output "Removed DenyMeshEgress (no mesh routes)"
        }
        return
    }

    # Create or update the deny policy.
    # Always remove and re-add because Set-BgpRoutingPolicy silently drops
    # IPv6 prefixes when updating the MatchPrefix list.
    $existing = Get-BgpRoutingPolicy -Name "DenyMeshEgress" -ErrorAction SilentlyContinue
    $needsUpdate = $true
    if ($existing)
    {
        $currentPrefixes = @($existing.MatchPrefix) | Sort-Object
        $desiredPrefixes = @($meshPrefixes) | Sort-Object
        if ($currentPrefixes.Count -eq $desiredPrefixes.Count -and (Compare-Object $currentPrefixes $desiredPrefixes -SyncWindow 0).Count -eq 0)
        {
            $needsUpdate = $false
        }
    }

    if ($needsUpdate)
    {
        # Add the updated policy under a new name first, apply it to all
        # eBGP peers, then remove the old one. This ensures there is no
        # window where mesh routes leak.
        Add-BgpRoutingPolicy -Name "DenyMeshEgress_v2" -PolicyType Deny -MatchPrefix $meshPrefixes
        foreach ($peerName in $ebgpPeers)
        {
            Add-BgpRoutingPolicyForPeer -PeerName $peerName -PolicyName "DenyMeshEgress_v2" -Direction Egress -Force
        }
        # Now safe to remove the old policy.
        if ($existing)
        {
            Remove-BgpRoutingPolicy -Name "DenyMeshEgress" -Force
        }
        # Swap: create final name, apply, remove temp.
        Add-BgpRoutingPolicy -Name "DenyMeshEgress" -PolicyType Deny -MatchPrefix $meshPrefixes
        foreach ($peerName in $ebgpPeers)
        {
            Add-BgpRoutingPolicyForPeer -PeerName $peerName -PolicyName "DenyMeshEgress" -Direction Egress -Force
        }
        Remove-BgpRoutingPolicy -Name "DenyMeshEgress_v2" -Force
        Write-Output "Updated DenyMeshEgress with $($meshPrefixes.Count) prefixes (IPv4+IPv6)"
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
