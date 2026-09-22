package main

import (
	"reflect"
	"testing"

	clab "github.com/appmana/labcontainers/pkg/containerlab"
	"github.com/appmana/labcontainers/pkg/spec"
	"github.com/srl-labs/containerlab/links"
)

func TestWindowsTopologyHasOnlyExplicitConnectivity(t *testing.T) {
	config := windowsTopology("local/windows:qualified", "local/peer:qualified")
	topology := config.Topology
	if topology.Defaults.NetworkMode != "none" || topology.Defaults.ImagePullPolicy != "Never" {
		t.Fatal("topology permits implicit network attachment or image downloads")
	}
	if len(topology.Nodes) != 2 || topology.Nodes["test"].Image != "local/windows:qualified" || topology.Nodes["peer"].Image != "local/peer:qualified" {
		t.Fatal("caller image selection changed or undeclared nodes added")
	}
	if len(topology.Links) != 1 {
		t.Fatalf("expected one explicit cable, got %d", len(topology.Links))
	}
	link, ok := topology.Links[0].Link.(*links.LinkBriefRaw)
	if !ok || !reflect.DeepEqual(link.Endpoints, []string{"test:eth1", "peer:eth1"}) {
		t.Fatal("unexpected connection")
	}
	source, err := clab.Source(config)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := spec.Prepare(source.GetYaml(), "runner-test", "runner-id", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.IsolatedNodes) != 2 {
		t.Fatalf("both wrappers must receive runtime isolation checks: %v", prepared.IsolatedNodes)
	}
	// Preparation must not mutate the caller's native objects.
	if config.Name != "calico-fork" || topology.Nodes["test"].NetworkMode != "" {
		t.Fatal("preparation mutated caller configuration")
	}
}
