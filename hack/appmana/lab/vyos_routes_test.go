package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
)

// Shared by fresh and retained qualification: never repair routes under test.
func verifyVyOSRoutes(t *testing.T, ipv6 bool, run func(string, ...string) []byte) {
	t.Helper()
	for _, target := range []struct{ name, node string }{{"hc-linux", "192.0.2.10"}, {"hc-windows", "192.0.2.20"}} {
		var pod v1.Pod
		if err := json.Unmarshal(run("linux", "k0s", "kubectl", "get", "pod", target.name, "-o", "json"), &pod); err != nil {
			t.Fatal(err)
		}
		if len(pod.Status.PodIPs) == 0 || (ipv6 && len(pod.Status.PodIPs) != 2) {
			t.Fatalf("missing required pod address families: %s %+v", target.name, pod.Status.PodIPs)
		}
		for _, address := range pod.Status.PodIPs {
			family, nextHop := "-4", target.node
			if strings.Contains(address.IP, ":") {
				family = "-6"
				nextHop = "fd00:10::" + strings.TrimPrefix(target.node, "192.0.2.")
			}
			deadline := time.Now().Add(90 * time.Second)
			for {
				data := run("tor", "ip", family, "-j", "route", "show")
				if err := requireVyOSBGPRoute(data, address.IP, nextHop); err == nil {
					break
				} else if time.Now().After(deadline) {
					t.Fatal(err)
				}
				time.Sleep(time.Second)
			}
			url := "http://" + address.IP + ":8080"
			if family == "-6" {
				url = "http://[" + address.IP + "]:8080"
			}
			got := strings.TrimSpace(string(run("gateway", "curl", "--noproxy", "*", "-fsS", "--connect-timeout", "3", "--max-time", "10", url)))
			if got != "ok "+strings.TrimPrefix(target.name, "hc-") {
				t.Fatalf("WAN through BGP ToR to %s: %q", address.IP, got)
			}
		}
	}
}
