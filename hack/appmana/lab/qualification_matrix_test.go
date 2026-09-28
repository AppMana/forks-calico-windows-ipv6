package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	matrix "github.com/appmana/labcontainers/pkg/kubernetes"
	k0s "github.com/appmana/labcontainers/pkg/kubernetes/k0s"
	native "github.com/k0sproject/k0s/pkg/apis/k0s/v1beta1"
)

func TestQualificationRejectsUnsupportedTupleBeforeMediaOrVM(t *testing.T) {
	for _, entry := range []string{"LABCONTAINERS_KUBERNETES_CNI=bogus", "LABCONTAINERS_KUBERNETES_CNI=kuberouter", "LABCONTAINERS_KUBERNETES_CNI=calico-bgp", "LABCONTAINERS_KUBERNETES_DISTRIBUTION=rke2", "LABCONTAINERS_KUBERNETES_VERSION=1.35.0"} {
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

func TestQualificationBGPCandidateNeverInventsRRASReadiness(t *testing.T) {
	tuple, err := readQualificationTuple(func(key string) string {
		if key == "LABCONTAINERS_KUBERNETES_CNI" {
			return "calico-bgp"
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "no pinned RRAS artifact") {
		t.Fatalf("unprepared BGP accepted: %v", err)
	}
	if tuple.Linux.DistributionBinary.SourceRevision != bgpK0sSource || tuple.WindowsBinary.SourceRevision != bgpK0sSource || tuple.Linux.DistributionBinary.Version != "v1.36.2+k0s.0.appmana.2a2a088" {
		t.Fatalf("lost fork provenance: %+v", tuple)
	}
	if tuple.Linux.WindowsBGP.RRASTooling.SHA256 != "" {
		t.Fatal("fabricated RRAS pin")
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
