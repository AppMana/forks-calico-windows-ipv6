package main

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
	"strings"
)

//go:embed rras-prerequisites.ps1
var rrasPrerequisites []byte

const rrasSHA256 = "7477d3a175f431785cee4bd13b74a6c81366c2c604f158e97b180d1a44b42454"
const rrasSource = "2fb454608cafa558d69b9727ea688d3fb29fa5d1"

func verifyRRASSource() error {
	if fmt.Sprintf("%x", sha256.Sum256(rrasPrerequisites)) != rrasSHA256 {
		return fmt.Errorf("RRAS prerequisite source changed without updating its artifact/source pin")
	}
	return nil
}

// A role installation and a forwarding change may each require one reboot.
// Errors never become retries; only the script's explicit reboot result does.
func prepareRRAS(run func(string) (string, error), reboot func() error) error {
	for attempt := 0; attempt < 3; attempt++ {
		state, err := run("Prepare")
		if err != nil {
			return err
		}
		switch strings.TrimSpace(state) {
		case "ready":
			verified, err := run("Verify")
			if err != nil {
				return err
			}
			if strings.TrimSpace(verified) != "ready" {
				return fmt.Errorf("RRAS verification did not report ready: %q", verified)
			}
			return nil
		case "reboot":
			if attempt == 2 {
				return fmt.Errorf("RRAS prerequisite reboot budget exhausted")
			}
			if err := reboot(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected RRAS preparation result %q", state)
		}
	}
	return fmt.Errorf("RRAS preparation incomplete")
}
