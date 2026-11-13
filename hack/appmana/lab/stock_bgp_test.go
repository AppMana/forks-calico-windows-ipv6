package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStockAdapterRequiresVerifiedBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adapter")
	manifest, err := stockBGPManifests()
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("#!/bin/sh\ncat <<'EXPECTED_NATIVE_MANIFEST'\n" + string(manifest) + "\nEXPECTED_NATIVE_MANIFEST\n")
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	get := func(key string) string {
		if key == "LABCONTAINERS_CALICO_ADAPTER_BINARY" {
			return path
		}
		return digest
	}
	if _, err := stockBGPAdapterBinary(get); err != nil {
		t.Fatal(err)
	}
	stale := []byte("#!/bin/sh\nprintf '{}\\n'\n")
	if err := os.WriteFile(path, stale, 0700); err != nil {
		t.Fatal(err)
	}
	digest = fmt.Sprintf("%x", sha256.Sum256(stale))
	if _, err := stockBGPAdapterBinary(get); err == nil {
		t.Fatal("checksum-valid stale adapter reached VM provisioning")
	}
	digest = strings.Repeat("0", 64)
	if _, err := stockBGPAdapterBinary(get); err == nil {
		t.Fatal("accepted wrong binary bytes")
	}
	if _, err := stockBGPAdapterBinary(func(string) string { return "" }); err == nil {
		t.Fatal("silently bypassed standalone adapter")
	}
}

func TestStockBGPSelectionUsesStockBytesAndSeparateOwner(t *testing.T) {
	tuple, err := readQualificationTuple(func(key string) string {
		switch key {
		case "LABCONTAINERS_KUBERNETES_CNI":
			return "calico-bgp"
		case "LABCONTAINERS_K0S_DEPLOYMENT":
			return "stock-declarative"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if tuple.Linux.DistributionBinary.Version != "v1.37.2+k0s.1" || tuple.WindowsBinary.Version != "v1.37.2+k0s.1" {
		t.Fatal("stock mode selected patched binaries")
	}
	if tuple.Linux.DistributionBinary.SHA256 != "4770cf89ff45e43292819f6e3a02a25a453bc750c777f6f7ad2de335f3f41df5" || tuple.WindowsBinary.SHA256 != "11e1b352148db2eeec5684156ef303aca8044b7048fd4be6753a9413f6ab9508" {
		t.Fatal("stock binaries are not the upstream release checksums")
	}
	if "v"+tuple.Linux.KubernetesVersion != qualificationNetworkRelease.KubernetesVersion {
		t.Fatalf("stock k0s runs Kubernetes %s; the release lock is for %s", tuple.Linux.KubernetesVersion, qualificationNetworkRelease.KubernetesVersion)
	}
	cap := tuple.Linux.WindowsBGP
	if cap.GeneratorSourceRevision != "" || cap.DeclarativeManifests == nil {
		t.Fatal("stock mode claims a patched generator")
	}
	data, err := stockBGPManifests()
	if err != nil {
		t.Fatal(err)
	}
	if err := cap.VerifyDeclarativeManifests(data); err != nil {
		t.Fatal(err)
	}
	var list struct{ Items []json.RawMessage }
	if err := json.Unmarshal(data, &list); err != nil || len(list.Items) != 3 {
		t.Fatal("invalid native resource list", err)
	}
	if !strings.Contains(string(data), qualificationCalicoWindowsDigest) {
		t.Fatal("manifest lost candidate image")
	}
	if err := cap.VerifyDeclarativeManifests(append(data, '\n')); err == nil {
		t.Fatal("accepted changed deployment bytes")
	}
}

func TestStockDeploymentRejectsImplicitFallback(t *testing.T) {
	for _, mode := range []string{"stock-declarative", "typo"} {
		_, err := readQualificationTuple(func(key string) string {
			if key == "LABCONTAINERS_K0S_DEPLOYMENT" {
				return mode
			}
			return ""
		})
		if err == nil {
			t.Fatal("accepted unknown or unsupported deployment mode", mode)
		}
	}
}
