package config

import "testing"

func TestResolveConfdPathPreservesWindowsDrivePathInHostProcess(t *testing.T) {
	t.Setenv("CONTAINER_SANDBOX_MOUNT_POINT", `C:\hpc`)

	got := resolveConfdPath(`C:\CalicoWindows\confd`)
	if got != `C:\CalicoWindows\confd` {
		t.Fatalf("expected explicit host path to be preserved, got %q", got)
	}
}

func TestResolveConfdPathMapsContainerPathInHostProcess(t *testing.T) {
	t.Setenv("CONTAINER_SANDBOX_MOUNT_POINT", `C:\hpc`)

	got := resolveConfdPath("/etc/confd")
	if got != "/etc/confd" {
		t.Fatalf("linux tests do not simulate winutils host path mapping; got %q", got)
	}
}

func TestResolveConfdPathMapsObservedHPCSandboxPath(t *testing.T) {
	for _, path := range []string{`C:\hpc\CalicoWindows\confd`, `C:\hpc/CalicoWindows/confd`} {
		got := resolveConfdPath(path)
		if got != `C:\CalicoWindows\confd` {
			t.Fatalf("expected observed HostProcess sandbox path to map to host confd directory, got %q", got)
		}
	}
}

func TestIsWindowsDriveAbsPath(t *testing.T) {
	for _, path := range []string{`C:\CalicoWindows\confd`, `d:/calico/confd`} {
		if !isWindowsDriveAbsPath(path) {
			t.Fatalf("expected %q to be a Windows absolute drive path", path)
		}
	}
	for _, path := range []string{`\\server\share`, `/etc/confd`, `C:relative`, ``} {
		if isWindowsDriveAbsPath(path) {
			t.Fatalf("expected %q not to be a Windows absolute drive path", path)
		}
	}
}
