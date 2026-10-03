# Calico-owned planned HNS interface replacement. Called only under the shared
# native CNI network mutex. Checkpoint lives outside the mirrored install tree.
function Read-CalicoBGPSessionCheckpoint($Checkpoint) {
    $state = Get-Content -LiteralPath $Checkpoint -Raw -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop
    if ($state.Version -ne 1 -or $null -eq $state.Peers) { throw 'invalid BGP session checkpoint' }
    foreach ($peer in $state.Peers) {
        if (!$peer.PeerName -or !$peer.LocalIPAddress -or !$peer.PeerIPAddress -or !$peer.PeerASN) {
            throw 'invalid BGP session checkpoint peer'
        }
    }
    return $state
}

function Test-CalicoBGPSessionIdentity($Saved, $Current) {
    return ($null -ne $Current -and $Saved.PeerName -eq $Current.PeerName -and
        $Saved.LocalIPAddress -eq $Current.LocalIPAddress -and
        $Saved.PeerIPAddress -eq $Current.PeerIPAddress -and
        $Saved.PeerASN -eq $Current.PeerASN -and
        $Saved.PeeringMode -eq [string]$Current.PeeringMode)
}

function Begin-CalicoBGPSessionTransition($Checkpoint) {
    $saved = @()
    if (Test-Path -LiteralPath $Checkpoint) {
        $saved = @((Read-CalicoBGPSessionCheckpoint $Checkpoint).Peers)
    }
    # VXLAN/fresh hosts may have no RRAS at all. A running service with a
    # failing query is different: fail before deleting a working interface.
    $service = @(Get-Service -ErrorAction Stop | Where-Object { $_.Name -eq 'RemoteAccess' })
    if (!$service -or $service.Status -ne 'Running') { return }
    $current = @(Get-BgpPeer -ErrorAction Stop)
    $localAddresses = @(Get-NetIPAddress -ErrorAction Stop | Select-Object -ExpandProperty IPAddress)
    foreach ($peer in $current) {
        # These are confd's generated names; don't adopt arbitrary RRAS peers
        # or manually stopped sessions. Snapshot only sessions we interrupt.
        if ($peer.PeerName -notmatch '^(Mesh6?_|Global6?_|Node6?_)' -or
            $peer.ConnectivityStatus -ne 'Connected' -or
            $peer.LocalIPAddress -notin $localAddresses) { continue }
        $exists = @($saved | Where-Object { Test-CalicoBGPSessionIdentity $_ $peer }).Count -gt 0
        if (!$exists) {
            $saved += [pscustomobject]@{
                PeerName=$peer.PeerName; LocalIPAddress=$peer.LocalIPAddress;
                PeerIPAddress=$peer.PeerIPAddress; PeerASN=$peer.PeerASN;
                PeeringMode=[string]$peer.PeeringMode
            }
        }
    }
    if (!$saved.Count) { return }
    New-Item -ItemType Directory -Force (Split-Path $Checkpoint -Parent) -ErrorAction Stop | Out-Null
    $pending = $Checkpoint + '.' + [guid]::NewGuid().ToString('N')
    @{Version=1; Peers=@($saved)} | ConvertTo-Json -Depth 5 |
        Set-Content -LiteralPath $pending -Encoding UTF8 -ErrorAction Stop
    Move-Item -LiteralPath $pending -Destination $Checkpoint -Force -ErrorAction Stop
    foreach ($peer in $saved) {
        $matching = @($current | Where-Object { Test-CalicoBGPSessionIdentity $peer $_ })
        if ($matching.Count -eq 1 -and $matching[0].ConnectivityStatus -eq 'Connected') {
            Stop-BgpPeer -Name $peer.PeerName -Force -ErrorAction Stop | Out-Null
            if ((Get-BgpPeer -Name $peer.PeerName -ErrorAction Stop).ConnectivityStatus -eq 'Connected') {
                throw 'BGP session still connected before HNS replacement'
            }
        }
    }
}

function Complete-CalicoBGPSessionTransition($Checkpoint) {
    if (!(Test-Path -LiteralPath $Checkpoint)) { return }
    $state = Read-CalicoBGPSessionCheckpoint $Checkpoint
    $current = @(Get-BgpPeer -ErrorAction Stop)
    foreach ($peer in $state.Peers) {
        $matching = @($current | Where-Object { Test-CalicoBGPSessionIdentity $peer $_ })
        # A deleted/reconfigured peer belongs to the newer confd configuration,
        # not this checkpoint. Never recreate it or start its replacement.
        if ($matching.Count -eq 1 -and $matching[0].ConnectivityStatus -ne 'Connected') {
            Start-BgpPeer -Name $peer.PeerName -ErrorAction Stop | Out-Null
        }
    }
    Remove-Item -LiteralPath $Checkpoint -ErrorAction Stop
}
