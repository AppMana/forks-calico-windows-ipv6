package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/client"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const retainedNetworkIdentityPath = "/var/tmp/qualification-network-identities.json"
const networkProbeWindowsImage = "mcr.microsoft.com/windows/servercore@sha256:e10503b9a4f7faafa30aa0f5d0e8e7f7ca30a4496b3b87d61178b4d7c6815fb5"

func retainedNetworkHealthArgs() []string {
	return []string{"bash", "/usr/local/bin/calico-health-check", "--existing", "--namespace", "default", "--ipv4-only", "--skip-inbound", "--skip-external", "linux", "windows"}
}

func strictInternalNetworkResult(r *labv1.ExecResponse) bool {
	if r == nil || r.ExitCode != 0 {
		return false
	}
	for _, line := range strings.Split(string(r.Stdout), "\n") {
		if strings.TrimSpace(line) == "Total: 10  Pass: 10  Fail: 0" {
			return true
		}
	}
	return false
}

func probeIdentities(data []byte) (map[string]string, error) {
	var list metav1.List
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, item := range list.Items {
		var object struct {
			metav1.TypeMeta `json:",inline"`
			Metadata        metav1.ObjectMeta `json:"metadata"`
			Spec            v1.PodSpec        `json:"spec"`
		}
		if err := json.Unmarshal(item.Raw, &object); err != nil {
			return nil, err
		}
		key := object.Kind + "/" + object.Metadata.Name
		if object.Metadata.Namespace != "default" || object.Metadata.UID == "" {
			return nil, fmt.Errorf("invalid probe identity: %s", key)
		}
		if object.Kind == "Pod" {
			wantNode := strings.TrimPrefix(object.Metadata.Name, "hc-")
			if object.Spec.NodeName != wantNode || object.Spec.HostNetwork {
				return nil, fmt.Errorf("probe not ordinary expected-node pod: %s", key)
			}
		}
		ids[key] = string(object.Metadata.UID)
	}
	for _, key := range []string{"Pod/hc-linux", "Pod/hc-windows", "Service/svc-hc-linux-v4", "Service/svc-hc-windows-v4"} {
		if ids[key] == "" {
			return nil, fmt.Errorf("missing probe %s", key)
		}
	}
	if len(ids) != 4 || len(list.Items) != 4 {
		return nil, fmt.Errorf("unexpected probe inventory")
	}
	return ids, nil
}

// PREPARE=1 explicitly creates/reuses native probes and records their UIDs.
// The default observes the same objects, never applies or deletes resources.
// Both modes attach through serial RPC without Resume/cleanup ownership.
func TestRetainedKubernetesNetwork(t *testing.T) {
	socket, id := os.Getenv("LABCONTAINERS_RETAINED_SOCKET"), os.Getenv("LABCONTAINERS_RETAINED_SESSION")
	if socket == "" && id == "" {
		t.Skip("explicit retained Kubernetes session required")
	}
	if socket == "" || id == "" {
		t.Fatal("partial retained session configuration")
	}
	mode := os.Getenv("LABCONTAINERS_RETAINED_NETWORK_PREPARE")
	if mode != "" && mode != "0" && mode != "1" {
		t.Fatal("prepare must be unset,0,or1")
	}
	prepare := mode == "1"
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	c, err := client.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	execute := func(timeout time.Duration, argv ...string) *labv1.ExecResponse {
		t.Helper()
		r, err := c.RPC().Exec(ctx, &labv1.ExecRequest{Node: &labv1.NodeRef{SessionId: id, Node: "linux"}, Argv: argv, TimeoutMillis: timeout.Milliseconds()})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%v: %s\n%s", argv, r.Stdout, r.Stderr)
		if r.ExitCode != 0 {
			t.Fatalf("command exit%d", r.ExitCode)
		}
		return r
	}
	put := func(path string, data []byte, mode uint32) {
		t.Helper()
		if !prepare {
			t.Fatal("observe-only attempted a write")
		}
		_, err := c.RPC().Put(ctx, &labv1.PutRequest{Node: &labv1.NodeRef{SessionId: id, Node: "linux"}, Path: path, Content: data, Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
	}
	health, err := os.ReadFile("../ipv6-health-check.sh")
	if err != nil {
		t.Fatal(err)
	}
	execute(time.Minute, "k0s", "kubectl", "get", "--raw", "/readyz")
	if prepare {
		objects := append(windowsCNIStatusObjects(), networkProbeObjects(networkProbeWindowsImage)...)
		list := &metav1.List{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "List"}}
		for _, object := range objects {
			list.Items = append(list.Items, runtime.RawExtension{Object: object})
		}
		body, err := json.Marshal(list)
		if err != nil {
			t.Fatal(err)
		}
		put("/var/tmp/qualification-network-probes.json", body, 0600)
		execute(time.Minute, "k0s", "kubectl", "apply", "-f", "/var/tmp/qualification-network-probes.json")
		put("/usr/local/bin/calico-health-check", health, 0755)
		put("/usr/local/bin/kubectl", []byte("#!/bin/sh\nexec /usr/local/bin/k0s kubectl --request-timeout=15s \"$@\"\n"), 0755)
	}
	gotHash := strings.Fields(string(execute(time.Minute, "sha256sum", "/usr/local/bin/calico-health-check").Stdout))
	if len(gotHash) == 0 || gotHash[0] != fmt.Sprintf("%x", sha256.Sum256(health)) {
		t.Fatal("retained health script differs from checked-in source; explicitly prepare first")
	}
	execute(4*time.Minute, "k0s", "kubectl", "wait", "--for=condition=Ready", "pod/hc-linux", "pod/hc-windows", "--namespace=default", "--timeout=180s")
	readIDs := func() map[string]string {
		r := execute(time.Minute, "k0s", "kubectl", "get", "pod/hc-linux", "pod/hc-windows", "service/svc-hc-linux-v4", "service/svc-hc-windows-v4", "--namespace=default", "-o", "json")
		ids, err := probeIdentities(r.Stdout)
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	before := readIDs()
	if prepare {
		body, err := json.Marshal(before)
		if err != nil {
			t.Fatal(err)
		}
		put(retainedNetworkIdentityPath, body, 0600)
	} else {
		var expected map[string]string
		if err := json.Unmarshal(execute(time.Minute, "cat", retainedNetworkIdentityPath).Stdout, &expected); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, expected) {
			t.Fatalf("probes were recreated since prepare: current=%v expected=%v", before, expected)
		}
	}
	r := execute(3*time.Minute, retainedNetworkHealthArgs()...)
	if !strictInternalNetworkResult(r) {
		t.Fatal("expected strict Total10Pass10Fail0 internal mixed network result")
	}
	if after := readIDs(); !reflect.DeepEqual(before, after) {
		t.Fatalf("probe identities changed during observation: before=%v after=%v", before, after)
	}
	t.Logf("RETAINED_NETWORK_COMPLETE prepare=%v identities=%v", prepare, before)
}
