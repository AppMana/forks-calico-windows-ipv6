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
	for key, want := range map[string]string{"IP6": "none", "FELIX_IPV6SUPPORT": "false", "CALICO_NETWORKING_BACKEND": "windows-bgp", "IP": "autodetect", "IP_AUTODETECTION_METHOD": options().AutodetectionMethod, "KUBERNETES_SERVICE_HOST": options().APIHost, "KUBECONFIG": `c:\etc\cni\net.d\calico-kubeconfig`, "CALICO_DSR_DISABLE": "true"} {
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
		func(o *WindowsBGPOptions) { o.IPv6AutodetectionMethod = "  " },
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

// The real Windows lab killed Felix while felix-service.ps1 was still in
// Wait-ForCalicoInit. Its health server does not exist until that wait ends.
// Startup must be checked separately, without declaring a waiting process
// ready or weakening liveness once Felix has actually started.
func TestFelixInitializationDoesNotConsumeRunningLivenessBudget(t *testing.T) {
	objects, err := WindowsBGP(options())
	if err != nil {
		t.Fatal(err)
	}
	felix := objects[2].(*apps.DaemonSet).Spec.Template.Spec.Containers[1]
	if felix.StartupProbe == nil {
		t.Fatal("Felix can be killed by liveness while waiting for Calico initialization")
	}
	startup := felix.StartupProbe
	if startup.Exec == nil || strings.Join(startup.Exec.Command, " ") != strings.Join(felix.LivenessProbe.Exec.Command, " ") {
		t.Fatal("startup must require the real Felix health server, not a successful wrapper wait")
	}
	if startup.PeriodSeconds != 10 || startup.TimeoutSeconds != 10 || startup.FailureThreshold != 60 {
		t.Fatal("startup must have an explicit bounded initialization window")
	}
	if felix.LivenessProbe.PeriodSeconds != 10 || felix.LivenessProbe.FailureThreshold != 6 ||
		felix.ReadinessProbe.Exec.Command[1] != "-felix-ready" {
		t.Fatal("startup protection must not weaken running liveness or readiness")
	}
}

// In the real VyOS lab Felix became ready at 04:20:37, before node startup
// removed the External HNS network at 04:20:40. The management address then
// disappeared while the DaemonSet already reported all containers ready.
// Felix health alone must not claim node initialization has completed.
func TestWindowsBGPNodeInitializationHasIndependentReadiness(t *testing.T) {
	objects, err := WindowsBGP(options())
	if err != nil {
		t.Fatal(err)
	}
	node := objects[2].(*apps.DaemonSet).Spec.Template.Spec.Containers[0]
	if node.Name != "node" {
		t.Fatal("expected node initialization container")
	}
	if node.ReadinessProbe == nil || node.ReadinessProbe.Exec == nil || len(node.ReadinessProbe.Exec.Command) == 0 {
		t.Fatal("node initialization is implicitly ready while HNS replacement is still in progress")
	}
	if strings.Contains(strings.Join(node.ReadinessProbe.Exec.Command, " "), "-felix-ready") {
		t.Fatal("Felix readiness does not prove node initialization completed")
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
