package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/appmana/labcontainers/pkg/client"
	"github.com/appmana/labcontainers/pkg/kubernetes/kube"
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
	t.Log("RETAINED_VYOS_ROUTES_COMPLETE")
}
