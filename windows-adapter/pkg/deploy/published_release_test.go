package deploy

import (
	"os"
	"testing"
)

func TestPublishedQualificationRelease(t *testing.T) {
	for _, tc := range []struct {
		calicoVersion string
		revision      string
	}{
		{"v3.32.2", "a20459b114ade0a33cf42ed0080bfe05141422b5"},
		{"v3.32.2", "cedaccf032194eb62122f829893aae78e08fa7d1"},
		{"v3.32.2", "04b64e49943e41a3e32845323dbc52568040262a"},
		{"v3.32.2", "3c0b4e73a063e0691ebe438ebf47f91615fbc37f"},
		{"v3.32.2", "78c36e290c954874c684b7e53475e1a0281cb279"},
		{"v3.32.2", "6ce4ce16fa52f9a02eaaa7de8e872493337c8436"},
		{"v3.32.2", "b6d7701d30565d6695e77e3fc717a7f4a472f8f0"},
		{"v3.32.2", "9c44cea69e6047e7dd34b523c3675c5c95018aa6"},
		{"v3.33.0", "a0d5d6b708ac2de565a134a292e295ef71a60a2f"},
	} {
		t.Run(tc.revision[:7], func(t *testing.T) {
			testPublishedQualificationRelease(t, tc.calicoVersion, tc.revision)
		})
	}
}

func testPublishedQualificationRelease(t *testing.T, calicoVersion, revision string) {
	t.Helper()
	f, err := os.Open("../../releases/k0s-1.36.4-calico-" + calicoVersion[1:] + "-" + revision[:7] + ".json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	release, err := ReadRelease(f)
	if err != nil {
		t.Fatal(err)
	}
	if release.KubernetesVersion != "v1.36.4" || release.CalicoVersion != calicoVersion {
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
