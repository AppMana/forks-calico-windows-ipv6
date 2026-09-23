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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

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
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
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
	lab, err := c.Start(ctx, &labv1.LabSpec{Topology: source, Nodes: map[string]*labv1.NodeExtension{"linux": {Control: "qga"}, "windows": {Control: "qga"}}}, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("evidence: %s", lab.Artifacts())
	linux, windows := lab.Node("linux"), lab.Node("windows")
	psArgs := func(script string) []string {
		return []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference='Stop'; " + script}
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
		deadline := time.Now().Add(duration)
		for {
			out, err := node.Commands().Exec(ctx, args...)
			if err == nil {
				return out
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				t.Fatalf("waiting for %v: %s: %v", args, out, err)
			}
			time.Sleep(2 * time.Second)
		}
	}
	defer func() {
		if t.Failed() {
			dctx, done := context.WithTimeout(context.Background(), 30*time.Second)
			defer done()
			out, err := linux.Commands().Exec(dctx, "sh", "-c", "k0s kubectl get nodes,pods -A -o wide; journalctl -u k0scontroller -n 50 --no-pager")
			t.Logf("diagnostics: %s (%v)", out, err)
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
test -z "$(ip -4 route show default)"
test -z "$(ip -6 route show default)"
test ! -e /var/lib/k0s
mkdir -p /mnt/qualification /var/lib/k0s/images
mount -o ro /dev/disk/by-label/LCQUAL /mnt/qualification
install -m 0755 /mnt/qualification/k0s /usr/local/bin/k0s
cp /mnt/qualification/linux-*.tar /var/lib/k0s/images/
`)
	features := run(windows, psArgs(`$v=Get-CimInstance Win32_OperatingSystem; if($v.Version -ne '10.0.20348'){throw "unexpected Windows $($v.Version)"}; $n=@(Get-NetAdapter -Physical); if($n.Count -ne 1){throw 'expected one NIC'}; if((Get-WindowsFeature Containers).Installed){'ready'}else{$r=Install-WindowsFeature Containers; if(!$r.Success){throw 'Containers feature failed'}; 'reboot'}`)...)
	if strings.Contains(string(features), "reboot") {
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
			CNI:             image("ghcr.io/appmana/cni", "94e7f665707d7f604cc927877e5ab0f5741121ab61965ab07eb172ec2a43ea80"),
			KubeControllers: image("ghcr.io/appmana/kube-controllers", "007baf8198a9523a7ffc59273aa70aca05d937acc45aa81f20109b1d3f48b133"),
			Windows: &native.CalicoWindowsImageSpec{
				Node: image("ghcr.io/appmana/node", "c198551c1cad94daaabe49c556fdad27d97ca7743ae874b2095ba15e9e06cbbf"),
				CNI:  image("ghcr.io/appmana/cni-windows", "1f238a6b0eb70a82c1b92c3551d78ef4f0a46ab60f97faaddc40d751b331c42a"),
			},
		},
		Windows: &native.WindowsImageSpec{Pause: image("registry.k8s.io/pause", "3d33315f585d65b89f70cba238c3e4f66b96d576b3f40af801ceb1b3c7bfb5b9"), KubeProxy: image("ghcr.io/appmana/kube-proxy", "c544cb2761f6b67b4acba16183355f8d5b0fecb11626e244da26f80ef9161c15")},
	}
	config := &native.ClusterConfig{TypeMeta: metav1.TypeMeta{APIVersion: native.ClusterConfigAPIVersion, Kind: native.ClusterConfigKind}, Spec: &native.ClusterSpec{
		API: &native.APISpec{Address: "192.0.2.10", SANs: []string{"192.0.2.10"}}, Images: images,
		Network: &native.Network{Provider: "calico", PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12", Calico: &native.Calico{Mode: native.CalicoModeVXLAN, MTU: 1450, VxlanVNI: 4096, VxlanPort: 4789, IPAutodetectionMethod: "can-reach=192.0.2.20"}},
	}}
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
	objects := []runtime.Object{}
	for _, name := range []string{"linux", "windows"} {
		container := v1.Container{Name: "server", Image: "docker.io/library/alpine:3.20", ImagePullPolicy: v1.PullNever, Command: []string{"sh", "-ec", "mkdir -p /www; echo linux >/www/index.html; exec httpd -f -p 8080 -h /www"}}
		if name == "windows" {
			container.Image = winImage
			container.Command = psArgs(`$l=[Net.Sockets.TcpListener]::new([Net.IPAddress]::Any,8080); $l.Start(); while($true){$c=$l.AcceptTcpClient();try{$s=$c.GetStream();$b=New-Object byte[] 4096;$null=$s.Read($b,0,$b.Length);$r=[Text.Encoding]::ASCII.GetBytes("HTTP/1.1 200 OK` + "`r`nContent-Length: 7`r`nConnection: close`r`n`r`nwindows" + `");$s.Write($r,0,$r.Length)}finally{$c.Dispose()}}`)
		}
		objects = append(objects, &v1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: name + "-server", Namespace: "default", Labels: map[string]string{"app": name + "-server"}}, Spec: v1.PodSpec{NodeName: name, Containers: []v1.Container{container}, RestartPolicy: v1.RestartPolicyAlways}},
			&v1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Name: name + "-server", Namespace: "default"}, Spec: v1.ServiceSpec{Selector: map[string]string{"app": name + "-server"}, Ports: []v1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt32(8080)}}}})
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
	wait(linux, 5*time.Minute, "k0s", "kubectl", "wait", "--for=condition=Ready", "pod/linux-server", "pod/windows-server", "--timeout=10s")
	winIP := strings.TrimSpace(string(run(linux, "k0s", "kubectl", "get", "pod", "windows-server", "-o", "jsonpath={.status.podIP}")))
	serviceIP := strings.TrimSpace(string(run(linux, "k0s", "kubectl", "get", "service", "windows-server", "-o", "jsonpath={.spec.clusterIP}")))
	probe := func(target string) []string {
		return []string{"k0s", "kubectl", "exec", "linux-server", "--", "wget", "-T", "3", "-qO-", "http://" + target + ":8080"}
	}
	for _, target := range []string{winIP, serviceIP, "windows-server.default.svc.cluster.local"} {
		out := wait(linux, time.Minute, probe(target)...)
		if strings.TrimSpace(string(out)) != "windows" {
			t.Fatalf("unexpected reply %q", out)
		}
	}
	fault, err := lab.SetLink(ctx, "windows", "eth1", false)
	if err != nil {
		t.Fatal(err)
	}
	defer fault.Revert(context.Background())
	for _, target := range []string{winIP, serviceIP} {
		out, err := linux.Commands().Exec(ctx, probe(target)...)
		if err == nil {
			t.Fatalf("reachability survived sole-path cut: %s: %s", target, out)
		}
	}
	if err := fault.Revert(ctx); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{winIP, serviceIP} {
		wait(linux, time.Minute, probe(target)...)
	}
	t.Log("ordinary Windows pod, ClusterIP, DNS, sole-path failure, and recovery verified")
}
