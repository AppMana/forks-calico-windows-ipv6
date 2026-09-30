package main

import (
	"encoding/json"
	"strings"
	"testing"
)

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
	if tuple.Linux.DistributionBinary.Version != "v1.36.4+k0s.1" || tuple.WindowsBinary.Version != "v1.36.4+k0s.1" {
		t.Fatal("stock mode selected patched binaries")
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
