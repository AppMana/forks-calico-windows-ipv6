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

// Consumer tests can reuse this exact Kubernetes fixture. Executables run only
// in its disposable controller VM, with offline inputs from the pinned media.
func readKubernetesWorkload(path, digest, arguments, marker string) ([]byte, []string, error) {
	if path == "" {
		if digest != "" || arguments != "" || marker != "" {
			return nil, nil, fmt.Errorf("partial workload configuration")
		}
		return nil, nil, nil
	}
	if !filepath.IsAbs(path) || marker == "" || strings.ContainsAny(marker, "\r\n") {
		return nil, nil, fmt.Errorf("absolute workload and single-line success marker required")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if len(body) == 0 || fmt.Sprintf("%x", sha256.Sum256(body)) != digest {
		return nil, nil, fmt.Errorf("workload checksum mismatch")
	}
	var args []string
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, nil, err
	}
	return body, args, nil
}

func TestKubernetesWorkloadInputs(t *testing.T) {
	p := filepath.Join(t.TempDir(), "workload")
	if err := os.WriteFile(p, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("fixture")))
	if _, args, err := readKubernetesWorkload(p, digest, `["-test.v"]`, "COMPLETE"); err != nil || len(args) != 1 {
		t.Fatalf("valid input: %v", err)
	}
	for _, tc := range [][4]string{{p, "bad", `[]`, "COMPLETE"}, {p, digest, `broken`, "COMPLETE"}, {p, digest, `[]`, ""}, {"", digest, `[]`, "COMPLETE"}, {p, digest, `[]`, "two\nlines"}} {
		if _, _, err := readKubernetesWorkload(tc[0], tc[1], tc[2], tc[3]); err == nil {
			t.Fatal("accepted invalid workload", tc)
		}
	}
}
