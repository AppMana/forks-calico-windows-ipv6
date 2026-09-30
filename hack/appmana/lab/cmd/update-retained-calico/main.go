// Update an explicitly retained lab through k0s's native configuration, not
// through a DaemonSet patch that the installation controller can overwrite.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"regexp"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/client"
	native "github.com/k0sproject/k0s/pkg/apis/k0s/v1beta1"
	"sigs.k8s.io/yaml"
)

func replaceImage(cfg *native.ClusterConfig, before, after string) error {
	valid := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if !valid.MatchString(before) || !valid.MatchString(after) || before == after {
		return fmt.Errorf("require distinct full SHA256 image digests")
	}
	if cfg.Spec == nil || cfg.Spec.Images == nil || cfg.Spec.Images.Calico == nil || cfg.Spec.Images.Calico.Windows == nil || cfg.Spec.Images.Calico.Windows.Node == nil {
		return fmt.Errorf("missing explicit Windows Calico image")
	}
	node := cfg.Spec.Images.Calico.Windows.Node
	if node.Image != "ghcr.io/appmana/node" || node.Version != "pinned@sha256:"+before {
		return fmt.Errorf("current Windows Calico image does not match expected fork digest")
	}
	node.Version = "pinned@sha256:" + after
	return nil
}

func updatedConfig(original []byte, before, after string) ([]byte, error) {
	var cfg native.ClusterConfig
	if err := yaml.UnmarshalStrict(original, &cfg); err != nil {
		return nil, err
	}
	if err := replaceImage(&cfg, before, after); err != nil {
		return nil, err
	}
	// Native UnmarshalJSON supplies defaults using the caller's host. Validate
	// with the native API, but preserve the original field-presence exactly.
	// Never serialize those defaults into a remote cluster's configuration.
	var fields map[string]any
	if err := yaml.UnmarshalStrict(original, &fields); err != nil {
		return nil, err
	}
	node := fields
	for _, key := range []string{"spec", "images", "calico", "windows", "node"} {
		next, ok := node[key].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("missing original object %s", key)
		}
		node = next
	}
	node["version"] = cfg.Spec.Images.Calico.Windows.Node.Version
	return yaml.Marshal(fields)
}

func main() {
	socket := flag.String("socket", "", "existing lab daemon socket")
	session := flag.String("session", "", "explicit retained session")
	before := flag.String("before", "", "expected old image digest")
	after := flag.String("after", "", "already imported replacement image digest")
	apply := flag.Bool("apply", false, "write configuration and restart the isolated k0s controller")
	flag.Parse()
	if *socket == "" || *session == "" {
		flag.Usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, err := client.Dial(ctx, *socket)
	if err != nil {
		panic(err)
	}
	defer c.Close() // RPC-only: never acquire lifecycle ownership with Resume.
	node := &labv1.NodeRef{SessionId: *session, Node: "linux"}
	run := func(args ...string) []byte {
		r, err := c.RPC().Exec(ctx, &labv1.ExecRequest{Node: node, Argv: args, TimeoutMillis: 120000})
		if err != nil {
			panic(err)
		}
		if r.ExitCode != 0 {
			panic(fmt.Sprintf("command failed: %s %s", r.Stdout, r.Stderr))
		}
		return r.Stdout
	}
	original := run("cat", "/etc/k0s/k0s.yaml")
	data, err := updatedConfig(original, *before, *after)
	if err != nil {
		panic(err)
	}
	fmt.Printf("session=%s Windows Calico %s -> %s apply=%t\n", *session, *before, *after, *apply)
	if !*apply {
		return
	}
	// Refuse to modify a cluster while its qualification consumer is running.
	run("sh", "-ec", "if pgrep -f '^/usr/local/bin/kubernetes-workload' >/dev/null; then echo 'qualification consumer still running' >&2; exit 1; fi")
	put := func(path string, content []byte) {
		if _, err := c.RPC().Put(ctx, &labv1.PutRequest{Node: node, Path: path, Mode: 0600, Content: content}); err != nil {
			panic(err)
		}
	}
	put("/etc/k0s/k0s.yaml.before-calico-"+*before, original)
	put("/etc/k0s/k0s.yaml.next", data)
	run("mv", "/etc/k0s/k0s.yaml.next", "/etc/k0s/k0s.yaml")
	run("systemctl", "restart", "k0scontroller")
	fmt.Println("configuration applied; runtime rollout and route assertions are still required")
}
