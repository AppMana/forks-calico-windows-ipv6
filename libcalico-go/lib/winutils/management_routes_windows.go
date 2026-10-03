package winutils

import (
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The Windows package installs this same source for node-service.ps1.
//
//go:embed management_routes.ps1
var managementRouteScript string

const managementRouteCheckpoint = `C:\var\lib\calico\cni-management-routes-pending.json`

// ManagementRoutesPending is cheap on the ordinary CNI ADD path: do not start
// PowerShell or query HNS merely to find there is no interrupted transition.
func ManagementRoutesPending() (bool, error) {
	_, err := os.Stat(managementRouteCheckpoint)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// TransitionManagementRoutes uses the same checked restoration as node startup,
// with a distinct checkpoint owned by the process-shared CNI network lock.
// endpointsJSON is the native HNS endpoint snapshot, not executable input.
func TransitionManagementRoutes(begin bool, endpointsJSON []byte) error {
	return runManagementRouteCommand(managementRouteCommand(begin, endpointsJSON, managementRouteCheckpoint))
}

func managementRouteCommand(begin bool, endpointsJSON []byte, checkpoint string) string {
	action := "Complete-ManagementRouteTransition"
	if begin {
		action = "Begin-ManagementRouteTransition"
	}
	return "$ErrorActionPreference='Stop'; try {\n" + managementRouteScript +
		"\nfunction Get-HnsEndpoint { $items = ConvertFrom-Json ([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + base64.StdEncoding.EncodeToString(endpointsJSON) + "'))); foreach ($item in $items) { $item } }\n" +
		"function Get-ManagementRouteCheckpointPath { '" + strings.ReplaceAll(checkpoint, "'", "''") + "' }\n" + action +
		"\n} catch { Write-Error $_ -ErrorAction Continue; exit 1 }"
}

func runManagementRouteCommand(command string) error {
	// Feed the script on stdin: the endpoint snapshot can exceed Windows'
	// command-line limit on busy nodes. Use the system binary, not PATH.
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(root, `System32\WindowsPowerShell\v1.0\powershell.exe`),
		"-NoProfile", "-NonInteractive", "-Command", "& ([scriptblock]::Create([Console]::In.ReadToEnd()))")
	cmd.Stdin = strings.NewReader(command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("management route transition: %w (%s)", err, output)
	}
	return nil
}
