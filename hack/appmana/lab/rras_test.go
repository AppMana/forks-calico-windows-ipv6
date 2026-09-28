package main

import (
	"errors"
	"os/exec"
	"reflect"
	"testing"
)

func TestRRASPreparationSequencing(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		states    []string
		wantCalls []string
		fail      bool
	}{
		{"ready", []string{"ready", "ready"}, []string{"Prepare", "Verify"}, false},
		{"roles-and-forwarding-reboot", []string{"reboot", "reboot", "ready", "ready"}, []string{"Prepare", "reboot", "Prepare", "reboot", "Prepare", "Verify"}, false},
		{"unexpected", []string{"success"}, []string{"Prepare"}, true},
		{"verify-fails", []string{"ready", "reboot"}, []string{"Prepare", "Verify"}, true},
		{"bounded", []string{"reboot", "reboot", "reboot"}, []string{"Prepare", "reboot", "Prepare", "reboot", "Prepare"}, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var calls []string
			i := 0
			err := prepareRRAS(func(mode string) (string, error) {
				calls = append(calls, mode)
				if i >= len(scenario.states) {
					t.Fatal("extra attempt")
				}
				state := scenario.states[i]
				i++
				return state, nil
			}, func() error { calls = append(calls, "reboot"); return nil })
			if (err != nil) != scenario.fail || !reflect.DeepEqual(calls, scenario.wantCalls) {
				t.Fatalf("err=%v calls=%v", err, calls)
			}
		})
	}
	boom := errors.New("servicing failed")
	calls := 0
	err := prepareRRAS(func(string) (string, error) { calls++; return "", boom }, func() error { t.Fatal("unexpected reboot"); return nil })
	if !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("retried failed operation: %v %d", err, calls)
	}
}

func TestRRASPrerequisitePowerShellBehavior(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell/Pester prerequisite suite requires pwsh")
	}
	cmd := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", `Invoke-Pester -Path ./rras-prerequisites.Tests.ps1 -Output Normal -CI`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s\n%v", out, err)
	}
}
