package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	labv1 "github.com/appmana/labcontainers/api/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func TestNetworkProbeContracts(t *testing.T) {
	objects := networkProbeObjects(networkProbeWindowsImage)
	if len(objects) != 4 {
		t.Fatalf("object count=%d", len(objects))
	}
	list := &metav1.List{}
	for _, obj := range objects {
		switch o := obj.(type) {
		case *v1.Pod:
			if o.Spec.HostNetwork || len(o.Spec.Containers) != 1 || o.Spec.Containers[0].ImagePullPolicy != v1.PullNever || o.Spec.RestartPolicy != v1.RestartPolicyAlways {
				t.Fatalf("probe lost ordinary offline pod contract: %+v", o.Spec)
			}
			if o.Name != "hc-"+o.Spec.NodeName {
				t.Fatalf("probe node mismatch: %s/%s", o.Name, o.Spec.NodeName)
			}
			if o.Spec.NodeName == "windows" && o.Spec.Containers[0].Image != networkProbeWindowsImage {
				t.Fatal("Windows image changed")
			}
			o.UID = types.UID(o.Name + "-original")
		case *v1.Service:
			if len(o.Spec.Ports) != 1 || o.Spec.Ports[0].Port != 8080 || o.Spec.Ports[0].TargetPort.IntVal != 8080 || o.Spec.Selector["app"] == "" {
				t.Fatal("service probe contract changed")
			}
			o.UID = types.UID(o.Name + "-original")
		default:
			t.Fatalf("unexpected object %T", obj)
		}
		list.Items = append(list.Items, runtime.RawExtension{Object: obj})
	}
	data, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := probeIdentities(data)
	if err != nil || len(ids) != 4 {
		t.Fatalf("identity snapshot=%v,%v", ids, err)
	}
	objects[0].(*v1.Pod).Spec.HostNetwork = true
	data, _ = json.Marshal(list)
	if _, err := probeIdentities(data); err == nil {
		t.Fatal("accepted host-network probe")
	}
	objects[0].(*v1.Pod).Spec.HostNetwork = false
	objects[0].(*v1.Pod).UID = ""
	data, _ = json.Marshal(list)
	if _, err := probeIdentities(data); err == nil {
		t.Fatal("accepted missing UID")
	}
}

func TestRetainedNetworkStrictResultAndExistingMode(t *testing.T) {
	want := []string{"bash", "/usr/local/bin/calico-health-check", "--existing", "--namespace", "default", "--ipv4-only", "--skip-inbound", "--skip-external", "linux", "windows"}
	if !reflect.DeepEqual(retainedNetworkHealthArgs(), want) {
		t.Fatalf("health args=%v", retainedNetworkHealthArgs())
	}
	for _, tc := range []struct {
		code   int32
		output string
		want   bool
	}{
		{0, "Total: 10  Pass: 10  Fail: 0\n", true}, {1, "Total: 10  Pass: 10  Fail: 0\n", false},
		{0, "Total: 9  Pass: 9  Fail: 0\n", false}, {0, "Total: 10  Pass: 9  Fail: 1\n", false}, {0, "missing", false},
	} {
		if got := strictInternalNetworkResult(&labv1.ExecResponse{ExitCode: tc.code, Stdout: []byte(tc.output)}); got != tc.want {
			t.Errorf("result %+v = %v", tc, got)
		}
	}
	file, err := parser.ParseFile(token.NewFileSet(), "retained_network_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Resume" || sel.Sel.Name == "Start" || sel.Sel.Name == "Destroy") {
				t.Errorf("retained observer acquired lifecycle ownership: %s", sel.Sel.Name)
			}
		}
		return true
	})
}
