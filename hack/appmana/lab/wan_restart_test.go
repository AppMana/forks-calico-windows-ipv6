package main

import (
	"context"
	"os"
	"testing"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/client"
	clab "github.com/appmana/labcontainers/pkg/containerlab"
	"github.com/srl-labs/containerlab/links"
	"github.com/srl-labs/containerlab/types"
)

// Exercise the WAN startup contract without Kubernetes, Calico or VM boot.
// The actual VyOS/BGP dataplane remains covered by TestRetainedVyOSRoutes.
func TestLiveWANStartupSurvivesRestart(t *testing.T) {
	if os.Getenv("LABCONTAINERS_WAN_RESTART") != "1" {
		t.Skip("explicit native WAN restart qualification required")
	}
	image, daemon := os.Getenv("LABCONTAINERS_WAN_IMAGE"), os.Getenv("LABCONTAINERS_LABD")
	if image == "" || daemon == "" {
		t.Fatal("explicit offline WAN image and matching daemon required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	c, err := client.Launch(ctx, client.Options{LabdPath: daemon})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()
	cfg := qualificationVyOSTopology(&types.NodeDefinition{}, &types.NodeDefinition{}, image, "unused")
	// No outside access is needed for this two-container component test.
	cfg.Mgmt = nil
	delete(cfg.Topology.Nodes, "linux")
	delete(cfg.Topology.Nodes, "windows")
	delete(cfg.Topology.Nodes, "tor")
	cfg.Topology.Nodes["gateway"].NetworkMode = "none"
	cfg.Topology.Nodes["peer"] = &types.NodeDefinition{Kind: "linux", Image: image, ImagePullPolicy: "Never", NetworkMode: "none", Entrypoint: "/bin/sleep", Cmd: "infinity"}
	cfg.Topology.Links = []*links.LinkDefinition{{Link: &links.LinkBriefRaw{Endpoints: []string{"gateway:eth1", "peer:eth1"}}}}
	source, err := clab.Source(cfg)
	if err != nil {
		t.Fatal(err)
	}
	lab, err := c.Start(ctx, &labv1.LabSpec{Topology: source}, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	wan := lab.Node("gateway")
	assert := func() {
		t.Helper()
		var last error
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
			_, last = wan.Commands().Exec(ctx, "sh", "-ec", `
ip -4 addr show dev eth1 | grep -q '198.18.0.1/30'
ip -6 addr show dev eth1 | grep -q '2001:db8:ffff::1/64'
ip -4 route show 10.244.0.0/16 | grep -q 'via 198.18.0.2'
ip -6 route show 2001:db8:100::/56 | grep -q 'via 2001:db8:ffff::2'
iptables -S FORWARD | grep -q -- '-P FORWARD DROP'
iptables -t nat -C POSTROUTING -s 198.18.0.2/32 -o eth0 -j MASQUERADE
`)
			if last == nil {
				return
			}
		}
		t.Fatalf("startup network state absent: %v", last)
	}
	assert()
	if err := wan.Crash(ctx); err != nil {
		t.Fatal(err)
	}
	if err := wan.Start(ctx); err != nil {
		t.Fatal(err)
	}
	assert()
	t.Log("WAN_STARTUP_RESTART_COMPLETE: no caller-side address or route repair")
}
