package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	matrix "github.com/appmana/labcontainers/pkg/kubernetes"
	k0s "github.com/appmana/labcontainers/pkg/kubernetes/k0s"
	native "github.com/k0sproject/k0s/pkg/apis/k0s/v1beta1"
)

func TestBGPCapabilityPinsActualRRASProvisioner(t *testing.T) {
	script, err := os.ReadFile("rras-prerequisites.ps1")
	if err != nil {
		t.Fatal(err)
	}
	tuple := qualificationCandidate(matrix.CNICalicoBGP)
	if got, want := tuple.Linux.WindowsBGP.RRASTooling.SHA256, fmt.Sprintf("%x", sha256.Sum256(script)); got != want {
		t.Fatalf("RRAS script is not pinned: got %q want %s", got, want)
	}
}

func TestQualificationRejectsUnsupportedTupleBeforeMediaOrVM(t *testing.T) {
	for _, entry := range []string{"LABCONTAINERS_KUBERNETES_CNI=bogus", "LABCONTAINERS_KUBERNETES_CNI=kuberouter", "LABCONTAINERS_KUBERNETES_DISTRIBUTION=rke2", "LABCONTAINERS_KUBERNETES_VERSION=1.35.0"} {
		t.Run(entry, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestLiveK0sWindowsNetwork$", "-test.v")
			for _, env := range os.Environ() {
				if !strings.HasPrefix(env, "LABCONTAINERS_") {
					cmd.Env = append(cmd.Env, env)
				}
			}
			cmd.Env = append(cmd.Env, entry)
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "qualification matrix:") {
				t.Fatalf("invalid tuple was not rejected before media/VM: %v\n%s", err, out)
			}
		})
	}
}

func TestQualificationDefaultTupleAndNativeNetwork(t *testing.T) {
	tuple, err := readQualificationTuple(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if tuple.Linux.CNI != matrix.CNICalicoVXLAN || !tuple.Linux.WindowsWorkers || tuple.Linux.KubernetesVersion != "1.36.2" || tuple.WindowsBinary.Version != tuple.Linux.DistributionBinary.Version {
		t.Fatalf("unexpected default: %+v", tuple)
	}
	cfg := &native.ClusterConfig{Spec: &native.ClusterSpec{Network: &native.Network{Calico: &native.Calico{MTU: 1450, VxlanVNI: 4096}}}}
	if err := k0s.ConfigureNetwork(cfg, tuple.Linux); err != nil {
		t.Fatal(err)
	}
	if cfg.Spec.Network.Calico.Mode != native.CalicoModeVXLAN || cfg.Spec.Network.Calico.Overlay != "Always" || cfg.Spec.Network.Calico.MTU != 1450 || cfg.Spec.Network.Calico.VxlanVNI != 4096 {
		t.Fatal(cfg.Spec.Network)
	}
}

func TestQualificationBGPCandidatePinsPrerequisitesWithoutClaimingReadiness(t *testing.T) {
	tuple, err := readQualificationTuple(func(key string) string {
		if key == "LABCONTAINERS_KUBERNETES_CNI" {
			return "calico-bgp"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if tuple.Linux.DistributionBinary.SourceRevision != qualificationK0sSource || tuple.WindowsBinary.SourceRevision != qualificationK0sSource || tuple.Linux.DistributionBinary.Version != "v1.36.2+k0s.0.appmana.13893f0" {
		t.Fatalf("lost fork provenance: %+v", tuple)
	}
	if tuple.Linux.WindowsBGP.RRASTooling.SourceRevision != rrasSource {
		t.Fatal("RRAS source pin missing")
	}
	cfg := &native.ClusterConfig{Spec: &native.ClusterSpec{Network: &native.Network{}}}
	if err := k0s.ConfigureNetwork(cfg, tuple.Linux); err != nil {
		t.Fatal(err)
	}
	if cfg.Spec.Network.Calico.Mode != native.CalicoModeBIRD || cfg.Spec.Network.Calico.Overlay != "Never" {
		t.Fatal(cfg.Spec.Network)
	}
}

func TestQualificationDigestChecksActualOutput(t *testing.T) {
	tuple := qualificationCandidate(matrix.CNICalicoVXLAN)
	for _, pin := range []matrix.ArtifactPin{tuple.Linux.DistributionBinary, tuple.WindowsBinary} {
		if err := verifyQualificationDigest(pin, strings.ToUpper(pin.SHA256)+"  k0s\r\n"); err != nil {
			t.Fatal(err)
		}
		for _, out := range []string{"", pin.SHA256, strings.Repeat("0", 64) + " k0s", pin.SHA256 + " k0s extra"} {
			if err := verifyQualificationDigest(pin, out); err == nil {
				t.Fatalf("accepted %q", out)
			}
		}
	}
}

func TestQualificationArtifactVersionMatchesMeasuredBytes(t *testing.T) {
	// The legacy Windows byte stream passed its digest gate in real VM
	// ea90397286e038d6d92de27203968b5e, then reported appmana.1 rather than
	// vanilla k0s. Keep that identity distinct from reproducible new builds.
	versions := map[string]string{
		// Linux version executed; Windows embedded version inspected. The live
		// fixture must additionally execute and verify both binaries in guests.
		"cfadfdf1b9056ac1cc1e205a697acc6f08645562a61d0d6c7a37f1beb6b9fdf4": "v1.36.2+k0s.0.appmana.13893f0",
		"7d38034e57a631f1f7fb2615eb858a400ed608aa5fcbdc790828db077a209226": "v1.36.2+k0s.0.appmana.13893f0",
		"8b5d985f803df27acb44f900b2574a5b48e600bd2f87a3335d2a853b888e9298": "v1.36.2+k0s.0",
		"ee46a95bde767f65fd7472173b0028233adc8431c2c208931b82df5e88009e3e": "v1.36.2+k0s.0.appmana.1",
		"2a85fe00cd0fc0eda557c572307f251e87a6c34059105d15416a73c633ea7c43": "v1.36.2+k0s.0.appmana.2a2a088",
		"2409b0f2e69b8f11e26bcedaddf525ae49111f25134359c1d0fc113d6b00e7a3": "v1.36.2+k0s.0.appmana.2a2a088",
		"663374a3bbadcb4172474fb1d1180d6d6c02259d7e7373dfe8fad05cb287a72a": "v1.36.2+k0s.0.appmana.7c95b42",
		"a28f4a03b47ad898f225abc96a3056616c4cc2cbb760b7c8d2d9ae5dfa042d69": "v1.36.2+k0s.0.appmana.7c95b42",
		"820383940b69a9d4edf5173417b0dd1c74e544c5aaa809669615cae3d9d9b533": "v1.36.2+k0s.0.appmana.f1fa349",
		"42bb84b933c320d07a33a25abf2acb0be5d53f3a8343cac7090596e50c34a653": "v1.36.2+k0s.0.appmana.f1fa349",
	}
	for _, cni := range []matrix.CNI{matrix.CNICalicoVXLAN, matrix.CNICalicoBGP} {
		tuple := qualificationCandidate(cni)
		for _, pin := range []matrix.ArtifactPin{tuple.Linux.DistributionBinary, tuple.WindowsBinary} {
			want, known := versions[pin.SHA256]
			if !known || pin.Version != want {
				t.Errorf("%s artifact %s labels version %q; measured identity is %q (known=%v)", cni, pin.SHA256, pin.Version, want, known)
			}
		}
	}
}
