package winutils

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
)

//go:embed bgp_transition.ps1
var bgpTransitionScript string

const bgpTransitionCheckpoint = `C:\var\lib\calico\bgp-sessions-pending.json`

// TransitionBGPSessions is called under the shared CNI network mutex. Normal
// compatible ADDs perform only a stat; they never start PowerShell or touch BGP.
func TransitionBGPSessions(begin bool, networkID, managementIP string) error {
	if !begin {
		if _, err := os.Stat(bgpTransitionCheckpoint); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
	}
	dir := os.Getenv("CALICO_HNS_HOOK_INSTALL_DIR")
	if dir == "" {
		dir = `C:\opt\calico-hns-ipv6`
	}
	return runManagementRouteCommand(bgpTransitionCommand(begin, bgpTransitionCheckpoint, networkID, managementIP, filepath.Join(dir, "bridge-epoch.flag")))
}

func bgpTransitionCommand(begin bool, checkpoint, networkID, managementIP, epochPath string) string {
	action := "Complete-CalicoBGPSessionTransition"
	if begin {
		action = "Begin-CalicoBGPSessionTransition"
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	// The shared management script supplies Restart-CalicoRemoteAccess.
	return "$ErrorActionPreference='Stop'; try {\n" + managementRouteScript + "\n" + bgpTransitionScript +
		"\nfunction Get-CalicoBGPBinding { @{NetworkID=" + quote(networkID) + ";ManagementIP=" + quote(managementIP) + ";EpochPath=" + quote(epochPath) + "} }\n" +
		"\n" + action + " -Checkpoint '" + strings.ReplaceAll(checkpoint, "'", "''") +
		"'\n} catch { Write-Error $_ -ErrorAction Continue; exit 1 }"
}
