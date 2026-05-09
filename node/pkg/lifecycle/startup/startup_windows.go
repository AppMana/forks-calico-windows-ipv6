// Copyright (c) 2018-2021 Tigera, Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package startup

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/projectcalico/calico/cni-plugin/pkg/dataplane/windows"
	"github.com/projectcalico/calico/libcalico-go/lib/apis/internalapi"
	client "github.com/projectcalico/calico/libcalico-go/lib/clientv3"
	"github.com/projectcalico/calico/libcalico-go/lib/ipam"
	"github.com/projectcalico/calico/libcalico-go/lib/options"
	"github.com/projectcalico/calico/libcalico-go/lib/winutils"
)

func getOSType() string {
	return OSTypeWindows
}

// Checks that the filesystem is as expected and fix it if possible
func ensureFilesystemAsExpected() {
	logrus.Debug("ensureFilesystemAsExpected called on Windows; nothing to do.")
}

func ipv6Supported() bool {
	return strings.EqualFold(os.Getenv("FELIX_IPV6SUPPORT"), "true")
}

// configureCloudOrchRef does not do anything for windows
func configureCloudOrchRef(node *internalapi.Node) {
	logrus.Debug("configureCloudOrchRef called on Windows; nothing to do.")
}

func ensureNetworkForOS(ctx context.Context, c client.Interface, nodeName string) error {
	backend := os.Getenv("CALICO_NETWORKING_BACKEND")
	switch backend {
	case "none":
		logrus.Info("Backend networking is none, no network setup needed.")
	case "vxlan", "windows-bgp":
		rsvdAttrWindows := &ipam.HostReservedAttr{
			StartOfBlock: 3,
			EndOfBlock:   1,
			Handle:       ipam.WindowsReservedHandle,
			Note:         "windows host rsvd",
		}

		args := ipam.BlockArgs{
			Hostname:              nodeName,
			HostReservedAttrIPv4s: rsvdAttrWindows,
		}

		// For windows-bgp with IPv6 enabled, also request an IPv6 block.
		if backend == "windows-bgp" && ipv6Supported() {
			args.HostReservedAttrIPv6s = rsvdAttrWindows
		}

		cidrV4, cidrV6, err := c.IPAM().EnsureBlock(ctx, args)
		if err != nil {
			return err
		}

		var subnet *net.IPNet
		if cidrV4 != nil {
			subnet = &net.IPNet{IP: cidrV4.IP, Mask: cidrV4.Mask}
		}

		var subnetV6 *net.IPNet
		if cidrV6 != nil {
			subnetV6 = &net.IPNet{IP: cidrV6.IP, Mask: cidrV6.Mask}
		}

		networkName := "Calico"

		if backend == "vxlan" {
			logrus.Info("Backend networking is vxlan, ensure vxlan network.")
			vniString := os.Getenv("VXLAN_VNI")
			vni, err := strconv.ParseInt(vniString, 10, 64)
			if err != nil {
				return err
			}
			_, err = windows.SetupVxlanNetwork(networkName, subnet, uint64(vni), logrus.WithField("subnet", subnet.String()))
			if err != nil {
				return err
			}
			for attempts := 3; attempts > 0; attempts-- {
				err = windows.EnsureVXLANTunnelAddr(ctx, c, nodeName, subnet, networkName)
				if err != nil {
					logrus.WithError(err).Warn("Failed to set node's VXLAN tunnel IP, node may not receive traffic.  May retry...")
					time.Sleep(1 * time.Second)
					continue
				}
				break
			}
			if err != nil {
				logrus.WithError(err).Error("Failed to set node's VXLAN tunnel IP after retries, node may not receive traffic.")
				return err
			}
		} else {
			logrus.Info("Backend networking is windows-bgp, ensure l2bridge network.")
			if subnetV6 != nil {
				logrus.WithField("subnetV6", subnetV6.String()).Info("IPv6 subnet allocated for dual-stack L2Bridge network.")
			}
			// Read autodetected BGP IPs from this node's spec (just
			// written by configureIPsAndSubnets earlier in the same
			// startup pass) and pin them as HNS ManagementIP/v6 so
			// VFP delivers NS for the ULA. See hns_types.go for why.
			mgmtIP, mgmtIPv6 := readNodeBGPIPs(ctx, c, nodeName)
			_, err = windows.SetupL2bridgeNetworkAllowRecreate(networkName, subnet, subnetV6, mgmtIP, mgmtIPv6, logrus.WithField("subnet", subnet.String()))
			if err != nil {
				return err
			}

			// When a dual-stack L2Bridge is created, HNS adds a default IPv6
			// route (::/0) through the Calico_ep interface.  This competes
			// with the SLAAC default route to the upstream router, causing
			// outbound IPv6 traffic to loop through the pod subnet gateway
			// instead of reaching the physical network.  Remove it so the
			// SLAAC route wins.
			if subnetV6 != nil {
				cmd := `$idx=(Get-NetAdapter|Where-Object{$_.Name -eq 'vEthernet (Calico_ep)'}).ifIndex;if($idx){Remove-NetRoute -DestinationPrefix '::/0' -InterfaceIndex $idx -Confirm:$false -ErrorAction SilentlyContinue}`
				if _, _, err := winutils.Powershell(cmd); err != nil {
					logrus.WithError(err).Warn("Failed to remove Calico_ep IPv6 default route")
				} else {
					logrus.Info("Removed Calico_ep IPv6 default route to prevent conflict with SLAAC default route")
				}
			}
		}
	default:
		logrus.WithField("backend", backend).Errorf("Invalid backend networking type")
		return errors.New("invalid backend configuration")
	}

	logrus.Info("Ensure network is done.")
	return nil
}

// readNodeBGPIPs returns this node's BGP IPv4/IPv6 addresses (without
// CIDR prefix length), as set by configureIPsAndSubnets / IP
// autodetection earlier in the same startup pass. Returns ("","") on
// any error or missing data so the caller can fall back to legacy
// HNS-auto-pick behaviour.
func readNodeBGPIPs(ctx context.Context, c client.Interface, nodeName string) (string, string) {
	node, err := c.Nodes().Get(ctx, nodeName, options.GetOptions{})
	if err != nil {
		logrus.WithError(err).Warnf("Failed to look up node %s for HNS ManagementIP/v6; using HNS auto-pick", nodeName)
		return "", ""
	}
	if node.Spec.BGP == nil {
		return "", ""
	}
	stripPrefix := func(s string) string {
		if i := strings.IndexByte(s, '/'); i >= 0 {
			return s[:i]
		}
		return s
	}
	return stripPrefix(node.Spec.BGP.IPv4Address), stripPrefix(node.Spec.BGP.IPv6Address)
}
