package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/client"
)

func controllerNetworkConfig(mac string, wan bool) (string, error) {
	address, err := net.ParseMAC(mac)
	if err != nil || len(address) != 6 {
		return "", fmt.Errorf("expected one Ethernet MAC")
	}
	config := "[Match]\nMACAddress=" + address.String() + "\n[Link]\nRequiredForOnline=yes\n[Network]\nDHCP=no\nLinkLocalAddressing=no\nIPv6AcceptRA=no\nAddress=192.0.2.10/24\n[Route]\nDestination=10.96.0.0/12\nScope=link\n[Route]\nDestination=169.254.1.1/32\nScope=link\n"
	if os.Getenv("LABCONTAINERS_KUBERNETES_IPV6") == "1" {
		config = strings.Replace(config, "LinkLocalAddressing=no\n", "LinkLocalAddressing=ipv6\n", 1)
		config = strings.Replace(config, "Address=192.0.2.10/24\n", "Address=192.0.2.10/24\nAddress=fd00:10::10/64\n", 1)
	}
	if wan {
		config += "[Route]\nDestination=0.0.0.0/0\nGateway=192.0.2.1\n"
	}
	return config, nil
}

const qualificationMediaMount = `[Unit]
Description=Read-only qualification inputs
Before=k0scontroller.service
[Mount]
What=/dev/disk/by-label/LCQUAL
Where=/mnt/qualification
Type=iso9660
Options=ro
[Install]
WantedBy=multi-user.target
`

func persistControllerNetwork(ctx context.Context, c *client.Client, id string, wan bool) error {
	node := &labv1.NodeRef{SessionId: id, Node: "linux"}
	execute := func(argv ...string) ([]byte, error) {
		r, err := c.RPC().Exec(ctx, &labv1.ExecRequest{Node: node, Argv: argv, TimeoutMillis: 30000})
		if err != nil {
			return nil, err
		}
		if r.ExitCode != 0 {
			return nil, fmt.Errorf("controller persistence: %s %s", r.Stdout, r.Stderr)
		}
		return r.Stdout, nil
	}
	mac, err := execute("sh", "-ec", `test -b /dev/disk/by-label/LCQUAL; iface=$(for p in /sys/class/net/*; do test ! -e "$p/device" || basename "$p"; done); test "$(printf '%s\n' "$iface" | wc -l)" = 1; cat /sys/class/net/"$iface"/address`)
	if err != nil {
		return err
	}
	config, err := controllerNetworkConfig(strings.TrimSpace(string(mac)), wan)
	if err != nil {
		return err
	}
	if _, err := execute("mkdir", "-p", "/etc/systemd/network", "/etc/systemd/system/k0scontroller.service.d", "/mnt/qualification"); err != nil {
		return err
	}
	for path, body := range map[string]string{
		"/etc/systemd/network/05-qualification.network":                          config,
		"/etc/systemd/system/mnt-qualification.mount":                            qualificationMediaMount,
		"/etc/systemd/system/k0scontroller.service.d/qualification-network.conf": "[Unit]\nWants=network-online.target mnt-qualification.mount\nAfter=network-online.target mnt-qualification.mount\n",
	} {
		if _, err := c.RPC().Put(ctx, &labv1.PutRequest{Node: node, Path: path, Mode: 0644, Content: []byte(body)}); err != nil {
			return err
		}
	}
	_, err = execute("sh", "-ec", "systemctl daemon-reload; systemctl enable systemd-networkd-wait-online.service mnt-qualification.mount")
	return err
}

func TestControllerNetworkConfiguration(t *testing.T) {
	t.Setenv("LABCONTAINERS_KUBERNETES_IPV6", "")
	for _, wan := range []bool{false, true} {
		config, err := controllerNetworkConfig("aa:c1:ab:9a:1f:2f", wan)
		if err != nil {
			t.Fatal(err)
		}
		for _, required := range []string{"MACAddress=aa:c1:ab:9a:1f:2f", "Address=192.0.2.10/24", "Destination=10.96.0.0/12", "Destination=169.254.1.1/32", "DHCP=no", "LinkLocalAddressing=no"} {
			if !strings.Contains(config, required) {
				t.Fatal("missing persistence", required)
			}
		}
		if strings.Contains(config, "Destination=0.0.0.0/0") != wan {
			t.Fatal("implicit or missing WAN")
		}
	}
	if _, err := controllerNetworkConfig("bad\nGateway=1.2.3.4", false); err == nil {
		t.Fatal("invalid MAC accepted")
	}
	if !strings.Contains(qualificationMediaMount, "Options=ro") {
		t.Fatal("media must stay read-only")
	}
}

func TestDualStackControllerPreservesIPv6LinkLocal(t *testing.T) {
	t.Setenv("LABCONTAINERS_KUBERNETES_IPV6", "1")
	config, err := controllerNetworkConfig("aa:c1:ab:9a:1f:2f", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"LinkLocalAddressing=ipv6", "IPv6AcceptRA=no", "DHCP=no", "Address=fd00:10::10/64", "Gateway=192.0.2.1"} {
		if !strings.Contains(config, required) {
			t.Fatalf("missing %s", required)
		}
	}
}

// Repair the original fixture configuration only, without replacing disks,
// network probes, etcd state or Kubernetes objects. Explicit retained lab only.
func TestRetainedControllerNetworkPersistence(t *testing.T) {
	if os.Getenv("LABCONTAINERS_RETAINED_CONTROLLER_NETWORK") != "1" {
		t.Skip("explicit retained controller opt-in required")
	}
	id, socket := os.Getenv("LABCONTAINERS_RETAINED_SESSION"), os.Getenv("LABCONTAINERS_RETAINED_SOCKET")
	if id == "" || socket == "" {
		t.Fatal("retained session required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, err := client.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	wan := os.Getenv("LABCONTAINERS_RETAINED_CONTROLLER_WAN")
	if wan != "" && wan != "1" {
		t.Fatal("WAN must be unset or explicitly 1")
	}
	if err := persistControllerNetwork(ctx, c, id, wan == "1"); err != nil {
		t.Fatal(err)
	}
	r, err := c.RPC().Exec(ctx, &labv1.ExecRequest{Node: &labv1.NodeRef{SessionId: id, Node: "linux"}, TimeoutMillis: 90000, Argv: []string{"sh", "-ec", `networkctl reload; iface=$(for p in /sys/class/net/*; do test ! -e "$p/device" || basename "$p"; done); networkctl reconfigure "$iface"; /lib/systemd/systemd-networkd-wait-online --interface="$iface" --timeout=30; systemctl start mnt-qualification.mount; ip -4 addr show dev "$iface" | grep -F '192.0.2.10/24'; systemctl restart k0scontroller; findmnt /var/lib/k0s; findmnt /mnt/qualification`}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s %s", r.Stdout, r.Stderr)
	if r.ExitCode != 0 {
		t.Fatal("controller restore failed", r.ExitCode)
	}
}

// Reboot an already configured retained controller, without repairing its
// configuration or recreating Kubernetes objects. This must pass independently
// of the explicit repair helper above.
func TestRetainedControllerBootPersistence(t *testing.T) {
	if os.Getenv("LABCONTAINERS_RETAINED_CONTROLLER_REBOOT") != "1" {
		t.Skip("explicit isolated controller reboot required")
	}
	id, socket := os.Getenv("LABCONTAINERS_RETAINED_SESSION"), os.Getenv("LABCONTAINERS_RETAINED_SOCKET")
	if id == "" || socket == "" {
		t.Fatal("retained session required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	c, err := client.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	node := &labv1.NodeRef{SessionId: id, Node: "linux"}
	execute := func(timeout time.Duration, argv ...string) (string, error) {
		r, err := c.RPC().Exec(ctx, &labv1.ExecRequest{Node: node, TimeoutMillis: timeout.Milliseconds(), Argv: argv})
		if err != nil {
			return "", err
		}
		if r.ExitCode != 0 {
			return "", fmt.Errorf("exit%d: %s %s", r.ExitCode, r.Stdout, r.Stderr)
		}
		return strings.TrimSpace(string(r.Stdout)), nil
	}
	before, err := execute(10*time.Second, "cat", "/proc/sys/kernel/random/boot_id")
	if err != nil || len(before) != 36 {
		t.Fatalf("missing controller boot identity: %q %v", before, err)
	}
	// Delay just the shutdown request so serial RPC can collect its exit status.
	if out, err := execute(10*time.Second, "systemd-run", "--on-active=3s", "--unit=qualification-controller-reboot", "systemctl", "reboot"); err != nil {
		t.Fatalf("schedule controller reboot: %s %v", out, err)
	}
	changed := false
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		after, err := execute(10*time.Second, "cat", "/proc/sys/kernel/random/boot_id")
		if err == nil && len(after) == 36 && after != before {
			t.Logf("controller boot identity %s -> %s", before, after)
			changed = true
			break
		}
		time.Sleep(3 * time.Second)
	}
	if !changed {
		t.Fatal("controller did not return with a new kernel boot identity")
	}
	checks := `set -eu
iface=$(for p in /sys/class/net/*; do test ! -e "$p/device" || basename "$p"; done)
test "$(printf '%s\n' "$iface" | wc -l)" = 1
/lib/systemd/systemd-networkd-wait-online --interface="$iface" --timeout=30
ip -4 addr show dev "$iface" | grep -F '192.0.2.10/24'
ip -4 route show 10.96.0.0/12 | grep -F "dev $iface"
ip -4 route show 169.254.1.1/32 | grep -F "dev $iface"
test -z "$(ip -4 route show default)"
test -z "$(ip -6 route show default)"
findmnt --mountpoint /var/lib/k0s
findmnt -no OPTIONS --mountpoint /mnt/qualification | grep -E '(^|,)ro(,|$)'
test -r /mnt/qualification/k0s
printf 'CONTROLLER_CONFIGURATION_PERSISTED\n'
`
	out, err := execute(45*time.Second, "sh", "-ec", checks)
	t.Log(out)
	if err != nil || !strings.Contains(out, "CONTROLLER_CONFIGURATION_PERSISTED") {
		t.Fatalf("persistent controller configuration: %v", err)
	}
	// Serial control and networking become available before the API server.
	// Retry only the read-only readiness observation, never the reboot or repair.
	readyCtx, readyCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer readyCancel()
	err = waitForControllerReady(readyCtx, 3*time.Second, func() error {
		_, err := execute(15*time.Second, "k0s", "kubectl", "wait", "--request-timeout=10s", "--for=condition=Ready", "node/linux", "--timeout=10s")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("CONTROLLER_BOOT_PERSISTENCE_COMPLETE")
}

func waitForControllerReady(ctx context.Context, interval time.Duration, probe func() error) error {
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("controller readiness: %w; last observation: %v", err, last)
		}
		last = probe()
		if last == nil {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func TestControllerReadinessAfterBoot(t *testing.T) {
	t.Run("API starts after serial control", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		calls := 0
		err := waitForControllerReady(ctx, time.Millisecond, func() error {
			calls++
			if calls < 3 {
				return fmt.Errorf("connection refused")
			}
			return nil
		})
		if err != nil || calls != 3 {
			t.Fatalf("readiness: calls=%d err=%v", calls, err)
		}
	})
	t.Run("persistent failure stays failed", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := waitForControllerReady(ctx, time.Millisecond, func() error {
			cancel()
			return fmt.Errorf("API unavailable")
		})
		if err == nil || !strings.Contains(err.Error(), "API unavailable") {
			t.Fatalf("lost readiness failure: %v", err)
		}
	})
}
