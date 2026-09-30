$ErrorActionPreference = 'Stop'
# The isolated fixture has no default route. HNS/VFP can only process a host
# ClusterIP packet if Windows first resolves a reachable next-hop neighbor.
# An on-link route instead ARPs for the virtual service IP, which has no owner.
# Never add WAN access, modify the default route, or replace a conflicting route.
$addresses = @(Get-NetIPAddress -AddressFamily IPv4 -IPAddress 192.0.2.20)
if ($addresses.Count -ne 1) { throw 'expected one isolated Windows management address' }
$index = $addresses[0].InterfaceIndex
$prefix = '10.96.0.0/12'
$gateway = '192.0.2.10'
$existing = @{}
foreach ($store in @('PersistentStore', 'ActiveStore')) {
    $routes = @(Get-NetRoute -DestinationPrefix $prefix -PolicyStore $store -ErrorAction SilentlyContinue)
    if (@($routes | Where-Object { $_.InterfaceIndex -ne $index -or $_.NextHop -ne $gateway }).Count) {
        throw "conflicting $store service route; refusing to replace it"
    }
    $existing[$store] = $routes
}
# New-NetRoute explicitly rejects -PolicyStore PersistentStore. Its default
# writes both stores; -PolicyStore is only supported for ActiveStore creation.
# https://learn.microsoft.com/powershell/module/nettcpip/new-netroute
if (!$existing['PersistentStore'].Count) {
    New-NetRoute -DestinationPrefix $prefix -InterfaceIndex $index -NextHop $gateway | Out-Null
} elseif (!$existing['ActiveStore'].Count) {
    New-NetRoute -DestinationPrefix $prefix -InterfaceIndex $index -NextHop $gateway -PolicyStore ActiveStore | Out-Null
}
foreach ($store in @('PersistentStore', 'ActiveStore')) {
    $routes = @(Get-NetRoute -DestinationPrefix $prefix -PolicyStore $store -ErrorAction Stop)
    if ($routes.Count -ne 1 -or $routes[0].InterfaceIndex -ne $index -or $routes[0].NextHop -ne $gateway) {
        throw "service route not established in $store"
    }
}
Write-Output "WINDOWS_SERVICE_ROUTE_READY:$index`:$prefix"
