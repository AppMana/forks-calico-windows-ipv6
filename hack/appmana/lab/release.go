package main

import (
	"encoding/json"
	"fmt"
	"os"

	native "github.com/k0sproject/k0s/pkg/apis/k0s/v1beta1"
	"github.com/projectcalico/calico/windows-adapter/pkg/deploy"
)

// The deployment utility and live fixture consume the same reviewed release.
// Updating this selection does not claim that the selected images passed a lab.
const qualificationReleasePath = "../../../windows-adapter/releases/k0s-1.36.4-calico-3.32.2-b6d7701.json"

var qualificationNetworkRelease = loadQualificationRelease()

func loadQualificationRelease() deploy.Release {
	f, err := os.Open(qualificationReleasePath)
	if err != nil {
		panic(fmt.Errorf("qualification release: %w", err))
	}
	defer f.Close()
	r, err := deploy.ReadRelease(f)
	if err != nil {
		panic(fmt.Errorf("qualification release: %w", err))
	}
	return r
}

func applyQualificationImages(images *native.ClusterImages) error {
	inputs, err := qualificationNetworkRelease.K0sImages(true)
	if err != nil {
		return err
	}
	data, err := json.Marshal(inputs)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, images)
}
