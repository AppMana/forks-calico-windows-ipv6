param(
    [Parameter(Mandatory=$true)][ValidateSet('Capture','Verify')][string]$Mode,
    [Parameter(Mandatory=$true)][string]$ExpectedScriptSHA256,
    [string]$EvidencePath = 'C:\LabQualification\route-transition-before.json'
)
$ErrorActionPreference = 'Stop'
# Read-only route oracle for the isolated fixture's separately seeded witness.
# Neither mode restores routes, modifies HNS, nor accepts an old-interface route.
$hash = (Get-FileHash 'C:\CalicoWindows\node-service.ps1' -Algorithm SHA256).Hash
if ($hash -ne $ExpectedScriptSHA256) { throw 'installed Calico script digest mismatch' }
$addresses = @(Get-NetIPAddress -AddressFamily IPv4 -IPAddress 192.0.2.20)
if ($addresses.Count -ne 1) { throw 'management address is not unique' }
$index = $addresses[0].InterfaceIndex
foreach ($store in @('ActiveStore','PersistentStore')) {
    $routes = @(Get-NetRoute -DestinationPrefix '198.18.123.0/24' -PolicyStore $store |
        Where-Object { $_.InterfaceIndex -eq $index })
    if ($routes.Count -ne 1 -or $routes[0].NextHop -ne '192.0.2.10' -or $routes[0].RouteMetric -ne 123) {
        throw "witness route missing or changed in $store on interface $index"
    }
}
$boot = (Get-CimInstance Win32_OperatingSystem).LastBootUpTime.ToUniversalTime().Ticks
$observed = [pscustomobject]@{Boot=$boot; InterfaceIndex=$index; ScriptSHA256=$hash; Prefix='198.18.123.0/24'; NextHop='192.0.2.10'; Metric=123}
if ($Mode -eq 'Capture') {
    if (Test-Path $EvidencePath) { throw 'refusing to overwrite pre-reboot evidence' }
    $observed | ConvertTo-Json | Set-Content -LiteralPath $EvidencePath
    'ROUTE_TRANSITION_CAPTURED'
} else {
    $before = Get-Content -Raw -LiteralPath $EvidencePath | ConvertFrom-Json
    if ($before.ScriptSHA256 -ne $hash) { throw 'candidate changed across reboot' }
    if ($boot -le $before.Boot) { throw 'new guest boot not proven' }
    'ROUTE_TRANSITION_REBOOT_PASSED'
}
$observed | ConvertTo-Json -Compress
