# Copyright (c) 2018-2021 Tigera, Inc. All rights reserved.
# Licensed under the Apache License, Version 2.0.
# Shared by node-service.ps1 and the native Windows CNI network reconciler.

function Get-ManagementRouteSnapshot()
{
    $addresses = @(Get-NetIPAddress -ErrorAction Stop)
    # HNS host endpoints also own persistent NetMgmt routes. Their pod-network
    # addresses can disappear during reconciliation and are not administrator
    # management intent. HNS, not this checkpoint, must recreate those routes.
    $hnsAddresses = @(Get-HnsEndpoint | Where-Object { !$_.IsRemoteEndpoint } |
        ForEach-Object { $_.IPAddress; $_.IPv6Address } | Where-Object { $_ })
    foreach ($route in @(Get-NetRoute -PolicyStore PersistentStore -ErrorAction Stop)) {
        if ($route.Protocol -ne 'NetMgmt') { continue }
        $owners = @($addresses | Where-Object {
            $_.InterfaceIndex -eq $route.InterfaceIndex -and
            $_.IPAddress -notin $hnsAddresses -and
            $_.IPAddress -notmatch '^(127\.|169\.254\.|::1$|fe80:)'
        } | Select-Object -ExpandProperty IPAddress)
        # Do not adopt routes from unaddressed/stale or link-local adapters.
        if (!$owners.Count) { continue }
        [pscustomobject]@{
            Addresses=$owners; DestinationPrefix=$route.DestinationPrefix;
            NextHop=$route.NextHop; RouteMetric=$route.RouteMetric
        }
    }
}

function Restore-ManagementRoutes($Snapshot)
{
    if (!$Snapshot.Count) { return }
    $addresses = @(Get-NetIPAddress -ErrorAction Stop)
    $hnsAddresses = @(Get-HnsEndpoint | Where-Object { !$_.IsRemoteEndpoint } |
        ForEach-Object { $_.IPAddress; $_.IPv6Address } | Where-Object { $_ })
    $plan = @()
    foreach ($saved in $Snapshot) {
        # Older checkpoints may include HNS-owned routes. Leave those routes
        # untouched rather than blocking recovery on a transient host endpoint.
        $owners = @($saved.Addresses | Where-Object { $_ -notin $hnsAddresses })
        if (!$owners.Count) { continue }
        $indices = @($addresses | Where-Object { $_.IPAddress -in $owners } |
            Select-Object -ExpandProperty InterfaceIndex -Unique)
        if ($indices.Count -ne 1) { throw 'cannot uniquely resolve the original management route address owner after HNS creation' }
        $index = $indices[0]
        $present = @{}
        foreach ($store in @('ActiveStore', 'PersistentStore')) {
            $routes = @(Get-NetRoute -PolicyStore $store -ErrorAction Stop | Where-Object {
                $_.DestinationPrefix -eq $saved.DestinationPrefix -and $_.InterfaceIndex -eq $index
            })
            if (@($routes | Where-Object { $_.NextHop -ne $saved.NextHop -or $_.RouteMetric -ne $saved.RouteMetric }).Count) {
                throw "conflicting management route in $store; refusing to overwrite it"
            }
            $present[$store] = $routes.Count -gt 0
        }
        if ($present['ActiveStore'] -and !$present['PersistentStore']) {
            throw 'management route exists only in ActiveStore; refusing to replace it to change persistence'
        }
        $plan += [pscustomobject]@{Saved=$saved;Index=$index;Active=$present['ActiveStore'];Persistent=$present['PersistentStore']}
    }
    # Validate the entire plan before writing. Never remove routes or infer new
    # CIDRs/gateways; only restore the administrator's pre-creation intent.
    foreach ($item in $plan) {
        if ($item.Active -and $item.Persistent) { continue }
        $routeParameters = @{
            DestinationPrefix=$item.Saved.DestinationPrefix; NextHop=$item.Saved.NextHop;
            InterfaceIndex=$item.Index; RouteMetric=$item.Saved.RouteMetric; ErrorAction='Stop'
        }
        if ($item.Persistent) { $routeParameters.PolicyStore = 'ActiveStore' }
        New-NetRoute @routeParameters | Out-Null
    }
    foreach ($item in $plan) {
        foreach ($store in @('ActiveStore', 'PersistentStore')) {
            $restored = @(Get-NetRoute -PolicyStore $store -ErrorAction Stop | Where-Object {
                $_.DestinationPrefix -eq $item.Saved.DestinationPrefix -and
                $_.NextHop -eq $item.Saved.NextHop -and $_.RouteMetric -eq $item.Saved.RouteMetric -and
                $_.InterfaceIndex -eq $item.Index
            })
            if ($restored.Count -ne 1) { throw "management route restoration not verified in $store" }
        }
    }
}

function Get-ManagementRouteCheckpointPath()
{
    # The install tree is mirrored with robocopy /MIR on every start. Recovery
    # intent belongs in Calico's persistent host state, not that replaced tree.
    return 'C:\var\lib\calico\management-routes-pending.json'
}

function Begin-ManagementRouteTransition()
{
    $checkpoint = Get-ManagementRouteCheckpointPath
    # Never replace prior intent with the already-damaged post-HNS table.
    if (Test-Path $checkpoint) {
        # Validate before allowing any HNS mutation; preserve corrupt evidence
        # rather than discovering it only after the adapter has been rebound.
        $null = Get-Content -Raw $checkpoint -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop
        return
    }
    $managementRoutes = @(Get-ManagementRouteSnapshot)
    New-Item -ItemType Directory -Force (Split-Path $checkpoint -Parent) -ErrorAction Stop | Out-Null
    $pending = $checkpoint + '.' + [guid]::NewGuid().ToString('N')
    ConvertTo-Json -InputObject $managementRoutes -Depth 5 |
        Set-Content $pending -Encoding UTF8 -ErrorAction Stop
    Move-Item $pending $checkpoint -Force -ErrorAction Stop
}

function Complete-ManagementRouteTransition()
{
    $checkpoint = Get-ManagementRouteCheckpointPath
    if (!(Test-Path $checkpoint)) { return }
    # PS 5.1 emits the decoded JSON array as one pipeline object; wrapping this
    # pipeline in @() creates a nested array and breaks route parameter binding.
    $managementRoutes = Get-Content -Raw $checkpoint -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop
    Restore-ManagementRoutes $managementRoutes
    Remove-Item $checkpoint -ErrorAction Stop
}
