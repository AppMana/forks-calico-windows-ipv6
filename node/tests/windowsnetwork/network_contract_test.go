package network_test

import (
	"encoding/json"
	"fmt"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"strings"
	"testing"
)

const networkProbeWindowsImage = "registry.example.test/windows-probe:fixture"

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
