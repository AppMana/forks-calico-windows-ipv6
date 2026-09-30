$ErrorActionPreference = 'Stop'
# Observation only: a passing workload must not hide loss of configured host
# routes during HNS adapter transitions. Never repair the state under test.
$addresses = @(Get-NetIPAddress -AddressFamily IPv4 -IPAddress 192.0.2.20)
if ($addresses.Count -ne 1) { throw 'expected one isolated Windows management address' }
$index = $addresses[0].InterfaceIndex
foreach ($store in @('ActiveStore', 'PersistentStore')) {
    $routes = @(Get-NetRoute -DestinationPrefix '10.96.0.0/12' -PolicyStore $store -ErrorAction SilentlyContinue |
        Where-Object { $_.InterfaceIndex -eq $index })
    if ($routes.Count -ne 1 -or $routes[0].NextHop -ne '192.0.2.10') {
        throw "configured service route lost from $store on management interface $index"
    }
}
Write-Output "WINDOWS_SERVICE_ROUTE_PRESERVED:$index"
