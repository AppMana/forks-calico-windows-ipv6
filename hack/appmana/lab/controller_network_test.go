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
	if err := persistControllerNetwork(ctx, c, id, false); err != nil {
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
