package main

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/appmana/labcontainers/pkg/client"
	"github.com/appmana/labcontainers/pkg/kubernetes/kube"
	"github.com/appmana/labcontainers/pkg/network"
	"github.com/appmana/labcontainers/pkg/network/vyos"
	v1 "k8s.io/api/core/v1"
)

// Reuse installed guests and the exact fresh-lab route/HTTP assertions. This
// cannot qualify bootstrap, crash recovery, or consumer lifecycle by itself.
func TestRetainedVyOSRoutes(t *testing.T) {
	if os.Getenv("LABCONTAINERS_RETAINED_VYOS") != "1" {
		t.Skip("explicit retained real VyOS qualification required")
	}
	socket, id := os.Getenv("LABCONTAINERS_RETAINED_SOCKET"), os.Getenv("LABCONTAINERS_RETAINED_SESSION")
	if socket == "" || id == "" || os.Getenv("LABCONTAINERS_KUBERNETES_IPV6") != "1" {
		t.Fatal("explicit retained session and dual-stack required")
	}
	configure := os.Getenv("LABCONTAINERS_RETAINED_VYOS_CONFIGURE")
	if configure != "" && configure != "1" {
		t.Fatal("configure must be unset or explicitly 1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	c, err := client.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	lab, err := c.Resume(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve this explicitly retained diagnosis lab before acquiring cleanup
	// ownership. Do not Close on Keep failure and destroy the evidence.
	if err := lab.Keep(ctx, 4*time.Hour); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	run := func(node string, args ...string) []byte {
		t.Helper()
		out, err := lab.Node(node).Commands().Exec(ctx, args...)
		if err != nil {
			t.Fatalf("%s %v: %s: %v", node, args, out, err)
		}
		return out
	}
	run("tor", "sh", "-ec", `test "$(. /etc/os-release; echo "$ID")" = vyos`)
	if configure == "1" {
		// Apply only the normal, checked-in BGPPeer objects. Never add a peer
		// through RRAS or repair the kernel FIB to make the assertions pass.
		api := &kube.Client{Bastion: lab.Node("linux").Commands(), Kubectl: []string{"k0s", "kubectl"}, ControlPlanes: []string{"192.0.2.10"}}
		if err := api.ApplyObjects(ctx, vyosBGPPeers(true)...); err != nil {
			t.Fatal(err)
		}
	}
	verifyVyOSRoutes(t, true, run)
	if baselinePath := os.Getenv("LABCONTAINERS_WINDOWS_RECOVERY_BASELINE"); baselinePath != "" {
		baseline, err := os.ReadFile(baselinePath)
		if err != nil {
			t.Fatal(err)
		}
		var before, after v1.PodList
		if err := json.Unmarshal(baseline, &before); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(run("linux", "k0s", "kubectl", "get", "pods", "hc-linux", "hc-windows", "-o", "json"), &after); err != nil {
			t.Fatal(err)
		}
		if err := validateEndpointRecovery(before, after); err != nil {
			t.Fatal(err)
		}
		t.Log("WINDOWS_ENDPOINT_RECOVERY_IDENTITIES_COMPLETE")
	}
	t.Log("RETAINED_VYOS_ROUTES_COMPLETE")
}

// Explicitly migrate an earlier lab declaration and then test persistence.
// This is configuration qualification, never part of the route assertions.
func TestRetainedVyOSConfigurationRestart(t *testing.T) {
	if os.Getenv("LABCONTAINERS_RETAINED_VYOS_RESTART") != "1" {
		t.Skip("explicit retained router configuration/restart required")
	}
	socket, id := os.Getenv("LABCONTAINERS_RETAINED_SOCKET"), os.Getenv("LABCONTAINERS_RETAINED_SESSION")
	if socket == "" || id == "" {
		t.Fatal("explicit retained session required")
	}
	stale := strings.Fields(os.Getenv("LABCONTAINERS_VYOS_STALE_BRIDGE_PORTS"))
	for _, port := range stale {
		if !regexp.MustCompile(`^eth[0-9]+$`).MatchString(port) {
			t.Fatal("invalid explicit stale port")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	c, err := client.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	lab, err := c.Resume(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := lab.Keep(ctx, 4*time.Hour); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tor := lab.Node("tor")
	ports, err := network.InterfaceNames(ctx, tor.Commands(), vyosPortMACs...)
	if err != nil {
		t.Fatal(err)
	}
	commands := []vyos.Command{}
	for _, old := range stale {
		for _, current := range ports {
			if old == current {
				t.Fatal("refusing to remove a current declared port")
			}
		}
		commands = append(commands, vyos.Command{Operation: "delete", Path: []string{"interfaces", "bridge", "br0", "member", "interface", old}})
	}
	commands = append(commands, vyosToRConfiguration(ports, true)...)
	if err := vyos.Apply(ctx, tor.Commands(), commands); err != nil {
		t.Fatal(err)
	}
	boot, err := tor.Commands().Exec(ctx, "cat", "/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	if err := tor.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		attempt, done := context.WithTimeout(ctx, 5*time.Second)
		current, err := tor.Commands().Exec(attempt, "cat", "/proc/sys/kernel/random/boot_id")
		done()
		if err == nil && strings.TrimSpace(string(current)) != "" && string(current) != string(boot) {
			// QGA can start before udev renaming and native configuration
			// loading. Wait for VyOS's service, not the desired NIC names.
			ready, readyDone := context.WithTimeout(ctx, 5*time.Second)
			state, readyErr := tor.Commands().Exec(ready, "systemctl", "show", "vyos-router.service", "-p", "SubState", "-p", "Result")
			readyDone()
			// This installed VyOS unit is Type=simple + RemainAfterExit:
			// active/running means bootstrap is still executing.
			if readyErr == nil && strings.Contains(string(state), "SubState=exited") && strings.Contains(string(state), "Result=success") {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("router did not complete a new boot")
		}
		time.Sleep(time.Second)
	}
	after, err := network.InterfaceNames(ctx, tor.Commands(), vyosPortMACs...)
	if err != nil || !reflect.DeepEqual(ports, after) {
		t.Fatalf("port identity changed: %v -> %v: %v", ports, after, err)
	}
	// Do not reapply anything after restart. Verify saved LAN/WAN adjacencies.
	// The WAN fixture has return routes to pod prefixes, not private node ULAs;
	// routed WAN-to-pod coverage remains in verifyVyOSRoutes.
	for _, probe := range []struct{ node, target string }{
		{"linux", "192.0.2.1"}, {"linux", "fd00:10::1"},
		{"tor", "198.18.0.1"}, {"tor", "2001:db8:ffff::1"},
	} {
		out, err := lab.Node(probe.node).Commands().Exec(ctx, "ping", "-c", "2", "-W", "3", probe.target)
		if err != nil {
			t.Fatalf("post-restart reachability %s -> %s: %s: %v", probe.node, probe.target, out, err)
		}
	}
	t.Logf("VYOS_PORT_IDENTITY_RESTART_COMPLETE ports=%v", after)
}
