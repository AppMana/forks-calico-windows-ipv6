package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

type consumerCrashPlan struct {
	Args    []string `json:"args"`
	Success string   `json:"success"`
}

// The already hash-pinned workload may publish one readback-only continuation.
// This does not accept a replacement executable or a host command.
func readConsumerCrashPlan(output string) (consumerCrashPlan, error) {
	var plan consumerCrashPlan
	count := 0
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "KUBERNETES_CRASH_VERIFY=") {
			continue
		}
		count++
		d := json.NewDecoder(strings.NewReader(strings.TrimPrefix(line, "KUBERNETES_CRASH_VERIFY=")))
		d.DisallowUnknownFields()
		if err := d.Decode(&plan); err != nil {
			return plan, err
		}
		var extra any
		if err := d.Decode(&extra); err != io.EOF {
			return plan, fmt.Errorf("trailing crash plan JSON")
		}
	}
	if count != 1 || len(plan.Args) == 0 || plan.Success == "" || strings.ContainsAny(plan.Success, "\r\n\x00") {
		return plan, fmt.Errorf("one explicit crash readback plan required")
	}
	for _, arg := range plan.Args {
		if strings.ContainsRune(arg, '\x00') {
			return plan, fmt.Errorf("invalid crash argument")
		}
	}
	return plan, nil
}

func TestConsumerCrashPlan(t *testing.T) {
	valid := `KUBERNETES_CRASH_VERIFY={"args":["-test.run=^ReadExisting$","literal; value"],"success":"VERIFIED:token"}`
	plan, err := readConsumerCrashPlan("unrelated\n" + valid + "\n")
	if err != nil || len(plan.Args) != 2 || plan.Args[1] != "literal; value" || plan.Success != "VERIFIED:token" {
		t.Fatalf("%+v %v", plan, err)
	}
	for _, bad := range []string{"", valid + "\n" + valid, valid + ` {}`, valid + `]`, `KUBERNETES_CRASH_VERIFY={}`, `KUBERNETES_CRASH_VERIFY={"args":["x"],"success":"a\nb"}`, `KUBERNETES_CRASH_VERIFY={"args":["x"],"success":"ok","executable":"replacement"}`} {
		if _, err := readConsumerCrashPlan(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
