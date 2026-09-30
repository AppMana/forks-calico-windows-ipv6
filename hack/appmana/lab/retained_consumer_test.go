package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/client"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// A retained cluster already has an authenticated HostProcess control path.
// Keep this an explicit preparation-only choice; it is not evidence that the
// independent serial control channel survived a crash or restart.
func retainedWindowsPrepareCommand(mode string) (string, []string, error) {
	script := `$ErrorActionPreference='Stop'; & C:\LabQualification\k0s.exe ctr images import --local --snapshotter windows C:\var\lib\k0s\images\windows-workload.tar; if($LASTEXITCODE -ne 0){exit $LASTEXITCODE}`
	if mode == "" || mode == "serial" {
		return "windows", []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script}, nil
	}
	if mode != "kubernetes" {
		return "", nil, fmt.Errorf("retained Windows preparation control must be serial or kubernetes")
	}
	units := utf16.Encode([]rune(script))
	data := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(data[i*2:], unit)
	}
	return "linux", []string{"k0s", "kubectl", "exec", "--namespace=kube-system", "daemonset/calico-node-windows", "--container=node", "--", "powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(data)}, nil
}

func TestRetainedWindowsPrepareCommand(t *testing.T) {
	node, serial, err := retainedWindowsPrepareCommand("")
	if err != nil || node != "windows" || serial[len(serial)-2] != "-Command" {
		t.Fatalf("serial default: %s %v %v", node, serial, err)
	}
	node, kube, err := retainedWindowsPrepareCommand("kubernetes")
	if err != nil || node != "linux" || strings.Join(kube[:8], " ") != "k0s kubectl exec --namespace=kube-system daemonset/calico-node-windows --container=node -- powershell.exe" {
		t.Fatalf("native host-process path: %s %v %v", node, kube, err)
	}
	data, err := base64.StdEncoding.DecodeString(kube[len(kube)-1])
	if err != nil || len(data)%2 != 0 {
		t.Fatalf("encoded command: %v", err)
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(data[i*2:])
	}
	if string(utf16.Decode(units)) != serial[len(serial)-1] {
		t.Fatal("preparation content differs between control paths")
	}
	if _, _, err := retainedWindowsPrepareCommand("auto"); err == nil {
		t.Fatal("implicit fallback accepted")
	}
}

func windowsCNIStatusObjects() []runtime.Object {
	return []runtime.Object{
		&rbacv1.ClusterRole{TypeMeta: metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRole"}, ObjectMeta: metav1.ObjectMeta{Name: "qualification-windows-cni"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"nodes/status"}, ResourceNames: []string{"windows"}, Verbs: []string{"update"}}}},
		&rbacv1.ClusterRoleBinding{TypeMeta: metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRoleBinding"}, ObjectMeta: metav1.ObjectMeta{Name: "qualification-windows-cni"}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "qualification-windows-cni"}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "calico-cni-plugin", Namespace: "kube-system"}}},
	}
}

// This is explicitly retained-cluster qualification, not a clean bootstrap
// pass. It reuses the same guest helpers, typed prerequisites and workload.
func TestRetainedKubernetesConsumer(t *testing.T) {
	socket, id := os.Getenv("LABCONTAINERS_RETAINED_SOCKET"), os.Getenv("LABCONTAINERS_RETAINED_SESSION")
	if socket == "" && id == "" {
		t.Skip("explicit retained Kubernetes session required")
	}
	if socket == "" || id == "" {
		t.Fatal("partial retained session configuration")
	}
	body, args, err := readKubernetesWorkload(os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD"), os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD_SHA256"), os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD_ARGS"), os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD_SUCCESS"))
	if err != nil || body == nil {
		t.Fatalf("workload inputs: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	defer cancel()
	c, err := client.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	// No Resume: do not acquire cleanup ownership of a running retained lab.
	defer c.Close()
	execute := func(node string, timeout time.Duration, argv ...string) *labv1.ExecResponse {
		t.Helper()
		r, err := c.RPC().Exec(ctx, &labv1.ExecRequest{Node: &labv1.NodeRef{SessionId: id, Node: node}, Argv: argv, TimeoutMillis: timeout.Milliseconds()})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s\n%s", node, r.Stdout, r.Stderr)
		if r.ExitCode != 0 {
			t.Fatalf("%s exit %d", node, r.ExitCode)
		}
		return r
	}
	execute("linux", time.Minute, "k0s", "kubectl", "wait", "--for=condition=Ready", "node/linux", "node/windows", "--timeout=45s")
	prepareNode, prepareArgs, err := retainedWindowsPrepareCommand(os.Getenv("LABCONTAINERS_RETAINED_WINDOWS_PREPARE_CONTROL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained Windows image preparation control=%s (not a serial recovery assertion)", os.Getenv("LABCONTAINERS_RETAINED_WINDOWS_PREPARE_CONTROL"))
	execute(prepareNode, 12*time.Minute, prepareArgs...)
	list := &metav1.List{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "List"}}
	for _, o := range windowsCNIStatusObjects() {
		list.Items = append(list.Items, runtime.RawExtension{Object: o})
	}
	data, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	put := func(path string, data []byte) {
		t.Helper()
		_, err := c.RPC().Put(ctx, &labv1.PutRequest{Node: &labv1.NodeRef{SessionId: id, Node: "linux"}, Path: path, Mode: 0755, Content: data})
		if err != nil {
			t.Fatal(err)
		}
	}
	put("/var/tmp/qualification-cni-prerequisites.json", data)
	execute("linux", time.Minute, "k0s", "kubectl", "apply", "-f", "/var/tmp/qualification-cni-prerequisites.json")
	put("/usr/local/bin/kubernetes-workload-retained", body)
	directory := fmt.Sprintf("/var/tmp/kubernetes-consumer-%d", time.Now().UnixNano())
	t.Logf("persistent guest workload evidence: %s", directory)
	r := execute("linux", 38*time.Minute, workloadCommand(directory, "/usr/local/bin/kubernetes-workload-retained", args)...)
	found := false
	for _, line := range bytes.Split(r.Stdout, []byte("\n")) {
		if strings.TrimSpace(string(line)) == os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD_SUCCESS") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing consumer completion evidence")
	}
}
