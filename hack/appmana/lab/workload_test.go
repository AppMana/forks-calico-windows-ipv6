package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func workloadCommand(directory, executable string, args []string) []string {
	// Keep output on the guest even if QGA loses its completed-process record.
	// Never interpolate workload arguments into shell code or overwrite an old
	// attempt. Preserve the real exit code; an output marker alone is not a pass.
	script := `directory=$1; shift
mkdir -- "$directory" || exit 125
"$@" >"$directory/output.log" 2>&1
status=$?
printf '%s\n' "$status" >"$directory/exit-code" || exit 125
cat -- "$directory/output.log" || exit 125
exit "$status"`
	return append([]string{"sh", "-c", script, "kubernetes-consumer", directory, executable}, args...)
}

func TestWorkloadPreservesFailureEvidence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attempt")
	args := workloadCommand(dir, "sh", []string{"-c", "printf '%s\\n' \"$1\"; printf 'error\\n' >&2; exit 17", "workload", "literal; $(not-a-command)"})
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 17 {
		t.Fatalf("changed exit status: %v %s", err, out)
	}
	log, err := os.ReadFile(filepath.Join(dir, "output.log"))
	if err != nil {
		t.Fatal("lost guest output:", err)
	}
	if string(log) != "literal; $(not-a-command)\nerror\n" || string(out) != string(log) {
		t.Fatalf("changed output: %q %q", log, out)
	}
	status, err := os.ReadFile(filepath.Join(dir, "exit-code"))
	if err != nil || string(status) != "17\n" {
		t.Fatalf("missing terminal status: %q %v", status, err)
	}
	if _, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err == nil {
		t.Fatal("accepted overwrite of previous attempt")
	}
	again, _ := os.ReadFile(filepath.Join(dir, "output.log"))
	if !bytes.Equal(log, again) {
		t.Fatal("overwrote prior evidence")
	}
}

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
