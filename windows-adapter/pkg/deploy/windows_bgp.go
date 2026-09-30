// Package deploy renders caller-owned Kubernetes resources for stock k0s.
// It performs no installation, network access, or mutation of k0s-owned objects.
package deploy

import (
	"fmt"
	"net"
	"strings"

	"github.com/distribution/reference"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type WindowsBGPOptions struct {
	NodeImage, APIHost, APIPort, ServiceCIDR, DNSAddress, AutodetectionMethod string
	// Offline requires a preloaded, digest-matched image. Ordinary deployments
	// may download that same immutable image without changing its identity.
	Offline bool
}

// WindowsBGP mirrors the separately owned node/Felix/confd deployment used by
// GitOps. The distinct ConfigMap name avoids the stock k0s renderer's ConfigMap.
// k0s still owns Linux Calico, its CRDs, and the calico-node service account/RBAC.
// Apply these resources only after those prerequisites exist and only in BGP
// mode, where stock k0s does not generate a competing Windows DaemonSet.
func WindowsBGP(o WindowsBGPOptions) ([]runtime.Object, error) {
	ref, err := reference.ParseAnyReference(o.NodeImage)
	if err != nil {
		return nil, fmt.Errorf("node image: %w", err)
	}
	if _, ok := ref.(reference.Digested); !ok {
		return nil, fmt.Errorf("node image requires an immutable digest")
	}
	if net.ParseIP(o.APIHost).To4() == nil || net.ParseIP(o.DNSAddress).To4() == nil {
		return nil, fmt.Errorf("explicit IPv4 API and DNS addresses are required; IPv6 is not supported by this adapter yet")
	}
	serviceIP, serviceNet, err := net.ParseCIDR(o.ServiceCIDR)
	if err != nil || serviceIP.To4() == nil || !serviceNet.Contains(net.ParseIP(o.DNSAddress)) {
		return nil, fmt.Errorf("an IPv4 service CIDR containing the DNS address is required")
	}
	if o.APIPort != "443" && o.APIPort != "6443" {
		return nil, fmt.Errorf("explicit Kubernetes API port must be 443 or 6443")
	}
	if strings.TrimSpace(o.AutodetectionMethod) == "" {
		return nil, fmt.Errorf("explicit address autodetection method is required")
	}
	const configName = "calico-windows-config-actual"
	cm := &v1.ConfigMap{TypeMeta: meta.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: meta.ObjectMeta{Name: configName, Namespace: "kube-system"}, Data: map[string]string{
		"CALICO_NETWORKING_BACKEND": "windows-bgp", "DATASTORE_TYPE": "kubernetes",
		"KUBERNETES_SERVICE_HOST": o.APIHost, "KUBERNETES_SERVICE_PORT": o.APIPort,
		"K8S_SERVICE_CIDR": o.ServiceCIDR, "DNS_NAME_SERVERS": o.DNSAddress,
		"IP": "autodetect", "IP_AUTODETECTION_METHOD": o.AutodetectionMethod,
		"IP6": "none", "FELIX_IPV6SUPPORT": "false", "FELIX_HEALTHENABLED": "true",
		"CALICO_DSR_DISABLE": "true", "CNI_BIN_DIR": `c:\opt\cni\bin`, "CNI_CONF_DIR": `c:\etc\cni\net.d`,
		"KUBECONFIG": `c:\etc\cni\net.d\calico-kubeconfig`,
	}}
	labels := map[string]string{"k8s-app": "calico-node-windows"}
	maxUnavailable := intstr.FromInt32(1)
	hostProcess, system := true, `NT AUTHORITY\SYSTEM`
	ds := &apps.DaemonSet{TypeMeta: meta.TypeMeta{APIVersion: "apps/v1", Kind: "DaemonSet"}, ObjectMeta: meta.ObjectMeta{Name: "calico-node-windows", Namespace: "kube-system"}, Spec: apps.DaemonSetSpec{
		Selector: &meta.LabelSelector{MatchLabels: labels}, UpdateStrategy: apps.DaemonSetUpdateStrategy{Type: apps.RollingUpdateDaemonSetStrategyType, RollingUpdate: &apps.RollingUpdateDaemonSet{MaxUnavailable: &maxUnavailable}},
		Template: v1.PodTemplateSpec{ObjectMeta: meta.ObjectMeta{Labels: labels}, Spec: v1.PodSpec{
			ServiceAccountName: "calico-node", HostNetwork: true, NodeSelector: map[string]string{"kubernetes.io/os": "windows"},
			SecurityContext: &v1.PodSecurityContext{WindowsOptions: &v1.WindowsSecurityContextOptions{HostProcess: &hostProcess, RunAsUserName: &system}},
			Tolerations:     []v1.Toleration{{Operator: v1.TolerationOpExists, Effect: v1.TaintEffectNoSchedule}, {Operator: v1.TolerationOpExists, Effect: v1.TaintEffectNoExecute}},
			Volumes:         []v1.Volume{{Name: "host-root", VolumeSource: v1.VolumeSource{HostPath: &v1.HostPathVolumeSource{Path: "/"}}}},
		}},
	}}
	const root = "$env:CONTAINER_SANDBOX_MOUNT_POINT/CalicoWindows"
	pullPolicy := v1.PullIfNotPresent
	if o.Offline {
		pullPolicy = v1.PullNever
	}
	for _, entry := range []struct{ name, script string }{{"node", "/node-service.ps1"}, {"felix", "/felix-service.ps1"}, {"confd", "/confd/confd-service.ps1"}} {
		container := v1.Container{Name: entry.name, Image: o.NodeImage, ImagePullPolicy: pullPolicy, Args: []string{root + entry.script}, WorkingDir: root,
			VolumeMounts: []v1.VolumeMount{{Name: "host-root", MountPath: "/host"}},
			EnvFrom:      []v1.EnvFromSource{{ConfigMapRef: &v1.ConfigMapEnvSource{LocalObjectReference: v1.LocalObjectReference{Name: configName}}}},
			Env:          []v1.EnvVar{{Name: "NODENAME", ValueFrom: &v1.EnvVarSource{FieldRef: &v1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}}, {Name: "NODE_IP", ValueFrom: &v1.EnvVarSource{FieldRef: &v1.ObjectFieldSelector{FieldPath: "status.hostIP"}}}},
		}
		if entry.name == "felix" {
			container.ReadinessProbe = &v1.Probe{ProbeHandler: v1.ProbeHandler{Exec: &v1.ExecAction{Command: []string{root + "/calico-node.exe", "-felix-ready"}}}, PeriodSeconds: 10, TimeoutSeconds: 10}
			container.LivenessProbe = &v1.Probe{ProbeHandler: v1.ProbeHandler{Exec: &v1.ExecAction{Command: []string{root + "/calico-node.exe", "-felix-live"}}}, InitialDelaySeconds: 10, PeriodSeconds: 10, TimeoutSeconds: 10, FailureThreshold: 6}
			// felix-service.ps1 waits for node-service's HNS initialization
			// before starting Felix. Until then there is no health listener.
			// Gate normal liveness on the real server, never on wrapper life.
			container.StartupProbe = container.LivenessProbe.DeepCopy()
			container.StartupProbe.InitialDelaySeconds = 0
			container.StartupProbe.FailureThreshold = 60
			container.Lifecycle = &v1.Lifecycle{PreStop: &v1.LifecycleHandler{Exec: &v1.ExecAction{Command: []string{root + "/calico-node.exe", "-shutdown"}}}}
		}
		ds.Spec.Template.Spec.Containers = append(ds.Spec.Template.Spec.Containers, container)
	}
	ipam := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "crd.projectcalico.org/v1", "kind": "IPAMConfig", "metadata": map[string]interface{}{"name": "default"}, "spec": map[string]interface{}{"strictAffinity": true, "autoAllocateBlocks": true}}}
	return []runtime.Object{cm, ipam, ds}, nil
}
