package winutils

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests execute the production embedded script and subprocess runner in
// native Windows PowerShell. Only network cmdlets are mocked: host routes are
// never modified, and checkpoints live exclusively under t.TempDir().
const mockRouteCmdlets = `
$script:routes=@()
function Get-NetIPAddress {
    [pscustomobject]@{IPAddress='192.0.2.20';InterfaceIndex=9}
    [pscustomobject]@{IPAddress='10.244.1.2';InterfaceIndex=8}
}
function Get-NetRoute { param($PolicyStore); $script:routes }
function New-NetRoute {
    param($DestinationPrefix,$NextHop,$InterfaceIndex,$RouteMetric,$PolicyStore)
    if($DestinationPrefix -ne '10.96.0.0/12' -or $NextHop -ne '192.0.2.10' -or $InterfaceIndex -ne 9 -or $RouteMetric -ne 42) { throw 'incorrect restored route' }
    $script:routes=@([pscustomobject]@{DestinationPrefix=$DestinationPrefix;NextHop=$NextHop;InterfaceIndex=$InterfaceIndex;RouteMetric=$RouteMetric})
}
`

func TestNativeManagementRouteTransition(t *testing.T) {
	checkpoint := filepath.Join(t.TempDir(), "route's-checkpoint.json")
	// Exceed CreateProcess's command-line limit; the production runner must
	// stream the actual endpoint snapshot, not place it in -Command arguments.
	endpoints, err := json.Marshal([]map[string]any{
		{"IPAddress": "10.244.1.2", "Name": strings.Repeat("endpoint", 10000)},
		{"IPAddress": "192.0.2.20", "IsRemoteEndpoint": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	seed := `$script:routes=@(
    [pscustomobject]@{DestinationPrefix='10.96.0.0/12';NextHop='192.0.2.10';InterfaceIndex=9;RouteMetric=42;Protocol='NetMgmt'},
    [pscustomobject]@{DestinationPrefix='0.0.0.0/0';NextHop='10.244.1.1';InterfaceIndex=8;RouteMetric=256;Protocol='NetMgmt'})
`
	if err := runManagementRouteCommand(mockRouteCmdlets + seed + managementRouteCommand(true, endpoints, checkpoint)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var saved []struct {
		DestinationPrefix string
		Addresses         []string
	}
	if err := json.Unmarshal(bytes.TrimPrefix(before, []byte{0xef, 0xbb, 0xbf}), &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].DestinationPrefix != "10.96.0.0/12" || len(saved[0].Addresses) != 1 || saved[0].Addresses[0] != "192.0.2.20" {
		t.Fatalf("checkpoint adopted HNS-owned route or lost intent: %s", before)
	}
	// A retry after HNS deletion must not overwrite intent with an empty table.
	if err := runManagementRouteCommand(mockRouteCmdlets + managementRouteCommand(true, endpoints, checkpoint)); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(checkpoint)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("retry changed checkpoint: %v", err)
	}
	// A native subprocess error must propagate and retain the checkpoint.
	failure := "\nfunction New-NetRoute { throw 'injected route failure' }\n"
	err = runManagementRouteCommand(mockRouteCmdlets + failure + managementRouteCommand(false, endpoints, checkpoint))
	if err == nil || !strings.Contains(err.Error(), "injected route failure") {
		t.Fatalf("failure was swallowed: %v", err)
	}
	if _, err := os.Stat(checkpoint); err != nil {
		t.Fatal("failed restoration lost checkpoint", err)
	}
	if err := runManagementRouteCommand(mockRouteCmdlets + managementRouteCommand(false, endpoints, checkpoint)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(checkpoint); !os.IsNotExist(err) {
		t.Fatalf("successful restoration did not finish checkpoint: %v", err)
	}
}
