package main

import (
	"encoding/json"
	"strings"
	"testing"

	native "github.com/k0sproject/k0s/pkg/apis/k0s/v1beta1"
	"github.com/projectcalico/calico/windows-adapter/pkg/deploy"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestNetworkReleaseK0sWireContract(t *testing.T) {
	pin := func(name, version string) deploy.ImagePin {
		return deploy.ImagePin{Reference: "registry.example/" + name + "@sha256:" + strings.Repeat("a", 64), UpstreamVersion: version, SourceRevision: strings.Repeat("b", 40)}
	}
	r := deploy.Release{SchemaVersion: 1, KubernetesVersion: "v1.36.4", CalicoVersion: "v3.32.2", CalicoNode: pin("node", "v3.32.2"), CalicoWindowsNode: pin("windows-node", "v3.32.2"), CalicoCNI: pin("cni", "v3.32.2"), CalicoWindowsCNI: pin("windows-cni", "v3.32.2"), CalicoControllers: pin("controllers", "v3.32.2"), KubeProxyLinux: pin("linux-proxy", "v1.36.4"), KubeProxyWindows: pin("windows-proxy", "v1.36.4")}
	fragment, err := r.K0sImages(true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(fragment)
	if err != nil {
		t.Fatal(err)
	}
	images := native.DefaultClusterImages()
	dns, pause := *images.CoreDNS, *images.Pause
	if err := json.Unmarshal(data, images); err != nil {
		t.Fatal(err)
	}
	if errs := images.Validate(field.NewPath("spec", "images")); len(errs) != 0 {
		t.Fatal(errs)
	}
	if *images.CoreDNS != dns || *images.Pause != pause {
		t.Fatal("network overlay changed unrelated image inputs")
	}
	for _, entry := range []struct {
		image *native.ImageSpec
		pin   deploy.ImagePin
	}{
		{images.KubeProxy, r.KubeProxyLinux}, {images.Windows.KubeProxy, r.KubeProxyWindows},
		{images.Calico.Node, r.CalicoNode}, {images.Calico.Windows.Node, r.CalicoWindowsNode},
		{images.Calico.CNI, r.CalicoCNI}, {images.Calico.Windows.CNI, r.CalicoWindowsCNI},
		{images.Calico.KubeControllers, r.CalicoControllers},
	} {
		if entry.image.URI() != strings.Replace(entry.pin.Reference, "@", ":pinned@", 1) {
			t.Fatalf("distribution altered image identity: %s", entry.image.URI())
		}
	}
}
