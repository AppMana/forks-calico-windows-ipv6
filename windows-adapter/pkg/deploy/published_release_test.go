package deploy

import (
	"os"
	"testing"
)

func TestPublishedQualificationRelease(t *testing.T) {
	f, err := os.Open("../../releases/k0s-1.36.4-calico-3.32.2-a20459b.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	release, err := ReadRelease(f)
	if err != nil {
		t.Fatal(err)
	}
	if release.KubernetesVersion != "v1.36.4" || release.CalicoVersion != "v3.32.2" {
		t.Fatal("unexpected upstream bases")
	}
	if release.CalicoNode.SourceRevision != "a20459b114ade0a33cf42ed0080bfe05141422b5" {
		t.Fatal("not the published qualified-build candidate")
	}
	if release.KubeProxyLinux.SourceRevision == release.KubeProxyWindows.SourceRevision {
		t.Fatal("upstream Linux and fork Windows provenance must remain distinct")
	}
	for _, offline := range []bool{false, true} {
		if _, err := release.K0sImages(offline); err != nil {
			t.Fatal(err)
		}
	}
	// This verifies the lock and renderer contract, not live networking.
	options := options()
	options.NodeImage = ""
	options, err = release.WindowsOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPlan(options); err != nil {
		t.Fatal(err)
	}
}
