package winutils

import (
	"os"
	"path/filepath"
	"testing"
)

// Execute the embedded production script in separate native PowerShell
// processes. Only RRAS/network cmdlets are mocked; no host BGP state is touched.
func TestNativeBGPSessionCheckpointRecovery(t *testing.T) {
	checkpoint := filepath.Join(t.TempDir(), "bgp's-checkpoint.json")
	epoch := filepath.Join(t.TempDir(), "bridge-epoch.flag")
	stubs := `
function Get-Service { [pscustomobject]@{Name='RemoteAccess';Status='Running'} }
function Get-NetIPAddress { [pscustomobject]@{IPAddress='fd00:10::20';InterfaceIndex=6;AddressState='Preferred'} }
function Get-CimInstance { [pscustomobject]@{LastBootUpTime=[datetime]'2026-10-01T00:00:00Z'} }
function Restart-Service { param($Name, [switch]$Force) }
$script:peer=[pscustomobject]@{PeerName='Mesh6_fd00_10__10';LocalIPAddress='fd00:10::20';PeerIPAddress='fd00:10::10';PeerASN=64512;PeeringMode='Automatic';ConnectivityStatus='Connected'}
function Get-BgpPeer { $script:peer }
function Get-BgpRouteInformation { @() }
function Stop-BgpPeer { $script:peer.ConnectivityStatus='Disconnected' }
function Start-BgpPeer { throw 'must not start during Begin' }
`
	unavailable := stubs + "\n$script:peer.ConnectivityStatus='Stopped'\nfunction Get-BgpRouteInformation { throw 'RRAS service is not running' }\n"
	if err := runManagementRouteCommand(unavailable + bgpTransitionCommand(true, checkpoint, "bridge-2", "fd00:10::20", epoch)); err == nil {
		t.Fatal("unavailable live RRAS state was accepted as stopped peers")
	}
	if _, err := os.Stat(checkpoint); !os.IsNotExist(err) {
		t.Fatalf("unavailable live RRAS state changed BGP intent: %v", err)
	}
	if err := runManagementRouteCommand(stubs + bgpTransitionCommand(true, checkpoint, "bridge-2", "fd00:10::20", epoch)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(checkpoint); err != nil {
		t.Fatal("missing recovery checkpoint", err)
	}
	failure := stubs + "\n$script:peer.ConnectivityStatus='Disconnected'\nfunction Start-BgpPeer { throw 'injected start failure' }\n"
	if err := runManagementRouteCommand(failure + bgpTransitionCommand(false, checkpoint, "bridge-2", "fd00:10::20", epoch)); err == nil {
		t.Fatal("native subprocess swallowed start failure")
	}
	if _, err := os.Stat(checkpoint); err != nil {
		t.Fatal("failed resume lost checkpoint", err)
	}
	recovery := stubs + "\n$script:peer.ConnectivityStatus='Disconnected'\nfunction Start-BgpPeer { $script:peer.ConnectivityStatus='Connecting' }\n"
	if err := runManagementRouteCommand(recovery + bgpTransitionCommand(false, checkpoint, "bridge-2", "fd00:10::20", epoch)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(checkpoint); !os.IsNotExist(err) {
		t.Fatalf("resume did not finish checkpoint: %v", err)
	}
}
