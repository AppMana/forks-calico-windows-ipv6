package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	matrix "github.com/appmana/labcontainers/pkg/kubernetes"
	"github.com/projectcalico/calico/hack/appmana/lab/deploy"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func stockBGPManifests() ([]byte, error) {
	objects, err := deploy.WindowsBGP(deploy.WindowsBGPOptions{
		NodeImage: "ghcr.io/appmana/node@sha256:" + qualificationCalicoWindowsDigest,
		APIHost:   "192.0.2.10", APIPort: "6443", ServiceCIDR: "10.96.0.0/12", DNSAddress: "10.96.0.10", AutodetectionMethod: "can-reach=192.0.2.10",
	})
	if err != nil {
		return nil, err
	}
	list := &meta.List{TypeMeta: meta.TypeMeta{APIVersion: "v1", Kind: "List"}}
	for _, object := range objects {
		list.Items = append(list.Items, runtime.RawExtension{Object: object})
	}
	return json.Marshal(list)
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
