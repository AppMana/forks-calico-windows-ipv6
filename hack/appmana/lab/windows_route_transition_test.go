package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRouteTransitionOracle(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell required")
	}
	script, err := filepath.Abs("windows_route_transition_assert.ps1")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"healthy", "lost-active", "wrong-metric", "old-interface", "same-boot", "wrong-script"} {
		t.Run(scenario, func(t *testing.T) {
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
			evidence := quote(filepath.Join(t.TempDir(), "before.json"))
			body := fmt.Sprintf(`$ErrorActionPreference='Stop'
$script:phase='before'; $script:scenario=%s
function Get-FileHash { [pscustomobject]@{Hash=$(if($script:phase -eq 'after' -and $script:scenario -eq 'wrong-script'){'bad'}else{'candidate'})} }
function Get-NetIPAddress { [pscustomobject]@{InterfaceIndex=$(if($script:phase -eq 'after'){12}else{11})} }
function Get-NetRoute {
 param($DestinationPrefix,$PolicyStore)
 if($DestinationPrefix -ne '198.18.123.0/24'){throw 'wrong prefix'}
 if($script:phase -eq 'after' -and $script:scenario -eq 'lost-active' -and $PolicyStore -eq 'ActiveStore'){return}
 [pscustomobject]@{InterfaceIndex=$(if($script:phase -eq 'after' -and $script:scenario -ne 'old-interface'){12}else{11});NextHop='192.0.2.10';RouteMetric=$(if($script:phase -eq 'after' -and $script:scenario -eq 'wrong-metric'){999}else{123})}
}
function Get-CimInstance { [pscustomobject]@{LastBootUpTime=$([datetime]'2026-09-30T00:00:00Z').AddMinutes($(if($script:phase -eq 'after' -and $script:scenario -ne 'same-boot'){1}else{0}))} }
function New-NetRoute { throw 'oracle attempted repair' }
function Remove-NetRoute { throw 'oracle attempted deletion' }
& %s -Mode Capture -ExpectedScriptSHA256 candidate -EvidencePath %s
$script:phase='after'
& %s -Mode Verify -ExpectedScriptSHA256 candidate -EvidencePath %s
`, quote(scenario), quote(script), evidence, quote(script), evidence)
			body = strings.ReplaceAll(body, "$script:phase", "$global:routePhase")
			body = strings.ReplaceAll(body, "$script:scenario", "$global:routeScenario")
			out, err := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", body).CombinedOutput()
			if !strings.Contains(string(out), "ROUTE_TRANSITION_CAPTURED") {
				t.Fatalf("baseline capture failed: %v %s", err, out)
			}
			if scenario == "healthy" {
				if err != nil || !strings.Contains(string(out), "ROUTE_TRANSITION_REBOOT_PASSED") {
					t.Fatalf("%v %s", err, out)
				}
			} else if err == nil {
				t.Fatalf("accepted %s: %s", scenario, out)
			}
		})
	}
}
