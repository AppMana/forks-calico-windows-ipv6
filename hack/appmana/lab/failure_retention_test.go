package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/srl-labs/containerlab/types"
)

func TestRetainFailurePowersOffDeclaredTopology(t *testing.T) {
	for _, withToR := range []bool{false, true} {
		cfg := qualificationTopology(&types.NodeDefinition{}, &types.NodeDefinition{}, "wan:test")
		if withToR {
			cfg = qualificationVyOSTopology(&types.NodeDefinition{}, &types.NodeDefinition{}, "wan:test", "vyos:test")
		}
		for _, failure := range []string{"", "keep", "linux", "windows", "gateway"} {
			stopped := map[string]bool{}
			err := retainFailedQualification(func() error {
				if failure == "keep" {
					return errors.New(failure)
				}
				return nil
			}, func(name string) error {
				stopped[name] = true
				if name == failure {
					return errors.New(failure)
				}
				return nil
			}, cfg.Topology.Nodes)
			for name := range cfg.Topology.Nodes {
				if !stopped[name] {
					t.Errorf("router=%v failure=%q: node %s left running", withToR, failure, name)
				}
			}
			if (err != nil) != (failure != "") {
				t.Errorf("failure=%q error=%v", failure, err)
			}
		}
	}
}

func TestRetainFailurePowersOffBothNodes(t *testing.T) {
	for _, failure := range []string{"", "keep", "linux", "windows"} {
		var got []string
		step := func(name string) error {
			got = append(got, name)
			if name == failure {
				return errors.New(name)
			}
			return nil
		}
		err := retainFailedQualification(func() error { return step("keep") }, step, map[string]*types.NodeDefinition{"linux": {}, "windows": {}})
		if !reflect.DeepEqual(got, []string{"keep", "linux", "windows"}) {
			t.Fatalf("incomplete cleanup: %v", got)
		}
		if (err != nil) != (failure != "") {
			t.Fatalf("failure=%s err=%v", failure, err)
		}
	}
}
