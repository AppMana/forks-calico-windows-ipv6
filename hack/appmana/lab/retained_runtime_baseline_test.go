package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/client"
	runtimeimages "github.com/appmana/labcontainers/pkg/containerd"
	"github.com/appmana/labcontainers/pkg/kubernetes/kube"
	"github.com/srl-labs/containerlab/types"
	v1 "k8s.io/api/core/v1"
)

// Continue an explicitly retained baseline after external runtime installation.
// Reuse stock worker installation, SDK offline image handling and the existing
// consumer; never reinstall the controller or repair networking in a verifier.
func TestRetainedRuntimeBaseline(t *testing.T) {
	if os.Getenv("LABCONTAINERS_RESUME_RUNTIME_BASELINE") != "1" {
		t.Skip("explicit retained baseline continuation required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	input, err := readWindowsRuntime(ctx, os.Getenv)
	if err != nil || input == nil {
		t.Fatalf("runtime inputs: %v", err)
	}
	workload, args, err := readKubernetesWorkload(os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD"), os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD_SHA256"), os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD_ARGS"), os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD_SUCCESS"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateIPv6Consumer(workload, os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD_SUCCESS"), true); err != nil {
		t.Fatal(err)
	}
	id, socket := os.Getenv("LABCONTAINERS_RETAINED_SESSION"), os.Getenv("LABCONTAINERS_RETAINED_SOCKET")
	if id == "" || socket == "" {
		t.Fatal("explicit retained session and socket required")
	}
	c, err := client.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	lab, err := c.Resume(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := lab.Keep(ctx, 4*time.Hour); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	defer func() {
		if !t.Failed() {
			return
		}
		err := retainFailedQualification(func() error { return nil }, func(name string) error {
			stop, done := context.WithTimeout(context.Background(), time.Minute)
			defer done()
			return lab.Node(name).PowerOff(stop)
		}, map[string]*types.NodeDefinition{"linux": {}, "windows": {}, "tor": {}, "gateway": {}})
		if err != nil {
			t.Error(err)
		}
	}()
	linux, windows := lab.Node("linux"), lab.Node("windows")
	run := func(node *client.Node, argv ...string) []byte {
		t.Helper()
		out, err := node.Commands().Exec(ctx, argv...)
		if err != nil {
			t.Fatalf("%s: %s: %v", node.Ref().Node, out, err)
		}
		return out
	}
	ps := func(script string) []byte {
		return run(windows, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference='Stop'; "+script)
	}
	// The transaction has already installed this verified stage. A resumed
	// test may observe it but must not apply or change runtime configuration.
	stage := fmt.Sprintf(`C:\Program Files\AppMana\containerd\releases\%s-%s`, input.Version, input.ArchiveSHA256[:16])
	ps(fmt.Sprintf(`if((Get-FileHash C:\LabQualification\runtime.toml).Hash -ne '%s'){throw 'runtime config mismatch'}; if((Get-FileHash C:\LabQualification\runtime-transaction.ps1).Hash -ne '%s'){throw 'runtime transaction mismatch'}; if((Get-Service containerd).Status -ne 'Running'){throw 'runtime not running'}`, input.ConfigSHA256, input.TransactionSHA256))
	images := runtimeimages.Images{Command: []string{stage + `\bin\ctr.exe`, "--namespace", "k8s.io"}, Snapshotter: "windows"}
	var archives []string
	for _, name := range []string{"cni", "node", "pause", "proxy", "workload"} {
		archives = append(archives, `C:\var\lib\k0s\images\windows-`+name+".tar")
	}
	t.Log("resuming SDK offline runtime image import")
	if err := images.Import(ctx, windows.Commands(), archives, true); err != nil {
		t.Fatal(err)
	}
	installed := strings.TrimSpace(string(ps(`if(Get-Service k0sworker -ErrorAction SilentlyContinue){'installed'}else{'absent'}`)))
	if installed == "absent" {
		token := run(linux, "k0s", "token", "create", "--role=worker", "--expiry=1h")
		if err := windows.Put(ctx, `C:\LabQualification\token`, 0600, bytes.TrimSpace(token)); err != nil {
			t.Fatal(err)
		}
		run(windows, windowsWorkerInstallArgs(true)...)
	} else if installed != "installed" {
		t.Fatalf("unexpected worker state: %q", installed)
	}
	ps(`if((Get-Service k0sworker).Status -ne 'Running'){Start-Service k0sworker}`)
	_, err = lab.RunTimeline(ctx, &labv1.TimelineAction{Action: &labv1.TimelineAction_WaitExec{WaitExec: &labv1.WaitExec{
		Exec:          &labv1.ExecRequest{Node: linux.Ref(), Argv: []string{"k0s", "kubectl", "wait", "--for=condition=Ready", "node/linux", "node/windows", "--timeout=10s"}, TimeoutMillis: 15000},
		TimeoutMillis: 420000, RetryMillis: 2000,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var node v1.Node
	if err := json.Unmarshal(run(linux, "k0s", "kubectl", "get", "node", "windows", "-o", "json"), &node); err != nil {
		t.Fatal(err)
	}
	if node.Status.NodeInfo.ContainerRuntimeVersion != "containerd://"+input.Version {
		t.Fatalf("wrong baseline runtime: %s", node.Status.NodeInfo.ContainerRuntimeVersion)
	}
	run(linux, "k0s", "kubectl", "rollout", "status", "daemonset/calico-node-windows", "-n", "kube-system", "--timeout=300s")
	api := &kube.Client{Bastion: linux.Commands(), Kubectl: []string{"k0s", "kubectl"}, ControlPlanes: []string{"192.0.2.10"}}
	if err := api.ApplyObjects(ctx, vyosBGPPeers(true)...); err != nil {
		t.Fatal(err)
	}
	ps(windowsServiceRouteAssert)
	images.LocalImport = true
	if err := images.Import(ctx, windows.Commands(), []string{`C:\var\lib\k0s\images\windows-workload.tar`}, false); err != nil {
		t.Fatal(err)
	}
	if err := images.RequireReady(ctx, windows.Commands(), []string{networkProbeWindowsImage}); err != nil {
		t.Fatal(err)
	}
	if err := api.ApplyObjects(ctx, networkProbeObjects(networkProbeWindowsImage)...); err != nil {
		t.Fatal(err)
	}
	run(linux, "k0s", "kubectl", "wait", "--for=condition=Ready", "pod/hc-linux", "pod/hc-windows", "--timeout=300s")
	verifyVyOSRoutes(t, true, func(name string, argv ...string) []byte { return run(lab.Node(name), argv...) })
	if err := linux.Put(ctx, "/usr/local/bin/kubernetes-workload", 0755, workload); err != nil {
		t.Fatal(err)
	}
	directory := fmt.Sprintf("/var/tmp/kubernetes-consumer-%d", time.Now().UnixNano())
	out := run(linux, workloadCommand(directory, "/usr/local/bin/kubernetes-workload", args)...)
	t.Logf("baseline consumer: %s", out)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "RUNTIME_WORKLOAD_BASELINE_COMPLETE" {
			t.Log("RETAINED_RUNTIME_BASELINE_COMPLETE")
			return
		}
	}
	t.Fatal("consumer did not complete baseline preparation")
}
