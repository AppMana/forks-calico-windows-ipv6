package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
)

type hookActivation struct {
	PID           uint32 `json:"pid"`
	InstalledPath string `json:"installedPath"`
	LoadedPath    string `json:"loadedPath"`
	SHA256        string `json:"sha256"`
}

func validateHookActivation(expected string, observed hookActivation) error {
	digest, err := hex.DecodeString(expected)
	if err != nil || len(digest) != 32 {
		return fmt.Errorf("missing valid packaged hook digest")
	}
	if observed.PID == 0 || observed.InstalledPath == "" || !strings.EqualFold(observed.InstalledPath, observed.LoadedPath) {
		return fmt.Errorf("HNS did not load the configured installed hook: %+v", observed)
	}
	if !strings.EqualFold(expected, observed.SHA256) {
		return fmt.Errorf("HNS hook differs from the running package: wanted %s, observed %+v", expected, observed)
	}
	return nil
}

// Called only after the existing crash sequence proves a new Ready boot. The
// package digest is read independently inside its HostProcess mount; observing
// the host file alone could otherwise certify a stale, successfully loaded DLL.
func verifyPackagedWindowsHook(execute func(string, time.Duration, ...string) (*labv1.ExecResponse, error)) error {
	r, err := execute("linux", 30*time.Second, "k0s", "kubectl", "exec", "--namespace=kube-system", "daemonset/calico-node-windows", "--container=node", "--", "powershell.exe", "-NoProfile", "-Command",
		`$ErrorActionPreference='Stop'; (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $env:CONTAINER_SANDBOX_MOUNT_POINT 'CalicoWindows\hns-ipv6-hook.dll')).Hash`)
	if err != nil {
		return err
	}
	expected := strings.TrimSpace(string(r.Stdout))
	r, err = execute("windows", 30*time.Second, "powershell.exe", "-NoProfile", "-Command", `$ErrorActionPreference='Stop';
Import-Module C:\CalicoWindows\libs\calico\calico.psm1 -Force -WarningAction SilentlyContinue;
$paths=Get-CalicoHnsHookPaths;
$service=Get-CimInstance Win32_Service -Filter "Name='hns'";
if(!$service.ProcessId){throw 'HNS is not running'};
$modules=@((Get-Process -Id $service.ProcessId).Modules | Where-Object ModuleName -eq 'hns-ipv6-hook.dll');
if($modules.Count -ne 1){throw 'Expected exactly one loaded HNS hook module'};
@{pid=$service.ProcessId; installedPath=[IO.Path]::GetFullPath($paths.DllPath); loadedPath=$modules[0].FileName; sha256=(Get-FileHash -Algorithm SHA256 -LiteralPath $modules[0].FileName).Hash} | ConvertTo-Json -Compress`)
	if err != nil {
		return err
	}
	var observed hookActivation
	if err := json.Unmarshal(r.Stdout, &observed); err != nil {
		return err
	}
	if err := validateHookActivation(expected, observed); err != nil {
		return err
	}
	fmt.Printf("PACKAGED_WINDOWS_HOOK_ACTIVE pid=%d sha256=%s path=%s\n", observed.PID, observed.SHA256, observed.LoadedPath)
	return nil
}

func TestHookActivationRejectsStaleOrUnloadedArtifact(t *testing.T) {
	digest := strings.Repeat("a", 64)
	valid := hookActivation{PID: 10, InstalledPath: `C:\opt\hook.dll`, LoadedPath: `c:\OPT\hook.dll`, SHA256: strings.ToUpper(digest)}
	if err := validateHookActivation(digest, valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*hookActivation){
		func(o *hookActivation) { o.PID = 0 },
		func(o *hookActivation) { o.LoadedPath = "" },
		func(o *hookActivation) { o.InstalledPath = "" },
		func(o *hookActivation) { o.LoadedPath = `C:\old\hook.dll` },
		func(o *hookActivation) { o.SHA256 = strings.Repeat("b", 64) },
	} {
		o := valid
		change(&o)
		if err := validateHookActivation(digest, o); err == nil {
			t.Fatalf("accepted invalid hook activation: %+v", o)
		}
	}
	if err := validateHookActivation("", valid); err == nil {
		t.Fatal("accepted absent package identity")
	}
}
