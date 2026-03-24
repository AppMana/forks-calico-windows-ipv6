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

# Return Null if no action is taken. Otherwise return action logs.
FUNCTION ProcessBgpBlocks ($Blocks)
{
    $current_blocks = (Get-BgpCustomRoute).Network
    $unused_blocks = [System.Collections.ArrayList]$current_blocks

    foreach ($block in $Blocks)
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

# Implement keepOriginalNextHop for eBGP peers on Windows.
#
# Windows RRAS BGP always rewrites next-hop to self for eBGP advertisements.
# BIRD on Linux has "next hop keep;" but RRAS has no equivalent global setting.
# Workaround: create per-prefix egress routing policies with Add-BgpRoutingPolicy
# that set -NewNextHop to the original next-hop from the iBGP RIB.
#
# See: https://github.com/projectcalico/calico/issues/12208
FUNCTION ProcessBgpNextHopPolicies ($Peerings, $LocalAsn)
{
    # Find eBGP peers that have keepOriginalNextHop set.
    $ebgpPeersWithKeepNH = @()
    foreach ($peering in $Peerings)
    {
        if (-not $peering.Name) { continue }
        if ($peering.AS -eq $LocalAsn) { continue }
        if ($peering.KeepOriginalNextHop -eq $true)
        {
            $ebgpPeersWithKeepNH += $peering.Name
        }
    }

    if ($ebgpPeersWithKeepNH.Count -eq 0)
    {
        # No eBGP peers with keepOriginalNextHop. Clean up any stale policies.
        Get-BgpRoutingPolicy -ErrorAction SilentlyContinue | Where-Object { $_.PolicyName -like "KeepNH_*" } | ForEach-Object {
            Remove-BgpRoutingPolicy -Name $_.PolicyName -Force
            Write-Output "Removed stale policy $($_.PolicyName)"
        }
        return
    }

    # Get routes learned from iBGP mesh peers (these have the original next-hops).
    # Skip routes with duplicate prefixes (e.g. service CIDR advertised by many nodes)
    # since a routing policy name must be unique per prefix.
    $routes = Get-BgpRouteInformation -ErrorAction SilentlyContinue | Where-Object { $_.LearnedFromPeer -like "Mesh_*" }
    $seenPrefixes = @{}
    $uniqueRoutes = @()
    foreach ($route in $routes)
    {
        if (-not $seenPrefixes.ContainsKey($route.Network))
        {
            $seenPrefixes[$route.Network] = $true
            $uniqueRoutes += $route
        }
    }

    # Build desired policy set.
    $existingPolicies = @{}
    Get-BgpRoutingPolicy -ErrorAction SilentlyContinue | Where-Object { $_.PolicyName -like "KeepNH_*" } | ForEach-Object {
        $existingPolicies[$_.PolicyName] = $_
    }

    $desiredPolicies = @{}
    foreach ($route in $uniqueRoutes)
    {
        $safeName = $route.Network -replace "[/.:]+", "_"
        $policyName = "KeepNH_$safeName"
        $desiredPolicies[$policyName] = @{ Prefix = $route.Network; NextHop = $route.NextHop }

        if ($existingPolicies.ContainsKey($policyName))
        {
            # Policy exists. Check if the next-hop changed.
            $existing = $existingPolicies[$policyName]
            if ($existing.NewNextHop -ne $route.NextHop)
            {
                Set-BgpRoutingPolicy -Name $policyName -NewNextHop $route.NextHop -Force
                Write-Output "Updated $policyName -> $($route.NextHop)"
            }
        }
        else
        {
            # New policy.
            Add-BgpRoutingPolicy -Name $policyName -PolicyType ModifyAttribute -MatchPrefix $route.Network -NewNextHop $route.NextHop
            foreach ($peerName in $ebgpPeersWithKeepNH)
            {
                Add-BgpRoutingPolicyForPeer -PeerName $peerName -PolicyName $policyName -Direction Egress -Force
            }
            Write-Output "Added $policyName ($($route.Network) -> $($route.NextHop))"
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

    Write-Output "Next-hop policies synced: $($desiredPolicies.Count) active"
}

Export-ModuleMember -Function ProcessBGPRouter
Export-ModuleMember -Function ProcessBGPBlocks
Export-ModuleMember -Function ProcessBGPPeers
Export-ModuleMember -Function ProcessBGPNextHopPolicies

