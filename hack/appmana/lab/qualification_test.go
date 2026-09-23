package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/client"
	clab "github.com/appmana/labcontainers/pkg/containerlab"
	k0s "github.com/appmana/labcontainers/pkg/kubernetes/k0s"
	native "github.com/k0sproject/k0s/pkg/apis/k0s/v1beta1"
	"github.com/srl-labs/containerlab/core"
	"github.com/srl-labs/containerlab/links"
	"github.com/srl-labs/containerlab/types"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	appsapply "k8s.io/client-go/applyconfigurations/apps/v1"
	coreapply "k8s.io/client-go/applyconfigurations/core/v1"
)

func windowsAutodetectPatch() *appsapply.DaemonSetApplyConfiguration {
	// Generated apply types omit unset fields; a zero-valued DaemonSetSpec
	// would serialize selector:null and accidentally delete the selector.
	return appsapply.DaemonSet("calico-node-windows", "kube-system").WithSpec(
		appsapply.DaemonSetSpec().WithTemplate(coreapply.PodTemplateSpec().WithSpec(
			coreapply.PodSpec().WithContainers(
				coreapply.Container().WithName("node").WithEnv(coreapply.EnvVar().WithName("IP").WithValue("autodetect")),
				coreapply.Container().WithName("felix").WithEnv(coreapply.EnvVar().WithName("IP").WithValue("autodetect")),
			),
		)),
	)
}

func TestWindowsAutodetectPatchOmitsUnchangedFields(t *testing.T) {
	data, err := json.Marshal(windowsAutodetectPatch())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"selector"`, `"status"`, `"image"`, `null`} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("patch includes unchanged field %s: %s", forbidden, data)
		}
	}
}

// TestLiveK0sWindowsNetwork is a product qualification, not an SDK default.
// The caller supplies hash-verified offline media and both prepared VM images.
// Topology, cluster configuration, pods, and services are upstream Go objects.
func TestLiveK0sWindowsNetwork(t *testing.T) {
	media := os.Getenv("LABCONTAINERS_CALICO_MEDIA")
	if media == "" {
		t.Skip("set LABCONTAINERS_CALICO_MEDIA, its _SHA256, and both VM image inputs")
	}
	f, err := os.Open(media)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", h.Sum(nil)); got != os.Getenv("LABCONTAINERS_CALICO_MEDIA_SHA256") {
		t.Fatalf("media checksum mismatch: %s", got)
	}
	linuxImage, windowsImage := os.Getenv("LABCONTAINERS_VM_IMAGE"), os.Getenv("LABCONTAINERS_WINDOWS_IMAGE")
	if linuxImage == "" || windowsImage == "" {
		t.Fatal("both VM images must be explicit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	c, err := client.Launch(ctx, client.Options{LabdPath: os.Getenv("LABCONTAINERS_LABD")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	vm := func(image string) *types.NodeDefinition {
		return &types.NodeDefinition{
			Kind: "generic_vm", Image: image, NetworkMode: "none", ImagePullPolicy: "Never",
			Binds: []string{media + ":/qualification.iso:ro"}, Env: map[string]string{
				"QEMU_MEMORY": "8192", "QEMU_SMP": "4",
				"QEMU_ADDITIONAL_ARGS": "-drive file=/qualification.iso,format=raw,media=cdrom,readonly=on",
			},
		}
	}
	source, err := clab.Source(&core.Config{Topology: &types.Topology{
		Nodes: map[string]*types.NodeDefinition{"linux": vm(linuxImage), "windows": vm(windowsImage)},
		Links: []*links.LinkDefinition{{Link: &links.LinkBriefRaw{Endpoints: []string{"linux:eth1", "windows:eth1"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	lab, err := c.Start(ctx, &labv1.LabSpec{Topology: source, Nodes: map[string]*labv1.NodeExtension{"linux": {Control: "qga"}, "windows": {Control: "qga"}}}, 45*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("evidence: %s", lab.Artifacts())
	linux, windows := lab.Node("linux"), lab.Node("windows")
	psArgs := func(script string) []string {
		return []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference='Stop'; " + script}
	}
	// Transport failure is never evidence of a successful network outage.
	exec := func(node *client.Node, timeout time.Duration, args ...string) *labv1.ExecResponse {
		t.Helper()
		ectx, done := context.WithTimeout(ctx, timeout+5*time.Second)
		defer done()
		r, err := c.RPC().Exec(ectx, &labv1.ExecRequest{Node: node.Ref(), Argv: args, TimeoutMillis: timeout.Milliseconds()})
		if err != nil {
			t.Fatalf("serial exec transport: %v", err)
		}
		return r
	}
	run := func(node *client.Node, args ...string) []byte {
		t.Helper()
		out, err := node.Commands().Exec(ctx, args...)
		if err != nil {
			t.Fatalf("%v: %s: %v", args, out, err)
		}
		return out
	}
	wait := func(node *client.Node, duration time.Duration, args ...string) []byte {
		t.Helper()
		wctx, done := context.WithTimeout(ctx, duration)
		defer done()
		for {
			out, err := node.Commands().Exec(wctx, args...)
			if err == nil {
				return out
			}
			if wctx.Err() != nil {
				t.Fatalf("waiting for %v: %s: %v", args, out, err)
			}
			time.Sleep(2 * time.Second)
		}
	}
	defer func() {
		if t.Failed() {
			dctx, done := context.WithTimeout(context.Background(), 90*time.Second)
			defer done()
			out, err := linux.Commands().Exec(dctx, "sh", "-c", "k0s kubectl get nodes,pods -A -o wide; k0s kubectl get events -A --sort-by=.lastTimestamp | tail -60; journalctl -u k0scontroller -n 50 --no-pager")
			t.Logf("Linux diagnostics: %s (%v)", out, err)
			// Use serial control: diagnostics must remain available when the
			// dataplane or Windows kubelet connectivity is broken.
			out, err = windows.Commands().Exec(dctx, psArgs(`Get-Date -Format o; Get-NetAdapter | Format-Table -AutoSize; Get-NetRoute | Format-Table -AutoSize; Get-HnsNetwork | ConvertTo-Json -Depth 12; Get-HnsEndpoint | ConvertTo-Json -Depth 12; if(Test-Path C:\var\log\calico\cni\cni.log){Get-Content C:\var\log\calico\cni\cni.log -Tail 100}`)...)
			t.Logf("Windows diagnostics: %s (%v)", out, err)
		}
	}()
	wait(linux, 3*time.Minute, "test", "-b", "/dev/disk/by-label/LCQUAL")
	wait(windows, 5*time.Minute, psArgs(`if (!(Get-Volume -FileSystemLabel LCQUAL -ErrorAction SilentlyContinue)) { throw 'media unavailable' }`)...)
	// QEMU supplies a UTC RTC. A Windows image using Pacific local hardware
	// time otherwise starts seven hours ahead and corrupts token/cache timing.
	run(windows, psArgs(fmt.Sprintf(`Set-TimeZone -Id UTC; Set-Date -Date ([DateTime]::Parse('%s').ToLocalTime()) | Out-Null; $v=Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'; if($v.CurrentBuild -ne '20348' -or $v.UBR -lt 5622){throw 'unexpected Windows kernel'}; New-NetFirewallRule -Name LabQualificationKubelet -DisplayName LabQualificationKubelet -Direction Inbound -Action Allow -Protocol TCP -LocalPort 10250 -RemoteAddress 192.0.2.10 | Out-Null`, time.Now().UTC().Format(time.RFC3339)))...)
	run(linux, "sh", "-ec", `
iface=$(ls /sys/class/net | grep -v '^lo$')
test "$(printf '%s\n' "$iface" | wc -l)" = 1
ip link set "$iface" up
ip addr add 192.0.2.10/24 dev "$iface"
ip route add 10.96.0.0/12 dev "$iface"
# Calico's proxy-ARP gateway needs a unicast route even without a default.
ip route add 169.254.1.1/32 dev "$iface"
test -z "$(ip -4 route show default)"
test -z "$(ip -6 route show default)"
test ! -e /var/lib/k0s
mkdir -p /mnt/qualification /var/lib/k0s/images
mount -o ro /dev/disk/by-label/LCQUAL /mnt/qualification
install -m 0755 /mnt/qualification/k0s /usr/local/bin/k0s
cp /mnt/qualification/linux-*.tar /var/lib/k0s/images/
`)
	t.Log("preparing Windows Containers feature (separate provisioning deadline)")
	features := exec(windows, 8*time.Minute, psArgs(`$v=Get-CimInstance Win32_OperatingSystem; if($v.Version -ne '10.0.20348'){throw "unexpected Windows $($v.Version)"}; $n=@(Get-NetAdapter -Physical); if($n.Count -ne 1){throw 'expected one NIC'}; if((Get-WindowsFeature Containers).Installed){'ready'}else{$r=Install-WindowsFeature Containers; if(!$r.Success){throw 'Containers feature failed'}; 'reboot'}`)...)
	if features.ExitCode != 0 {
		t.Fatalf("Windows feature preparation: %s %s (exit %d)", features.Stdout, features.Stderr, features.ExitCode)
	}
	if strings.Contains(string(features.Stdout), "reboot") {
		run(windows, psArgs(`shutdown.exe /r /t 2; if($LASTEXITCODE -ne 0){throw 'reboot failed'}`)...)
		time.Sleep(10 * time.Second)
		wait(windows, 5*time.Minute, psArgs(`if(!(Get-WindowsFeature Containers).Installed){throw 'Containers missing'}`)...)
	}
	run(windows, psArgs(`$n=@(Get-NetAdapter -Physical); if($n.Count -ne 1){throw 'expected one NIC'}; Set-NetIPInterface -InterfaceIndex $n[0].ifIndex -AddressFamily IPv4 -Dhcp Disabled; New-NetIPAddress -InterfaceIndex $n[0].ifIndex -IPAddress 192.0.2.20 -PrefixLength 24 | Out-Null; if(Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue){throw 'unexpected default route'}; $v=Get-Volume -FileSystemLabel LCQUAL; $media="$($v.DriveLetter):\"; if(Test-Path C:\var\lib\k0s){throw 'unexpected prior k0s state'}; New-Item -ItemType Directory -Force C:\LabQualification,C:\var\lib\k0s\images | Out-Null; Copy-Item ($media+'k0s.exe') C:\LabQualification\k0s.exe; Copy-Item ($media+'windows-*.tar') C:\var\lib\k0s\images\; & C:\LabQualification\k0s.exe version; if($LASTEXITCODE -ne 0){throw 'k0s version failed'}`)...)
	image := func(repo, digest string) *native.ImageSpec {
		return &native.ImageSpec{Image: repo, Version: "pinned@sha256:" + digest}
	}
	images := &native.ClusterImages{DefaultPullPolicy: "Never",
		Pause:     image("quay.io/k0sproject/pause", "3fd84d58de3c3c61df545c1597fb76427a014a60778538225776e619214e8e90"),
		CoreDNS:   image("quay.io/k0sproject/coredns", "cf936e390f35ab76f62d2fdb0ebfc265b94acdfdb71c3345c50568f55dcbb82b"),
		KubeProxy: &native.ImageSpec{Image: "docker.io/labcontainers/kube-proxy", Version: "a2c4329d5a8-linux"},
		Calico: &native.CalicoImageSpec{
			Node:            image("ghcr.io/appmana/node", "000a21d168a52d279f60bbc5ce18c3667f239ecbaeecb4a21ec312810db00be0"),
			CNI:             &native.ImageSpec{Image: "docker.io/labcontainers/calico-cni", Version: "b55378edd776-linux"},
			KubeControllers: image("ghcr.io/appmana/kube-controllers", "007baf8198a9523a7ffc59273aa70aca05d937acc45aa81f20109b1d3f48b133"),
			Windows: &native.CalicoWindowsImageSpec{
				Node: image("ghcr.io/appmana/node", "c198551c1cad94daaabe49c556fdad27d97ca7743ae874b2095ba15e9e06cbbf"),
				CNI:  image("ghcr.io/appmana/cni-windows", "1f238a6b0eb70a82c1b92c3551d78ef4f0a46ab60f97faaddc40d751b331c42a"),
			},
		},
		Windows: &native.WindowsImageSpec{Pause: image("registry.k8s.io/pause", "3d33315f585d65b89f70cba238c3e4f66b96d576b3f40af801ceb1b3c7bfb5b9"), KubeProxy: image("ghcr.io/appmana/kube-proxy", "c544cb2761f6b67b4acba16183355f8d5b0fecb11626e244da26f80ef9161c15")},
	}
	// CoreDNS's own configuration language is passed through in its native
	// Kubernetes ConfigMap. No upstream DNS exists in this isolated topology.
	dnsPatch, err := json.Marshal(&v1.ConfigMap{Data: map[string]string{"Corefile": `.:53 {
    errors
    health
    ready
    kubernetes cluster.local in-addr.arpa ip6.arpa {
        pods insecure
        ttl 30
    }
    prometheus :9153
    cache 30
    reload
    loadbalance
}
`}})
	if err != nil {
		t.Fatal(err)
	}
	// k0s currently copies the autodetection method into IP as well as
	// IP_AUTODETECTION_METHOD in its Windows template. IP expects an address
	// or "autodetect", not a method expression.
	windowsCalicoPatch, err := json.Marshal(windowsAutodetectPatch())
	if err != nil {
		t.Fatal(err)
	}
	config := &native.ClusterConfig{TypeMeta: metav1.TypeMeta{APIVersion: native.ClusterConfigAPIVersion, Kind: native.ClusterConfigKind}, Spec: &native.ClusterSpec{
		API: &native.APISpec{Address: "192.0.2.10", SANs: []string{"192.0.2.10"}}, Images: images,
		Network: &native.Network{Provider: "calico", PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12", Calico: &native.Calico{Mode: native.CalicoModeVXLAN, MTU: 1450, VxlanVNI: 4096, VxlanPort: 4789, IPAutodetectionMethod: "can-reach=192.0.2.20"}, CoreDNS: &native.CoreDNS{Patches: native.Patches{{Target: native.PatchTarget{Kind: "ConfigMap", Name: "coredns", Namespace: "kube-system"}, Patch: native.PatchSpec{Type: native.MergePatchType, Content: string(dnsPatch)}}}}},
	}}
	config.Spec.Network.Calico.Patches = native.Patches{{Target: native.PatchTarget{Kind: "DaemonSet", Name: "calico-node-windows", Namespace: "kube-system"}, Patch: native.PatchSpec{Type: native.StrategicMergePatchType, Content: string(windowsCalicoPatch)}}}
	if err := k0s.WriteConfig(ctx, linux.Commands(), "/etc/k0s/k0s.yaml", config); err != nil {
		t.Fatal(err)
	}
	if err := k0s.Install(ctx, linux.Commands(), "controller", "--enable-worker", "--no-taints", "--config=/etc/k0s/k0s.yaml", "--disable-components=metrics-server,konnectivity-server,autopilot", "--kubelet-extra-args=--node-ip=192.0.2.10 --hostname-override=linux"); err != nil {
		t.Fatal(err)
	}
	run(linux, "k0s", "start")
	wait(linux, 5*time.Minute, "k0s", "kubectl", "get", "--raw", "/readyz")
	token := run(linux, "k0s", "token", "create", "--role=worker", "--expiry=1h")
	if err := windows.Put(ctx, `C:\LabQualification\token`, 0600, bytes.TrimSpace(token)); err != nil {
		t.Fatal(err)
	}
	run(windows, psArgs(`& C:\LabQualification\k0s.exe install worker --token-file C:\LabQualification\token --kubelet-extra-args '--node-ip=192.0.2.20 --hostname-override=windows'; if($LASTEXITCODE -ne 0){throw 'worker install failed'}; & C:\LabQualification\k0s.exe start; if($LASTEXITCODE -ne 0){throw 'worker start failed'}`)...)
	wait(linux, 7*time.Minute, "k0s", "kubectl", "wait", "--for=condition=Ready", "node/linux", "node/windows", "--timeout=10s")
	winImage := "mcr.microsoft.com/windows/servercore@sha256:e10503b9a4f7faafa30aa0f5d0e8e7f7ca30a4496b3b87d61178b4d7c6815fb5"
	t.Log("preparing Windows workload layers before kubelet CreateContainer")
	prepared := exec(windows, 12*time.Minute, psArgs(`& C:\LabQualification\k0s.exe ctr images import --local --snapshotter windows C:\var\lib\k0s\images\windows-workload.tar; if($LASTEXITCODE -ne 0){exit $LASTEXITCODE}; $ready=@(& C:\LabQualification\k0s.exe ctr images check --snapshotter windows --quiet); if($LASTEXITCODE -ne 0){exit $LASTEXITCODE}; $ready; if($ready -notcontains '`+winImage+`'){throw 'workload image not completely unpacked'}`)...)
	t.Logf("Windows layer preparation: %s %s", prepared.Stdout, prepared.Stderr)
	if prepared.ExitCode != 0 {
		t.Fatalf("Windows layer preparation exited %d", prepared.ExitCode)
	}
	// The Windows VXLAN CNI updates Calico node annotations via Nodes.UpdateStatus.
	// k0s's CNI role omits that permission; grant only the named Windows node,
	// without replacing the upstream role or granting broad cluster-admin.
	objects := []runtime.Object{
		&rbacv1.ClusterRole{TypeMeta: metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRole"}, ObjectMeta: metav1.ObjectMeta{Name: "qualification-windows-cni"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"nodes/status"}, ResourceNames: []string{"windows"}, Verbs: []string{"update"}}}},
		&rbacv1.ClusterRoleBinding{TypeMeta: metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRoleBinding"}, ObjectMeta: metav1.ObjectMeta{Name: "qualification-windows-cni"}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "qualification-windows-cni"}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "calico-cni-plugin", Namespace: "kube-system"}}},
	}
	for _, name := range []string{"linux", "windows"} {
		container := v1.Container{Name: "server", Image: "docker.io/nicolaka/netshoot@sha256:34eeca872db74067b1ed7fdc6201f278578bf57df7bd3081e99b5097a28464b5", ImagePullPolicy: v1.PullNever, Command: []string{"sh", "-ec", `mkdir -p /tmp/www; printf 'ok linux' > /tmp/www/index.html; exec httpd -f -p 8080 -h /tmp/www`}}
		if name == "windows" {
			container.Image = winImage
			container.Command = psArgs(`$l=[Net.Sockets.TcpListener]::new([Net.IPAddress]::Any,8080); $l.Start(); while($true){$c=$l.AcceptTcpClient();try{$s=$c.GetStream();$s.ReadTimeout=5000;$b=New-Object byte[] 4096;$null=$s.Read($b,0,$b.Length);$r=[Text.Encoding]::ASCII.GetBytes("HTTP/1.1 200 OK` + "`r`nContent-Length: 10`r`nConnection: close`r`n`r`nok windows" + `");$s.Write($r,0,$r.Length)}catch{}finally{$c.Dispose()}}`)
		}
		objects = append(objects, &v1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: "hc-" + name, Namespace: "default", Labels: map[string]string{"app": "hc-" + name}}, Spec: v1.PodSpec{NodeName: name, Containers: []v1.Container{container}, RestartPolicy: v1.RestartPolicyAlways}},
			&v1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Name: "svc-hc-" + name + "-v4", Namespace: "default"}, Spec: v1.ServiceSpec{Selector: map[string]string{"app": "hc-" + name}, Ports: []v1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt32(8080)}}}})
	}
	list := &metav1.List{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "List"}}
	for _, object := range objects {
		list.Items = append(list.Items, runtime.RawExtension{Object: object})
	}
	body, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := linux.Commands().Pipe(ctx, bytes.NewReader(body), "k0s", "kubectl", "apply", "-f", "-"); err != nil {
		t.Fatalf("apply: %s: %v", out, err)
	}
	wait(linux, 5*time.Minute, "k0s", "kubectl", "wait", "--for=condition=Ready", "pod/hc-linux", "pod/hc-windows", "--timeout=10s")
	healthScript, err := os.ReadFile("../ipv6-health-check.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := linux.Put(ctx, "/usr/local/bin/calico-health-check", 0755, healthScript); err != nil {
		t.Fatal(err)
	}
	if err := linux.Put(ctx, "/usr/local/bin/kubectl", 0755, []byte("#!/bin/sh\nexec /usr/local/bin/k0s kubectl --request-timeout=15s \"$@\"\n")); err != nil {
		t.Fatal(err)
	}
	health := func(phase string) {
		t.Helper()
		r := exec(linux, 3*time.Minute, "bash", "/usr/local/bin/calico-health-check", "--existing", "--namespace", "default", "--ipv4-only", "--skip-external", "--skip-inbound", "linux", "windows")
		t.Logf("%s health script: %s %s", phase, r.Stdout, r.Stderr)
		if r.ExitCode != 0 || !strings.Contains(string(r.Stdout), "Total: 10  Pass: 10  Fail: 0") {
			t.Fatalf("%s health matrix failed (exit %d)", phase, r.ExitCode)
		}
	}
	get := func(kind, name, field string) string {
		return strings.TrimSpace(string(run(linux, "k0s", "kubectl", "get", kind, name, "-o", "jsonpath={"+field+"}")))
	}
	winIP, serviceIP := get("pod", "hc-windows", ".status.podIP"), get("service", "svc-hc-windows-v4", ".spec.clusterIP")
	linuxIP, linuxServiceIP := get("pod", "hc-linux", ".status.podIP"), get("service", "svc-hc-linux-v4", ".spec.clusterIP")
	cid := strings.TrimPrefix(get("pod", "hc-windows", ".status.containerStatuses[0].containerID"), "containerd://")
	if cid == "" {
		t.Fatal("missing Windows container ID")
	}
	probe := func(node *client.Node, target string) *labv1.ExecResponse {
		if node == linux {
			return exec(node, 20*time.Second, "k0s", "kubectl", "--request-timeout=15s", "exec", "hc-linux", "--", "sh", "-c", `command -v curl >/dev/null || exit 90; curl --fail --silent --show-error --connect-timeout 3 --max-time 5 "$1"`, "probe", "http://"+target+":8080")
		}
		// Execute inside the ordinary container through serial -> local runtime,
		// never through the API server/kubelet path being deliberately severed.
		return exec(node, 20*time.Second, `C:\LabQualification\k0s.exe`, "ctr", "tasks", "exec", "--exec-id", fmt.Sprintf("probe-%d", time.Now().UnixNano()), cid, "curl.exe", "--fail", "--silent", "--show-error", "--connect-timeout", "3", "--max-time", "5", "http://"+target+":8080")
	}
	check := func(node *client.Node, target, body string, outage bool) {
		t.Helper()
		r := probe(node, target)
		t.Logf("probe %s target=%s exit=%d body=%q stderr=%q", node.Ref().GetNode(), target, r.ExitCode, r.Stdout, r.Stderr)
		if outage {
			// curl 7 = connection failed, 28 = request deadline. Other failures
			// (missing command, runtime failure, HTTP errors) are not outages.
			if r.ExitCode != 7 && r.ExitCode != 28 {
				t.Fatalf("not an expected network failure: %d", r.ExitCode)
			}
		} else if r.ExitCode != 0 || strings.TrimSpace(string(r.Stdout)) != body {
			t.Fatalf("unexpected reachability result: %+v", r)
		}
	}
	health("baseline")
	check(windows, linuxIP, "ok linux", false)
	check(windows, linuxServiceIP, "ok linux", false)
	fault, err := lab.SetLink(ctx, "windows", "eth1", false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		rctx, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		if err := fault.Revert(rctx); err != nil {
			t.Errorf("restore link: %v", err)
		}
	}()
	check(linux, linuxIP, "ok linux", false)
	check(windows, winIP, "ok windows", false)
	for _, target := range []string{winIP, serviceIP} {
		check(linux, target, "", true)
	}
	for _, target := range []string{linuxIP, linuxServiceIP} {
		check(windows, target, "", true)
	}
	if err := fault.Revert(ctx); err != nil {
		t.Fatal(err)
	}
	health("recovery")
	t.Log("bidirectional ordinary pod, ClusterIP, DNS, sole-path failure, and recovery verified")
}
