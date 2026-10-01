package main

import (
	_ "embed"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"strings"
	"testing"
)

//go:embed windows_service_route.ps1
var windowsServiceRoute string

//go:embed windows_service_route_assert.ps1
var windowsServiceRouteAssert string

//go:embed windows_route_checkpoint_shape.ps1
var windowsRouteCheckpointShape string

func TestRoutePreservationHasPreexistingIntentBeforeWindowsJoin(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "qualification_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var route, join token.Pos
	setups, observations := 0, 0
	ast.Inspect(file, func(node ast.Node) bool {
		if id, ok := node.(*ast.Ident); ok {
			if id.Name == "windowsServiceRoute" {
				route = id.Pos()
				setups++
			}
			if id.Name == "windowsServiceRouteAssert" {
				observations++
			}
			if id.Name == "windowsWorkerInstallArgs" {
				join = id.Pos()
			}
		}
		if literal, ok := node.(*ast.BasicLit); ok && strings.Contains(literal.Value, "k0s.exe install worker") {
			join = literal.Pos()
		}
		return true
	})
	if setups != 1 || join == 0 || route >= join {
		t.Fatal("route preservation must configure intent once before joining Windows; node Ready can precede Calico's HNS transition")
	}
	if observations < 2 {
		t.Fatal("post-transition route loss assertions must remain in place")
	}
}

func TestWindowsServiceRouteObservationRejectsLostRoutes(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell required")
	}
	for _, scenario := range []string{"healthy", "lost-active", "old-interface", "wrong-gateway"} {
		t.Run(scenario, func(t *testing.T) {
			prefix := `$ErrorActionPreference='Stop'
function Get-NetIPAddress { [pscustomobject]@{InterfaceIndex=9} }
function Get-NetRoute {
    param($DestinationPrefix,$PolicyStore,$ErrorAction)
    if($scenario -eq 'lost-active' -and $PolicyStore -eq 'ActiveStore'){return}
    [pscustomobject]@{InterfaceIndex=$(if($scenario -eq 'old-interface'){7}else{9});NextHop=$(if($scenario -eq 'wrong-gateway'){'192.0.2.99'}else{'192.0.2.10'})}
}
function New-NetRoute { throw 'observation attempted a repair' }
function Remove-NetRoute { throw 'observation attempted deletion' }
`
			out, err := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", "$scenario='"+scenario+"'\n"+prefix+windowsServiceRouteAssert).CombinedOutput()
			if scenario == "healthy" {
				if err != nil || !strings.Contains(string(out), "WINDOWS_SERVICE_ROUTE_PRESERVED:9") {
					t.Fatalf("healthy route rejected: %v %s", err, out)
				}
			} else if err == nil || !strings.Contains(string(out), "configured service route lost") {
				t.Fatalf("lost route not rejected: %v %s", err, out)
			}
		})
	}
}

func TestWindowsServiceRoute(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell required")
	}
	for _, scenario := range []string{"absent", "present", "conflict", "ambiguous"} {
		t.Run(scenario, func(t *testing.T) {
			prefix := `$ErrorActionPreference='Stop'
$script:mockRoutes=@{ActiveStore=@();PersistentStore=@()}
$script:added=0
function Get-NetIPAddress { if($scenario -eq 'ambiguous'){return @([pscustomobject]@{InterfaceIndex=7},[pscustomobject]@{InterfaceIndex=8})}; [pscustomobject]@{InterfaceIndex=7} }
function Get-NetRoute { param($DestinationPrefix,$PolicyStore,$ErrorAction); if($DestinationPrefix -ne '10.96.0.0/12'){throw 'unexpected route scope'}; $script:mockRoutes[$PolicyStore] }
function New-NetRoute { param($DestinationPrefix,$InterfaceIndex,$NextHop,$PolicyStore); if($PolicyStore -eq 'PersistentStore'){throw 'Invalid parameter PolicyStore PersistentStore'}; if($DestinationPrefix -ne '10.96.0.0/12' -or $InterfaceIndex -ne 7 -or $NextHop -ne '192.0.2.10'){throw 'route escaped fixture scope or selected unreachable service VIP neighbor'}; $script:added++; $stores=if($PolicyStore){@($PolicyStore)}else{@('ActiveStore','PersistentStore')}; foreach($s in $stores){$script:mockRoutes[$s]=@([pscustomobject]@{InterfaceIndex=$InterfaceIndex;NextHop=$NextHop})} }
if($scenario -in @('present','conflict')) { foreach($s in @('ActiveStore','PersistentStore')) { $script:mockRoutes[$s]=@([pscustomobject]@{InterfaceIndex=$(if($scenario -eq 'present'){7}else{8});NextHop='192.0.2.10'}) } }
`
			body := "$scenario='" + scenario + "'\n" + prefix + "\ntry {\n" + windowsServiceRoute + "\n} catch { if($scenario -notin @('conflict','ambiguous')){throw}; if($script:added){throw 'changed routes before rejecting conflict'}; Write-Output 'EXPECTED_REJECTION'; exit 0 }; if($scenario -in @('conflict','ambiguous')){throw 'unsafe input accepted'}; if($scenario -eq 'absent' -and $script:added -ne 1){throw 'expected one persistent/active route creation'}; if($scenario -eq 'present' -and $script:added){throw 'not idempotent'}"
			cmd := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", body)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v %s", err, out)
			}
			marker := "WINDOWS_SERVICE_ROUTE_READY"
			if scenario == "conflict" || scenario == "ambiguous" {
				marker = "EXPECTED_REJECTION"
			}
			if !strings.Contains(string(out), marker) {
				t.Fatalf("missing evidence: %s", out)
			}
		})
	}
}
