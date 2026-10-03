package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/appmana/labcontainers/pkg/client"
	clab "github.com/appmana/labcontainers/pkg/containerlab"
	"github.com/srl-labs/containerlab/core"
	"gopkg.in/yaml.v2"
)

// Explicit provisioning migration, separate from read-only route qualification.
// Read the daemon's existing native topology; never reconstruct VM definitions.
func TestRetainedWANStartupMigration(t *testing.T) {
	path := os.Getenv("LABCONTAINERS_RETAINED_WAN_TOPOLOGY")
	if path == "" {
		t.Skip("explicit existing native topology required")
	}
	socket, id := os.Getenv("LABCONTAINERS_RETAINED_SOCKET"), os.Getenv("LABCONTAINERS_RETAINED_SESSION")
	if socket == "" || id == "" {
		t.Fatal("explicit retained socket and session required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg core.Config
	if err := yaml.UnmarshalStrict(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Topology == nil || cfg.Topology.Nodes["gateway"] == nil || cfg.Topology.Nodes["tor"] == nil {
		t.Fatal("not the retained VyOS topology")
	}
	wan := cfg.Topology.Nodes["gateway"]
	if wan.Kind != "linux" || wan.Entrypoint != "/bin/sleep" || wan.Cmd != "infinity" {
		t.Fatal("unexpected WAN startup; refuse overwrite")
	}
	wan.Entrypoint = "/bin/sh"
	wan.Cmd = "-ec '" + strings.ReplaceAll(vyosWANStartup, "'", "'\"'\"'") + "'"
	source, err := clab.Source(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c, err := client.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lab, err := c.Resume(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := lab.Keep(ctx, 4*time.Hour); err != nil {
		t.Fatal(err)
	}
	plan, err := lab.Plan(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("native gateway startup migration preview: %+v", plan)
	if plan.DeployedLab || len(plan.AddedNodes)+len(plan.DeletedNodes)+len(plan.RestartedNodes)+len(plan.StartedNodes) != 0 || len(plan.RecreatedNodes) != 1 || plan.RecreatedNodes[0] != "gateway" {
		t.Fatal("plan affects more than the explicitly selected WAN container")
	}
	for _, link := range plan.AddedLinks {
		if link != "gateway:eth1 -- tor:eth3" && link != "tor:eth3 -- gateway:eth1" {
			t.Fatalf("unexpected link change: %s", link)
		}
	}
	for _, endpoint := range plan.DeletedEndpoints {
		if endpoint != "gateway:eth1" && endpoint != "tor:eth3" {
			t.Fatalf("unexpected endpoint change: %s", endpoint)
		}
	}
	if os.Getenv("LABCONTAINERS_RETAINED_WAN_APPLY") != "1" {
		t.Log("preview only; no configuration applied")
		return
	}
	if err := lab.Apply(ctx, source, plan, nil); err != nil {
		t.Fatal(err)
	}
	t.Log("WAN_STARTUP_MIGRATION_APPLIED; route qualification remains separate")
}
