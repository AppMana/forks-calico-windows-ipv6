// Command appmana-calico-lab runs Calico's AppMana tests inside disposable,
// Labcontainers-owned topologies.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/client"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "appmana-calico-lab: %v\n", err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	flag.Parse()
	if flag.NArg() != 1 {
		return fmt.Errorf("usage: go run . <script-tests|windows-smoke>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	root, err := repoRoot()
	if err != nil {
		return fmt.Errorf("find repository: %w", err)
	}
	labd := os.Getenv("LABCONTAINERS_LABD")
	if labd == "" {
		labd = "labd"
	}
	c, err := client.Launch(ctx, client.Options{LabdPath: labd})
	if err != nil {
		return fmt.Errorf("launch Labcontainers: %w", err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close Labcontainers: %w", err))
		}
	}()

	switch flag.Arg(0) {
	case "script-tests":
		err = scriptTests(ctx, c, root)
	case "windows-smoke":
		err = windowsSmoke(ctx, c)
	default:
		return fmt.Errorf("unknown test %q", flag.Arg(0))
	}
	if err != nil {
		return fmt.Errorf("%s: %w", flag.Arg(0), err)
	}
	return nil
}

func scriptTests(ctx context.Context, c *client.Client, root string) error {
	topology := fmt.Sprintf(`name: ignored
topology:
  nodes:
    test:
      kind: linux
      image: python:3.12-bookworm
      network-mode: none
      cmd: sleep infinity
      binds:
        - %s
`, strconv.Quote(root+":/workspace:ro"))
	lab, err := c.Start(ctx, &labv1.LabSpec{Topology: &labv1.TopologySource{
		Source: &labv1.TopologySource_Yaml{Yaml: []byte(topology)},
	}}, 10*time.Minute)
	if err != nil {
		return err
	}
	result, err := lab.Node("test").Exec(ctx, "bash", "/workspace/hack/appmana/tests/run-appmana-script-tests.sh")
	if err != nil {
		return err
	}
	os.Stdout.Write(result.GetStdout())
	os.Stderr.Write(result.GetStderr())
	if result.GetExitCode() != 0 {
		return fmt.Errorf("test container exited %d", result.GetExitCode())
	}
	return nil
}

func windowsSmoke(ctx context.Context, c *client.Client) error {
	topology := []byte(`name: ignored
topology:
  nodes:
    windows:
      kind: generic_vm
      image: labcontainers/windows-server-2022:latest
      network-mode: none
    peer:
      kind: linux
      image: alpine:3.20
      network-mode: none
  links:
    - endpoints: [windows:eth1, peer:eth1]
`)
	lab, err := c.Start(ctx, &labv1.LabSpec{
		Topology: &labv1.TopologySource{Source: &labv1.TopologySource_Yaml{Yaml: topology}},
		Nodes: map[string]*labv1.NodeExtension{
			"windows": {
				Control: "qga",
				Disks: []*labv1.Disk{{
					Name:      "data",
					SizeBytes: 256 << 20,
				}},
			},
		},
	}, 20*time.Minute)
	if err != nil {
		return err
	}
	node := lab.Node("windows")
	if err := node.Put(ctx, `C:\Labcontainers\calico-smoke.txt`, 0o600, []byte("calico-labcontainers-ok")); err != nil {
		return err
	}
	result, err := node.Exec(ctx,
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		"-NoProfile", "-NonInteractive", "-Command",
		`$ErrorActionPreference='Stop'; `+
			`$systemDisk=(Get-Partition -DriveLetter C | Get-Disk).Number; `+
			`$dataDisk=Get-Disk | Where-Object Number -ne $systemDisk | Select-Object -First 1; `+
			`if (!$dataDisk) { throw 'Labcontainers data disk is missing' }; `+
			`if ($dataDisk.PartitionStyle -eq 'RAW') { `+
			`  $dataDisk | Initialize-Disk -PartitionStyle GPT -PassThru | `+
			`    New-Partition -AssignDriveLetter -UseMaximumSize | `+
			`    Format-Volume -FileSystem NTFS -NewFileSystemLabel LABDATA -Confirm:$false | Out-Null `+
			`}; `+
			`$volume=Get-Volume -FileSystemLabel LABDATA; `+
			`$marker=$volume.DriveLetter + ':\calico-smoke.txt'; `+
			`$bytes=[Text.Encoding]::UTF8.GetBytes('disk-survived'); `+
			`$stream=[IO.File]::Open($marker,[IO.FileMode]::Create,[IO.FileAccess]::Write,[IO.FileShare]::None); `+
			`try { $stream.Write($bytes,0,$bytes.Length); $stream.Flush($true) } finally { $stream.Dispose() }; `+
			`Write-VolumeCache -DriveLetter $volume.DriveLetter; `+
			`(Get-Content C:\Labcontainers\calico-smoke.txt -Raw).Trim(); `+
			`(Get-CimInstance Win32_OperatingSystem).Caption; `+
			`$nics=@(Get-NetAdapter -Physical); `+
			`if ($nics.Count -ne 1) { throw "expected one topology NIC, found $($nics.Count)" }; `+
			`'data-nics=' + $nics.Count`)
	if err != nil {
		return err
	}
	os.Stdout.Write(result.GetStdout())
	os.Stderr.Write(result.GetStderr())
	if result.GetExitCode() != 0 {
		return fmt.Errorf("PowerShell exited %d", result.GetExitCode())
	}
	if err := node.PowerOff(ctx); err != nil {
		return fmt.Errorf("power off Windows VM: %w", err)
	}
	if err := node.Start(ctx); err != nil {
		return fmt.Errorf("restart Windows VM: %w", err)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		result, err = node.Exec(ctx,
			`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
			"-NoProfile", "-NonInteractive", "-Command",
			`$ErrorActionPreference='Stop'; `+
				`$systemDisk=(Get-Partition -DriveLetter C | Get-Disk).Number; `+
				`$dataDisk=Get-Disk | Where-Object Number -ne $systemDisk | Select-Object -First 1; `+
				`if (!$dataDisk) { throw 'Labcontainers data disk is missing after restart' }; `+
				`if ($dataDisk.IsOffline) { $dataDisk | Set-Disk -IsOffline $false }; `+
				`$partition=Get-Partition -DiskNumber $dataDisk.Number | Sort-Object Size -Descending | Select-Object -First 1; `+
				`if (!$partition.DriveLetter) { $partition | Add-PartitionAccessPath -AssignDriveLetter; $partition=Get-Partition -DiskNumber $dataDisk.Number | Where-Object DriveLetter | Select-Object -First 1 }; `+
				`(Get-Content ($partition.DriveLetter + ':\calico-smoke.txt') -Raw).Trim()`)
		if err == nil && result.GetExitCode() == 0 && strings.TrimSpace(string(result.GetStdout())) == "disk-survived" {
			fmt.Fprintln(os.Stdout, "disk-survived-after-power-cycle")
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Windows VM did not recover persistent disk after power cycle: result=%v err=%v", result, err)
		}
		time.Sleep(5 * time.Second)
	}
	return nil
}

func repoRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(string(out))), nil
}
