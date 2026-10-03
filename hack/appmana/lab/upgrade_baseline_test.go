package main

import "testing"

func TestUpgradeBaselineCannotQualifyPrefixRotations(t *testing.T) {
	for _, tc := range []struct {
		marker         string
		upgrade, valid bool
	}{
		{"PREFIX_ROTATION_COMPLETE", false, true},
		{"RUNTIME_WORKLOAD_BASELINE_COMPLETE", true, true},
		{"RUNTIME_WORKLOAD_BASELINE_COMPLETE", false, false},
		{"PREFIX_ROTATION_COMPLETE", true, false},
		{"SMOKE_COMPLETE", false, false},
		{"SMOKE_COMPLETE", true, false},
	} {
		if err := validateIPv6Consumer([]byte("pinned binary"), tc.marker, tc.upgrade); (err == nil) != tc.valid {
			t.Fatalf("marker=%s upgrade=%v: %v", tc.marker, tc.upgrade, err)
		}
		if err := validateIPv6Consumer(nil, tc.marker, tc.upgrade); err == nil {
			t.Fatal("accepted absent consumer")
		}
	}
}
