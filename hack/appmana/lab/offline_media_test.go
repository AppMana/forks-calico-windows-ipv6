package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWindowsMediaNames(t *testing.T) {
	for _, tool := range []string{"xorriso", "isoinfo"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " required")
		}
	}
	dir := t.TempDir()
	inputs := filepath.Join(dir, "inputs")
	if err := os.Mkdir(inputs, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"k0s.exe", "windows-node.tar", "windows-cni.tar", "windows-pause.tar", "windows-proxy.tar", "windows-workload.tar"} {
		if err := os.WriteFile(filepath.Join(inputs, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, joliet := range []bool{false, true} {
		name := "rockridge.iso"
		if joliet {
			name = "joliet.iso"
		}
		iso := filepath.Join(dir, name)
		args := []string{"-as", "mkisofs", "-R", "-V", "LCQUAL", "-o", iso}
		if joliet {
			args = append(args, "-J")
		}
		args = append(args, inputs)
		if out, err := exec.Command("xorriso", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		if err := verifyWindowsMedia(iso); (err == nil) != joliet {
			t.Fatalf("Joliet=%v: %v", joliet, err)
		}
	}
	if path := os.Getenv("LABCONTAINERS_CALICO_MEDIA"); path != "" {
		if err := verifyWindowsMedia(path); err != nil {
			t.Fatal(err)
		}
	}
}
