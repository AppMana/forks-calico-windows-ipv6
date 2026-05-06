// Package cni internal tests — separate file (white-box) so we can
// exercise unexported helpers like getKubeconfigPath without changing
// the public surface.

package cni

import (
	"os"
	"testing"
)

// TestGetKubeconfigPath_DefaultMatchesHardcode keeps the historical
// hardcoded value as the default. Deployments that don't set the env
// var must continue to work unchanged.
func TestGetKubeconfigPath_DefaultMatchesHardcode(t *testing.T) {
	t.Setenv("CALICO_CNI_KUBECONFIG_PATH", "")
	got := getKubeconfigPath()
	want := "/host/etc/cni/net.d/calico-kubeconfig"
	if got != want {
		t.Fatalf("default path = %q, want %q", got, want)
	}
}

// TestGetKubeconfigPath_EnvOverride confirms a non-empty env var
// replaces the default so HostProcess installations that mount the
// CNI conf dir somewhere other than /etc/cni/net.d (or that want the
// kubeconfig at a different filename) can point the refresher at the
// matching path. Pairs with the configmap KUBECONFIG entry rendered
// into 10-calico.conf — the two MUST agree.
func TestGetKubeconfigPath_EnvOverride(t *testing.T) {
	t.Setenv("CALICO_CNI_KUBECONFIG_PATH", "/host/var/lib/cni/calico-kubeconfig")
	got := getKubeconfigPath()
	want := "/host/var/lib/cni/calico-kubeconfig"
	if got != want {
		t.Fatalf("override path = %q, want %q", got, want)
	}
}

// TestGetKubeconfigPath_EnvEmptyFallsBack ensures an explicitly empty
// env var does not produce an empty path, which would write to the
// process CWD silently.
func TestGetKubeconfigPath_EnvEmptyFallsBack(t *testing.T) {
	// Setenv("", "") still sets the env but to empty; restore on exit.
	old := os.Getenv("CALICO_CNI_KUBECONFIG_PATH")
	t.Cleanup(func() { os.Setenv("CALICO_CNI_KUBECONFIG_PATH", old) })
	os.Setenv("CALICO_CNI_KUBECONFIG_PATH", "")
	if got := getKubeconfigPath(); got != "/host/etc/cni/net.d/calico-kubeconfig" {
		t.Fatalf("empty env => %q, want default", got)
	}
}
