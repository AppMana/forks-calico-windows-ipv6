package winutils

import (
	_ "embed"
	"os"
	"strings"
)

//go:embed bgp_transition.ps1
var bgpTransitionScript string

const bgpTransitionCheckpoint = `C:\var\lib\calico\bgp-sessions-pending.json`

// TransitionBGPSessions is called under the shared CNI network mutex. Normal
// compatible ADDs perform only a stat; they never start PowerShell or touch BGP.
func TransitionBGPSessions(begin bool) error {
	if !begin {
		if _, err := os.Stat(bgpTransitionCheckpoint); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
	}
	return runManagementRouteCommand(bgpTransitionCommand(begin, bgpTransitionCheckpoint))
}

func bgpTransitionCommand(begin bool, checkpoint string) string {
	action := "Complete-CalicoBGPSessionTransition"
	if begin {
		action = "Begin-CalicoBGPSessionTransition"
	}
	return "$ErrorActionPreference='Stop'; try {\n" + bgpTransitionScript +
		"\n" + action + " -Checkpoint '" + strings.ReplaceAll(checkpoint, "'", "''") +
		"'\n} catch { Write-Error $_ -ErrorAction Continue; exit 1 }"
}
