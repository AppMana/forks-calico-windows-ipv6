# Read-only RRAS diagnostics for a retained Windows VM. Never restarts services,
# changes peers/routes, or converts an unhealthy sample into a qualification pass.
param(
    [ValidateRange(1,300)][int]$Count = 1,
    [ValidateRange(100,10000)][int]$IntervalMilliseconds = 1000
)
$ErrorActionPreference = 'Stop'

if (-not ('CalicoLab.RrasStatus' -as [type])) {
    # Native checks distinguish service/API state from the RemoteAccess CIM
    # provider's view. Signatures: Windows SDK mprapi.h (MprAdminIsService*).
    Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
namespace CalicoLab {
    public static class RrasStatus {
        [DllImport("mprapi.dll", CharSet=CharSet.Unicode, ExactSpelling=true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        public static extern bool MprAdminIsServiceRunning(string server);
        [DllImport("mprapi.dll", CharSet=CharSet.Unicode, ExactSpelling=true)]
        public static extern uint MprAdminIsServiceInitialized(string server,
            [MarshalAs(UnmanagedType.Bool)] out bool initialized);
    }
}
'@
}

for ($sample = 0; $sample -lt $Count; $sample++) {
    $timer = [Diagnostics.Stopwatch]::StartNew()
    $record = [ordered]@{Time=[DateTime]::UtcNow.ToString('o'); Sample=$sample; ProcessID=$PID}
    try { $record.SCM = [string](Get-Service RemoteAccess -ErrorAction Stop).Status }
    catch { $record.SCMError = $_.Exception.Message }
    $initialized = $false
    $record.NativeInitializedError = [CalicoLab.RrasStatus]::MprAdminIsServiceInitialized($null, [ref]$initialized)
    $record.NativeInitialized = $initialized
    $record.NativeRunning = [CalicoLab.RrasStatus]::MprAdminIsServiceRunning($null)
    try {
        $record.Peers = @(Get-BgpPeer -ErrorAction Stop | ForEach-Object {
            [ordered]@{Name=$_.PeerName; LocalIP=$_.LocalIPAddress; PeerIP=$_.PeerIPAddress;
                Status=[string]$_.ConnectivityStatus; StatusValue=[int]$_.ConnectivityStatus;
                Mode=[string]$_.PeeringMode}
        })
    } catch { $record.PeerError = $_.Exception.Message }
    try {
        $record.RouteCount = @(Get-BgpRouteInformation -ErrorAction Stop).Count
        $record.RouteQuerySucceeded = $true
    } catch {
        $record.RouteQuerySucceeded = $false
        $record.RouteError = $_.Exception.Message
        $record.RouteErrorID = $_.FullyQualifiedErrorId
    }
    $record.ProviderHosts = @(Get-Process WmiPrvSE -ErrorAction SilentlyContinue | ForEach-Object {
        [ordered]@{ID=$_.Id; StartTime=$_.StartTime.ToUniversalTime().ToString('o')}
    })
    $record.ElapsedMilliseconds = $timer.ElapsedMilliseconds
    $record | ConvertTo-Json -Depth 6 -Compress
    if ($sample + 1 -lt $Count) { Start-Sleep -Milliseconds $IntervalMilliseconds }
}
