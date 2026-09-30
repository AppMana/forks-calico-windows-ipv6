package main

import (
	"fmt"
	"strings"

	matrix "github.com/appmana/labcontainers/pkg/kubernetes"
)

type qualificationTuple struct {
	Linux         matrix.Selection
	WindowsBinary matrix.ArtifactPin
}

const qualificationK0sSource = "13893f0ab766ab03eafecaa4807ce6bf3bc59668"
const qualificationK0sVersion = "v1.36.2+k0s.0.appmana.13893f0"
const qualificationCalicoWindowsSource = "c847ba23777a7b1cc5fa803baca9f35f07932b1b"
const qualificationCalicoWindowsDigest = "0e3ea090330a98c509a6f5daf3bd7a04549c0b0c261d9222dbf0d0961535f94f"

// These are built artifact identities, not claims that a network gate passed.
func qualificationCandidate(cni matrix.CNI) qualificationTuple {
	tuple := qualificationTuple{
		Linux: matrix.Selection{Distribution: matrix.DistributionK0s, KubernetesVersion: "1.36.2", CNI: cni, WindowsWorkers: true,
			DistributionBinary: matrix.ArtifactPin{Version: qualificationK0sVersion, SHA256: "cfadfdf1b9056ac1cc1e205a697acc6f08645562a61d0d6c7a37f1beb6b9fdf4", SourceRevision: qualificationK0sSource}},
		WindowsBinary: matrix.ArtifactPin{Version: qualificationK0sVersion, SHA256: "7d38034e57a631f1f7fb2615eb858a400ed608aa5fcbdc790828db077a209226", SourceRevision: qualificationK0sSource},
	}
	// Both lanes preserve the legacy Windows Traefik replacement fix as well
	// as the BGP renderer and Windows strict-IPAM-affinity fixes.
	// The legacy appmana.1 executable is not vanilla;
	// neither relabel it nor substitute the BGP-only build that drops its fix.
	if cni == matrix.CNICalicoBGP {
		tuple.Linux.WindowsBGP = &matrix.WindowsBGPCapability{GeneratorSourceRevision: qualificationK0sSource,
			RRASTooling:        matrix.ArtifactPin{Version: "rras-prerequisites-v1", SHA256: rrasSHA256, SourceRevision: rrasSource},
			CalicoWindowsImage: matrix.ArtifactPin{Version: "calico-windows-" + qualificationCalicoWindowsSource[:10], SHA256: qualificationCalicoWindowsDigest, SourceRevision: qualificationCalicoWindowsSource}}
	}
	return tuple
}

func readQualificationTuple(getenv func(string) string) (qualificationTuple, error) {
	cni := matrix.CNICalicoVXLAN
	if value := getenv("LABCONTAINERS_KUBERNETES_CNI"); value != "" {
		cni = matrix.CNI(value)
	}
	tuple := qualificationCandidate(cni)
	if cni == matrix.CNICalicoBGP {
		if err := verifyRRASSource(); err != nil {
			return tuple, err
		}
	}
	if value := getenv("LABCONTAINERS_KUBERNETES_DISTRIBUTION"); value != "" {
		tuple.Linux.Distribution = matrix.Distribution(value)
	}
	if value := getenv("LABCONTAINERS_KUBERNETES_VERSION"); value != "" {
		tuple.Linux.KubernetesVersion = value
	}
	if err := tuple.Linux.Validate(); err != nil {
		return tuple, err
	}
	windows := tuple.Linux
	windows.DistributionBinary = tuple.WindowsBinary
	if err := windows.Validate(); err != nil {
		return tuple, fmt.Errorf("Windows distribution artifact: %w", err)
	}
	return tuple, nil
}

func verifyQualificationDigest(pin matrix.ArtifactPin, output string) error {
	fields := strings.Fields(output)
	if len(fields) != 2 || !strings.EqualFold(fields[0], pin.SHA256) {
		return fmt.Errorf("distribution artifact checksum mismatch for %s: %q", pin.Version, output)
	}
	return nil
}
