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

const qualificationK0sSource = "f1fa3492f13a6f0052da67cacb9ff6a59e148816"
const qualificationK0sVersion = "v1.36.2+k0s.0.appmana.f1fa349"

// These are built artifact identities, not claims that a network gate passed.
func qualificationCandidate(cni matrix.CNI) qualificationTuple {
	tuple := qualificationTuple{
		Linux: matrix.Selection{Distribution: matrix.DistributionK0s, KubernetesVersion: "1.36.2", CNI: cni, WindowsWorkers: true,
			DistributionBinary: matrix.ArtifactPin{Version: qualificationK0sVersion, SHA256: "820383940b69a9d4edf5173417b0dd1c74e544c5aaa809669615cae3d9d9b533", SourceRevision: qualificationK0sSource}},
		WindowsBinary: matrix.ArtifactPin{Version: qualificationK0sVersion, SHA256: "42bb84b933c320d07a33a25abf2acb0be5d53f3a8343cac7090596e50c34a653", SourceRevision: qualificationK0sSource},
	}
	// Both lanes preserve the legacy Windows Traefik replacement fix as well
	// as the BGP renderer and Windows strict-IPAM-affinity fixes.
	// The legacy appmana.1 executable is not vanilla;
	// neither relabel it nor substitute the BGP-only build that drops its fix.
	if cni == matrix.CNICalicoBGP {
		tuple.Linux.WindowsBGP = &matrix.WindowsBGPCapability{GeneratorSourceRevision: qualificationK0sSource,
			RRASTooling:        matrix.ArtifactPin{Version: "rras-prerequisites-v1", SHA256: rrasSHA256, SourceRevision: rrasSource},
			CalicoWindowsImage: matrix.ArtifactPin{Version: "calico-windows-96796e69c4", SHA256: "fe3a1f41f534a80c18fc8ce2d59d970f62e1fef48bcd22948b7de54dd2006197"}}
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
