# Stubs for Windows RRAS BGP cmdlets used by config-bgp.psm1.
# These simulate the real cmdlet behavior for testing on Linux.

$script:BgpRouter = $null
$script:BgpPeers = @()
$script:BgpCustomRoutes = @()
$script:BgpRoutingPolicies = @()
$script:BgpPolicyPeerBindings = @()
$script:BgpRouteInformation = @()

function Reset-BgpStubs {
    $script:BgpRouter = $null
    $script:BgpPeers = @()
    $script:BgpCustomRoutes = @()
    $script:BgpRoutingPolicies = @()
    $script:BgpPolicyPeerBindings = @()
    $script:BgpRouteInformation = @()
}

function Add-BgpRouteInformation {
    param($Network, $NextHop, $LearnedFromPeer)
    $script:BgpRouteInformation += [PSCustomObject]@{
        Network = $Network; NextHop = $NextHop; LearnedFromPeer = $LearnedFromPeer
    }
}

function Get-BgpRouter {
    if ($script:BgpRouter) { return $script:BgpRouter }
    throw "BGP router not configured"
}

function Add-BgpRouter {
    param([string]$BgpIdentifier, [uint32]$LocalASN)
    $script:BgpRouter = [PSCustomObject]@{
        BgpIdentifier = $BgpIdentifier; LocalASN = $LocalASN;
        IPv6Routing = "Disabled"; LocalIPv6Address = ""
    }
}

function Remove-BgpRouter {
    param([switch]$Force)
    $script:BgpRouter = $null
}

function Set-BgpRouter {
    param([string]$IPv6Routing, [string]$LocalIPv6Address)
    if (-not $script:BgpRouter) { throw "No BGP router" }
    if ($IPv6Routing) { $script:BgpRouter.IPv6Routing = $IPv6Routing }
    if ($LocalIPv6Address) { $script:BgpRouter.LocalIPv6Address = $LocalIPv6Address }
}

function Get-BgpPeer {
    # Always return as array to match real cmdlet behavior.
    return @($script:BgpPeers)
}

function Add-BgpPeer {
    param([string]$Name, [string]$LocalIPAddress, [string]$PeerIPAddress, [uint32]$PeerASN)
    $script:BgpPeers += [PSCustomObject]@{
        PeerName = $Name; LocalIPAddress = $LocalIPAddress;
        PeerIPAddress = $PeerIPAddress; PeerASN = $PeerASN
    }
}

function Remove-BgpPeer {
    param([string]$Name, [switch]$Force)
    $script:BgpPeers = @($script:BgpPeers | Where-Object { $_.PeerName -ne $Name })
}

function Get-BgpCustomRoute {
    return [PSCustomObject]@{ Network = $script:BgpCustomRoutes }
}

function Add-BgpCustomRoute {
    param([string]$Network)
    $script:BgpCustomRoutes += $Network
}

function Remove-BgpCustomRoute {
    param([string]$Network, [switch]$Force)
    $script:BgpCustomRoutes = @($script:BgpCustomRoutes | Where-Object { $_ -ne $Network })
}

function Get-BgpRoutingPolicy {
    return $script:BgpRoutingPolicies
}

function Add-BgpRoutingPolicy {
    param([string]$Name, [string]$PolicyType, [string]$MatchPrefix, [string]$NewNextHop)
    $script:BgpRoutingPolicies += [PSCustomObject]@{
        PolicyName = $Name; PolicyType = $PolicyType;
        MatchPrefix = @($MatchPrefix); NewNextHop = $NewNextHop
    }
}

function Set-BgpRoutingPolicy {
    param([string]$Name, [string]$NewNextHop, [switch]$Force)
    $pol = $script:BgpRoutingPolicies | Where-Object { $_.PolicyName -eq $Name }
    if ($pol) { $pol.NewNextHop = $NewNextHop }
}

function Remove-BgpRoutingPolicy {
    param([string]$Name, [switch]$Force)
    $script:BgpRoutingPolicies = @($script:BgpRoutingPolicies | Where-Object { $_.PolicyName -ne $Name })
}

function Add-BgpRoutingPolicyForPeer {
    param([string]$PeerName, [string]$PolicyName, [string]$Direction, [switch]$Force)
    $script:BgpPolicyPeerBindings += [PSCustomObject]@{
        PeerName = $PeerName; PolicyName = $PolicyName; Direction = $Direction
    }
}

function Get-BgpRouteInformation {
    return $script:BgpRouteInformation
}

Export-ModuleMember -Function *
