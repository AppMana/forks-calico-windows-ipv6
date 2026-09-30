package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/client"
	clab "github.com/appmana/labcontainers/pkg/containerlab"
	matrix "github.com/appmana/labcontainers/pkg/kubernetes"
	k0s "github.com/appmana/labcontainers/pkg/kubernetes/k0s"
	native "github.com/k0sproject/k0s/pkg/apis/k0s/v1beta1"
	"github.com/srl-labs/containerlab/core"
	"github.com/srl-labs/containerlab/links"
	"github.com/srl-labs/containerlab/types"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// TestLiveK0sWindowsNetwork is a product qualification, not an SDK default.
// The caller supplies hash-verified offline media and both prepared VM images.
// Topology, cluster configuration, pods, and services are upstream Go objects.
func TestLiveK0sWindowsNetwork(t *testing.T) {
	qualifyK0sWindowsNetwork(t, false)
}

func TestLiveK0sWindowsWAN(t *testing.T) {
	qualifyK0sWindowsNetwork(t, true)
}

func qualificationTopology(linux, windows *types.NodeDefinition, wanImage string) *core.Config {
	config := &core.Config{Topology: &types.Topology{
		Nodes: map[string]*types.NodeDefinition{"linux": linux, "windows": windows},
		Links: []*links.LinkDefinition{{Link: &links.LinkBriefRaw{Endpoints: []string{"linux:eth1", "windows:eth1"}}}},
	}}
	if wanImage != "" {
		// Only the gateway has an external attachment. Containerlab calls its
		// runtime network "mgmt"; here it is exclusively the gateway's WAN.
		external := true
		config.Mgmt = &types.MgmtNet{Network: fmt.Sprintf("lc-wan-%d", time.Now().UnixNano()), IPv4Subnet: "172.31.254.0/24", ExternalAccess: &external}
		config.Topology.Nodes["gateway"] = &types.NodeDefinition{Kind: "linux", Image: wanImage, NetworkMode: "bridge", ImagePullPolicy: "Never", Entrypoint: "/bin/sleep", Cmd: "infinity", Sysctls: map[string]string{"net.ipv4.ip_forward": "1"}}
		config.Topology.Links = []*links.LinkDefinition{
			{Link: &links.LinkBriefRaw{Endpoints: []string{"linux:eth1", "gateway:eth1"}}},
			{Link: &links.LinkBriefRaw{Endpoints: []string{"windows:eth1", "gateway:eth2"}}},
		}
	}
	return config
}

func TestQualificationWANIsExplicit(t *testing.T) {
	for _, wanImage := range []string{"", "router:test"} {
		config := qualificationTopology(&types.NodeDefinition{NetworkMode: "none"}, &types.NodeDefinition{NetworkMode: "none"}, wanImage)
		for _, name := range []string{"linux", "windows"} {
			if config.Topology.Nodes[name].NetworkMode != "none" {
				t.Fatalf("%s has an implicit network", name)
			}
			count := 0
			for _, link := range config.Topology.Links {
				for _, endpoint := range link.Link.(*links.LinkBriefRaw).Endpoints {
					if endpoint == name+":eth1" {
						count++
					}
				}
			}
			if count != 1 {
				t.Fatalf("%s must have exactly one dataplane NIC", name)
			}
		}
		if (config.Mgmt != nil) != (wanImage != "") {
			t.Fatal("WAN must be explicit")
		}
	}
}

func qualifyK0sWindowsNetwork(t *testing.T, wan bool) {
	tuple, err := readQualificationTuple(os.Getenv)
	if err != nil {
		t.Fatalf("qualification matrix: %v", err)
	}
	retainFor, err := qualificationRetention(os.Getenv("LABCONTAINERS_RETAIN_TTL"))
	if err != nil {
		t.Fatal(err)
	}
	failureRetainFor, err := qualificationRetention(os.Getenv("LABCONTAINERS_RETAIN_ON_FAILURE_TTL"))
	if err != nil {
		t.Fatalf("failure retention: %v", err)
	}
	workload, workloadArgs, err := readKubernetesWorkload(os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD"), os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD_SHA256"), os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD_ARGS"), os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD_SUCCESS"))
	if err != nil {
		t.Fatal(err)
	}
	media := os.Getenv("LABCONTAINERS_CALICO_MEDIA")
	crashVerify := os.Getenv("LABCONTAINERS_KUBERNETES_CRASH_VERIFY")
	if crashVerify != "" && (crashVerify != "1" || workload == nil) {
		t.Fatal("crash verification requires explicit 1 and a pinned workload")
	}
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
	if err := verifyWindowsMedia(media); err != nil {
		t.Fatal(err)
	}
	linuxImage, windowsImage := os.Getenv("LABCONTAINERS_VM_IMAGE"), os.Getenv("LABCONTAINERS_WINDOWS_IMAGE")
	if linuxImage == "" || windowsImage == "" {
		t.Fatal("both VM images must be explicit")
	}
	budget := 40 * time.Minute
	if workload != nil {
		budget += 40 * time.Minute
	}
	if crashVerify == "1" {
		budget += 40 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	stateDir, err := qualificationStateDir(os.Getenv("LABCONTAINERS_STATE_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.Launch(ctx, client.Options{LabdPath: os.Getenv("LABCONTAINERS_LABD"), StateDir: stateDir})
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
	wanImage := ""
	if wan {
		wanImage = os.Getenv("LABCONTAINERS_WAN_IMAGE")
		if wanImage == "" {
			t.Fatal("WAN qualification requires explicit LABCONTAINERS_WAN_IMAGE with ip and iptables")
		}
	}
	source, err := clab.Source(qualificationTopology(vm(linuxImage), vm(windowsImage), wanImage))
	if err != nil {
		t.Fatal(err)
	}
	lab, err := c.Start(ctx, &labv1.LabSpec{Topology: source, AllowExternalAccess: wan, Nodes: freshQualificationNodes()}, budget+5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("evidence: %s", lab.Artifacts())
	if retainFor > 0 {
		if err := lab.Keep(ctx, retainFor); err != nil {
			t.Fatal(err)
		}
		t.Logf("retained session=%s socket=%s state=%s ttl=%s", lab.ID(), c.Socket(), c.StateDirectory(), retainFor)
	}
	linux, windows := lab.Node("linux"), lab.Node("windows")
	defer func() {
		if !t.Failed() || failureRetainFor == 0 {
			return
		}
		err := retainFailedQualification(func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return lab.Keep(ctx, failureRetainFor)
		}, func(name string) error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			return lab.Node(name).PowerOff(ctx)
		})
		if err != nil {
			t.Errorf("retain/power off failed lab: %v", err)
		} else {
			t.Logf("failed lab retained powered off: session=%s socket=%s state=%s ttl=%s", lab.ID(), c.Socket(), c.StateDirectory(), failureRetainFor)
		}
	}()
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
			dctx, done := context.WithTimeout(context.Background(), 30*time.Second)
			out, err := linux.Commands().Exec(dctx, "sh", "-c", "k0s kubectl get nodes,pods -A -o wide; k0s kubectl get events -A --sort-by=.lastTimestamp | tail -60; k0s kubectl logs -n kube-system daemonset/kube-proxy-windows --tail=150; k0s kubectl logs -n kube-system daemonset/kube-proxy-windows --previous --tail=150; journalctl -u k0scontroller -n 50 --no-pager")
			done()
			t.Logf("Linux diagnostics: %s (%v)", out, err)
			// Use serial control: diagnostics must remain available when the
			// dataplane or Windows kubelet connectivity is broken.
			dctx, done = context.WithTimeout(context.Background(), 30*time.Second)
			defer done()
			out, err = windows.Commands().Exec(dctx, psArgs(`Get-Date -Format o; Get-Service k0sworker -ErrorAction SilentlyContinue; Get-ChildItem C:\var\lib\k0s -Filter 'k0s_*.log' -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending | Select-Object -First 1 | ForEach-Object {Get-Content $_.FullName -Tail 60}; Get-NetAdapter | Format-Table -AutoSize; Get-NetRoute | Format-Table -AutoSize; if(Get-Command Get-HnsNetwork -ErrorAction SilentlyContinue){Get-HnsNetwork | ConvertTo-Json -Depth 12; Get-HnsEndpoint | ConvertTo-Json -Depth 12}; if(Test-Path C:\var\log\calico\cni\cni.log){Get-Content C:\var\log\calico\cni\cni.log -Tail 60}`)...)
			t.Logf("Windows diagnostics: %s (%v)", out, err)
		}
	}()
	wait(linux, 3*time.Minute, "test", "-b", "/dev/disk/by-label/LCQUAL")
	wait(linux, time.Minute, "test", "-b", "/dev/disk/by-id/virtio-lc-k0s-state")
	t.Log(string(run(linux, "sh", "-ec", controllerDiskScript, "controller-disk", "/var/lib/k0s", "/etc/fstab")))
	if err := persistControllerNetwork(ctx, c, lab.ID(), wan); err != nil {
		t.Fatal(err)
	}
	wait(windows, 5*time.Minute, psArgs(`if (!(Get-Volume -FileSystemLabel LCQUAL -ErrorAction SilentlyContinue)) { throw 'media unavailable' }`)...)
	// QEMU supplies a UTC RTC. A Windows image using Pacific local hardware
	// time otherwise starts seven hours ahead and corrupts token/cache timing.
	run(windows, psArgs(fmt.Sprintf(`Set-TimeZone -Id UTC; Set-Date -Date ([DateTime]::Parse('%s').ToLocalTime()) | Out-Null; $v=Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'; if($v.CurrentBuild -ne '20348' -or $v.UBR -lt 5622){throw 'unexpected Windows kernel'}; New-NetFirewallRule -Name LabQualificationKubelet -DisplayName LabQualificationKubelet -Direction Inbound -Action Allow -Protocol TCP -LocalPort 10250 -RemoteAddress 192.0.2.10 | Out-Null`, time.Now().UTC().Format(time.RFC3339)))...)
	run(linux, "sh", "-ec", controllerDiskMountedScript+`
iface=$(ls /sys/class/net | grep -v '^lo$')
test "$(printf '%s\n' "$iface" | wc -l)" = 1
ip link set "$iface" up
ip addr add 192.0.2.10/24 dev "$iface"
ip route add 10.96.0.0/12 dev "$iface"
# Calico's proxy-ARP gateway needs a unicast route even without a default.
ip route add 169.254.1.1/32 dev "$iface"
test -z "$(ip -4 route show default)"
test -z "$(ip -6 route show default)"
mkdir -p /mnt/qualification /var/lib/k0s/images
mount -o ro /dev/disk/by-label/LCQUAL /mnt/qualification
`)
	if err := verifyQualificationDigest(tuple.Linux.DistributionBinary, string(run(linux, "sha256sum", "/mnt/qualification/k0s"))); err != nil {
		t.Fatal(err)
	}
	run(linux, "sh", "-ec", controllerDiskMountedScript+`
install -m 0755 /mnt/qualification/k0s /usr/local/bin/k0s
cp /mnt/qualification/linux-*.tar /var/lib/k0s/images/
`)
	if version := strings.TrimSpace(string(run(linux, "k0s", "version"))); version != tuple.Linux.DistributionBinary.Version {
		t.Fatalf("Linux distribution version %q does not match pinned %q", version, tuple.Linux.DistributionBinary.Version)
	}
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
	if tuple.Linux.CNI == matrix.CNICalicoBGP {
		const scriptPath = `C:\rras-prerequisites.ps1`
		if err := windows.Put(ctx, scriptPath, 0600, rrasPrerequisites); err != nil {
			t.Fatal(err)
		}
		digest := run(windows, psArgs(`$hash=Get-FileHash -Algorithm SHA256 -LiteralPath C:\rras-prerequisites.ps1; '{0} rras.ps1' -f $hash.Hash`)...)
		if err := verifyQualificationDigest(tuple.Linux.WindowsBGP.RRASTooling, string(digest)); err != nil {
			t.Fatal(err)
		}
		err := prepareRRAS(func(mode string) (string, error) {
			result := exec(windows, 8*time.Minute, "powershell.exe", "-NoProfile", "-NonInteractive", "-File", scriptPath, "-Mode", mode)
			if result.ExitCode != 0 {
				return "", fmt.Errorf("RRAS %s failed: %s %s", mode, result.Stdout, result.Stderr)
			}
			return string(result.Stdout), nil
		}, func() error {
			boot := strings.TrimSpace(string(run(windows, psArgs(`(Get-CimInstance Win32_OperatingSystem).LastBootUpTime.ToUniversalTime().Ticks`)...)))
			if _, err := strconv.ParseInt(boot, 10, 64); err != nil {
				return fmt.Errorf("invalid boot identity %q", boot)
			}
			run(windows, psArgs(`shutdown.exe /r /t 2; if($LASTEXITCODE -ne 0){throw 'RRAS guest reboot request failed'}`)...)
			wait(windows, 5*time.Minute, psArgs(fmt.Sprintf(`$boot=(Get-CimInstance Win32_OperatingSystem).LastBootUpTime.ToUniversalTime().Ticks; if($boot -eq %s){throw 'waiting for new guest boot'}; $boot`, boot))...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	windowsDigest := run(windows, psArgs(`$v=@(Get-Volume -FileSystemLabel LCQUAL); if($v.Count -ne 1){throw 'expected one media volume'}; $hash=Get-FileHash -Algorithm SHA256 -LiteralPath ($v[0].DriveLetter+':\k0s.exe'); '{0} k0s.exe' -f $hash.Hash`)...)
	if err := verifyQualificationDigest(tuple.WindowsBinary, string(windowsDigest)); err != nil {
		t.Fatal(err)
	}
	run(windows, psArgs(`$n=@(Get-NetAdapter -Physical); if($n.Count -ne 1){throw 'expected one NIC'}; Set-NetIPInterface -InterfaceIndex $n[0].ifIndex -AddressFamily IPv4 -Dhcp Disabled; New-NetIPAddress -InterfaceIndex $n[0].ifIndex -IPAddress 192.0.2.20 -PrefixLength 24 | Out-Null; if(Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue){throw 'unexpected default route'}; $v=Get-Volume -FileSystemLabel LCQUAL; $media="$($v.DriveLetter):\"; if(Test-Path C:\var\lib\k0s){throw 'unexpected prior k0s state'}; New-Item -ItemType Directory -Force C:\LabQualification,C:\var\lib\k0s\images | Out-Null; Copy-Item ($media+'k0s.exe') C:\LabQualification\k0s.exe; Copy-Item ($media+'windows-*.tar') C:\var\lib\k0s\images\; & C:\LabQualification\k0s.exe version; if($LASTEXITCODE -ne 0){throw 'k0s version failed'}`)...)
	if version := strings.TrimSpace(string(run(windows, psArgs(`& C:\LabQualification\k0s.exe version; if($LASTEXITCODE -ne 0){throw 'k0s version failed'}`)...))); version != tuple.WindowsBinary.Version {
		t.Fatalf("Windows distribution version %q does not match pinned %q", version, tuple.WindowsBinary.Version)
	}
	if wan {
		// NAT only node-source traffic: an unmasqueraded pod must not pass.
		// Keep forwarding rules inside the gateway namespace, not on the host.
		run(lab.Node("gateway"), "sh", "-ec", `
ip link add lan type bridge
ip link set eth1 master lan
ip link set eth2 master lan
ip link set eth1 up
ip link set eth2 up
ip link set lan up
ip addr add 192.0.2.1/24 dev lan
iptables -P FORWARD DROP
iptables -A FORWARD -i lan -o lan -j ACCEPT
iptables -A FORWARD -i eth0 -o lan -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -A FORWARD -i lan -o eth0 -s 192.0.2.0/24 -d 1.1.1.1/32 -p tcp --dport 443 -j ACCEPT
iptables -t nat -A POSTROUTING -s 192.0.2.0/24 -o eth0 -j MASQUERADE
`)
		run(linux, "sh", "-ec", `ip route add default via 192.0.2.1; ip route get 1.1.1.1`)
		run(windows, psArgs(`$n=@(Get-NetAdapter -Physical); New-NetRoute -DestinationPrefix '0.0.0.0/0' -InterfaceIndex $n[0].ifIndex -NextHop 192.0.2.1 | Out-Null`)...)
	}
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
				Node: image("ghcr.io/appmana/node", qualificationCalicoWindowsDigest),
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
	config := &native.ClusterConfig{TypeMeta: metav1.TypeMeta{APIVersion: native.ClusterConfigAPIVersion, Kind: native.ClusterConfigKind}, Spec: &native.ClusterSpec{
		API: &native.APISpec{Address: "192.0.2.10", SANs: []string{"192.0.2.10"}}, Images: images,
		Network: &native.Network{PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12", Calico: &native.Calico{MTU: 1450, VxlanVNI: 4096, VxlanPort: 4789, IPAutodetectionMethod: "can-reach=192.0.2.20"}, CoreDNS: &native.CoreDNS{Patches: native.Patches{{Target: native.PatchTarget{Kind: "ConfigMap", Name: "coredns", Namespace: "kube-system"}, Patch: native.PatchSpec{Type: native.MergePatchType, Content: string(dnsPatch)}}}}},
	}}
	if err := k0s.ConfigureNetwork(config, tuple.Linux); err != nil {
		t.Fatal(err)
	}
	if err := k0s.WriteConfig(ctx, linux.Commands(), "/etc/k0s/k0s.yaml", config); err != nil {
		t.Fatal(err)
	}
	run(linux, "sh", "-ec", controllerDiskMountedScript)
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
	t.Log(string(run(windows, psArgs(windowsServiceRoute)...)))
	winImage := networkProbeWindowsImage
	t.Log("preparing Windows workload layers before kubelet CreateContainer")
	prepared := exec(windows, 12*time.Minute, psArgs(`& C:\LabQualification\k0s.exe ctr images import --local --snapshotter windows C:\var\lib\k0s\images\windows-workload.tar; if($LASTEXITCODE -ne 0){exit $LASTEXITCODE}; $ready=@(& C:\LabQualification\k0s.exe ctr images check --snapshotter windows --quiet); if($LASTEXITCODE -ne 0){exit $LASTEXITCODE}; $ready; if($ready -notcontains '`+winImage+`'){throw 'workload image not completely unpacked'}`)...)
	t.Logf("Windows layer preparation: %s %s", prepared.Stdout, prepared.Stderr)
	if prepared.ExitCode != 0 {
		t.Fatalf("Windows layer preparation exited %d", prepared.ExitCode)
	}
	// Calico configuration and RBAC must come from the pinned installer.
	objects := networkProbeObjects(winImage)
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
	health := func(phase string, external bool) {
		t.Helper()
		t.Log(string(run(windows, psArgs(windowsServiceRouteAssert)...)))
		args := []string{"bash", "/usr/local/bin/calico-health-check", "--existing", "--namespace", "default", "--ipv4-only", "--skip-inbound"}
		count := 12
		if !external {
			args = append(args, "--skip-external")
			count = 10
		}
		args = append(args, "linux", "windows")
		r := exec(linux, 3*time.Minute, args...)
		t.Logf("%s health script: %s %s", phase, r.Stdout, r.Stderr)
		if r.ExitCode != 0 || !strings.Contains(string(r.Stdout), fmt.Sprintf("Total: %d  Pass: %d  Fail: 0", count, count)) {
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
	health("baseline", wan)
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
	health("recovery", wan)
	if wan {
		gateway := lab.Node("gateway")
		run(gateway, "sh", "-ec", "ip -4 route show default > /tmp/wan-default-route; test -s /tmp/wan-default-route")
		uplink, err := lab.SetLink(ctx, "gateway", "eth0", false)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			rctx, done := context.WithTimeout(context.Background(), 15*time.Second)
			defer done()
			if err := uplink.Revert(rctx); err != nil {
				t.Errorf("restore WAN: %v", err)
			}
		}()
		for _, node := range []*client.Node{linux, windows} {
			args := []string{"curl.exe", "-fsSk", "--ssl-no-revoke", "--connect-timeout", "3", "--max-time", "5", "https://1.1.1.1/", "-o", "NUL"}
			if node == linux {
				args = []string{"k0s", "kubectl", "--request-timeout=15s", "exec", "hc-linux", "--", "curl", "-fsSk", "--connect-timeout", "3", "--max-time", "5", "https://1.1.1.1/", "-o", "/dev/null"}
			} else {
				args = append([]string{`C:\LabQualification\k0s.exe`, "ctr", "tasks", "exec", "--exec-id", fmt.Sprintf("wan-%d", time.Now().UnixNano()), cid}, args...)
			}
			r := exec(node, 20*time.Second, args...)
			t.Logf("WAN outage %s: exit=%d stderr=%s", node.Ref().GetNode(), r.ExitCode, r.Stderr)
			if r.ExitCode != 7 && r.ExitCode != 28 {
				t.Fatalf("WAN outage was not a network failure: %+v", r)
			}
		}
		health("WAN cut, internal positive controls", false)
		if err := uplink.Revert(ctx); err != nil {
			t.Fatal(err)
		}
		// Restore the exact route removed by lowering the uplink.
		run(gateway, "sh", "-ec", `ip route replace $(cat /tmp/wan-default-route)`)
		health("WAN recovery", true)
	}
	t.Log("bidirectional ordinary pod, ClusterIP, DNS, sole-path failure, and recovery verified")
	// Exercise the installed production reader with the guest's actual Windows
	// PowerShell, whose JSON array enumeration differs from host PowerShell 7.
	if err := windows.Put(ctx, `C:\LabQualification\checkpoint-shape.ps1`, 0600, []byte(windowsRouteCheckpointShape)); err != nil {
		t.Fatal(err)
	}
	t.Log(string(run(windows, "powershell.exe", "-NoProfile", "-NonInteractive", "-File", `C:\LabQualification\checkpoint-shape.ps1`, "-Source", `C:\CalicoWindows\node-service.ps1`)))
	if workload != nil {
		t.Log("running pinned Kubernetes consumer qualification")
		if err := linux.Put(ctx, "/usr/local/bin/kubernetes-workload", 0755, workload); err != nil {
			t.Fatal(err)
		}
		directory := fmt.Sprintf("/var/tmp/kubernetes-consumer-%d", time.Now().UnixNano())
		t.Logf("persistent guest workload evidence: %s", directory)
		r := exec(linux, 38*time.Minute, workloadCommand(directory, "/usr/local/bin/kubernetes-workload", workloadArgs)...)
		t.Logf("consumer qualification: %s\n%s", r.Stdout, r.Stderr)
		marker := os.Getenv("LABCONTAINERS_KUBERNETES_WORKLOAD_SUCCESS")
		found := false
		for _, line := range strings.Split(string(r.Stdout), "\n") {
			if strings.TrimSpace(line) == marker {
				found = true
			}
		}
		if r.ExitCode != 0 || !found {
			t.Fatalf("consumer qualification did not complete: exit=%d marker=%v", r.ExitCode, found)
		}
		t.Log(string(run(windows, psArgs(windowsServiceRouteAssert)...)))
		if crashVerify == "1" {
			plan, err := readConsumerCrashPlan(string(r.Stdout))
			if err != nil {
				t.Fatal(err)
			}
			runKubernetesCrashConsumer(t, ctx, c, lab.ID(), workload, plan.Args, plan.Success)
		}
	}
}
