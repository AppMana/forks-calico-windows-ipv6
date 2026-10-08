package deploy

import (
	"os"
	"testing"
)

func TestPublishedQualificationRelease(t *testing.T) {
	for _, revision := range []string{
		"a20459b114ade0a33cf42ed0080bfe05141422b5",
		"cedaccf032194eb62122f829893aae78e08fa7d1",
		"04b64e49943e41a3e32845323dbc52568040262a",
		"3c0b4e73a063e0691ebe438ebf47f91615fbc37f",
		"78c36e290c954874c684b7e53475e1a0281cb279",
		"6ce4ce16fa52f9a02eaaa7de8e872493337c8436",
		"b6d7701d30565d6695e77e3fc717a7f4a472f8f0",
		"9c44cea69e6047e7dd34b523c3675c5c95018aa6",
	} {
		t.Run(revision[:7], func(t *testing.T) {
			testPublishedQualificationRelease(t, revision)
		})
	}
}

func testPublishedQualificationRelease(t *testing.T, revision string) {
	t.Helper()
	f, err := os.Open("../../releases/k0s-1.36.4-calico-3.32.2-" + revision[:7] + ".json")
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
	if release.CalicoNode.SourceRevision != revision {
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
