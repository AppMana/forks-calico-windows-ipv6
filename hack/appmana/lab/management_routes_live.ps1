param([Parameter(Mandatory)][string]$HelperPath, [switch]$ExpectConflict)
$ErrorActionPreference='Stop'
Import-Module C:\management-route-hns.psm1 -Force -DisableNameChecking
. $HelperPath
$adapters=@(Get-NetAdapter -Physical | Where-Object Status -eq Up)
if ($adapters.Count -ne 1) { throw 'Expected exactly one explicitly linked lab adapter' }
$index=$adapters[0].InterfaceIndex
foreach ($fixture in @(
    @{Address='192.0.2.20';Length=24;Prefix='192.0.2.20/32';Gateway='192.0.2.1';OnLink='0.0.0.0'},
    @{Address='2001:db8:42::20';Length=64;Prefix='2001:db8:42::20/128';Gateway='2001:db8:42::1';OnLink='::'}
)) {
    # Fresh isolated VM only. Never use a management address supplied by a user.
    if (@(Get-NetIPAddress | Where-Object IPAddress -eq $fixture.Address).Count) { throw 'Fixture address already exists' }
    $address=New-NetIPAddress -InterfaceIndex $index -IPAddress $fixture.Address -PrefixLength $fixture.Length
    try {
        # Address creation returns while duplicate-address detection is still
        # running. The automatic host route exists only after the address is
        # usable; do not confuse fixture initialization with recovery behavior.
        $deadline=[DateTime]::UtcNow.AddSeconds(20)
        do {
            $current=@(Get-NetIPAddress -InterfaceIndex $index | Where-Object IPAddress -eq $fixture.Address)
            if ($current.Count -eq 1 -and $current[0].AddressState -eq 'Preferred') { break }
            if ([DateTime]::UtcNow -ge $deadline) { throw "Fixture address did not become Preferred: $($current | ConvertTo-Json -Compress)" }
            Start-Sleep -Milliseconds 100
        } while ($true)
        New-NetRoute -InterfaceIndex $index -DestinationPrefix $fixture.Prefix -NextHop $fixture.Gateway -RouteMetric 100 | Out-Null
        try {
            $local=@(Get-NetRoute -PolicyStore ActiveStore | Where-Object {
                $_.InterfaceIndex -eq $index -and $_.DestinationPrefix -eq $fixture.Prefix -and
                $_.Protocol -eq 'Local' -and $_.NextHop -eq $fixture.OnLink
            })
            if ($local.Count -ne 1) {
                $observed=Get-NetRoute -InterfaceIndex $index -PolicyStore ActiveStore | Select-Object DestinationPrefix,NextHop,Protocol,RouteMetric
                throw "Windows did not create the expected automatic host route: $($observed | ConvertTo-Json -Compress)"
            }
            $saved=@(Get-ManagementRouteSnapshot | Where-Object DestinationPrefix -eq $fixture.Prefix)
            if ($saved.Count -ne 1) { throw 'Expected exactly one saved administrator route' }
            $caught=$null
            try { Restore-ManagementRoutes $saved } catch { $caught=$_ }
            if ($ExpectConflict) {
                if (!$caught -or "$caught" -notlike '*conflicting management route*') { throw 'Baseline did not reproduce the expected conflict' }
            } elseif ($caught) { throw $caught }
            foreach ($store in @('ActiveStore','PersistentStore')) {
                $admin=@(Get-NetRoute -PolicyStore $store | Where-Object {
                    $_.InterfaceIndex -eq $index -and $_.DestinationPrefix -eq $fixture.Prefix -and
                    $_.NextHop -eq $fixture.Gateway -and $_.RouteMetric -eq 100
                })
                if ($admin.Count -ne 1) { throw "Administrator route lost or modified in $store" }
            }
            $after=@(Get-NetRoute -PolicyStore ActiveStore | Where-Object {
                $_.InterfaceIndex -eq $index -and $_.DestinationPrefix -eq $fixture.Prefix -and $_.Protocol -eq 'Local'
            })
            if ($after.Count -ne 1 -or $after[0].NextHop -ne $local[0].NextHop -or $after[0].RouteMetric -ne $local[0].RouteMetric) {
                throw 'Automatic host route modified'
            }
            Write-Output "HOST_ROUTE_VERIFIED $($fixture.Prefix) baseline=$ExpectConflict"
        } finally {
            Remove-NetRoute -InterfaceIndex $index -DestinationPrefix $fixture.Prefix -NextHop $fixture.Gateway -Confirm:$false
        }
    } finally {
        # New-NetIPAddress can return objects for both stores. Removing one
        # can invalidate the other CIM object; select the remaining fixture
        # afresh in each store instead of reusing those stale objects.
        foreach ($store in @('PersistentStore','ActiveStore')) {
            $remaining=@(Get-NetIPAddress -PolicyStore $store | Where-Object {
                $_.InterfaceIndex -eq $index -and $_.IPAddress -eq $fixture.Address
            })
            if ($remaining.Count) {
                Remove-NetIPAddress -PolicyStore $store -InterfaceIndex $index -IPAddress $fixture.Address -Confirm:$false
            }
        }
    }
}
