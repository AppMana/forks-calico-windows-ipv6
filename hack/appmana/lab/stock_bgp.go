package main

import (
	"crypto/sha256"
	"fmt"
	"os"

	matrix "github.com/appmana/labcontainers/pkg/kubernetes"
	"github.com/projectcalico/calico/windows-adapter/pkg/deploy"
)

func stockBGPOptions() deploy.WindowsBGPOptions {
	return deploy.WindowsBGPOptions{
		NodeImage: "ghcr.io/appmana/node@sha256:" + qualificationCalicoWindowsDigest,
		APIHost:   "192.0.2.10", APIPort: "6443", ServiceCIDR: "10.96.0.0/12", DNSAddress: "10.96.0.10", AutodetectionMethod: "can-reach=192.0.2.10",
		Offline: true,
	}
}

func stockBGPManifests() ([]byte, error) {
	plan, err := deploy.NewPlan(stockBGPOptions())
	if err != nil {
		return nil, err
	}
	return plan.Manifest(), nil
}

// A real stock-k0s qualification invokes the redistributable binary, rather
// than recreating its installation steps in a test-only shell script.
func stockBGPAdapterBinary(getenv func(string) string) ([]byte, error) {
	path, digest := getenv("LABCONTAINERS_CALICO_ADAPTER_BINARY"), getenv("LABCONTAINERS_CALICO_ADAPTER_SHA256")
	if path == "" || len(digest) != 64 {
		return nil, fmt.Errorf("stock BGP requires an explicit adapter binary and SHA256")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != digest {
		return nil, fmt.Errorf("adapter binary SHA256 mismatch")
	}
	return data, nil
}

func stockBGPAdapterArgs() []string {
	o := stockBGPOptions()
	return []string{"--node-image=" + o.NodeImage, "--api-host=" + o.APIHost, "--api-port=" + o.APIPort, "--service-cidr=" + o.ServiceCIDR, "--dns-address=" + o.DNSAddress, "--autodetection-method=" + o.AutodetectionMethod, "--offline"}
}

func useStockBGP(tuple *qualificationTuple) error {
	if tuple.Linux.CNI != matrix.CNICalicoBGP {
		return fmt.Errorf("stock declarative mode currently requires calico-bgp")
	}
	data, err := stockBGPManifests()
	if err != nil {
		return err
	}
	const source = "c37fe960bdee93ca14c5a5b9f2ab9700f8e82e60"
	tuple.Linux.DistributionBinary = matrix.ArtifactPin{Version: "v1.36.4+k0s.1", SHA256: "18c304d53cdd70095e99c6b859b269b4fef0bb84579d7e7185271a9135694a31", SourceRevision: source}
	tuple.WindowsBinary = matrix.ArtifactPin{Version: "v1.36.4+k0s.1", SHA256: "13806ad31bfce926ce8bbfbf073231a87fb5e97b9b9ed5dc5c4525101c3dc919", SourceRevision: source}
	tuple.Linux.WindowsBGP.GeneratorSourceRevision = ""
	tuple.Linux.WindowsBGP.DeclarativeManifests = &matrix.ArtifactPin{Version: "windows-bgp-3.32.2", SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), SourceRevision: "e92928653c45dd7e3ed63f915d6d9a50496270e5"}
	return tuple.Linux.WindowsBGP.VerifyDeclarativeManifests(data)
}
