// Command appmana-calico-lab runs this fork's tests in Labcontainers-owned labs.
// The project owns test binaries; this runner consumes prebuilt artifacts.
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/client"
	clab "github.com/appmana/labcontainers/pkg/containerlab"
	"github.com/srl-labs/containerlab/core"
	"github.com/srl-labs/containerlab/links"
	"github.com/srl-labs/containerlab/types"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	scenario := flag.String("case", "script-tests", "script-tests or windows-tests")
	image := flag.String("image", "", "prebuilt test-container or Windows VM image")
	peerImage := flag.String("peer-image", "", "preloaded peer container image (required for windows-tests)")
	binary := flag.String("binary", "", "prebuilt Windows Go test executable from this fork")
	artifacts := flag.String("artifacts", "", "directory for retained Labcontainers evidence")
	flag.Parse()
	if *image == "" || *artifacts == "" {
		return fmt.Errorf("-image and -artifacts are required")
	}
	if *scenario != "script-tests" && *scenario != "windows-tests" {
		return fmt.Errorf("unknown case %q", *scenario)
	}
	if *scenario == "windows-tests" && (*peerImage == "" || *binary == "") {
		return fmt.Errorf("-peer-image and -binary are required for windows-tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	config := &core.Config{Name: "calico-fork", Topology: &types.Topology{
		Defaults: &types.NodeDefinition{ImagePullPolicy: "Never", NetworkMode: "none"},
		Nodes:    map[string]*types.NodeDefinition{},
	}}
	spec := &labv1.LabSpec{ArtifactDirectory: *artifacts}
	var argv []string
	var payload []byte
	if *scenario == "script-tests" {
		root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return err
		}
		config.Topology.Nodes["test"] = &types.NodeDefinition{
			Kind: "linux", Image: *image, Cmd: "sleep infinity",
			Binds: []string{filepath.Clean(strings.TrimSpace(string(root))) + ":/workspace:ro"},
		}
		argv = []string{"bash", "/workspace/hack/appmana/tests/run-appmana-script-tests.sh"}
	} else {
		var err error
		payload, err = os.ReadFile(*binary)
		if err != nil {
			return fmt.Errorf("read prebuilt test binary: %w", err)
		}
		if len(payload) == 0 {
			return fmt.Errorf("empty Windows test executable")
		}
		fmt.Printf("Windows test artifact sha256:%x\n", sha256.Sum256(payload))
		config = windowsTopology(*image, *peerImage)
		spec.Nodes = map[string]*labv1.NodeExtension{"test": {Control: "qga"}}
		argv = []string{`C:\Labcontainers\calico.test.exe`, "-test.v", "-test.timeout=15m"}
	}
	source, err := clab.Source(config)
	if err != nil {
		return err
	}
	spec.Topology = source
	c, err := client.Launch(ctx, client.Options{LabdPath: os.Getenv("LABCONTAINERS_LABD")})
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, c.Close()) }()
	lab, err := c.Start(ctx, spec, 25*time.Minute)
	if err != nil {
		return err
	}
	if *scenario == "windows-tests" {
		_, err = lab.RunTimeline(ctx, &labv1.TimelineAction{Action: &labv1.TimelineAction_WaitExec{WaitExec: &labv1.WaitExec{
			Exec:          &labv1.ExecRequest{Node: &labv1.NodeRef{Node: "test"}, Argv: []string{`C:\Windows\System32\cmd.exe`, "/c", "ver"}, TimeoutMillis: 10000},
			TimeoutMillis: 600000, RetryMillis: 2000, StdoutContains: []byte("Windows"),
		}}})
		if err != nil {
			return err
		}
		if err := lab.Node("test").Put(ctx, argv[0], 0o700, payload); err != nil {
			return err
		}
	}
	result, err := c.RPC().Exec(ctx, &labv1.ExecRequest{Node: lab.Node("test").Ref(), Argv: argv, TimeoutMillis: 15 * 60 * 1000})
	if err != nil {
		return err
	}
	_, _ = os.Stdout.Write(result.GetStdout())
	_, _ = os.Stderr.Write(result.GetStderr())
	if result.GetExitCode() != 0 {
		return fmt.Errorf("Calico fork tests exited %d", result.GetExitCode())
	}
	return nil
}

func windowsTopology(image, peerImage string) *core.Config {
	return &core.Config{Name: "calico-fork", Topology: &types.Topology{
		Defaults: &types.NodeDefinition{ImagePullPolicy: "Never", NetworkMode: "none"},
		Nodes: map[string]*types.NodeDefinition{
			"test": {Kind: "generic_vm", Image: image},
			"peer": {Kind: "linux", Image: peerImage},
		},
		Links: []*links.LinkDefinition{{Link: &links.LinkBriefRaw{Endpoints: []string{"test:eth1", "peer:eth1"}}}},
	}}
}
