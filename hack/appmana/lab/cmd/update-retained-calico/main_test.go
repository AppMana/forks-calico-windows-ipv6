package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	native "github.com/k0sproject/k0s/pkg/apis/k0s/v1beta1"
	"sigs.k8s.io/yaml"
)

func TestRejectAdapterOwnedWindowsCalico(t *testing.T) {
	base := `{"kind":"DaemonSet","metadata":{"name":"calico-node-windows","namespace":"kube-system"}}`
	if err := validateDistributionOwnership([]byte(base)); err != nil {
		t.Fatal(err)
	}
	owned := `{"kind":"DaemonSet","metadata":{"name":"calico-node-windows","namespace":"kube-system","annotations":{"projectcalico.org/windows-adapter-owner":"calico-windows-adapter"}}}`
	if err := validateDistributionOwnership([]byte(owned)); err == nil || !strings.Contains(err.Error(), "render/plan/apply") {
		t.Fatalf("file-only update must reject adapter-owned resources: %v", err)
	}
	for _, invalid := range []string{`{}`, `not json`, strings.ReplaceAll(base, "calico-node-windows", "unrelated")} {
		if err := validateDistributionOwnership([]byte(invalid)); err == nil {
			t.Fatalf("accepted unidentified owner: %s", invalid)
		}
	}
}

func TestSerializedUpdateDoesNotInjectHostDefaults(t *testing.T) {
	before, after := strings.Repeat("a", 64), strings.Repeat("b", 64)
	cfg := &native.ClusterConfig{Spec: &native.ClusterSpec{Images: &native.ClusterImages{Calico: &native.CalicoImageSpec{Windows: &native.CalicoWindowsImageSpec{Node: &native.ImageSpec{Image: "ghcr.io/appmana/node", Version: "pinned@sha256:" + before}}}}}}
	original, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := updatedConfig(original, before, after)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := yaml.Unmarshal(updated, &got); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte(strings.ReplaceAll(string(original), before, after)), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serialization injected unrelated defaults: %s", updated)
	}
}

func TestReplaceImageChangesOnlyWindowsNodePin(t *testing.T) {
	before, after := strings.Repeat("a", 64), strings.Repeat("b", 64)
	cfg := &native.ClusterConfig{Spec: &native.ClusterSpec{Images: &native.ClusterImages{
		Calico: &native.CalicoImageSpec{Windows: &native.CalicoWindowsImageSpec{
			Node: &native.ImageSpec{Image: "ghcr.io/appmana/node", Version: "pinned@sha256:" + before},
			CNI:  &native.ImageSpec{Image: "untouched-cni", Version: "untouched"},
		}},
	}}}
	expected := cfg.DeepCopy()
	expected.Spec.Images.Calico.Windows.Node.Version = "pinned@sha256:" + after
	if err := replaceImage(cfg, before, after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, expected) {
		t.Fatal("changed configuration beyond Windows node image")
	}
	unchanged := cfg.DeepCopy()
	if err := replaceImage(cfg, before, after); err == nil {
		t.Fatal("accepted stale expected digest")
	}
	if !reflect.DeepEqual(cfg, unchanged) {
		t.Fatal("modified config on rejected update")
	}
	for _, bad := range []string{"", "latest", before[:63]} {
		if err := replaceImage(cfg, after, bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if err := replaceImage(&native.ClusterConfig{}, before, after); err == nil {
		t.Fatal("accepted missing image")
	}
}
