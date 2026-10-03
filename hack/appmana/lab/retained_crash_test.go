package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/client"
	corev1 "k8s.io/api/core/v1"
)

func crashConsumerSequence(verify func(string) error, boot func() (string, error), lifecycle func(labv1.LifecycleAction) error, recoverBoot func(string) error) error {
	if err := verify("before"); err != nil {
		return fmt.Errorf("pre-crash verification: %w", err)
	}
	before, err := boot()
	if err != nil {
		return err
	}
	if strings.TrimSpace(before) == "" {
		return fmt.Errorf("missing pre-crash boot identity")
	}
	if err := lifecycle(labv1.LifecycleAction_CRASH); err != nil {
		return fmt.Errorf("crash: %w", err)
	}
	if err := lifecycle(labv1.LifecycleAction_START); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if err := recoverBoot(before); err != nil {
		return fmt.Errorf("new boot readiness: %w", err)
	}
	return verify("after")
}

func TestCrashConsumerSequenceFailsClosed(t *testing.T) {
	want := []string{"before", "boot", "CRASH", "START", "recover", "after"}
	for failure := -1; failure < len(want); failure++ {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			var got []string
			boom := errors.New("injected failure")
			step := func(name string) error {
				got = append(got, name)
				if len(got)-1 == failure {
					return boom
				}
				return nil
			}
			err := crashConsumerSequence(step, func() (string, error) { return "old-boot", step("boot") }, func(a labv1.LifecycleAction) error { return step(a.String()) }, func(before string) error {
				if before != "old-boot" {
					t.Fatal(before)
				}
				return step("recover")
			})
			n := len(want)
			if failure >= 0 {
				n = failure + 1
				if !errors.Is(err, boom) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want[:n]) {
				t.Fatalf("wrong sequence: %v", got)
			}
		})
	}
	crashed := false
	err := crashConsumerSequence(func(string) error { return nil }, func() (string, error) { return "", nil }, func(labv1.LifecycleAction) error { crashed = true; return nil }, func(string) error { return nil })
	if err == nil || crashed {
		t.Fatal("missing boot identity allowed crash")
	}
}

// Explicit opt-in only. The same pinned consumer verifies the same dataset on
// each side of one abrupt VM kill. Never redeploy, reset disks, or retry a data
// failure. Dial grants no ownership of retained-session cleanup.
func TestRetainedKubernetesCrashConsumer(t *testing.T) {
	node := os.Getenv("LABCONTAINERS_CRASH_NODE")
	if node == "" {
		t.Skip("explicit retained Windows crash qualification required")
	}
	socket, id := os.Getenv("LABCONTAINERS_RETAINED_SOCKET"), os.Getenv("LABCONTAINERS_RETAINED_SESSION")
	if node != "windows" || !filepath.IsAbs(socket) || filepath.Clean(socket) == "/" || strings.TrimSpace(id) == "" {
		t.Fatal("explicit socket, retained session and windows node required")
	}
	marker := os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD_SUCCESS")
	body, args, err := readKubernetesWorkload(os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD"), os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD_SHA256"), os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD_ARGS"), marker)
	if err != nil || len(body) == 0 {
		t.Fatalf("pinned verification workload required: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	c, err := client.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	runKubernetesCrashConsumer(t, ctx, c, id, body, args, marker)
}

// Reused by fresh fixtures before their normal automatic cleanup. The pinned
// consumer must verify existing data, never rerun its seeding entry point.
func runKubernetesCrashConsumer(t *testing.T, ctx context.Context, c *client.Client, id string, body []byte, args []string, marker string) {
	t.Helper()
	const node = "windows"
	execute := func(node string, timeout time.Duration, argv ...string) (*labv1.ExecResponse, error) {
		requestCtx, done := context.WithTimeout(ctx, timeout+5*time.Second)
		defer done()
		r, err := c.RPC().Exec(requestCtx, &labv1.ExecRequest{Node: &labv1.NodeRef{SessionId: id, Node: node}, Argv: argv, TimeoutMillis: timeout.Milliseconds()})
		if err != nil {
			return nil, err
		}
		if r.ExitCode != 0 {
			return r, fmt.Errorf("%s exit %d: %s %s", node, r.ExitCode, r.Stdout, r.Stderr)
		}
		return r, nil
	}
	base := fmt.Sprintf("/var/tmp/crash-consumer-%d", time.Now().UnixNano())
	executable := base + ".test"
	_, err := c.RPC().Put(ctx, &labv1.PutRequest{Node: &labv1.NodeRef{SessionId: id, Node: "linux"}, Path: executable, Mode: 0700, Content: body})
	if err != nil {
		t.Fatal(err)
	}
	verify := func(phase string) error {
		directory := base + "-" + phase
		t.Logf("%s persistent consumer evidence: %s", phase, directory)
		r, err := execute("linux", 14*time.Minute, workloadCommand(directory, executable, args)...)
		if r != nil {
			t.Logf("%s consumer: %s\n%s", phase, r.Stdout, r.Stderr)
		}
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(r.Stdout), "\n") {
			if strings.TrimSpace(line) == marker {
				if phase == "after" && os.Getenv("LABCONTAINERS_VERIFY_WINDOWS_HOOK") == "1" {
					return verifyPackagedWindowsHook(execute)
				}
				return nil
			}
		}
		return fmt.Errorf("%s missing exact consumer completion marker", phase)
	}
	boot := func() (string, error) {
		r, err := execute("linux", 30*time.Second, "k0s", "kubectl", "--request-timeout=20s", "get", "node", node, "-o", "json")
		if err != nil {
			return "", err
		}
		var n corev1.Node
		if err := json.Unmarshal(r.Stdout, &n); err != nil {
			return "", err
		}
		if n.Name != node || n.Status.NodeInfo.BootID == "" {
			return "", fmt.Errorf("missing node boot identity")
		}
		for _, condition := range n.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				return n.Status.NodeInfo.BootID, nil
			}
		}
		return "", fmt.Errorf("Windows node not Ready")
	}
	lifecycle := func(action labv1.LifecycleAction) error {
		requestCtx, done := context.WithTimeout(ctx, 2*time.Minute)
		defer done()
		r, err := c.RPC().Lifecycle(requestCtx, &labv1.LifecycleRequest{Node: &labv1.NodeRef{SessionId: id, Node: node}, Action: action})
		if err != nil {
			return err
		}
		want := "running"
		if action == labv1.LifecycleAction_CRASH {
			want = "stopped"
		}
		if r == nil || r.Name != node || r.State != want {
			return fmt.Errorf("unexpected %s result: %v", action, r)
		}
		t.Logf("session=%s node=%s action=%s state=%s", id, node, action, r.State)
		return nil
	}
	recoverBoot := func(before string) error {
		deadline := time.Now().Add(10 * time.Minute)
		var last error
		for time.Now().Before(deadline) {
			after, err := boot()
			last = err
			if err == nil && after != before {
				t.Logf("changed Ready boot identity: %s -> %s", before, after)
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
		return fmt.Errorf("no changed Ready boot identity: %v", last)
	}
	if err := crashConsumerSequence(verify, boot, lifecycle, recoverBoot); err != nil {
		t.Fatal(err)
	}
	fmt.Println("RETAINED_KUBERNETES_CRASH_CONSUMER_COMPLETE")
}
