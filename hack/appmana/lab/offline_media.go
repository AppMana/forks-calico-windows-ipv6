package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// Check the namespace Windows actually mounts, before allocating any VMs.
// Rock Ridge filenames and a matching ISO hash do not prove Joliet survived
// an ISO rewrite; a missing wildcard match otherwise copies zero archives.
func verifyWindowsMedia(path string) error {
	out, err := exec.Command("isoinfo", "-J", "-f", "-i", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect Windows-visible offline media: %w: %s", err, out)
	}
	visible := map[string]bool{}
	for _, name := range strings.Split(string(out), "\n") {
		visible[strings.TrimSpace(name)] = true
	}
	for _, name := range []string{"k0s.exe", "windows-node.tar", "windows-cni.tar", "windows-pause.tar", "windows-proxy.tar", "windows-workload.tar"} {
		if !visible["/"+name] {
			return fmt.Errorf("Windows-visible offline input missing: %s (ISO requires Joliet filenames)", name)
		}
	}
	return nil
}
