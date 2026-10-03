package main

import (
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/srl-labs/containerlab/types"
)

// Retain evidence without leaving any declared node running, including WAN
// containers. Attempt every power-off even if retention or a shutdown fails.
func retainFailedQualification(keep func() error, powerOff func(string) error, nodes map[string]*types.NodeDefinition) error {
	errs := []error{keep()}
	names := make([]string, 0, len(nodes))
	for name := range nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := powerOff(name); err != nil {
			errs = append(errs, fmt.Errorf("power off %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func qualificationRetention(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < time.Minute || d > 24*time.Hour {
		return 0, fmt.Errorf("LABCONTAINERS_RETAIN_TTL must be between 1m and 24h")
	}
	return d, nil
}

func TestQualificationRetention(t *testing.T) {
	for _, value := range []string{"0", "-1h", "59s", "25h", "forever"} {
		if _, err := qualificationRetention(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	for value, want := range map[string]time.Duration{"": 0, "1m": time.Minute, "4h": 4 * time.Hour, "24h": 24 * time.Hour} {
		if got, err := qualificationRetention(value); err != nil || got != want {
			t.Fatalf("%q: %v %v", value, got, err)
		}
	}
}
