package deploy

import (
	"encoding/json"
	"strings"
	"testing"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func options() WindowsBGPOptions {
	return WindowsBGPOptions{NodeImage: "ghcr.io/appmana/node@sha256:" + strings.Repeat("a", 64), APIHost: "192.0.2.10", APIPort: "6443", ServiceCIDR: "10.96.0.0/12", DNSAddress: "10.96.0.10", AutodetectionMethod: "can-reach=192.0.2.10"}
}

func TestWindowsBGPOwnsOnlyMissingStockResources(t *testing.T) {
	objects, err := WindowsBGP(options())
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 3 {
		t.Fatalf("unexpected resources: %d", len(objects))
	}
	cm := objects[0].(*v1.ConfigMap)
	if cm.Name == "calico-windows-config" {
		t.Fatal("would fight the k0s-owned ConfigMap")
	}
	for key, want := range map[string]string{"CALICO_NETWORKING_BACKEND": "windows-bgp", "IP": "autodetect", "IP_AUTODETECTION_METHOD": options().AutodetectionMethod, "KUBERNETES_SERVICE_HOST": options().APIHost, "KUBECONFIG": `c:\etc\cni\net.d\calico-kubeconfig`, "CALICO_DSR_DISABLE": "true"} {
		if cm.Data[key] != want {
			t.Fatalf("%s=%q want %q", key, cm.Data[key], want)
		}
	}
	ipam := objects[1].(*unstructured.Unstructured)
	for _, key := range []string{"strictAffinity", "autoAllocateBlocks"} {
		value, found, err := unstructured.NestedBool(ipam.Object, "spec", key)
		if err != nil || !found || !value {
			t.Fatalf("missing IPAM %s", key)
		}
	}
	ds := objects[2].(*apps.DaemonSet)
	pod := ds.Spec.Template.Spec
	if !pod.HostNetwork || !*pod.SecurityContext.WindowsOptions.HostProcess || *pod.SecurityContext.WindowsOptions.RunAsUserName != `NT AUTHORITY\SYSTEM` || pod.NodeSelector["kubernetes.io/os"] != "windows" {
		t.Fatal("invalid HostProcess scheduling")
	}
	if pod.ServiceAccountName != "calico-node" || len(pod.ImagePullSecrets) != 0 || ds.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable.IntValue() != 1 {
		t.Fatal("changed ownership, credentials, or rollout scope")
	}
	if len(pod.Containers) != 3 {
		t.Fatal("BGP requires node, Felix and confd")
	}
	for i, name := range []string{"node", "felix", "confd"} {
		c := pod.Containers[i]
		if c.Name != name || c.Image != options().NodeImage || c.ImagePullPolicy != v1.PullIfNotPresent || c.EnvFrom[0].ConfigMapRef.Name != cm.Name {
			t.Fatal("lost pinned image/config", name)
		}
		if c.VolumeMounts[0].MountPath != "/host" || !strings.HasPrefix(c.Args[0], "$env:CONTAINER_SANDBOX_MOUNT_POINT/") {
			t.Fatal("lost host projection", name)
		}
	}
	if pod.Containers[1].ReadinessProbe == nil || pod.Containers[1].LivenessProbe == nil || pod.Containers[1].Lifecycle == nil {
		t.Fatal("missing native Felix lifecycle")
	}
	first, _ := json.Marshal(objects)
	again, _ := WindowsBGP(options())
	second, _ := json.Marshal(again)
	if string(first) != string(second) {
		t.Fatal("manifest bytes must be deterministic for pinning")
	}
}

func TestWindowsBGPRejectsIncompleteDeployment(t *testing.T) {
	for _, mutate := range []func(*WindowsBGPOptions){
		func(o *WindowsBGPOptions) { o.NodeImage = "ghcr.io/appmana/node:latest" },
		func(o *WindowsBGPOptions) { o.APIHost = "" },
		func(o *WindowsBGPOptions) { o.DNSAddress = "" },
		func(o *WindowsBGPOptions) { o.APIPort = "" },
		func(o *WindowsBGPOptions) { o.ServiceCIDR = "10.96.0.0" },
		func(o *WindowsBGPOptions) { o.AutodetectionMethod = "" },
		func(o *WindowsBGPOptions) { o.APIHost = "2001:db8::1" },
		func(o *WindowsBGPOptions) { o.DNSAddress = "192.0.2.1" },
		func(o *WindowsBGPOptions) { o.ServiceCIDR = "fd00::/64" },
	} {
		o := options()
		mutate(&o)
		if objects, err := WindowsBGP(o); err == nil || objects != nil {
			t.Fatal("accepted incomplete deployment")
		}
	}
}

func TestImagePullPolicyIsPortableAndExplicitlyOffline(t *testing.T) {
	o := options()
	objects, err := WindowsBGP(o)
	if err != nil {
		t.Fatal(err)
	}
	if got := objects[2].(*apps.DaemonSet).Spec.Template.Spec.Containers[0].ImagePullPolicy; got != v1.PullIfNotPresent {
		t.Fatalf("default cannot download pinned image: %s", got)
	}
	o.Offline = true
	objects, err = WindowsBGP(o)
	if err != nil {
		t.Fatal(err)
	}
	if objects[2].(*apps.DaemonSet).Spec.Template.Spec.Containers[0].ImagePullPolicy != v1.PullNever {
		t.Fatal("offline image must be preloaded")
	}
}
