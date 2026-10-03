package main

import (
	"fmt"
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// This only validates observations. It never applies a manifest or repairs an
// endpoint. A deleted/recreated Pod must not masquerade as runtime recovery.
func validateEndpointRecovery(before, after v1.PodList) error {
	index := func(list v1.PodList) (map[string]v1.Pod, error) {
		pods := map[string]v1.Pod{}
		for _, pod := range list.Items {
			if pod.Name != "hc-linux" && pod.Name != "hc-windows" {
				return nil, fmt.Errorf("unexpected recovery probe %s", pod.Name)
			}
			if pod.Namespace != "default" || pod.UID == "" || pod.Spec.HostNetwork || pod.Spec.NodeName != pod.Name[3:] || len(pod.Status.ContainerStatuses) != 1 {
				return nil, fmt.Errorf("invalid ordinary workload identity for %s", pod.Name)
			}
			if _, exists := pods[pod.Name]; exists {
				return nil, fmt.Errorf("duplicate recovery probe %s", pod.Name)
			}
			pods[pod.Name] = pod
		}
		if len(pods) != 2 {
			return nil, fmt.Errorf("both recovery probes are required")
		}
		return pods, nil
	}
	old, err := index(before)
	if err != nil {
		return err
	}
	current, err := index(after)
	if err != nil {
		return err
	}
	for name, baseline := range old {
		pod := current[name]
		prior, now := baseline.Status.ContainerStatuses[0], pod.Status.ContainerStatuses[0]
		oldSandbox, newSandbox := baseline.Annotations["cni.projectcalico.org/containerID"], pod.Annotations["cni.projectcalico.org/containerID"]
		if pod.UID != baseline.UID || oldSandbox == "" || newSandbox == "" || prior.ContainerID == "" || now.ContainerID == "" || !now.Ready || now.State.Running == nil {
			return fmt.Errorf("%s identity replaced, missing, or not running", name)
		}
		if name == "hc-linux" {
			if oldSandbox != newSandbox || prior.ContainerID != now.ContainerID || prior.RestartCount != now.RestartCount || !reflect.DeepEqual(baseline.Status.PodIPs, pod.Status.PodIPs) {
				return fmt.Errorf("Linux control changed during Windows endpoint recovery")
			}
		} else if oldSandbox == newSandbox || prior.ContainerID == now.ContainerID || now.RestartCount <= prior.RestartCount {
			return fmt.Errorf("Windows sandbox/container has not recovered")
		}
	}
	return nil
}

func TestEndpointRecoveryIdentityBoundaries(t *testing.T) {
	before := v1.PodList{}
	for _, node := range []string{"linux", "windows"} {
		before.Items = append(before.Items, v1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "hc-" + node, Namespace: "default", UID: types.UID(node), Annotations: map[string]string{"cni.projectcalico.org/containerID": node + "-sandbox"}},
			Spec:       v1.PodSpec{NodeName: node},
			Status:     v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{ContainerID: node + "-container", Ready: true, State: v1.ContainerState{Running: &v1.ContainerStateRunning{}}}}},
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*v1.PodList)
		valid  bool
	}{
		{"native recovery", func(*v1.PodList) {}, true},
		{"pod recreated", func(p *v1.PodList) { p.Items[1].UID = "replacement" }, false},
		{"sandbox unchanged", func(p *v1.PodList) { p.Items[1].Annotations["cni.projectcalico.org/containerID"] = "windows-sandbox" }, false},
		{"container unchanged", func(p *v1.PodList) { p.Items[1].Status.ContainerStatuses[0].ContainerID = "windows-container" }, false},
		{"restart not observed", func(p *v1.PodList) { p.Items[1].Status.ContainerStatuses[0].RestartCount = 0 }, false},
		{"Linux restarted", func(p *v1.PodList) { p.Items[0].Status.ContainerStatuses[0].RestartCount++ }, false},
		{"not ready", func(p *v1.PodList) { p.Items[1].Status.ContainerStatuses[0].Ready = false }, false},
		{"missing control", func(p *v1.PodList) { p.Items = p.Items[1:] }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after := before.DeepCopy()
			after.Items[1].Annotations["cni.projectcalico.org/containerID"] = "new-sandbox"
			after.Items[1].Status.ContainerStatuses[0].ContainerID = "new-container"
			after.Items[1].Status.ContainerStatuses[0].RestartCount = 1
			tc.change(after)
			if err := validateEndpointRecovery(before, *after); (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}
