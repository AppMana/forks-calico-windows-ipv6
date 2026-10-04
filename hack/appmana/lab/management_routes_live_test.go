package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/client"
	clab "github.com/appmana/labcontainers/pkg/containerlab"
	"github.com/appmana/labcontainers/pkg/windows"
)

//go:embed management_routes_live.ps1
var managementRoutesLiveScript []byte

// Actual Windows route stores and the shared node/CNI recovery helper. No
// Kubernetes installation, production route changes, or mocked OS commands.
func TestLiveWindowsManagementHostRoutes(t *testing.T) {
	if os.Getenv("LABCONTAINERS_MANAGEMENT_ROUTES_LIVE") != "1" {
		t.Skip("explicit isolated Windows route qualification required")
	}
	image, peer := os.Getenv("LABCONTAINERS_WINDOWS_IMAGE"), os.Getenv("LABCONTAINERS_PEER_IMAGE")
	if image == "" || peer == "" {
		t.Fatal("explicit preloaded VM and peer images required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	c, err := client.Launch(ctx, client.Options{LabdPath: os.Getenv("LABCONTAINERS_LABD"), StateDir: os.Getenv("LABCONTAINERS_STATE_DIR")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()
	topology := windowsTopology(image, peer)
	topology.Topology.Nodes["test"].Env = map[string]string{"QEMU_MEMORY": "4096", "QEMU_SMP": "4"}
	source, err := clab.Source(topology)
	if err != nil {
		t.Fatal(err)
	}
	lab, err := c.Start(ctx, &labv1.LabSpec{Topology: source, Nodes: map[string]*labv1.NodeExtension{"test": {Control: "qga"}}}, 14*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("route fixture session=%s artifacts=%s", lab.ID(), lab.Artifacts())
	_, err = lab.RunTimeline(ctx, &labv1.TimelineAction{Action: &labv1.TimelineAction_WaitExec{WaitExec: &labv1.WaitExec{
		Exec:          &labv1.ExecRequest{Node: &labv1.NodeRef{Node: "test"}, Argv: []string{"cmd.exe", "/c", "ver"}, TimeoutMillis: 10000},
		TimeoutMillis: 480000, RetryMillis: 2000, StdoutContains: []byte("Windows"),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	node := lab.Node("test")
	// Match the Windows prerequisites used by the existing native CNI lab.
	// A generic Windows seed does not yet expose HNS endpoint enumeration.
	if err := windows.ConfigureUnattendedRecovery(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := windows.EnsureFeatures(ctx, node, windows.FeatureOptions{Names: []string{"Containers"}, AllowReboot: true}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct{ path, hash, destination string }{
		{os.Getenv("LABCONTAINERS_HNS_MODULE"), os.Getenv("LABCONTAINERS_HNS_MODULE_SHA256"), `C:\management-route-hns.psm1`},
		{os.Getenv("LABCONTAINERS_ROUTE_BASELINE"), os.Getenv("LABCONTAINERS_ROUTE_BASELINE_SHA256"), `C:\management-route-baseline.ps1`},
	} {
		data, err := os.ReadFile(input.path)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != input.hash {
			t.Fatal("fixture dependency checksum mismatch")
		}
		if err := node.Put(ctx, input.destination, 0600, data); err != nil {
			t.Fatal(err)
		}
	}
	helper, err := os.ReadFile("../../../libcalico-go/lib/winutils/management_routes.ps1")
	if err != nil {
		t.Fatal(err)
	}
	if err := node.Put(ctx, `C:\management-route-candidate.ps1`, 0600, helper); err != nil {
		t.Fatal(err)
	}
	if err := node.Put(ctx, `C:\management-route-fixture.ps1`, 0600, managementRoutesLiveScript); err != nil {
		t.Fatal(err)
	}
	for _, baseline := range []bool{true, false} {
		name, extra := "candidate", ""
		if baseline {
			name, extra = "baseline", " -ExpectConflict"
		}
		out, err := node.ExecWithTimeout(ctx, 2*time.Minute, windows.PowerShellCommand(`& C:\management-route-fixture.ps1 -HelperPath C:\management-route-`+name+`.ps1`+extra)...)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s", name, out.Stdout)
		if out.ExitCode != 0 || strings.Count(string(out.Stdout), "HOST_ROUTE_VERIFIED ") != 2 {
			t.Fatalf("%s verification exit=%d stderr=%s", name, out.ExitCode, out.Stderr)
		}
	}
}
