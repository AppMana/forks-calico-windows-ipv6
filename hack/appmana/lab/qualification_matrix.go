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

const bgpK0sSource = "2a2a0880d35d8dfc5eb7eab58509da4611280648"

// These are built artifact identities, not claims that a network gate passed.
func qualificationCandidate(cni matrix.CNI) qualificationTuple {
	tuple := qualificationTuple{
		Linux: matrix.Selection{Distribution: matrix.DistributionK0s, KubernetesVersion: "1.36.2", CNI: cni, WindowsWorkers: true,
			DistributionBinary: matrix.ArtifactPin{Version: "v1.36.2+k0s.0", SHA256: "8b5d985f803df27acb44f900b2574a5b48e600bd2f87a3335d2a853b888e9298"}},
		WindowsBinary: matrix.ArtifactPin{Version: "v1.36.2+k0s.0", SHA256: "ee46a95bde767f65fd7472173b0028233adc8431c2c208931b82df5e88009e3e"},
	}
	if cni == matrix.CNICalicoBGP {
		tuple.Linux.DistributionBinary = matrix.ArtifactPin{Version: "v1.36.2+k0s.0.appmana.2a2a088", SHA256: "2a85fe00cd0fc0eda557c572307f251e87a6c34059105d15416a73c633ea7c43", SourceRevision: bgpK0sSource}
		tuple.WindowsBinary = matrix.ArtifactPin{Version: tuple.Linux.DistributionBinary.Version, SHA256: "2409b0f2e69b8f11e26bcedaddf525ae49111f25134359c1d0fc113d6b00e7a3", SourceRevision: bgpK0sSource}
		tuple.Linux.WindowsBGP = &matrix.WindowsBGPCapability{GeneratorSourceRevision: bgpK0sSource,
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
