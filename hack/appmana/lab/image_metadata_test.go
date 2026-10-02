package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestPlatformBuildsAreIndependentAndPublicationIsFullyGated(t *testing.T) {
	data, err := os.ReadFile("../../../.github/workflows/build-images.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Needs []string `json:"needs"`
		} `json:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	win := workflow.Jobs["build-windows-image"].Needs
	for _, required := range []string{"metadata", "test-windows"} {
		if !slices.Contains(win, required) {
			t.Errorf("Windows candidate build lost %s", required)
		}
	}
	for _, unnecessary := range []string{"build-linux-image", "test-linux-all"} {
		if slices.Contains(win, unnecessary) {
			t.Errorf("Windows packaging unnecessarily waits for %s", unnecessary)
		}
	}
	publish := workflow.Jobs["publish-manifest"].Needs
	for _, required := range []string{"metadata", "build-linux-image", "build-windows-image", "test-linux-all", "test-linux-appmana", "test-native-lab-contracts", "test-windows", "build-windows-adapter"} {
		if !slices.Contains(publish, required) {
			t.Errorf("final publication must explicitly require %s", required)
		}
	}
}

// Execute the actual workflow metadata script, not a reimplementation. Branch
// builds must never race to overwrite another revision's platform image tags.
func TestImageMetadataPinsEveryBranchBuild(t *testing.T) {
	data, err := os.ReadFile("../../../.github/workflows/build-images.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Env  map[string]string `json:"env"`
		Jobs map[string]struct {
			Steps []struct {
				ID  string `json:"id"`
				Run string `json:"run"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	script := ""
	for _, step := range workflow.Jobs["metadata"].Steps {
		if step.ID == "meta" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("missing actual metadata step")
	}
	for _, tc := range []struct {
		refType, ref, suffix string
		fail                 bool
	}{
		{"branch", workflow.Env["RELEASE_BRANCH"], "-" + workflow.Env["RELEASE_BRANCH"] + "-0123456789ab", false},
		{"branch", "feature/test", "-feature-test-0123456789ab", false},
		{"tag", workflow.Env["IMAGE_TAG"], "", false},
		{"tag", "v0.0.0-wrong", "", true},
	} {
		t.Run(tc.refType+"/"+tc.ref, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "outputs")
			cmd := exec.Command("bash", "-euo", "pipefail", "-c", script)
			cmd.Env = append(os.Environ(), "GITHUB_REF_TYPE="+tc.refType, "GITHUB_REF_NAME="+tc.ref, "GITHUB_SHA=0123456789abcdef0123456789abcdef01234567", "GITHUB_OUTPUT="+output)
			for key, value := range workflow.Env {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			out, err := cmd.CombinedOutput()
			if tc.fail {
				if err == nil {
					t.Fatal("accepted mismatched release tag")
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: %v", out, err)
			}
			actual, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(actual), "image_tag="+workflow.Env["IMAGE_TAG"]+tc.suffix+"\n") {
				t.Fatalf("mutable or mismatched image tag: %s", actual)
			}
		})
	}
}
